package zone

import (
	"errors"
	"strings"
	"testing"
)

type coder interface{ Code() string }

func codeOf(err error) string {
	var c coder
	if errors.As(err, &c) {
		return c.Code()
	}
	return ""
}

func TestNormalizeName(t *testing.T) {
	ok := []struct{ zone, in, want string }{
		{"example.test", "", "@"},
		{"example.test", "@", "@"},
		{"example.test", "example.test.", "@"},
		{"example.test", "WWW", "www"},
		{"example.test", "www.example.test.", "www"},
		{"example.test", "_sip._tcp", "_sip._tcp"},
		{"example.test", "*", "*"},
		{"example.test", "*.dev", "*.dev"},
		{"example.test", "почта", "xn--80a1acny"},
	}
	for _, c := range ok {
		got, err := NormalizeName(c.zone, c.in)
		if err != nil || got != c.want {
			t.Errorf("NormalizeName(%q,%q)=%q,%v want %q", c.zone, c.in, got, err, c.want)
		}
	}
	bad := []struct{ zone, in, code string }{
		{"example.test", "www.other.test.", "name_outside_zone"},
		{"example.test", "a..b", "invalid_name"},
		{"example.test", "a.*", "invalid_name"},
		{"example.test", strings.Repeat("a", 64), "invalid_name"},
	}
	for _, c := range bad {
		_, err := NormalizeName(c.zone, c.in)
		if err == nil || codeOf(err) != c.code {
			t.Errorf("NormalizeName(%q,%q) err=%v code=%q want %q", c.zone, c.in, err, codeOf(err), c.code)
		}
	}
}

func TestNormalizeZone(t *testing.T) {
	got, err := NormalizeZone("Пример.РФ.")
	if err != nil || got != "xn--e1afmkfd.xn--p1ai" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestNormalizeNameAmbiguousZoneSuffix(t *testing.T) {
	for _, in := range []string{"www.example.test", "example.test", "WWW.Example.Test", "*.example.test"} {
		_, err := NormalizeName("example.test", in)
		if codeOf(err) != "ambiguous_name" {
			t.Errorf("NormalizeName(%q) err=%v want ambiguous_name", in, err)
		}
	}
	got, err := NormalizeName("example.test", "www.example.test.")
	if err != nil || got != "www" {
		t.Fatalf("absolute name: %q, %v", got, err)
	}
	got, err = NormalizeName("example.test", "www.example.net")
	if err != nil || got != "www.example.net" {
		t.Fatalf("relative name with other suffix: %q, %v", got, err)
	}
}
