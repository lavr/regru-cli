package zone

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

type ValueError struct {
	Type   Type
	Input  string
	Reason string
}

func (e *ValueError) Error() string {
	return fmt.Sprintf("invalid %s value %q: %s", e.Type, e.Input, e.Reason)
}

func (e *ValueError) Code() string { return "invalid_value" }

func ParseValue(t Type, s string) (string, error) {
	fail := func(reason string) (string, error) {
		return "", &ValueError{Type: t, Input: s, Reason: reason}
	}
	switch t {
	case A:
		a, err := netip.ParseAddr(s)
		if err != nil || !a.Is4() {
			return fail("not an IPv4 address")
		}
		return a.String(), nil
	case AAAA:
		a, err := netip.ParseAddr(s)
		if err != nil || !a.Is6() || a.Zone() != "" || a.Is4In6() {
			return fail("not an IPv6 address")
		}
		return a.String(), nil
	case CNAME, NS:
		h, err := parseTarget(s)
		if err != nil {
			return fail(err.Error())
		}
		return h, nil
	case MX:
		f := strings.Fields(s)
		if len(f) != 2 {
			return fail("expected \"<priority> <host.>\"")
		}
		p, err := parseUint(f[0], 65535)
		if err != nil {
			return fail("priority must be 0..65535")
		}
		h, err := parseTarget(f[1])
		if err != nil {
			return fail(err.Error())
		}
		return p + " " + h, nil
	case SRV:
		f := strings.Fields(s)
		if len(f) != 4 {
			return fail("expected \"<priority> <weight> <port> <target.>\"")
		}
		nums := make([]string, 3)
		for i := range nums {
			n, err := parseUint(f[i], 65535)
			if err != nil {
				return fail("priority, weight and port must be 0..65535")
			}
			nums[i] = n
		}
		h, err := parseTarget(f[3])
		if err != nil {
			return fail(err.Error())
		}
		return strings.Join(nums, " ") + " " + h, nil
	case CAA:
		rest := strings.Trim(s, " \t")
		var f [2]string
		for i := range f {
			j := strings.IndexAny(rest, " \t")
			if j < 0 {
				return fail("expected \"<flags> <tag> <value>\"")
			}
			f[i] = rest[:j]
			rest = strings.TrimLeft(rest[j:], " \t")
		}
		fl, err := parseUint(f[0], 255)
		if err != nil {
			return fail("flags must be 0..255")
		}
		tag := f[1]
		for i := 0; i < len(tag); i++ {
			c := tag[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return fail("tag must match [a-z0-9]+")
			}
		}
		if len(rest) >= 2 && rest[0] == '"' && rest[len(rest)-1] == '"' {
			rest = rest[1 : len(rest)-1]
		}
		if rest == "" {
			return fail("empty value")
		}
		return fl + " " + tag + " " + rest, nil
	case TXT:
		if s == "" {
			return fail("empty text")
		}
		for i := 0; i < len(s); i++ {
			if s[i] < 0x20 || s[i] > 0x7E {
				return fail("only printable ASCII is allowed")
			}
		}
		return s, nil
	}
	return fail("unsupported record type")
}

func parseUint(s string, max int) (string, error) {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return "", fmt.Errorf("not a number")
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > max {
		return "", fmt.Errorf("out of range")
	}
	return strconv.Itoa(n), nil
}

func parseTarget(s string) (string, error) {
	if !strings.HasSuffix(s, ".") || s == "." {
		return "", fmt.Errorf("target must be an absolute name ending with a dot")
	}
	n, err := normalizeLabels(s, strings.TrimSuffix(s, "."), false)
	if err != nil {
		return "", err
	}
	return n + ".", nil
}
