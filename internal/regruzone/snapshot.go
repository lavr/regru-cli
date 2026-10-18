package regruzone

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lavr/regru-cli/internal/regruapi"
	"github.com/lavr/regru-cli/internal/zone"
)

type Observed struct {
	Snapshot  zone.Snapshot
	ServiceID string
	Selectors map[zone.Record]Selector
}

type rawKey struct {
	name, typ, content string
	prio               int
	hasPrio            bool
}

func FromAPI(zoneName string, z regruapi.ZoneRecords) (Observed, error) {
	zn, err := zone.NormalizeZone(zoneName)
	if err != nil {
		return Observed{}, err
	}
	o := Observed{
		Snapshot:  zone.Snapshot{Zone: zn},
		ServiceID: z.ServiceID,
		Selectors: map[zone.Record]Selector{},
	}
	if z.SOA != nil {
		ttl, err := ParseTTL(z.SOA.TTL)
		if err != nil {
			return Observed{}, fmt.Errorf("soa ttl: %w", err)
		}
		min, err := ParseTTL(z.SOA.MinimumTTL)
		if err != nil {
			return Observed{}, fmt.Errorf("soa minimum_ttl: %w", err)
		}
		o.Snapshot.SOA = &zone.SOA{TTL: ttl, MinimumTTL: min}
	}

	type parsed struct {
		rr    regruapi.RR
		name  string
		rec   zone.Record
		exact bool
	}
	var raws []rawKey
	var order []zone.Record
	groups := map[zone.Record][]parsed{}
	for _, rr := range z.RRs {
		name, rec, ok := canonical(zn, rr)
		if !ok {
			raws = append(raws, toRawKey(name, rr))
			continue
		}
		if _, seen := groups[rec]; !seen {
			order = append(order, rec)
		}
		groups[rec] = append(groups[rec], parsed{rr: rr, name: name, rec: rec, exact: name == rr.Subname})
	}
	for _, rec := range order {
		g := groups[rec]
		if len(g) > 1 {
			for _, p := range g {
				raws = append(raws, toRawKey(p.name, p.rr))
			}
			continue
		}
		o.Snapshot.Records = append(o.Snapshot.Records, rec)
		if g[0].exact {
			o.Selectors[rec] = selectorOf(g[0].rr)
		}
	}

	counts := map[rawKey]int{}
	var rawOrder []rawKey
	for _, k := range raws {
		if counts[k] == 0 {
			rawOrder = append(rawOrder, k)
		}
		counts[k]++
	}
	for _, k := range rawOrder {
		r := zone.Raw{Name: k.name, Type: k.typ, Content: k.content, Count: counts[k]}
		if k.hasPrio {
			p := k.prio
			r.Prio = &p
		}
		o.Snapshot.Raw = append(o.Snapshot.Raw, r)
	}
	return o, nil
}

func selectorOf(rr regruapi.RR) Selector {
	s := Selector{Priority: "0", Content: rr.Content}
	if rr.Prio.Valid {
		s.Priority = strconv.Itoa(rr.Prio.Value)
	}
	return s
}

func toRawKey(name string, rr regruapi.RR) rawKey {
	return rawKey{name: name, typ: strings.ToUpper(rr.Rectype), content: rr.Content, prio: rr.Prio.Value, hasPrio: rr.Prio.Valid}
}

func canonical(zn string, rr regruapi.RR) (string, zone.Record, bool) {
	name, err := zone.NormalizeName(zn, rr.Subname)
	if err != nil {
		return strings.ToLower(rr.Subname), zone.Record{}, false
	}
	if strings.HasSuffix(rr.Subname, ".") {
		return name, zone.Record{}, false
	}
	t := zone.Type(rr.Rectype)
	if !writable(t) {
		return name, zone.Record{}, false
	}
	var value string
	switch t {
	case zone.MX:
		if !rr.Prio.Valid {
			return name, zone.Record{}, false
		}
		value, err = zone.ParseValue(t, strconv.Itoa(rr.Prio.Value)+" "+rr.Content)
	case zone.SRV:
		if !rr.Prio.Valid {
			return name, zone.Record{}, false
		}
		value, err = zone.ParseValue(t, strconv.Itoa(rr.Prio.Value)+" "+rr.Content)
		if err == nil && !srvFieldsMatch(value, rr) {
			return name, zone.Record{}, false
		}
	case zone.CAA:
		value, err = parseCAAContent(rr.Content)
	default:
		value, err = zone.ParseValue(t, rr.Content)
	}
	if err != nil {
		return name, zone.Record{}, false
	}
	return name, zone.Record{Name: name, Type: t, Value: value}, true
}

func srvFieldsMatch(value string, rr regruapi.RR) bool {
	f := strings.SplitN(value, " ", 4)
	if rr.Weight != nil && (!rr.Weight.Valid || strconv.Itoa(rr.Weight.Value) != f[1]) {
		return false
	}
	if rr.Port != nil && (!rr.Port.Valid || strconv.Itoa(rr.Port.Value) != f[2]) {
		return false
	}
	return true
}

func parseCAAContent(s string) (string, error) {
	bad := func(reason string) (string, error) {
		return "", &zone.ValueError{Type: zone.CAA, Input: s, Reason: reason}
	}
	f := strings.SplitN(s, " ", 3)
	if len(f) != 3 {
		return bad("expected <flags> <tag> \"<value>\"")
	}
	q := f[2]
	if len(q) < 2 || q[0] != '"' || q[len(q)-1] != '"' {
		return bad("value is not quoted")
	}
	var b strings.Builder
	in := q[1 : len(q)-1]
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch c {
		case '\\':
			if i+1 >= len(in) {
				return bad("dangling escape")
			}
			i++
			b.WriteByte(in[i])
		case '"':
			return bad("unescaped quote in value")
		default:
			b.WriteByte(c)
		}
	}
	v := b.String()
	if v == "" || strings.HasPrefix(v, "\"") && strings.HasSuffix(v, "\"") {
		return bad("value is not representable")
	}
	want := f[0] + " " + f[1] + " " + v
	canon, err := zone.ParseValue(zone.CAA, want)
	if err != nil {
		return "", err
	}
	if canon != want {
		return bad("value is not canonical")
	}
	return canon, nil
}
