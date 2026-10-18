package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	discoveryCacheTTL    = time.Hour
	discoveryCacheSchema = 1
)

type discoveryEntry struct {
	UserID         string    `json:"user_id"`
	UsernameSHA256 string    `json:"username_sha256"`
	FetchedAt      time.Time `json:"fetched_at"`
	Complete       bool      `json:"complete"`
	Domains        []string  `json:"domains"`
}

type discoveryCache struct {
	SchemaVersion int                       `json:"schema_version"`
	Accounts      map[string]discoveryEntry `json:"accounts"`
}

func discoveryErr(code, msg, domain string) error {
	return &CodedError{Exit: ExitUsage, Item: ErrorItem{Code: code, Message: msg, Domain: domain}}
}

func usernameHash(username string) string {
	sum := sha256.Sum256([]byte(username))
	return hex.EncodeToString(sum[:])
}

func normalizeDiscoveryDomain(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

func readDiscoveryCache(path string) discoveryCache {
	empty := discoveryCache{SchemaVersion: discoveryCacheSchema, Accounts: map[string]discoveryEntry{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return empty
	}
	var c discoveryCache
	if json.Unmarshal(b, &c) != nil || c.SchemaVersion != discoveryCacheSchema || c.Accounts == nil {
		return empty
	}
	return c
}

func writeDiscoveryCache(dir string, c discoveryCache) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "domains-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, filepath.Join(dir, "domains.json")); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func Discover(ctx context.Context, d *Deps, domain string, fresh bool) (string, error) {
	if d.active == nil || d.active.Config == nil || len(d.active.Config.Accounts) == 0 {
		return "", discoveryErr("config_not_found", "--account auto requires a config file with accounts", domain)
	}
	now := time.Now
	if d.Env.Now != nil {
		now = d.Env.Now
	}
	target := normalizeDiscoveryDomain(domain)
	cachePath := filepath.Join(d.Paths.Cache, "domains.json")
	cache := discoveryCache{SchemaVersion: discoveryCacheSchema, Accounts: map[string]discoveryEntry{}}
	if !fresh {
		cache = readDiscoveryCache(cachePath)
	}

	names := make([]string, 0, len(d.active.Config.Accounts))
	for n := range d.active.Config.Accounts {
		names = append(names, n)
	}
	sort.Strings(names)

	next := discoveryCache{SchemaVersion: discoveryCacheSchema, Accounts: map[string]discoveryEntry{}}
	var matches, failed []string
	for _, name := range names {
		spec := d.active.Config.Accounts[name]
		hash := usernameHash(spec.Username)
		entry, cached := cache.Accounts[name]
		age := now().Sub(entry.FetchedAt)
		if cached && entry.Complete && entry.UsernameSHA256 == hash && age >= 0 && age < discoveryCacheTTL {
			next.Accounts[name] = entry
		} else {
			fetched, err := d.discoverAccount(ctx, name, hash, now())
			if err != nil {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				failed = append(failed, name)
				continue
			}
			entry = fetched
			next.Accounts[name] = entry
		}
		for _, dn := range entry.Domains {
			if dn == target {
				matches = append(matches, name)
				break
			}
		}
	}
	if len(failed) > 0 {
		return "", discoveryErr("discovery_incomplete", fmt.Sprintf("could not check accounts: %s", strings.Join(failed, ", ")), domain)
	}
	_ = writeDiscoveryCache(d.Paths.Cache, next)
	switch len(matches) {
	case 0:
		return "", discoveryErr("domain_not_found", fmt.Sprintf("domain %s was not found in any configured account", target), domain)
	case 1:
		return matches[0], nil
	}
	return "", discoveryErr("ambiguous_account", fmt.Sprintf("domain %s is present in several accounts: %s", target, strings.Join(matches, ", ")), domain)
}

func (d *Deps) discoverAccount(ctx context.Context, name, hash string, now time.Time) (discoveryEntry, error) {
	acc, err := d.resolve(d.active, name)
	if err != nil {
		return discoveryEntry{}, err
	}
	client := d.NewClient(acc, d.Limiter, d.active.RequestTimeout)
	_, userID, err := client.Nop(ctx)
	if err != nil {
		return discoveryEntry{}, err
	}
	services, err := client.ListDomains(ctx)
	if err != nil {
		return discoveryEntry{}, err
	}
	domains := make([]string, 0, len(services))
	for _, s := range services {
		if dn := normalizeDiscoveryDomain(s.Domain); dn != "" {
			domains = append(domains, dn)
		}
	}
	sort.Strings(domains)
	return discoveryEntry{UserID: userID, UsernameSHA256: hash, FetchedAt: now.UTC(), Complete: true, Domains: domains}, nil
}
