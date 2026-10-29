// Package secret decides whether a root-relative path must not be sent to the
// build host. The name rules are fixed; callers can only add paths through
// the exclude list.
package secret

import (
	"path"
	"slices"
	"strings"
)

// denyNames are patterns matched case-insensitively against every path element.
var denyNames = []string{
	".env", ".env.*",
	"*.pem", "*.key", "*.p12", "*.pfx", "*.jks", "*.keystore",
	"id_rsa*", "id_ecdsa*", "id_ed25519*",
	".netrc", ".git-credentials", ".pypirc",
	"*.tfstate", "*.tfstate.*",
	"credentials*.json", "service-account*.json",
	"secrets", ".ssh", ".aws", ".gnupg",
}

// envTemplates are .env variants that hold no credentials and may be sent.
var envTemplates = []string{".env.example", ".env.sample", ".env.template"}

// rcNames are the files whose content decides the verdict.
var rcNames = []string{".npmrc", ".yarnrc.yml"}

// rcMarkers are lowercase keys that carry credentials in an rc file.
var rcMarkers = []string{"_authtoken", "_auth", "_password", "npmauthtoken", "npmauthident"}

// Check returns why rel must not be sent, or "" if it may be sent. rel is a
// slash-separated path relative to the repository root. read returns the
// content of rel; it is called only when rel is .npmrc or .yarnrc.yml, and a
// read error counts as secret.
func Check(rel string, excludes []string, read func(rel string) ([]byte, error)) string {
	for element := range strings.SplitSeq(rel, "/") {
		if pattern := matchedName(strings.ToLower(element)); pattern != "" {
			return "name matches " + pattern
		}
	}
	if excluded(rel, excludes) {
		return "excluded by exclude:"
	}
	if slices.Contains(rcNames, strings.ToLower(path.Base(rel))) {
		content, err := read(rel)
		if err != nil {
			return "unreadable rc file"
		}
		if RC(content) {
			return "credential key in rc file"
		}
	}
	return ""
}

// RC reports whether the content of an .npmrc or .yarnrc.yml has a credential key.
func RC(content []byte) bool {
	lower := strings.ToLower(string(content))
	for _, marker := range rcMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// matchedName returns the deny pattern that the lowercase element matches, or "".
func matchedName(element string) string {
	if slices.Contains(envTemplates, element) {
		return ""
	}
	for _, pattern := range denyNames {
		if ok, _ := path.Match(pattern, element); ok {
			return pattern
		}
	}
	return ""
}

// excluded reports whether rel equals an exclude entry or lies below one.
func excluded(rel string, excludes []string) bool {
	for _, entry := range excludes {
		entry = path.Clean(entry)
		if entry == "." || rel == entry || strings.HasPrefix(rel, entry+"/") {
			return true
		}
	}
	return false
}
