package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/lavr/regru-cli/internal/zone"
)

const fingerprintPrefix = "fp1:sha256:"

type fpRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

type fpRaw struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Prio    *int   `json:"prio"`
	Count   int    `json:"count"`
}

type fpSOA struct {
	TTL        int `json:"ttl"`
	MinimumTTL int `json:"minimum_ttl"`
}

type fpDoc struct {
	Zone      string     `json:"zone"`
	ServiceID string     `json:"service_id"`
	Records   []fpRecord `json:"records"`
	Raw       []fpRaw    `json:"raw"`
	SOA       *fpSOA     `json:"soa"`
}

func lessRaw(a, b zone.Raw) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	if a.Type != b.Type {
		return a.Type < b.Type
	}
	if a.Content != b.Content {
		return a.Content < b.Content
	}
	if (a.Prio == nil) != (b.Prio == nil) {
		return a.Prio == nil
	}
	if a.Prio != nil && *a.Prio != *b.Prio {
		return *a.Prio < *b.Prio
	}
	return a.Count < b.Count
}

func Fingerprint(zoneName, serviceID string, s zone.Snapshot) string {
	doc := fpDoc{Zone: zoneName, ServiceID: serviceID, Records: []fpRecord{}, Raw: []fpRaw{}}
	recs := append([]zone.Record(nil), s.Records...)
	sort.Slice(recs, func(i, j int) bool { return lessRecord(recs[i], recs[j]) })
	for _, r := range recs {
		doc.Records = append(doc.Records, fpRecord{Name: r.Name, Type: string(r.Type), Value: r.Value})
	}
	raws := append([]zone.Raw(nil), s.Raw...)
	sort.Slice(raws, func(i, j int) bool { return lessRaw(raws[i], raws[j]) })
	for _, r := range raws {
		doc.Raw = append(doc.Raw, fpRaw{Name: r.Name, Type: r.Type, Content: r.Content, Prio: r.Prio, Count: r.Count})
	}
	if s.SOA != nil {
		doc.SOA = &fpSOA{TTL: s.SOA.TTL, MinimumTTL: s.SOA.MinimumTTL}
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return fingerprintPrefix + hex.EncodeToString(sum[:])
}
