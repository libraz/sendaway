// Package shim holds the pieces a PATH shim needs before it decides what to
// run: locating .sendaway.yml, resolving the real binary, and exec'ing it.
// It must stay cheap to start, so it imports only the standard library.
package shim

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ConfigName is the file FindConfig searches for.
const ConfigName = ".sendaway.yml"

// PassthroughEnv, when set to a non-empty value, makes a shim run the real
// binary without matching any rule.
const PassthroughEnv = "SENDAWAY_PASSTHROUGH" //nolint:gosec // env var name, not a credential

// niceValue is the priority applied before a local fallback exec.
const niceValue = 10

// FindConfig searches upward from start for ConfigName using os.Stat only.
// The search begins at start with symlinks resolved and stops at the parent
// of $HOME or at "/", neither of which is searched. It returns the config
// path and true, or "" and false when none exists.
func FindConfig(start string) (string, bool) {
	dir, err := filepath.EvalSymlinks(start)
	if err != nil {
		return "", false
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	stop := ""
	if home := os.Getenv("HOME"); home != "" {
		stop = filepath.Dir(filepath.Clean(home))
	}
	for {
		if dir == stop {
			return "", false
		}
		p := filepath.Join(dir, ConfigName)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// ErrNotFound reports that no real executable was found on PATH.
var ErrNotFound = errors.New("executable not found")

// Resolve returns the first executable named name in the pathList
// directories (os.PathListSeparator-separated), skipping any directory that
// is the same file as shimDir.
func Resolve(name, shimDir, pathList string) (string, error) {
	resolved, err := filepath.EvalSymlinks(shimDir)
	if err != nil {
		return "", fmt.Errorf("resolve shim dir: %w", err)
	}
	shimInfo, err := os.Stat(resolved) //nolint:gosec // path resolved from the shim dir
	if err != nil {
		return "", fmt.Errorf("stat shim dir: %w", err)
	}
	for _, entry := range filepath.SplitList(pathList) {
		if entry == "" {
			entry = "."
		}
		di, err := os.Stat(entry) //nolint:gosec // PATH entries are probed by design
		if err != nil || !di.IsDir() || os.SameFile(di, shimInfo) {
			continue
		}
		cand := filepath.Join(entry, name)
		if fi, err := os.Stat(cand); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 { //nolint:gosec // PATH entries are probed by design
			return cand, nil
		}
	}
	return "", fmt.Errorf("%s: %w", name, ErrNotFound)
}

// Passthrough reports whether SENDAWAY_PASSTHROUGH is set.
func Passthrough() bool {
	return os.Getenv(PassthroughEnv) != ""
}

// Exec replaces the process with path. It never installs signal handlers, so
// signal dispositions reach the new program untouched. It returns only on
// failure.
func Exec(path string, argv, env []string) error {
	return syscall.Exec(path, argv, env)
}

// ExecLocal is Exec with SENDAWAY_PASSTHROUGH=1 appended to env. When nice is
// true the process priority is lowered first. It returns only on failure.
func ExecLocal(path string, argv, env []string, nice bool) error {
	if nice {
		if err := syscall.Setpriority(syscall.PRIO_PROCESS, 0, niceValue); err != nil {
			return fmt.Errorf("setpriority: %w", err)
		}
	}
	return Exec(path, argv, append(append([]string(nil), env...), PassthroughEnv+"=1"))
}
