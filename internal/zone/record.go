package zone

import "sort"

type Type string

const (
	A     Type = "A"
	AAAA  Type = "AAAA"
	CNAME Type = "CNAME"
	MX    Type = "MX"
	TXT   Type = "TXT"
	NS    Type = "NS"
	SRV   Type = "SRV"
	CAA   Type = "CAA"
	HTTPS Type = "HTTPS"
)

type Record struct {
	Name  string
	Type  Type
	Value string
}

type Key struct {
	Name string
	Type Type
}

func (r Record) Key() Key {
	return Key{Name: r.Name, Type: r.Type}
}

type Raw struct {
	Name    string
	Type    string
	Content string
	Prio    *int
	Count   int
}

type SOA struct {
	TTL        int
	MinimumTTL int
}

type Snapshot struct {
	Zone    string
	Records []Record
	Raw     []Raw
	SOA     *SOA
}

type Cap struct {
	Read        bool
	Write       bool
	ExactDelete bool
}

func (s Snapshot) Sorted() Snapshot {
	out := Snapshot{Zone: s.Zone, SOA: s.SOA}
	out.Records = append([]Record(nil), s.Records...)
	sort.SliceStable(out.Records, func(i, j int) bool {
		a, b := out.Records[i], out.Records[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Value < b.Value
	})
	out.Raw = append([]Raw(nil), s.Raw...)
	sort.SliceStable(out.Raw, func(i, j int) bool {
		a, b := out.Raw[i], out.Raw[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Content < b.Content
	})
	return out
}
