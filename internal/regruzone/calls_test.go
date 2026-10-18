package regruzone

import (
	"reflect"
	"testing"

	"github.com/lavr/regru-cli/internal/zone"
)

func TestAddCall(t *testing.T) {
	cases := []struct {
		r      zone.Record
		fn     string
		params map[string]string
	}{
		{zone.Record{Name: "@", Type: zone.A, Value: "192.0.2.1"}, "add_alias", map[string]string{"subdomain": "@", "ipaddr": "192.0.2.1"}},
		{zone.Record{Name: "v6", Type: zone.AAAA, Value: "2001:db8::1"}, "add_aaaa", map[string]string{"subdomain": "v6", "ipaddr": "2001:db8::1"}},
		{zone.Record{Name: "c", Type: zone.CNAME, Value: "t1.example.net."}, "add_cname", map[string]string{"subdomain": "c", "canonical_name": "t1.example.net."}},
		{zone.Record{Name: "m", Type: zone.MX, Value: "10 mx-a.example.net."}, "add_mx", map[string]string{"subdomain": "m", "priority": "10", "mail_server": "mx-a.example.net."}},
		{zone.Record{Name: "t", Type: zone.TXT, Value: "a \"b\" c"}, "add_txt", map[string]string{"subdomain": "t", "text": "a \"b\" c"}},
		{zone.Record{Name: "ns.probe", Type: zone.NS, Value: "ns1.example.net."}, "add_ns", map[string]string{"subdomain": "ns.probe", "dns_server": "ns1.example.net."}},
		{zone.Record{Name: "_s1._tcp.probe", Type: zone.SRV, Value: "10 5 5060 sip.example.net."}, "add_srv", map[string]string{"service": "_s1._tcp.probe", "priority": "10", "weight": "5", "port": "5060", "target": "sip.example.net."}},
		{zone.Record{Name: "caa1.probe", Type: zone.CAA, Value: "0 issue a.example.net"}, "add_caa", map[string]string{"subdomain": "caa1.probe", "flags": "0", "tag": "issue", "value": "a.example.net"}},
		{zone.Record{Name: "c", Type: zone.CAA, Value: "128 iodef mailto:x y@example.net"}, "add_caa", map[string]string{"subdomain": "c", "flags": "128", "tag": "iodef", "value": "mailto:x y@example.net"}},
	}
	for _, c := range cases {
		fn, params, err := AddCall(c.r)
		if err != nil {
			t.Fatalf("%v: %v", c.r, err)
		}
		if fn != c.fn || !reflect.DeepEqual(params, c.params) {
			t.Fatalf("%v: %s %v", c.r, fn, params)
		}
	}
}

func TestAddCallSRVUsesService(t *testing.T) {
	_, params, err := AddCall(zone.Record{Name: "_s1._tcp.probe", Type: zone.SRV, Value: "10 5 5060 sip.example.net."})
	if err != nil {
		t.Fatal(err)
	}
	if params["service"] != "_s1._tcp.probe" {
		t.Fatalf("params %v", params)
	}
	if _, ok := params["subdomain"]; ok {
		t.Fatalf("subdomain must be absent: %v", params)
	}
}

func TestAddCallRejects(t *testing.T) {
	cases := map[zone.Record]string{
		{Name: "x", Type: zone.HTTPS, Value: "1 . alpn=h3"}:         "unsupported_type",
		{Name: "x", Type: "SOA", Value: "x"}:                        "unsupported_type",
		{Name: "c", Type: zone.CAA, Value: "0 contactemail a@b.c"}:  "unsupported_value",
		{Name: "m", Type: zone.MX, Value: "mx.example.net."}:        "invalid_value",
		{Name: "s", Type: zone.SRV, Value: "10 5 sip.example.net."}: "invalid_value",
		{Name: "a", Type: zone.A, Value: "not-an-ip"}:               "invalid_value",
	}
	for r, code := range cases {
		fn, params, err := AddCall(r)
		if err == nil {
			t.Fatalf("%v: %s %v", r, fn, params)
		}
		ce, ok := err.(interface{ Code() string })
		if !ok || ce.Code() != code {
			t.Fatalf("%v: %v, want %s", r, err, code)
		}
	}
}

func TestValidateWrite(t *testing.T) {
	for _, tag := range []string{"issue", "issuewild", "iodef"} {
		if err := ValidateWrite(zone.Record{Name: "c", Type: zone.CAA, Value: "0 " + tag + " a.example.net"}); err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
	}
	err := ValidateWrite(zone.Record{Name: "c", Type: zone.CAA, Value: "0 contactphone 1"})
	ce, ok := err.(interface{ Code() string })
	if !ok || ce.Code() != "unsupported_value" {
		t.Fatalf("%v", err)
	}
	if err := ValidateWrite(zone.Record{Name: "a", Type: zone.A, Value: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveCall(t *testing.T) {
	r := zone.Record{Name: "caa1.probe", Type: zone.CAA, Value: "0 issue a.example.net"}
	fn, params := RemoveCall(r, Selector{Priority: "0", Content: "0 issue \"a.example.net\""})
	want := map[string]string{"subdomain": "caa1.probe", "record_type": "CAA", "priority": "0", "content": "0 issue \"a.example.net\""}
	if fn != "remove_record" || !reflect.DeepEqual(params, want) {
		t.Fatalf("%s %v", fn, params)
	}
	fn, params = RemoveCall(zone.Record{Name: "_s1._tcp.probe", Type: zone.SRV, Value: "10 5 5060 sip.example.net."}, Selector{Priority: "10", Content: "5 5060 sip.example.net."})
	want = map[string]string{"subdomain": "_s1._tcp.probe", "record_type": "SRV", "priority": "10", "content": "5 5060 sip.example.net."}
	if fn != "remove_record" || !reflect.DeepEqual(params, want) {
		t.Fatalf("%s %v", fn, params)
	}
}
