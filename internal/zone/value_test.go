package zone

import (
	"errors"
	"strings"
	"testing"
)

func TestParseValue(t *testing.T) {
	ok := []struct {
		t       Type
		in, out string
	}{
		{A, "192.0.2.1", "192.0.2.1"},
		{AAAA, "2001:DB8:0::1", "2001:db8::1"},
		{CNAME, "T.Example.NET.", "t.example.net."},
		{MX, "10 mx.example.net.", "10 mx.example.net."},
		{SRV, "10 5 5060 sip.example.net.", "10 5 5060 sip.example.net."},
		{CAA, "0 issue letsencrypt.org", "0 issue letsencrypt.org"},
		{CAA, "0 issue \"letsencrypt.org\"", "0 issue letsencrypt.org"},
		{TXT, "v=spf1 -all", "v=spf1 -all"},
		{TXT, "regru \"q\" back\\slash", "regru \"q\" back\\slash"},
		{TXT, "yes", "yes"},
		{TXT, strings.Repeat("a", 256), strings.Repeat("a", 256)},
	}
	for _, c := range ok {
		got, err := ParseValue(c.t, c.in)
		if err != nil || got != c.out {
			t.Errorf("ParseValue(%s,%q)=%q,%v want %q", c.t, c.in, got, err, c.out)
		}
	}
	bad := []struct {
		t  Type
		in string
	}{
		{A, "2001:db8::1"},
		{AAAA, "fe80::1%eth0"},
		{CNAME, "t.example.net"},
		{MX, "70000 mx.example.net."},
		{SRV, "10 5 sip.example.net."},
		{CAA, "256 issue x"},
		{TXT, ""},
		{TXT, "проверка"},
		{TXT, "a\tb"},
	}
	for _, c := range bad {
		_, err := ParseValue(c.t, c.in)
		var ve *ValueError
		if !errors.As(err, &ve) || ve.Code() != "invalid_value" {
			t.Errorf("ParseValue(%s,%q) err=%v want invalid_value", c.t, c.in, err)
		}
	}
}
