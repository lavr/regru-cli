package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func t14Setup(t *testing.T, config string) (*fakeAPI, *Deps) {
	t.Helper()
	api := newFakeAPI(t)
	api.addUser("userb", "pwb", 2000, "example.net")
	d := newTestDeps(t, api)
	writeConfig(t, d, config)
	withEnv(d, "A_PW", "pw", "B_PW", "pwb")
	return api, d
}

func t14Discover(t *testing.T, d *Deps, domain string, fresh bool) (string, error) {
	t.Helper()
	s, err := d.LoadSettings(t14Cmd(t, d))
	if err != nil {
		t.Fatal(err)
	}
	d.active = s
	return Discover(context.Background(), d, domain, fresh)
}

func t14Cmd(t *testing.T, d *Deps) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "t14"}
	c.Flags().String("config", "", "")
	c.Flags().String("account", "", "")
	return c
}

func t14Code(err error) (int, string) {
	ce := ToCoded(err)
	return ce.Exit, ce.Item.Code
}

func TestDiscoverSingle(t *testing.T) {
	api, d := t14Setup(t, twoAccounts)
	name, err := t14Discover(t, d, "example.net", true)
	if err != nil || name != "b" {
		t.Fatalf("%q %v", name, err)
	}
	if api.Requests.Load() != 4 {
		t.Fatalf("requests %d", api.Requests.Load())
	}
	info, err := os.Stat(filepath.Join(d.Paths.Cache, "domains.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info, err)
	}
	dir, _ := os.Stat(d.Paths.Cache)
	if dir.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", dir.Mode())
	}
}

func TestDiscoverNotFound(t *testing.T) {
	_, d := t14Setup(t, twoAccounts)
	_, err := t14Discover(t, d, "missing.test", true)
	if c, code := t14Code(err); c != 2 || code != "domain_not_found" {
		t.Fatalf("%d %s", c, code)
	}
}

func TestDiscoverAmbiguous(t *testing.T) {
	api, d := t14Setup(t, twoAccounts)
	api.addUser("userb", "pwb", 2000, "example.net", "example.test")
	_, err := t14Discover(t, d, "example.test", true)
	if c, code := t14Code(err); c != 2 || code != "ambiguous_account" {
		t.Fatalf("%d %s", c, code)
	}
}

func TestDiscoverIncompleteEvenIfOneMatches(t *testing.T) {
	_, d := t14Setup(t, twoAccounts)
	withEnv(d, "B_PW", "wrong")
	_, err := t14Discover(t, d, "example.test", true)
	if c, code := t14Code(err); c != 2 || code != "discovery_incomplete" {
		t.Fatalf("%d %s %v", c, code, err)
	}
}

func TestDiscoverIncompleteNotCached(t *testing.T) {
	api, d := t14Setup(t, twoAccounts)
	withEnv(d, "B_PW", "wrong")
	_, _ = t14Discover(t, d, "example.test", true)
	if _, err := os.Stat(filepath.Join(d.Paths.Cache, "domains.json")); !os.IsNotExist(err) {
		t.Fatalf("cache written: %v", err)
	}
	before := api.Requests.Load()
	_, _ = t14Discover(t, d, "example.test", false)
	if api.Requests.Load() == before {
		t.Fatal("second run made no requests")
	}
}

func TestDiscoverCacheTTLReadOnly(t *testing.T) {
	api, d := t14Setup(t, twoAccounts)
	if _, err := t14Discover(t, d, "example.test", false); err != nil {
		t.Fatal(err)
	}
	base := api.Requests.Load()
	api.clock.Advance(59 * time.Minute)
	name, err := t14Discover(t, d, "example.test", false)
	if err != nil || name != "a" {
		t.Fatalf("%q %v", name, err)
	}
	if api.Requests.Load() != base {
		t.Fatalf("cache not used: %d", api.Requests.Load()-base)
	}
	api.clock.Advance(2 * time.Minute)
	if _, err := t14Discover(t, d, "example.test", false); err != nil {
		t.Fatal(err)
	}
	if api.Requests.Load() == base {
		t.Fatal("expired cache used")
	}
}

func TestDiscoverMutationIgnoresCache(t *testing.T) {
	api, d := t14Setup(t, twoAccounts)
	if _, err := t14Discover(t, d, "example.test", false); err != nil {
		t.Fatal(err)
	}
	base := api.Requests.Load()
	if _, err := t14Discover(t, d, "example.test", true); err != nil {
		t.Fatal(err)
	}
	if api.Requests.Load() == base {
		t.Fatal("cache used for mutation")
	}
}

func TestDiscoverCacheInvalidOnCredentialChange(t *testing.T) {
	api, d := t14Setup(t, twoAccounts)
	if _, err := t14Discover(t, d, "example.net", false); err != nil {
		t.Fatal(err)
	}
	api.addUser("userc", "pwb", 3000, "example.org")
	writeConfig(t, d, `version: 1
default_account: a
accounts:
  a:
    username: user
    password: env:A_PW
  b:
    username: userc
    password: env:B_PW
`)
	base := api.Requests.Load()
	_, err := t14Discover(t, d, "example.net", false)
	if api.Requests.Load() == base {
		t.Fatal("stale cache used")
	}
	if c, code := t14Code(err); c != 2 || code != "domain_not_found" {
		t.Fatalf("%d %s %v", c, code, err)
	}
}

func TestDiscoverCacheFileShape(t *testing.T) {
	_, d := t14Setup(t, twoAccounts)
	if _, err := t14Discover(t, d, "example.test", true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(d.Paths.Cache, "domains.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		SchemaVersion int `json:"schema_version"`
		Accounts      map[string]struct {
			UserID         string   `json:"user_id"`
			UsernameSHA256 string   `json:"username_sha256"`
			Complete       bool     `json:"complete"`
			Domains        []string `json:"domains"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	a := f.Accounts["a"]
	if f.SchemaVersion != 1 || !a.Complete || a.UserID != "1000" || len(a.UsernameSHA256) != 64 || len(a.Domains) != 1 {
		t.Fatalf("%s", b)
	}
}
