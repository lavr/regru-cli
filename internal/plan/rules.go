package plan

import "github.com/lavr/regru-cli/internal/zone"

type Scope string

const (
	ScopeListed  Scope = "listed-rrsets"
	ScopeManaged Scope = "managed-zone"
)

type Rules struct {
	Caps          map[zone.Type]zone.Cap
	Protected     func(zone.Key) bool
	ValidateWrite func(zone.Record) error
}

func (r Rules) protected(k zone.Key) bool {
	return r.Protected != nil && r.Protected(k)
}

func (r Rules) writable(t zone.Type) bool {
	return r.Caps[t].Write
}
