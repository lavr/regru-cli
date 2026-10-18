package zone

import (
	"fmt"
	"strings"

	"golang.org/x/net/idna"
)

type NameError struct {
	CodeValue string
	Input     string
	Reason    string
}

func (e *NameError) Error() string {
	return fmt.Sprintf("%s: %q: %s", e.CodeValue, e.Input, e.Reason)
}

func (e *NameError) Code() string { return e.CodeValue }

func invalidName(input, reason string) error {
	return &NameError{CodeValue: "invalid_name", Input: input, Reason: reason}
}

func isPlainLabel(l string) bool {
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func normalizeLabel(input, l string, allowWildcard bool) (string, error) {
	if l == "" {
		return "", invalidName(input, "empty label")
	}
	if l == "*" {
		if allowWildcard {
			return l, nil
		}
		return "", invalidName(input, "wildcard is allowed only as the first label")
	}
	l = strings.ToLower(l)
	if !isPlainLabel(l) {
		a, err := idna.Lookup.ToASCII(l)
		if err != nil {
			return "", invalidName(input, err.Error())
		}
		l = strings.ToLower(a)
	}
	if len(l) < 1 || len(l) > 63 {
		return "", invalidName(input, "label length must be 1..63")
	}
	return l, nil
}

func normalizeLabels(input, s string, allowWildcard bool) (string, error) {
	labels := strings.Split(s, ".")
	for i, l := range labels {
		n, err := normalizeLabel(input, l, allowWildcard && i == 0)
		if err != nil {
			return "", err
		}
		labels[i] = n
	}
	out := strings.Join(labels, ".")
	if len(out) > 253 {
		return "", invalidName(input, "name is longer than 253")
	}
	return out, nil
}

func NormalizeZone(z string) (string, error) {
	s := strings.TrimSuffix(strings.TrimSpace(z), ".")
	if s == "" {
		return "", invalidName(z, "empty zone")
	}
	return normalizeLabels(z, s, false)
}

func NormalizeName(zone, name string) (string, error) {
	zn, err := NormalizeZone(zone)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(name)
	if s == "" || s == "@" {
		return "@", nil
	}
	absolute := strings.HasSuffix(s, ".")
	if absolute {
		s = strings.TrimSuffix(s, ".")
	}
	n, err := normalizeLabels(name, s, true)
	if err != nil {
		return "", err
	}
	if !absolute {
		if n == zn || strings.HasSuffix(n, "."+zn) {
			return "", &NameError{CodeValue: "ambiguous_name", Input: name,
				Reason: "relative name ends with zone " + zn + "; add a trailing dot for an absolute name or drop the zone suffix"}
		}
		return n, nil
	}
	if n == zn {
		return "@", nil
	}
	if rel, ok := strings.CutSuffix(n, "."+zn); ok {
		return rel, nil
	}
	return "", &NameError{CodeValue: "name_outside_zone", Input: name, Reason: "name is outside zone " + zn}
}
