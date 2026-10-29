// Package match decides whether a command line runs locally or remotely by
// comparing its argv against user-supplied rules.
//
// A rule is a string slice [prog, w1, w2, ...]. It matches when the basename
// of argv[0] equals prog and w1, w2, ... occur in argv[1:] in that order as a
// subsequence, each word being a path.Match glob. Rules are matched against
// argv only; shell strings are never interpreted.
package match

import (
	"path"
	"path/filepath"
)

// Side is where a command runs.
type Side int

const (
	// Local runs the command on this machine.
	Local Side = iota
	// Remote runs the command on the build host.
	Remote
)

// String returns "local" or "remote".
func (s Side) String() string {
	if s == Remote {
		return "remote"
	}
	return "local"
}

// Matches reports whether rule matches argv. An empty rule or empty argv never
// matches, and a malformed glob never matches a word.
func Matches(rule, argv []string) bool {
	if len(rule) == 0 || len(argv) == 0 || filepath.Base(argv[0]) != rule[0] {
		return false
	}
	rest := argv[1:]
	for _, word := range rule[1:] {
		// Greedy scan: taking the earliest match leaves the most room for later words.
		i := 0
		for ; i < len(rest); i++ {
			if ok, err := path.Match(word, rest[i]); err == nil && ok {
				break
			}
		}
		if i == len(rest) {
			return false
		}
		rest = rest[i+1:]
	}
	return true
}

// Index returns the index of the first rule in rules that matches argv, or -1.
func Index(rules [][]string, argv []string) int {
	for i, rule := range rules {
		if Matches(rule, argv) {
			return i
		}
	}
	return -1
}

// Decide returns the side argv runs on. A local match wins over a remote
// match, and a command matching neither runs locally.
func Decide(argv []string, remote, local [][]string) Side {
	if Index(local, argv) >= 0 {
		return Local
	}
	if Index(remote, argv) >= 0 {
		return Remote
	}
	return Local
}
