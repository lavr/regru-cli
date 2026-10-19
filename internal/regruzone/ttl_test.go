package regruzone

import "testing"

func TestParseTTL(t *testing.T) {
	ok := map[string]int{"1d": 86400, "3h": 10800, "30m": 1800, "2w": 1209600, "3600": 3600, "0": 0}
	for in, want := range ok {
		got, err := ParseTTL(in)
		if err != nil || got != want {
			t.Errorf("ParseTTL(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "1y", "-1", "d", "1.5h", " 1d", "1d ", "1D", "1h30m", "99999999999999999999", "+1"} {
		if got, err := ParseTTL(in); err == nil {
			t.Errorf("ParseTTL(%q) = %d, want error", in, got)
		}
	}
}
