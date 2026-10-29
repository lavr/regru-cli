package regruapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type domainItem struct {
	Dname       string          `json:"dname"`
	Result      string          `json:"result"`
	ServiceID   FlexString      `json:"service_id"`
	ErrorCode   string          `json:"error_code"`
	ErrorText   string          `json:"error_text"`
	ErrorParams json.RawMessage `json:"error_params"`
	RRs         *[]RR           `json:"rrs"`
	NSS         *[]struct {
		NS string `json:"ns"`
	} `json:"nss"`
	SOA *struct {
		TTL        FlexString `json:"ttl"`
		MinimumTTL FlexString `json:"minimum_ttl"`
	} `json:"soa"`
}

func hasAnswer(ans json.RawMessage) bool {
	t := bytes.TrimSpace(ans)
	return len(t) > 0 && string(t) != "null"
}

func (c *Client) domainErr(d domainItem) *DomainError {
	return &DomainError{
		Domain: c.cr.redact(d.Dname),
		Code:   c.cr.redact(d.ErrorCode),
		Text:   c.cr.redact(d.ErrorText),
		Params: c.cr.redactParams(d.ErrorParams),
	}
}

func (c *Client) decodeDomains(method string, ans json.RawMessage) ([]domainItem, error) {
	if !hasAnswer(ans) {
		return nil, &IncompleteReadError{Method: method, Reason: "response has no answer"}
	}
	var a struct {
		Domains *[]domainItem `json:"domains"`
	}
	if err := json.Unmarshal(ans, &a); err != nil {
		return nil, &IncompleteReadError{Method: method, Reason: "answer is not decodable"}
	}
	if a.Domains == nil {
		return nil, &IncompleteReadError{Method: method, Reason: "answer has no domains"}
	}
	return *a.Domains, nil
}

func (c *Client) Nop(ctx context.Context) (string, string, error) {
	ans, err := c.Call(ctx, Read, "nop", nil)
	if err != nil {
		return "", "", err
	}
	if !hasAnswer(ans) {
		return "", "", &IncompleteReadError{Method: "nop", Reason: "response has no answer"}
	}
	var a struct {
		Login  string     `json:"login"`
		UserID FlexString `json:"user_id"`
	}
	if err := json.Unmarshal(ans, &a); err != nil {
		return "", "", &IncompleteReadError{Method: "nop", Reason: "answer is not decodable"}
	}
	if a.Login == "" || a.UserID == "" {
		return "", "", &IncompleteReadError{Method: "nop", Reason: "answer lacks login or user_id"}
	}
	return a.Login, string(a.UserID), nil
}

func (c *Client) IsPartner(ctx context.Context) (bool, error) {
	_, err := c.Call(ctx, Read, "reseller_nop", nil)
	if err == nil {
		return true, nil
	}
	var ae *APIError
	if errors.As(err, &ae) && ae.Code == "RESELLER_AUTH_FAILED" {
		return false, nil
	}
	return false, err
}

func (c *Client) ListDomains(ctx context.Context) ([]Service, error) {
	const method = "service/get_list"
	ans, err := c.Call(ctx, Read, method, map[string]any{"servtype": "domain"})
	if err != nil {
		return nil, err
	}
	if !hasAnswer(ans) {
		return nil, &IncompleteReadError{Method: method, Reason: "response has no answer"}
	}
	var a struct {
		Services *[]struct {
			Dname     string     `json:"dname"`
			ServiceID FlexString `json:"service_id"`
			State     string     `json:"state"`
		} `json:"services"`
	}
	if err := json.Unmarshal(ans, &a); err != nil {
		return nil, &IncompleteReadError{Method: method, Reason: "answer is not decodable"}
	}
	if a.Services == nil {
		return nil, &IncompleteReadError{Method: method, Reason: "answer has no services array"}
	}
	out := make([]Service, 0, len(*a.Services))
	for _, s := range *a.Services {
		out = append(out, Service{Domain: s.Dname, ServiceID: string(s.ServiceID), State: s.State})
	}
	return out, nil
}

func (c *Client) ListDNSHosting(ctx context.Context) ([]Service, error) {
	const method = "service/get_list"
	ans, err := c.Call(ctx, Read, method, map[string]any{"servtype": "srv_dns_both"})
	if err != nil {
		return nil, err
	}
	if !hasAnswer(ans) {
		return nil, &IncompleteReadError{Method: method, Reason: "response has no answer"}
	}
	var a struct {
		Services *[]struct {
			Servtype  string     `json:"servtype"`
			Dname     string     `json:"dname"`
			ServiceID FlexString `json:"service_id"`
			State     string     `json:"state"`
		} `json:"services"`
	}
	if err := json.Unmarshal(ans, &a); err != nil {
		return nil, &IncompleteReadError{Method: method, Reason: "answer is not decodable"}
	}
	if a.Services == nil {
		return nil, &IncompleteReadError{Method: method, Reason: "answer has no services array"}
	}
	out := make([]Service, 0, len(*a.Services))
	for _, s := range *a.Services {
		if s.Servtype != "srv_dns_both" {
			continue
		}
		out = append(out, Service{Domain: s.Dname, ServiceID: string(s.ServiceID), State: s.State})
	}
	return out, nil
}

func (c *Client) zoneInput(domain string) []map[string]string {
	c.hostingMu.Lock()
	id := c.hostingIDs[domain]
	c.hostingMu.Unlock()
	if id != "" {
		return []map[string]string{{"service_id": id}}
	}
	return domainsInput([]string{domain})
}

func (c *Client) fallbackToHosting(ctx context.Context, domain string, err error) bool {
	var de *DomainError
	if !errors.As(err, &de) || de.Code != "DOMAIN_NOT_FOUND" {
		return false
	}
	c.hostingMu.Lock()
	known := c.hostingIDs[domain] != ""
	c.hostingMu.Unlock()
	if known {
		return false
	}
	services, lerr := c.ListDNSHosting(ctx)
	if lerr != nil {
		return false
	}
	for _, s := range services {
		if s.Domain == domain && s.ServiceID != "" {
			c.hostingMu.Lock()
			if c.hostingIDs == nil {
				c.hostingIDs = map[string]string{}
			}
			c.hostingIDs[domain] = s.ServiceID
			c.hostingMu.Unlock()
			return true
		}
	}
	return false
}

func domainsInput(names []string) []map[string]string {
	ds := make([]map[string]string, 0, len(names))
	for _, n := range names {
		ds = append(ds, map[string]string{"dname": n})
	}
	return ds
}

func (c *Client) GetNSS(ctx context.Context, domains []string) ([]DomainNSS, error) {
	if len(domains) == 0 {
		return nil, nil
	}
	out, err := c.getNSSBatch(ctx, domains)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) getNSSBatch(ctx context.Context, domains []string) ([]DomainNSS, error) {
	const method = "domain/get_nss"
	ans, err := c.Call(ctx, Read, method, map[string]any{"domains": domainsInput(domains)})
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Code == "TOO_MANY_OBJECTS_IN_ONE_REQUEST" && len(domains) > 1 {
			mid := len(domains) / 2
			left, lerr := c.getNSSBatch(ctx, domains[:mid])
			right, rerr := c.getNSSBatch(ctx, domains[mid:])
			if lerr != nil {
				return nil, lerr
			}
			if rerr != nil {
				return nil, rerr
			}
			return append(left, right...), nil
		}
		return nil, err
	}
	items, err := c.decodeDomains(method, ans)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]domainItem, len(items))
	for _, it := range items {
		if _, dup := byName[it.Dname]; dup {
			return nil, &IncompleteReadError{Method: method, Reason: "duplicate domain in response"}
		}
		byName[it.Dname] = it
	}
	out := make([]DomainNSS, 0, len(domains))
	for _, d := range domains {
		it, ok := byName[d]
		if !ok {
			return nil, &IncompleteReadError{Method: method, Reason: "requested domain missing from response"}
		}
		r := DomainNSS{Domain: d, ServiceID: string(it.ServiceID)}
		switch {
		case it.Result == "error" || it.ErrorCode != "":
			r.Err = c.domainErr(it)
		case it.NSS == nil || len(*it.NSS) == 0:
			return nil, &IncompleteReadError{Method: method, Reason: "domain has no nss"}
		case it.ServiceID == "":
			return nil, &IncompleteReadError{Method: method, Reason: "domain has no service_id"}
		default:
			r.NSS = make([]string, 0, len(*it.NSS))
			for _, n := range *it.NSS {
				r.NSS = append(r.NSS, n.NS)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func (c *Client) GetResourceRecords(ctx context.Context, domain string) (ZoneRecords, error) {
	z, err := c.getResourceRecords(ctx, domain)
	if c.fallbackToHosting(ctx, domain, err) {
		return c.getResourceRecords(ctx, domain)
	}
	return z, err
}

func (c *Client) getResourceRecords(ctx context.Context, domain string) (ZoneRecords, error) {
	const method = "zone/get_resource_records"
	ans, err := c.Call(ctx, Read, method, map[string]any{"domains": c.zoneInput(domain)})
	if err != nil {
		return ZoneRecords{}, err
	}
	items, err := c.decodeDomains(method, ans)
	if err != nil {
		return ZoneRecords{}, err
	}
	if len(items) != 1 || items[0].Dname != domain {
		return ZoneRecords{}, &IncompleteReadError{Method: method, Reason: "response does not contain exactly the requested domain"}
	}
	it := items[0]
	switch it.Result {
	case "success":
	case "error":
		return ZoneRecords{}, c.domainErr(it)
	default:
		return ZoneRecords{}, &IncompleteReadError{Method: method, Reason: "domain has no result"}
	}
	if it.RRs == nil {
		return ZoneRecords{}, &IncompleteReadError{Method: method, Reason: "domain has no rrs array"}
	}
	if it.ServiceID == "" {
		return ZoneRecords{}, &IncompleteReadError{Method: method, Reason: "domain has no service_id"}
	}
	z := ZoneRecords{Domain: domain, ServiceID: string(it.ServiceID), RRs: *it.RRs}
	if z.RRs == nil {
		z.RRs = []RR{}
	}
	if it.SOA != nil {
		z.SOA = &SOAStrings{TTL: string(it.SOA.TTL), MinimumTTL: string(it.SOA.MinimumTTL)}
	}
	return z, nil
}

var zoneMutateMethods = map[string]bool{
	"add_alias": true, "add_aaaa": true, "add_cname": true, "add_mx": true, "add_txt": true,
	"add_ns": true, "add_srv": true, "add_caa": true, "remove_record": true,
}

func (c *Client) ZoneMutate(ctx context.Context, fn, domain string, params map[string]string) error {
	if !zoneMutateMethods[fn] {
		return fmt.Errorf("unsupported_method: %q", fn)
	}
	err := c.zoneMutate(ctx, fn, domain, params)
	if c.fallbackToHosting(ctx, domain, err) {
		return c.zoneMutate(ctx, fn, domain, params)
	}
	return err
}

func (c *Client) zoneMutate(ctx context.Context, fn, domain string, params map[string]string) error {
	method := "zone/" + fn
	input := make(map[string]any, len(params)+1)
	for k, v := range params {
		input[k] = v
	}
	input["domains"] = c.zoneInput(domain)
	ans, err := c.Call(ctx, Mutate, method, input)
	if err != nil {
		return err
	}
	unknown := func(reason string) error {
		return &UnknownOutcomeError{Method: method, Err: errors.New(reason)}
	}
	if !hasAnswer(ans) {
		return unknown("response has no answer")
	}
	var a struct {
		Domains []domainItem `json:"domains"`
	}
	if err := json.Unmarshal(ans, &a); err != nil {
		return unknown("answer is not decodable")
	}
	if len(a.Domains) != 1 || a.Domains[0].Dname != domain {
		return unknown("response does not contain exactly the requested domain")
	}
	switch a.Domains[0].Result {
	case "success":
		return nil
	case "error":
		return c.domainErr(a.Domains[0])
	default:
		return unknown("domain has no result")
	}
}
