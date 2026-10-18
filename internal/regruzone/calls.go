package regruzone

import (
	"fmt"
	"strings"

	"github.com/lavr/regru-cli/internal/zone"
)

type Error struct {
	CodeValue string
	Msg       string
}

func (e *Error) Error() string { return e.CodeValue + ": " + e.Msg }

func (e *Error) Code() string { return e.CodeValue }

type Selector struct {
	Priority string
	Content  string
}

var caaTags = map[string]bool{"issue": true, "issuewild": true, "iodef": true}

func ValidateWrite(r zone.Record) error {
	if !writable(r.Type) {
		return &Error{CodeValue: "unsupported_type", Msg: fmt.Sprintf("record type %q cannot be written through reg.ru api", r.Type)}
	}
	canon, err := zone.ParseValue(r.Type, r.Value)
	if err != nil {
		return err
	}
	if canon != r.Value {
		return &zone.ValueError{Type: r.Type, Input: r.Value, Reason: "value is not canonical"}
	}
	if r.Type == zone.CAA {
		f := strings.SplitN(r.Value, " ", 3)
		if !caaTags[f[1]] {
			return &Error{CodeValue: "unsupported_value", Msg: fmt.Sprintf("CAA tag %q is not supported by reg.ru api (issue, issuewild, iodef)", f[1])}
		}
	}
	return nil
}

func AddCall(r zone.Record) (string, map[string]string, error) {
	if err := ValidateWrite(r); err != nil {
		return "", nil, err
	}
	switch r.Type {
	case zone.A:
		return "add_alias", map[string]string{"subdomain": r.Name, "ipaddr": r.Value}, nil
	case zone.AAAA:
		return "add_aaaa", map[string]string{"subdomain": r.Name, "ipaddr": r.Value}, nil
	case zone.CNAME:
		return "add_cname", map[string]string{"subdomain": r.Name, "canonical_name": r.Value}, nil
	case zone.MX:
		f := strings.SplitN(r.Value, " ", 2)
		return "add_mx", map[string]string{"subdomain": r.Name, "priority": f[0], "mail_server": f[1]}, nil
	case zone.TXT:
		return "add_txt", map[string]string{"subdomain": r.Name, "text": r.Value}, nil
	case zone.NS:
		return "add_ns", map[string]string{"subdomain": r.Name, "dns_server": r.Value}, nil
	case zone.SRV:
		f := strings.SplitN(r.Value, " ", 4)
		return "add_srv", map[string]string{"service": r.Name, "priority": f[0], "weight": f[1], "port": f[2], "target": f[3]}, nil
	case zone.CAA:
		f := strings.SplitN(r.Value, " ", 3)
		return "add_caa", map[string]string{"subdomain": r.Name, "flags": f[0], "tag": f[1], "value": f[2]}, nil
	}
	return "", nil, &Error{CodeValue: "unsupported_type", Msg: fmt.Sprintf("record type %q cannot be written through reg.ru api", r.Type)}
}

func RemoveCall(r zone.Record, s Selector) (string, map[string]string) {
	return "remove_record", map[string]string{
		"subdomain":   r.Name,
		"record_type": string(r.Type),
		"priority":    s.Priority,
		"content":     s.Content,
	}
}
