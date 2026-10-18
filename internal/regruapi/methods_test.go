package regruapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func okResp(answer string) string {
	return `{"result":"success","answer":` + answer + `}`
}

func TestFlexInt(t *testing.T) {
	cases := []struct {
		in    string
		val   int
		valid bool
		fail  bool
	}{
		{`10`, 10, true, false},
		{`"10"`, 10, true, false},
		{`null`, 0, false, false},
		{`"x"`, 0, false, true},
		{`1.5`, 0, false, true},
		{`true`, 0, false, true},
	}
	for _, tc := range cases {
		var f FlexInt
		err := json.Unmarshal([]byte(tc.in), &f)
		if (err != nil) != tc.fail {
			t.Fatalf("%s: err %v", tc.in, err)
		}
		if !tc.fail && (f.Value != tc.val || f.Valid != tc.valid) {
			t.Fatalf("%s: %+v", tc.in, f)
		}
	}
}

func TestGetResourceRecordsSRV(t *testing.T) {
	srv, _ := newServer(t, body(loadFixture(t, "zone_get_resource_records_srv_pair")))
	c := testClient(srv.URL, newFakeClock())
	z, err := c.GetResourceRecords(context.Background(), "example.test")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range z.RRs {
		if r.Rectype == "SRV" && r.Subname == "_s1._tcp.probe" && r.Content == "5 5060 sip.example.net." {
			found = true
			if !r.Prio.Valid || r.Prio.Value != 10 {
				t.Fatalf("prio %+v", r.Prio)
			}
			if r.Port == nil || r.Port.Value != 5060 || r.Weight == nil || r.Weight.Value != 5 {
				t.Fatalf("port/weight %+v %+v", r.Port, r.Weight)
			}
		}
	}
	if !found {
		t.Fatal("srv record not found")
	}
	if z.ServiceID != "1000001" || z.Domain != "example.test" {
		t.Fatalf("zone %+v", z)
	}
	if z.SOA == nil || z.SOA.TTL != "1d" || z.SOA.MinimumTTL != "3h" {
		t.Fatalf("soa %+v", z.SOA)
	}
}

func TestGetResourceRecordsAAAANullPrio(t *testing.T) {
	srv, _ := newServer(t, body(loadFixture(t, "zone_get_resource_records_aaaa_pair")))
	c := testClient(srv.URL, newFakeClock())
	z, err := c.GetResourceRecords(context.Background(), "example.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range z.RRs {
		if r.Rectype == "AAAA" {
			if r.Prio.Valid {
				t.Fatalf("prio %+v", r.Prio)
			}
			return
		}
	}
	t.Fatal("no AAAA")
}

func TestReadIncomplete(t *testing.T) {
	cases := map[string]string{
		"empty domains": `{"domains":[]}`,
		"other domain":  `{"domains":[{"dname":"other.test","result":"success","rrs":[],"service_id":"1"}]}`,
		"no rrs":        `{"domains":[{"dname":"example.test","result":"success","service_id":"1"}]}`,
		"null rrs":      `{"domains":[{"dname":"example.test","result":"success","rrs":null,"service_id":"1"}]}`,
		"no service id": `{"domains":[{"dname":"example.test","result":"success","rrs":[]}]}`,
		"no answer":     `null`,
		"two domains": `{"domains":[{"dname":"example.test","result":"success","rrs":[],"service_id":"1"},` +
			`{"dname":"example.test","result":"success","rrs":[],"service_id":"1"}]}`,
	}
	for name, ans := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := newServer(t, body(okResp(ans)))
			c := testClient(srv.URL, newFakeClock())
			_, err := c.GetResourceRecords(context.Background(), "example.test")
			var ie *IncompleteReadError
			if !errors.As(err, &ie) {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestReadEmptyZone(t *testing.T) {
	ans := `{"domains":[{"dname":"example.test","result":"success","rrs":[],"service_id":"1"}]}`
	srv, _ := newServer(t, body(okResp(ans)))
	c := testClient(srv.URL, newFakeClock())
	z, err := c.GetResourceRecords(context.Background(), "example.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(z.RRs) != 0 || z.ServiceID != "1" {
		t.Fatalf("zone %+v", z)
	}
}

func TestGetResourceRecordsDomainError(t *testing.T) {
	ans := `{"domains":[{"dname":"example.test","result":"error","error_code":"DOMAIN_NOT_FOUND","error_text":"nope","error_params":{}}]}`
	srv, _ := newServer(t, body(okResp(ans)))
	c := testClient(srv.URL, newFakeClock())
	_, err := c.GetResourceRecords(context.Background(), "example.test")
	var de *DomainError
	if !errors.As(err, &de) || de.Code != "DOMAIN_NOT_FOUND" || de.Domain != "example.test" {
		t.Fatalf("err %v", err)
	}
}

func TestMutateAmbiguous(t *testing.T) {
	cases := map[string]string{
		"empty domains": `{"domains":[]}`,
		"other domain":  `{"domains":[{"dname":"other.test","result":"success"}]}`,
		"no result":     `{"domains":[{"dname":"example.test"}]}`,
		"two elements":  `{"domains":[{"dname":"example.test","result":"success"},{"dname":"example.test","result":"success"}]}`,
		"no answer":     `null`,
	}
	for name, ans := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := newServer(t, body(okResp(ans)))
			c := testClient(srv.URL, newFakeClock())
			err := c.ZoneMutate(context.Background(), "add_txt", "example.test", map[string]string{"subdomain": "a", "text": "b"})
			var ue *UnknownOutcomeError
			if !errors.As(err, &ue) {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestZoneMutateSuccess(t *testing.T) {
	var path, data string
	srv, _ := newServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		parseForm(t, r)
		path, data = r.URL.Path, r.PostForm.Get("input_data")
		writeBody(t, w, loadFixture(t, "zone_add_txt_success"))
	})
	c := testClient(srv.URL, newFakeClock())
	err := c.ZoneMutate(context.Background(), "add_txt", "example.test", map[string]string{"subdomain": "a", "text": "b"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/zone/add_txt" {
		t.Fatalf("path %s", path)
	}
	var in struct {
		Subdomain string `json:"subdomain"`
		Domains   []struct {
			Dname string `json:"dname"`
		} `json:"domains"`
	}
	if err := json.Unmarshal([]byte(data), &in); err != nil {
		t.Fatal(err)
	}
	if in.Subdomain != "a" || len(in.Domains) != 1 || in.Domains[0].Dname != "example.test" {
		t.Fatalf("input %s", data)
	}
}

func TestZoneMutateDomainError(t *testing.T) {
	srv, _ := newServer(t, body(loadFixture(t, "zone_add_txt_domain_error_text_contains_invalid_characters")))
	c := testClient(srv.URL, newFakeClock())
	err := c.ZoneMutate(context.Background(), "add_txt", "example.test", map[string]string{"subdomain": "a", "text": "b"})
	var de *DomainError
	if !errors.As(err, &de) || de.Code != "TEXT_CONTAINS_INVALID_CHARACTERS" {
		t.Fatalf("err %v", err)
	}
}

func TestZoneMutateConflictParams(t *testing.T) {
	srv, _ := newServer(t, body(loadFixture(t, "zone_add_cname_domain_error_conflict_cname")))
	c := testClient(srv.URL, newFakeClock())
	err := c.ZoneMutate(context.Background(), "add_cname", "example.test", map[string]string{"subdomain": "c1.probe", "canonical_name": "t1.example.net."})
	var de *DomainError
	if !errors.As(err, &de) || !strings.Contains(string(de.Params), "conflicting_records") {
		t.Fatalf("err %v", err)
	}
}

func TestDomainErrorRedactsPassword(t *testing.T) {
	ans := `{"domains":[{"dname":"example.test","result":"error","error_code":"X","error_text":"bad s3cr3t-pass","error_params":{"k":"s3cr3t-pass"}}]}`
	srv, _ := newServer(t, body(okResp(ans)))
	c := testClient(srv.URL, newFakeClock())
	err := c.ZoneMutate(context.Background(), "add_txt", "example.test", nil)
	var de *DomainError
	if !errors.As(err, &de) {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(string(de.Params), "s3cr3t-pass") || strings.Contains(de.Text, "s3cr3t-pass") || strings.Contains(err.Error(), "s3cr3t-pass") {
		t.Fatalf("leak %+v", de)
	}
}

func TestZoneMutateRejectsMethod(t *testing.T) {
	srv, n := newServer(t, body(okResp(`{}`)))
	c := testClient(srv.URL, newFakeClock())
	err := c.ZoneMutate(context.Background(), "clear", "example.test", nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported_method") {
		t.Fatalf("err %v", err)
	}
	if n.Load() != 0 {
		t.Fatalf("requests %d", n.Load())
	}
}

func TestNop(t *testing.T) {
	srv, _ := newServer(t, body(loadFixture(t, "nop_success")))
	c := testClient(srv.URL, newFakeClock())
	login, id, err := c.Nop(context.Background())
	if err != nil || login != "user" || id != "1000" {
		t.Fatalf("%q %q %v", login, id, err)
	}
}

func TestIsPartnerFalse(t *testing.T) {
	var path string
	srv, _ := newServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		writeBody(t, w, `{"result":"error","error_code":"RESELLER_AUTH_FAILED","error_text":"x","error_params":{}}`)
	})
	c := testClient(srv.URL, newFakeClock())
	ok, err := c.IsPartner(context.Background())
	if err != nil || ok || path != "/reseller_nop" {
		t.Fatalf("%v %v %s", ok, err, path)
	}
}

func TestIsPartnerTrue(t *testing.T) {
	var path string
	srv, _ := newServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		writeBody(t, w, okResp(`{}`))
	})
	c := testClient(srv.URL, newFakeClock())
	ok, err := c.IsPartner(context.Background())
	if err != nil || !ok || path != "/reseller_nop" {
		t.Fatalf("%v %v %s", ok, err, path)
	}
}

func TestIsPartnerOtherError(t *testing.T) {
	srv, _ := newServer(t, body(`{"result":"error","error_code":"ACCESS_DENIED","error_text":"x","error_params":{}}`))
	c := testClient(srv.URL, newFakeClock())
	ok, err := c.IsPartner(context.Background())
	var ae *APIError
	if ok || !errors.As(err, &ae) {
		t.Fatalf("%v %v", ok, err)
	}
}

func TestListDomains(t *testing.T) {
	var data string
	srv, _ := newServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		parseForm(t, r)
		data = r.PostForm.Get("input_data")
		writeBody(t, w, loadFixture(t, "service_get_list_synthetic"))
	})
	c := testClient(srv.URL, newFakeClock())
	svcs, err := c.ListDomains(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 2 || svcs[0].Domain != "example.test" || svcs[0].ServiceID != "1000001" || svcs[0].State != "A" {
		t.Fatalf("svcs %+v", svcs)
	}
	if !strings.Contains(data, `"servtype":"domain"`) {
		t.Fatalf("input %s", data)
	}
}

func TestListDomainsIncomplete(t *testing.T) {
	for _, ans := range []string{`{}`, `{"services":null}`, `null`} {
		srv, _ := newServer(t, body(okResp(ans)))
		c := testClient(srv.URL, newFakeClock())
		_, err := c.ListDomains(context.Background())
		var ie *IncompleteReadError
		if !errors.As(err, &ie) {
			t.Fatalf("%s: err %v", ans, err)
		}
	}
}

func nssHandler(t *testing.T, limit int, sizes *[]int) respFn {
	return func(n int, w http.ResponseWriter, r *http.Request) {
		parseForm(t, r)
		var in struct {
			Domains []struct {
				Dname string `json:"dname"`
			} `json:"domains"`
		}
		if err := json.Unmarshal([]byte(r.PostForm.Get("input_data")), &in); err != nil {
			t.Error(err)
		}
		*sizes = append(*sizes, len(in.Domains))
		if len(in.Domains) > limit {
			writeBody(t, w, `{"result":"error","error_code":"TOO_MANY_OBJECTS_IN_ONE_REQUEST","error_text":"x","error_params":{}}`)
			return
		}
		var items []string
		for _, d := range in.Domains {
			items = append(items, fmt.Sprintf(`{"dname":%q,"service_id":"s-%s","nss":[{"ns":"ns1.reg.ru"}]}`, d.Dname, d.Dname))
		}
		writeBody(t, w, okResp(`{"domains":[`+strings.Join(items, ",")+`]}`))
	}
}

func TestGetNSSSplitsOnTooMany(t *testing.T) {
	var sizes []int
	srv, _ := newServer(t, nssHandler(t, 2, &sizes))
	c := testClient(srv.URL, newFakeClock())
	in := []string{"a.example.test", "b.example.test", "c.example.test", "d.example.test", "e.example.test"}
	out, err := c.GetNSS(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 5 {
		t.Fatalf("out %+v", out)
	}
	for i, d := range in {
		if out[i].Domain != d || len(out[i].NSS) != 1 || out[i].NSS[0] != "ns1.reg.ru" || out[i].ServiceID != "s-"+d {
			t.Fatalf("out[%d] %+v", i, out[i])
		}
	}
	if sizes[0] != 5 {
		t.Fatalf("first request %v", sizes)
	}
}

func TestGetNSSSingleTooMany(t *testing.T) {
	var sizes []int
	srv, n := newServer(t, nssHandler(t, 0, &sizes))
	c := testClient(srv.URL, newFakeClock())
	_, err := c.GetNSS(context.Background(), []string{"a.example.test", "b.example.test", "c.example.test", "d.example.test"})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "TOO_MANY_OBJECTS_IN_ONE_REQUEST" {
		t.Fatalf("err %v", err)
	}
	if n.Load() != 7 {
		t.Fatalf("requests %d", n.Load())
	}
}

func TestGetNSSMissingDomain(t *testing.T) {
	ans := `{"domains":[{"dname":"a.example.test","service_id":"1","nss":[{"ns":"ns1.reg.ru"}]}]}`
	srv, _ := newServer(t, body(okResp(ans)))
	c := testClient(srv.URL, newFakeClock())
	_, err := c.GetNSS(context.Background(), []string{"a.example.test", "b.example.test"})
	var ie *IncompleteReadError
	if !errors.As(err, &ie) {
		t.Fatalf("err %v", err)
	}
}

func TestGetNSSDomainError(t *testing.T) {
	ans := `{"domains":[{"dname":"a.example.test","result":"error","error_code":"DOMAIN_NOT_FOUND","error_text":"x","error_params":{}}]}`
	srv, _ := newServer(t, body(okResp(ans)))
	c := testClient(srv.URL, newFakeClock())
	out, err := c.GetNSS(context.Background(), []string{"a.example.test"})
	if err != nil || len(out) != 1 || out[0].Err == nil || out[0].Err.Code != "DOMAIN_NOT_FOUND" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestGetNSSFixture(t *testing.T) {
	srv, _ := newServer(t, body(loadFixture(t, "domain_get_nss_success")))
	c := testClient(srv.URL, newFakeClock())
	out, err := c.GetNSS(context.Background(), []string{"example.test"})
	if err != nil || len(out) != 1 || len(out[0].NSS) != 2 || out[0].NSS[1] != "ns2.reg.ru" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestGetNSSStrictItems(t *testing.T) {
	cases := map[string]string{
		"no nss":        `{"dname":"a.example.test","service_id":"1"}`,
		"null nss":      `{"dname":"a.example.test","service_id":"1","nss":null}`,
		"empty nss":     `{"dname":"a.example.test","service_id":"1","nss":[]}`,
		"no service id": `{"dname":"a.example.test","nss":[{"ns":"ns1.reg.ru"}]}`,
		"bare item":     `{"dname":"a.example.test"}`,
	}
	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := newServer(t, body(okResp(`{"domains":[`+item+`]}`)))
			c := testClient(srv.URL, newFakeClock())
			_, err := c.GetNSS(context.Background(), []string{"a.example.test"})
			var ie *IncompleteReadError
			if !errors.As(err, &ie) {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestGetNSSErrorCodeWithoutResult(t *testing.T) {
	ans := `{"domains":[{"dname":"a.example.test","error_code":"DOMAIN_NOT_FOUND","error_text":"x","error_params":{}}]}`
	srv, _ := newServer(t, body(okResp(ans)))
	c := testClient(srv.URL, newFakeClock())
	out, err := c.GetNSS(context.Background(), []string{"a.example.test"})
	if err != nil || len(out) != 1 || out[0].Err == nil || out[0].Err.Code != "DOMAIN_NOT_FOUND" {
		t.Fatalf("%+v %v", out, err)
	}
}
