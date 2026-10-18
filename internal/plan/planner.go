package plan

import (
	"fmt"
	"sort"

	"github.com/lavr/regru-cli/internal/zone"
)

type Policy struct {
	MaxDeletes     int
	AllowEmptyZone bool
	Rules          Rules
}

type OpKind string

const (
	OpAdd    OpKind = "add"
	OpRemove OpKind = "remove"
)

type Op struct {
	Seq       int
	Kind      OpKind
	Record    zone.Record
	DependsOn []int
}

type Plan struct {
	Ops             []Op
	Warnings        []string
	Initial, Target zone.Snapshot
}

func (p Plan) Empty() bool { return len(p.Ops) == 0 }

func (p Plan) Deletes() int {
	n := 0
	for _, o := range p.Ops {
		if o.Kind == OpRemove {
			n++
		}
	}
	return n
}

type PolicyError struct{ Code, Detail string }

func (e *PolicyError) Error() string { return e.Code + ": " + e.Detail }

func policyErr(code, format string, args ...any) error {
	return &PolicyError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

func copySnapshot(s zone.Snapshot) zone.Snapshot {
	out := zone.Snapshot{Zone: s.Zone}
	if s.Records != nil {
		out.Records = append([]zone.Record{}, s.Records...)
	}
	if s.Raw != nil {
		out.Raw = make([]zone.Raw, len(s.Raw))
		for i, r := range s.Raw {
			if r.Prio != nil {
				p := *r.Prio
				r.Prio = &p
			}
			out.Raw[i] = r
		}
	}
	if s.SOA != nil {
		soa := *s.SOA
		out.SOA = &soa
	}
	return out
}

func lessKey(a, b zone.Key) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.Type < b.Type
}

func lessRecord(a, b zone.Record) bool {
	if a.Key() != b.Key() {
		return lessKey(a.Key(), b.Key())
	}
	return a.Value < b.Value
}

func sortedKeys(m map[zone.Key][]string) []zone.Key {
	keys := make([]zone.Key, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return lessKey(keys[i], keys[j]) })
	return keys
}

func Build(cur zone.Snapshot, want Desired, pol Policy) (Plan, error) {
	r := pol.Rules
	for _, k := range sortedKeys(want.RRsets) {
		if r.protected(k) {
			return Plan{}, policyErr("protected_record", "%s %s is protected", k.Name, k.Type)
		}
		if !r.writable(k.Type) {
			return Plan{}, policyErr("unsupported_type", "%s %s: type %s is not writable", k.Name, k.Type, k.Type)
		}
		if r.ValidateWrite != nil {
			for _, v := range want.RRsets[k] {
				if err := r.ValidateWrite(zone.Record{Name: k.Name, Type: k.Type, Value: v}); err != nil {
					return Plan{}, policyErr("unsupported_value", "%s %s %q: %v", k.Name, k.Type, v, err)
				}
			}
		}
	}

	touched := map[zone.Key]bool{}
	for k := range want.RRsets {
		touched[k] = true
	}
	if want.Scope == ScopeManaged {
		for _, rec := range cur.Records {
			if r.writable(rec.Type) && !r.protected(rec.Key()) {
				touched[rec.Key()] = true
			}
		}
	}
	for _, raw := range cur.Raw {
		k := zone.Key{Name: raw.Name, Type: zone.Type(raw.Type)}
		if touched[k] {
			return Plan{}, policyErr("raw_conflict", "%s %s has records that cannot be managed exactly", k.Name, k.Type)
		}
	}

	target := copySnapshot(cur)
	target.Records = nil
	seen := map[zone.Record]bool{}
	addTarget := func(rec zone.Record) {
		if !seen[rec] {
			seen[rec] = true
			target.Records = append(target.Records, rec)
		}
	}
	for _, rec := range cur.Records {
		if !touched[rec.Key()] {
			addTarget(rec)
		}
	}
	for _, k := range sortedKeys(want.RRsets) {
		for _, v := range want.RRsets[k] {
			addTarget(zone.Record{Name: k.Name, Type: k.Type, Value: v})
		}
	}
	sort.SliceStable(target.Records, func(i, j int) bool { return lessRecord(target.Records[i], target.Records[j]) })

	if err := checkCNAME(target, touched, r); err != nil {
		return Plan{}, err
	}

	curSet := map[zone.Record]bool{}
	for _, rec := range cur.Records {
		curSet[rec] = true
	}
	var adds, removes []zone.Record
	for _, rec := range target.Records {
		if !curSet[rec] {
			adds = append(adds, rec)
		}
	}
	removed := map[zone.Record]bool{}
	for _, rec := range cur.Records {
		if !seen[rec] && !removed[rec] {
			removed[rec] = true
			removes = append(removes, rec)
		}
	}
	sort.Slice(removes, func(i, j int) bool { return lessRecord(removes[i], removes[j]) })

	for _, rec := range removes {
		if !r.Caps[rec.Type].ExactDelete {
			return Plan{}, policyErr("unsupported_exact_delete", "%s %s %q cannot be deleted exactly", rec.Name, rec.Type, rec.Value)
		}
	}
	if len(removes) > pol.MaxDeletes {
		return Plan{}, policyErr("max_deletes_exceeded", "plan deletes %d records, limit is %d", len(removes), pol.MaxDeletes)
	}
	if len(removes) > 0 && !pol.AllowEmptyZone && !hasWritableData(target, r) {
		return Plan{}, policyErr("empty_zone_not_allowed", "plan leaves zone %s without writable records", cur.Zone)
	}

	ops, warnings := order(adds, removes)
	return Plan{Ops: ops, Warnings: warnings, Initial: copySnapshot(cur), Target: target}, nil
}

func hasWritableData(target zone.Snapshot, r Rules) bool {
	for _, rec := range target.Records {
		if r.writable(rec.Type) && !r.protected(rec.Key()) {
			return true
		}
	}
	for _, raw := range target.Raw {
		if r.writable(zone.Type(raw.Type)) {
			return true
		}
	}
	return false
}

func checkCNAME(target zone.Snapshot, touched map[zone.Key]bool, r Rules) error {
	type info struct {
		cnames, others int
		scope, hard    bool
	}
	names := map[string]*info{}
	get := func(n string) *info {
		if names[n] == nil {
			names[n] = &info{}
		}
		return names[n]
	}
	count := func(in *info, t zone.Type) {
		if t == zone.CNAME {
			in.cnames++
		} else {
			in.others++
		}
	}
	for _, rec := range target.Records {
		in := get(rec.Name)
		count(in, rec.Type)
		k := rec.Key()
		switch {
		case r.protected(k) || !r.writable(k.Type):
			in.hard = true
		case !touched[k]:
			in.scope = true
		}
	}
	for _, raw := range target.Raw {
		in := get(raw.Name)
		count(in, zone.Type(raw.Type))
		in.hard = true
	}
	list := make([]string, 0, len(names))
	for n := range names {
		list = append(list, n)
	}
	sort.Strings(list)
	for _, n := range list {
		in := names[n]
		if in.cnames == 0 || (in.cnames == 1 && in.others == 0) {
			continue
		}
		if in.scope {
			return policyErr("scope_conflict", "%s: CNAME conflicts with records outside the desired scope", n)
		}
		if in.hard {
			return policyErr("cname_conflict", "%s: CNAME conflicts with protected or unmanaged records", n)
		}
		return policyErr("cname_conflict", "%s: CNAME must be the only record at the name", n)
	}
	return nil
}

type nameOps struct {
	adds, removes []zone.Record
}

func order(adds, removes []zone.Record) ([]Op, []string) {
	byName := map[string]*nameOps{}
	var names []string
	group := func(n string) *nameOps {
		if byName[n] == nil {
			byName[n] = &nameOps{}
			names = append(names, n)
		}
		return byName[n]
	}
	for _, rec := range adds {
		g := group(rec.Name)
		g.adds = append(g.adds, rec)
	}
	for _, rec := range removes {
		g := group(rec.Name)
		g.removes = append(g.removes, rec)
	}
	sort.Strings(names)

	var ops []Op
	var warnings []string
	next := func(kind OpKind, rec zone.Record, deps []int) int {
		seq := len(ops) + 1
		ops = append(ops, Op{Seq: seq, Kind: kind, Record: rec, DependsOn: deps})
		return seq
	}
	for _, n := range names {
		g := byName[n]
		sort.Slice(g.adds, func(i, j int) bool { return lessRecord(g.adds[i], g.adds[j]) })
		sort.Slice(g.removes, func(i, j int) bool { return lessRecord(g.removes[i], g.removes[j]) })
		cname := false
		for _, rec := range g.adds {
			cname = cname || rec.Type == zone.CNAME
		}
		for _, rec := range g.removes {
			cname = cname || rec.Type == zone.CNAME
		}
		if cname {
			var rm []int
			for _, rec := range g.removes {
				rm = append(rm, next(OpRemove, rec, nil))
			}
			for _, rec := range g.adds {
				next(OpAdd, rec, append([]int(nil), rm...))
			}
			if len(g.removes) > 0 && len(g.adds) > 0 {
				warnings = append(warnings, n+": remove before add, name stays empty until add succeeds")
			}
			continue
		}
		added := map[zone.Key][]int{}
		for _, rec := range g.adds {
			added[rec.Key()] = append(added[rec.Key()], next(OpAdd, rec, nil))
		}
		for _, rec := range g.removes {
			next(OpRemove, rec, append([]int(nil), added[rec.Key()]...))
		}
	}
	return ops, warnings
}

func DesiredAdd(cur zone.Snapshot, k zone.Key, values []string) Desired {
	vals := []string{}
	seen := map[string]bool{}
	push := func(v string) {
		if !seen[v] {
			seen[v] = true
			vals = append(vals, v)
		}
	}
	for _, rec := range cur.Records {
		if rec.Key() == k {
			push(rec.Value)
		}
	}
	for _, v := range values {
		push(v)
	}
	return Desired{Scope: ScopeListed, RRsets: map[zone.Key][]string{k: vals}}
}

func DesiredRemove(cur zone.Snapshot, k zone.Key, values []string, all bool) Desired {
	drop := map[string]bool{}
	for _, v := range values {
		drop[v] = true
	}
	vals := []string{}
	seen := map[string]bool{}
	if !all {
		for _, rec := range cur.Records {
			if rec.Key() == k && !drop[rec.Value] && !seen[rec.Value] {
				seen[rec.Value] = true
				vals = append(vals, rec.Value)
			}
		}
	}
	return Desired{Scope: ScopeListed, RRsets: map[zone.Key][]string{k: vals}}
}

func DesiredSet(k zone.Key, values []string) (Desired, error) {
	if len(values) == 0 {
		return Desired{}, &zone.ValueError{Type: k.Type, Reason: "set requires at least one value"}
	}
	return Desired{Scope: ScopeListed, RRsets: map[zone.Key][]string{k: append([]string(nil), values...)}}, nil
}
