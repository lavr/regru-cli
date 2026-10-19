package config

import (
	"path/filepath"
	"strings"
)

type Lookup func(string) (string, bool)

type Paths struct {
	Config, Cache, State string
}

func xdgDir(lookup Lookup, env, home, fallback string) string {
	if v, ok := lookup(env); ok && v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, "regru")
	}
	return filepath.Join(home, fallback, "regru")
}

func DefaultPaths(lookup Lookup, home string) Paths {
	return Paths{
		Config: xdgDir(lookup, "XDG_CONFIG_HOME", home, ".config"),
		Cache:  xdgDir(lookup, "XDG_CACHE_HOME", home, ".cache"),
		State:  xdgDir(lookup, "XDG_STATE_HOME", home, filepath.Join(".local", "state")),
	}
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}
