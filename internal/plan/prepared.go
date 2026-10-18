package plan

import (
	"github.com/lavr/regru-cli/internal/regruapi"
	"github.com/lavr/regru-cli/internal/zone"
)

type Account struct {
	Alias, Login, UserID string
}

type FilePolicy struct {
	MaxDeletes     int
	AllowEmptyZone bool
}

type Prepared struct {
	PlanID              string
	Account             Account
	Endpoint            string
	Zone                string
	ServiceID           string
	Desired             Desired
	Policy              FilePolicy
	BaselineFingerprint string
	Plan                Plan
}

func copyDesired(d Desired) Desired {
	out := Desired{Scope: d.Scope, RRsets: make(map[zone.Key][]string, len(d.RRsets))}
	for k, v := range d.RRsets {
		out.RRsets[k] = append([]string{}, v...)
	}
	return out
}

func Prepare(acc Account, zoneName, serviceID string, cur zone.Snapshot, want Desired, pol Policy, newID func() string) (Prepared, error) {
	p, err := Build(cur, want, pol)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{
		PlanID:              newID(),
		Account:             acc,
		Endpoint:            regruapi.Endpoint,
		Zone:                zoneName,
		ServiceID:           serviceID,
		Desired:             copyDesired(want),
		Policy:              FilePolicy{MaxDeletes: pol.MaxDeletes, AllowEmptyZone: pol.AllowEmptyZone},
		BaselineFingerprint: Fingerprint(zoneName, serviceID, cur),
		Plan:                p,
	}, nil
}
