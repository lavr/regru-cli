package config

import "testing"

func TestDotenvNoExpansion(t *testing.T) {
	m, err := ParseDotenv([]byte("# c\n\nexport A=1\nB=\"$A ${A}\"\nC='x y'\nD=$A\nE=v # note\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "$A ${A}", "C": "x y", "D": "$A", "E": "v"}
	for k, v := range want {
		if m[k] != v {
			t.Fatalf("%s = %q, want %q", k, m[k], v)
		}
	}
}

func TestDotenvBadLine(t *testing.T) {
	if _, err := ParseDotenv([]byte("novalue\n")); err == nil {
		t.Fatal("expected error")
	}
}
