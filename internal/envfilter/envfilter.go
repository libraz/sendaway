// Package envfilter selects the local environment variables that a remote run
// may receive: a fixed allowlist plus names requested by the project, minus
// secret-like names and values that only make sense on this machine.
package envfilter

import (
	"slices"
	"strings"
)

// defaults is the allowlist forwarded without being requested.
var defaults = []string{
	"CI", "NO_COLOR", "FORCE_COLOR", "CLICOLOR_FORCE",
	"MAKEFLAGS", "CMAKE_BUILD_PARALLEL_LEVEL", "CTEST_PARALLEL_LEVEL", "CTEST_OUTPUT_ON_FAILURE",
	"GOFLAGS", "CGO_ENABLED", "GOMAXPROCS", "GOTRACEBACK",
	"RUST_BACKTRACE", "RUST_LOG",
}

// secretSubstrings and secretPrefixes match names case-insensitively; they
// apply even when a name is requested explicitly.
var secretSubstrings = []string{
	"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL",
	"APIKEY", "API_KEY", "PRIVATE_KEY", "AUTH", "COOKIE", "SESSION",
}

var secretPrefixes = []string{
	"AWS_", "GH_", "GITHUB_", "NPM_", "ANTHROPIC_",
	"OPENAI_", "CLOUDFLARE_", "GOOGLE_", "AZURE_", "SSH_",
}

// macSubstrings mark values that point into macOS-only locations.
var macSubstrings = []string{
	"/opt/homebrew", "/Applications/", "/Library/", "/System/", "/var/folders/", "/private/",
}

// Dropped is an env: name that is set locally but was not forwarded.
type Dropped struct {
	Name   string
	Reason string
}

// Result is the outcome of Select.
type Result struct {
	// Env maps each forwarded name to its value.
	Env map[string]string
	// Dropped lists the requested names that were rejected, sorted by name.
	Dropped []Dropped
}

// Select picks the variables to forward from environ, a list of "NAME=value"
// entries. home is the local home directory, used to reject values under
// $HOME/. and $HOME/Library/. requested holds env: entries; an entry ending in
// "*" matches every name with that prefix.
func Select(environ []string, home string, requested []string) Result {
	vars := make(map[string]string, len(environ))
	for _, kv := range environ {
		if name, value, ok := strings.Cut(kv, "="); ok {
			vars[name] = value
		}
	}
	res := Result{Env: make(map[string]string)}
	for name, value := range vars {
		isRequested := matchesAny(name, requested)
		if !isRequested && !slices.Contains(defaults, name) {
			continue
		}
		reason := dropReason(name, value, home)
		switch {
		case reason == "":
			res.Env[name] = value
		case isRequested:
			res.Dropped = append(res.Dropped, Dropped{Name: name, Reason: reason})
		}
	}
	slices.SortFunc(res.Dropped, func(a, b Dropped) int {
		return strings.Compare(a.Name, b.Name)
	})
	return res
}

// matchesAny reports whether name matches one of the env: entries.
func matchesAny(name string, requested []string) bool {
	for _, entry := range requested {
		if prefix, ok := strings.CutSuffix(entry, "*"); ok {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		} else if name == entry {
			return true
		}
	}
	return false
}

// dropReason returns why a variable must not be forwarded, or "" if it may be.
func dropReason(name, value, home string) string {
	if isSecretName(name) {
		return "secret-like name"
	}
	if isMacValue(value, home) {
		return "Mac-specific value"
	}
	return ""
}

func isSecretName(name string) bool {
	upper := strings.ToUpper(name)
	for _, s := range secretSubstrings {
		if strings.Contains(upper, s) {
			return true
		}
	}
	for _, p := range secretPrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

func isMacValue(value, home string) bool {
	for _, s := range macSubstrings {
		if strings.Contains(value, s) {
			return true
		}
	}
	return home != "" && (strings.HasPrefix(value, home+"/.") || strings.HasPrefix(value, home+"/Library/"))
}
