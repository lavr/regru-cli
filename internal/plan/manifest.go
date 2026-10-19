package plan

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lavr/regru-cli/internal/zone"
)

const (
	apiVersion   = "regru-cli/v1"
	kindZone     = "Zone"
	codeManifest = "invalid_manifest"
)

type Desired struct {
	Scope  Scope
	RRsets map[zone.Key][]string
}

type Manifest struct {
	Account  string
	Zone     string
	Desired  Desired
	Readonly []zone.Record
	RawKeys  []zone.Key
}

type ManifestError struct {
	CodeValue string
	Reason    string
}

func (e *ManifestError) Error() string { return e.CodeValue + ": " + e.Reason }

func (e *ManifestError) Code() string { return e.CodeValue }

func manifestErr(code, format string, args ...any) error {
	return &ManifestError{CodeValue: code, Reason: fmt.Sprintf(format, args...)}
}

func fail(format string, args ...any) error {
	return manifestErr(codeManifest, format, args...)
}

func mapping(n *yaml.Node, path string, allowed ...string) (map[string]*yaml.Node, error) {
	if n.Kind != yaml.MappingNode {
		return nil, fail("%s: expected a mapping", path)
	}
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	out := map[string]*yaml.Node{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return nil, fail("%s: non-scalar key", path)
		}
		if v.Kind == yaml.AliasNode || k.Kind == yaml.AliasNode {
			return nil, fail("%s.%s: aliases are not supported", path, k.Value)
		}
		if !ok[k.Value] {
			return nil, fail("%s: unknown field %q", path, k.Value)
		}
		if _, dup := out[k.Value]; dup {
			return nil, fail("%s: repeated field %q", path, k.Value)
		}
		out[k.Value] = v
	}
	return out, nil
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

func strField(m map[string]*yaml.Node, path, key string) (string, error) {
	n, ok := m[key]
	if !ok || isNull(n) {
		return "", fail("%s.%s is required", path, key)
	}
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return "", fail("%s.%s must be a string", path, key)
	}
	if n.Value == "" {
		return "", fail("%s.%s must not be empty", path, key)
	}
	return n.Value, nil
}

func intValue(n *yaml.Node, path string) (int, error) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
		return 0, fail("%s must be an integer", path)
	}
	v, err := strconv.Atoi(n.Value)
	if err != nil {
		return 0, fail("%s must be an integer", path)
	}
	return v, nil
}

func intField(m map[string]*yaml.Node, path, key string) (int, error) {
	n, ok := m[key]
	if !ok {
		return 0, fail("%s.%s is required", path, key)
	}
	return intValue(n, path+"."+key)
}

type rrsetEntry struct {
	key    zone.Key
	values []string
}

func parseRRsets(n *yaml.Node, zoneName, path string, strict bool) ([]rrsetEntry, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, fail("%s must be a list", path)
	}
	var out []rrsetEntry
	seen := map[zone.Key]bool{}
	for i, item := range n.Content {
		p := fmt.Sprintf("%s[%d]", path, i)
		m, err := mapping(item, p, "name", "type", "values")
		if err != nil {
			return nil, err
		}
		rawName, err := strField(m, p, "name")
		if err != nil {
			return nil, err
		}
		rawType, err := strField(m, p, "type")
		if err != nil {
			return nil, err
		}
		name, err := zone.NormalizeName(zoneName, rawName)
		if err != nil {
			return nil, err
		}
		t := zone.Type(rawType)
		if !knownType(t) {
			return nil, fail("%s.type: unknown type %q", p, rawType)
		}
		vn, ok := m["values"]
		if !ok || vn.Kind != yaml.SequenceNode {
			return nil, fail("%s.values must be a list", p)
		}
		key := zone.Key{Name: name, Type: t}
		if seen[key] {
			return nil, manifestErr("duplicate", "%s: repeated rrset %s %s", p, name, t)
		}
		seen[key] = true
		vals := []string{}
		seenVal := map[string]bool{}
		for j, v := range vn.Content {
			if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
				return nil, &zone.ValueError{Type: t, Input: v.Value, Reason: fmt.Sprintf("%s.values[%d] must be a string", p, j)}
			}
			nv, err := zone.ParseValue(t, v.Value)
			if err != nil {
				if strict {
					return nil, err
				}
				nv = v.Value
			}
			if seenVal[nv] {
				return nil, manifestErr("duplicate", "%s: repeated value %q", p, nv)
			}
			seenVal[nv] = true
			vals = append(vals, nv)
		}
		out = append(out, rrsetEntry{key: key, values: vals})
	}
	return out, nil
}

func knownType(t zone.Type) bool {
	switch t {
	case zone.A, zone.AAAA, zone.CNAME, zone.MX, zone.TXT, zone.NS, zone.SRV, zone.CAA, zone.HTTPS:
		return true
	}
	return false
}

func checkRaw(n *yaml.Node, path string) ([]zone.Key, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, fail("%s must be a list", path)
	}
	var keys []zone.Key
	for i, item := range n.Content {
		p := fmt.Sprintf("%s[%d]", path, i)
		m, err := mapping(item, p, "name", "type", "content", "prio", "count")
		if err != nil {
			return nil, err
		}
		for _, k := range []string{"name", "type", "content"} {
			n, ok := m[k]
			if !ok || n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
				return nil, fail("%s.%s must be a string", p, k)
			}
		}
		if pn, ok := m["prio"]; ok && !isNull(pn) {
			if _, err := intValue(pn, p+".prio"); err != nil {
				return nil, err
			}
		}
		if _, err := intField(m, p, "count"); err != nil {
			return nil, err
		}
		keys = append(keys, rawKeyOf(m["name"].Value, m["type"].Value))
	}
	return keys, nil
}

func rawKeyOf(name, typ string) zone.Key {
	return zone.Key{Name: name, Type: zone.Type(strings.ToUpper(typ))}
}

func checkSOA(n *yaml.Node, path string) error {
	m, err := mapping(n, path, "ttl", "minimum_ttl")
	if err != nil {
		return err
	}
	for _, k := range []string{"ttl", "minimum_ttl"} {
		if _, err := intField(m, path, k); err != nil {
			return err
		}
	}
	return nil
}

func ParseManifest(b []byte) (Manifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return Manifest{}, fail("empty manifest")
		}
		return Manifest{}, fail("yaml: %v", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Manifest{}, fail("manifest must contain exactly one document")
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return Manifest{}, fail("empty manifest")
	}
	top, err := mapping(doc.Content[0], "manifest", "apiVersion", "kind", "account", "zone", "scope", "rrsets", "readonly")
	if err != nil {
		return Manifest{}, err
	}
	av, err := strField(top, "manifest", "apiVersion")
	if err != nil {
		return Manifest{}, err
	}
	if av != apiVersion {
		return Manifest{}, fail("unsupported apiVersion %q", av)
	}
	kind, err := strField(top, "manifest", "kind")
	if err != nil {
		return Manifest{}, err
	}
	if kind != kindZone {
		return Manifest{}, fail("unsupported kind %q", kind)
	}
	account, err := strField(top, "manifest", "account")
	if err != nil {
		return Manifest{}, err
	}
	rawZone, err := strField(top, "manifest", "zone")
	if err != nil {
		return Manifest{}, err
	}
	zoneName, err := zone.NormalizeZone(rawZone)
	if err != nil {
		return Manifest{}, err
	}
	scopeStr, err := strField(top, "manifest", "scope")
	if err != nil {
		return Manifest{}, err
	}
	scope := Scope(scopeStr)
	if scope != ScopeListed && scope != ScopeManaged {
		return Manifest{}, fail("unsupported scope %q", scopeStr)
	}
	rn, ok := top["rrsets"]
	if !ok || isNull(rn) {
		return Manifest{}, fail("manifest.rrsets is required")
	}
	entries, err := parseRRsets(rn, zoneName, "rrsets", true)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{
		Account: account,
		Zone:    zoneName,
		Desired: Desired{Scope: scope, RRsets: map[zone.Key][]string{}},
	}
	for _, e := range entries {
		m.Desired.RRsets[e.key] = e.values
	}
	if ro, ok := top["readonly"]; ok && !isNull(ro) {
		rm, err := mapping(ro, "readonly", "rrsets", "raw", "soa")
		if err != nil {
			return Manifest{}, err
		}
		if n, ok := rm["rrsets"]; ok && !isNull(n) {
			es, err := parseRRsets(n, zoneName, "readonly.rrsets", false)
			if err != nil {
				return Manifest{}, err
			}
			for _, e := range es {
				for _, v := range e.values {
					m.Readonly = append(m.Readonly, zone.Record{Name: e.key.Name, Type: e.key.Type, Value: v})
				}
			}
		}
		if n, ok := rm["raw"]; ok && !isNull(n) {
			keys, err := checkRaw(n, "readonly.raw")
			if err != nil {
				return Manifest{}, err
			}
			m.RawKeys = keys
		}
		if n, ok := rm["soa"]; ok && !isNull(n) {
			if err := checkSOA(n, "readonly.soa"); err != nil {
				return Manifest{}, err
			}
		}
	}
	return m, nil
}

func ValidateManifest(m Manifest, r Rules) error {
	mixed := map[zone.Key]bool{}
	for _, k := range m.RawKeys {
		mixed[k] = true
	}
	for _, rec := range m.Readonly {
		if r.writable(rec.Type) && !r.protected(rec.Key()) && !mixed[rec.Key()] {
			return manifestErr("readonly_writable", "readonly.rrsets contains writable %s %s; move it to rrsets", rec.Name, rec.Type)
		}
	}
	return nil
}

func str(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s, Style: yaml.DoubleQuotedStyle}
}

func plain(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

func integer(i int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(i)}
}

func mapNode(flow bool, kv ...*yaml.Node) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: kv}
	if flow {
		n.Style = yaml.FlowStyle
	}
	return n
}

func seqNode(flow bool, items []*yaml.Node) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
	if flow {
		n.Style = yaml.FlowStyle
	}
	return n
}

func groupRRsets(recs []zone.Record) *yaml.Node {
	var items []*yaml.Node
	var vals []*yaml.Node
	var cur zone.Key
	flush := func() {
		if len(vals) == 0 {
			return
		}
		items = append(items, mapNode(false,
			plain("name"), str(cur.Name),
			plain("type"), plain(string(cur.Type)),
			plain("values"), seqNode(true, vals),
		))
		vals = nil
	}
	for _, r := range recs {
		if k := r.Key(); k != cur {
			flush()
			cur = k
		}
		vals = append(vals, str(r.Value))
	}
	flush()
	return seqNode(len(items) == 0, items)
}

func ExportManifest(account string, s zone.Snapshot, scope Scope, r Rules) ([]byte, error) {
	sorted := s.Sorted()
	mixed := rawKeys(sorted.Raw)
	var write, ro []zone.Record
	for _, rec := range sorted.Records {
		if r.writable(rec.Type) && !r.protected(rec.Key()) && !mixed[rec.Key()] {
			write = append(write, rec)
		} else {
			ro = append(ro, rec)
		}
	}

	roKV := []*yaml.Node{}
	if len(ro) > 0 {
		roKV = append(roKV, plain("rrsets"), groupRRsets(ro))
	}
	if len(sorted.Raw) > 0 {
		var items []*yaml.Node
		for _, raw := range sorted.Raw {
			prio := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
			if raw.Prio != nil {
				prio = integer(*raw.Prio)
			}
			items = append(items, mapNode(true,
				plain("name"), str(raw.Name),
				plain("type"), str(raw.Type),
				plain("content"), str(raw.Content),
				plain("prio"), prio,
				plain("count"), integer(raw.Count),
			))
		}
		roKV = append(roKV, plain("raw"), seqNode(false, items))
	}
	if sorted.SOA != nil {
		roKV = append(roKV, plain("soa"), mapNode(false,
			plain("ttl"), integer(sorted.SOA.TTL),
			plain("minimum_ttl"), integer(sorted.SOA.MinimumTTL),
		))
	}

	top := []*yaml.Node{
		plain("apiVersion"), plain(apiVersion),
		plain("kind"), plain(kindZone),
		plain("account"), str(account),
		plain("zone"), str(sorted.Zone),
		plain("scope"), plain(string(scope)),
		plain("rrsets"), groupRRsets(write),
	}
	if len(roKV) > 0 {
		top = append(top, plain("readonly"), mapNode(false, roKV...))
	}
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{mapNode(false, top...)}}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func rawKeys(raws []zone.Raw) map[zone.Key]bool {
	out := map[zone.Key]bool{}
	for _, raw := range raws {
		out[rawKeyOf(raw.Name, raw.Type)] = true
	}
	return out
}
