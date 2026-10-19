package plan

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lavr/regru-cli/internal/zone"
)

const (
	fileAPIVersion = "regru-cli/plan/v1"
	codeInvalid    = "invalid_plan"
	codeStale      = "stale_plan"
)

type FileError struct {
	CodeValue string
	Reason    string
}

func (e *FileError) Error() string { return e.CodeValue + ": " + e.Reason }

func (e *FileError) Code() string { return e.CodeValue }

func invalid(format string, args ...any) error {
	return &FileError{CodeValue: codeInvalid, Reason: fmt.Sprintf(format, args...)}
}

type fileAccount struct {
	Alias  string `json:"alias"`
	Login  string `json:"login"`
	UserID string `json:"user_id"`
}

type filePolicy struct {
	MaxDeletes     *int  `json:"max_deletes"`
	AllowEmptyZone *bool `json:"allow_empty_zone"`
}

type fileRRset struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

type fileDesired struct {
	Scope  string      `json:"scope"`
	RRsets []fileRRset `json:"rrsets"`
}

type fileOp struct {
	Seq       int    `json:"seq"`
	Op        string `json:"op"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	DependsOn []int  `json:"depends_on"`
}

type fileDoc struct {
	APIVersion          string       `json:"apiVersion"`
	Created             string       `json:"created"`
	PlanID              string       `json:"plan_id"`
	Account             *fileAccount `json:"account"`
	Endpoint            string       `json:"endpoint"`
	Zone                string       `json:"zone"`
	ServiceID           string       `json:"service_id"`
	Policy              *filePolicy  `json:"policy"`
	BaselineFingerprint string       `json:"baseline_fingerprint"`
	Desired             *fileDesired `json:"desired"`
	Ops                 []fileOp     `json:"ops"`
	Warnings            []string     `json:"warnings"`
}

func EncodeFile(w io.Writer, p Prepared) error {
	md, aez := p.Policy.MaxDeletes, p.Policy.AllowEmptyZone
	doc := fileDoc{
		APIVersion:          fileAPIVersion,
		Created:             time.Now().UTC().Format(time.RFC3339),
		PlanID:              p.PlanID,
		Account:             &fileAccount{Alias: p.Account.Alias, Login: p.Account.Login, UserID: p.Account.UserID},
		Endpoint:            p.Endpoint,
		Zone:                p.Zone,
		ServiceID:           p.ServiceID,
		Policy:              &filePolicy{MaxDeletes: &md, AllowEmptyZone: &aez},
		BaselineFingerprint: p.BaselineFingerprint,
		Desired:             &fileDesired{Scope: string(p.Desired.Scope), RRsets: []fileRRset{}},
		Ops:                 []fileOp{},
		Warnings:            append([]string{}, p.Plan.Warnings...),
	}
	for _, k := range sortedKeys(p.Desired.RRsets) {
		doc.Desired.RRsets = append(doc.Desired.RRsets, fileRRset{Name: k.Name, Type: string(k.Type), Values: append([]string{}, p.Desired.RRsets[k]...)})
	}
	for _, op := range p.Plan.Ops {
		doc.Ops = append(doc.Ops, fileOp{
			Seq: op.Seq, Op: string(op.Kind),
			Name: op.Record.Name, Type: string(op.Record.Type), Value: op.Record.Value,
			DependsOn: append([]int{}, op.DependsOn...),
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

func DecodeFile(r io.Reader) (Prepared, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var doc fileDoc
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return Prepared{}, invalid("empty plan file")
		}
		return Prepared{}, invalid("json: %v", err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Prepared{}, invalid("plan file must contain exactly one JSON object")
	}
	if doc.APIVersion == "" {
		return Prepared{}, invalid("apiVersion is required")
	}
	if doc.APIVersion != fileAPIVersion {
		return Prepared{}, &FileError{CodeValue: codeStale, Reason: fmt.Sprintf("unsupported apiVersion %q", doc.APIVersion)}
	}
	return validate(doc)
}

func validate(doc fileDoc) (Prepared, error) {
	if _, err := time.Parse(time.RFC3339, doc.Created); err != nil {
		return Prepared{}, invalid("created: %v", err)
	}
	if doc.PlanID == "" {
		return Prepared{}, invalid("plan_id is required")
	}
	if doc.Account == nil || doc.Account.UserID == "" {
		return Prepared{}, invalid("account.user_id is required")
	}
	if doc.Endpoint == "" {
		return Prepared{}, invalid("endpoint is required")
	}
	if doc.ServiceID == "" {
		return Prepared{}, invalid("service_id is required")
	}
	zoneName, err := zone.NormalizeZone(doc.Zone)
	if err != nil {
		return Prepared{}, invalid("zone: %v", err)
	}
	if doc.Policy == nil || doc.Policy.MaxDeletes == nil || doc.Policy.AllowEmptyZone == nil {
		return Prepared{}, invalid("policy.max_deletes and policy.allow_empty_zone are required")
	}
	if *doc.Policy.MaxDeletes < 0 {
		return Prepared{}, invalid("policy.max_deletes must be >= 0")
	}
	if err := checkFingerprint(doc.BaselineFingerprint); err != nil {
		return Prepared{}, err
	}
	desired, err := decodeDesired(doc.Desired, zoneName)
	if err != nil {
		return Prepared{}, err
	}
	ops, err := decodeOps(doc.Ops, zoneName)
	if err != nil {
		return Prepared{}, err
	}
	var warnings []string
	if len(doc.Warnings) > 0 {
		warnings = append([]string(nil), doc.Warnings...)
	}
	return Prepared{
		PlanID:              doc.PlanID,
		Account:             Account{Alias: doc.Account.Alias, Login: doc.Account.Login, UserID: doc.Account.UserID},
		Endpoint:            doc.Endpoint,
		Zone:                zoneName,
		ServiceID:           doc.ServiceID,
		Desired:             desired,
		Policy:              FilePolicy{MaxDeletes: *doc.Policy.MaxDeletes, AllowEmptyZone: *doc.Policy.AllowEmptyZone},
		BaselineFingerprint: doc.BaselineFingerprint,
		Plan:                Plan{Ops: ops, Warnings: warnings},
	}, nil
}

func checkFingerprint(fp string) error {
	if !strings.HasPrefix(fp, "fp1:") {
		return &FileError{CodeValue: codeStale, Reason: "unsupported baseline_fingerprint version"}
	}
	h, ok := strings.CutPrefix(fp, fingerprintPrefix)
	if !ok || len(h) != 64 || strings.ToLower(h) != h {
		return invalid("malformed baseline_fingerprint")
	}
	if _, err := hex.DecodeString(h); err != nil {
		return invalid("malformed baseline_fingerprint")
	}
	return nil
}

func decodeDesired(d *fileDesired, zoneName string) (Desired, error) {
	if d == nil {
		return Desired{}, invalid("desired is required")
	}
	scope := Scope(d.Scope)
	if scope != ScopeListed && scope != ScopeManaged {
		return Desired{}, invalid("desired.scope: unsupported scope %q", d.Scope)
	}
	out := Desired{Scope: scope, RRsets: map[zone.Key][]string{}}
	for i, rr := range d.RRsets {
		name, err := zone.NormalizeName(zoneName, rr.Name)
		if err != nil {
			return Desired{}, invalid("desired.rrsets[%d].name: %v", i, err)
		}
		t := zone.Type(rr.Type)
		if !knownType(t) {
			return Desired{}, invalid("desired.rrsets[%d].type: unknown type %q", i, rr.Type)
		}
		if rr.Values == nil {
			return Desired{}, invalid("desired.rrsets[%d].values must be a list", i)
		}
		k := zone.Key{Name: name, Type: t}
		if _, dup := out.RRsets[k]; dup {
			return Desired{}, invalid("desired.rrsets[%d]: repeated rrset %s %s", i, name, t)
		}
		vals := []string{}
		seen := map[string]bool{}
		for j, v := range rr.Values {
			nv, err := zone.ParseValue(t, v)
			if err != nil {
				return Desired{}, invalid("desired.rrsets[%d].values[%d]: %v", i, j, err)
			}
			if seen[nv] {
				return Desired{}, invalid("desired.rrsets[%d]: repeated value %q", i, nv)
			}
			seen[nv] = true
			vals = append(vals, nv)
		}
		out.RRsets[k] = vals
	}
	return out, nil
}

func decodeOps(in []fileOp, zoneName string) ([]Op, error) {
	if in == nil {
		return nil, invalid("ops must be a list")
	}
	var ops []Op
	for i, o := range in {
		if o.Seq != i+1 {
			return nil, invalid("ops[%d].seq must be %d, got %d", i, i+1, o.Seq)
		}
		kind := OpKind(o.Op)
		if kind != OpAdd && kind != OpRemove {
			return nil, invalid("ops[%d].op: unsupported operation %q", i, o.Op)
		}
		name, err := zone.NormalizeName(zoneName, o.Name)
		if err != nil {
			return nil, invalid("ops[%d].name: %v", i, err)
		}
		t := zone.Type(o.Type)
		if !knownType(t) {
			return nil, invalid("ops[%d].type: unknown type %q", i, o.Type)
		}
		if o.Value == "" {
			return nil, invalid("ops[%d].value is required", i)
		}
		var deps []int
		seen := map[int]bool{}
		for _, d := range o.DependsOn {
			if d < 1 || d >= o.Seq {
				return nil, invalid("ops[%d].depends_on: %d must reference an earlier operation", i, d)
			}
			if seen[d] {
				return nil, invalid("ops[%d].depends_on: repeated %d", i, d)
			}
			seen[d] = true
			deps = append(deps, d)
		}
		ops = append(ops, Op{Seq: o.Seq, Kind: kind, Record: zone.Record{Name: name, Type: t, Value: o.Value}, DependsOn: deps})
	}
	return ops, nil
}
