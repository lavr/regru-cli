package apply

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLockZoneExclusive(t *testing.T) {
	dir := t.TempDir()
	unlock, err := LockZone(dir, "42", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = LockZone(dir, "42", "example.test")
	var le *LockedError
	if !errors.As(err, &le) || le.Code() != "zone_locked" {
		t.Fatalf("want zone_locked, got %v", err)
	}
	other, err := LockZone(dir, "42", "example.net")
	if err != nil {
		t.Fatalf("other zone: %v", err)
	}
	other()
	unlock()
	again, err := LockZone(dir, "42", "example.test")
	if err != nil {
		t.Fatalf("relock after unlock: %v", err)
	}
	again()
	path := filepath.Join(dir, "locks", "42-example.test.lock")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode %v", st.Mode().Perm())
	}
	dst, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dst.Mode().Perm() != 0o700 {
		t.Fatalf("locks dir mode %v", dst.Mode().Perm())
	}
	if _, err := LockZone(dir, "../42", "example.test"); err == nil {
		t.Fatal("path traversal accepted")
	}
}
