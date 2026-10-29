package run

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/libraz/sendaway/internal/manifest"
	"github.com/libraz/sendaway/internal/secret"
)

const (
	// largestShown is how many of the biggest files a large-tree warning names.
	largestShown = 5
	// modifiedShown is how many modified files the modified warning names.
	modifiedShown = 5
	mib           = 1 << 20
)

// plan is what a push sends and what it leaves behind.
type plan struct {
	// send is the request manifest: the local manifest minus notSent.
	send []string
	// excludes are the anchored rsync excludes, not yet escaped.
	excludes []string
	// notSent are manifest files withheld as secret-like or excluded.
	notSent     []string
	fingerprint string
	// warnings are lines shown right after the start line.
	warnings   []string
	ignoreCase bool
}

// buildPlan lists the files under root and splits them into sent and withheld.
// exclude holds the project's root-relative exclude: entries.
func buildPlan(root string, exclude []string) (*plan, error) {
	m, err := manifest.Build(root)
	if err != nil {
		return nil, err
	}
	ignored, err := manifest.Ignored(root)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	read := func(rel string) ([]byte, error) { return r.ReadFile(filepath.FromSlash(rel)) }

	p := &plan{fingerprint: m.Fingerprint(), ignoreCase: gitBool(root, "core.ignorecase")}
	sent := make(map[string]bool, len(m.Entries))
	for _, rel := range m.Paths() {
		if secret.Check(rel, exclude, read) != "" {
			p.notSent = append(p.notSent, rel)
			continue
		}
		p.send = append(p.send, rel)
		sent[rel] = true
	}
	p.excludes = composeExcludes(ignored, p.notSent, exclude)

	if total := m.TotalSize(); total > manifest.LargeTreeBytes {
		var big []string
		for _, e := range m.Largest(largestShown) {
			big = append(big, fmt.Sprintf("%s (%.1f MiB)", e.Path, float64(e.Size)/mib))
		}
		p.warnings = append(p.warnings, fmt.Sprintf("tree is %.1f MiB, over %d MiB; largest: %s",
			float64(total)/mib, manifest.LargeTreeBytes/mib, strings.Join(big, ", ")))
	}
	var outside []string
	for _, l := range m.OutsideLinks {
		if sent[l] {
			outside = append(outside, l)
		}
	}
	if len(outside) > 0 {
		p.warnings = append(p.warnings, "symlinks pointing outside the repository are sent but dangle on the host: "+strings.Join(outside, ", "))
	}
	return p, nil
}

// composeExcludes anchors every entry at the root. Ignored directories keep
// their trailing slash; exclude: entries have none, so they match a file or a
// directory of that name. rsync escaping is left to the transport.
func composeExcludes(ignored, withheld, exclude []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, list := range [][]string{ignored, withheld, exclude} {
		for _, p := range list {
			e := "/" + p
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	return out
}

// caseMismatches returns the transferred names whose last element is not
// spelled byte for byte as in its parent directory.
func caseMismatches(root string, names []string) []string {
	dirs := make(map[string]map[string]bool)
	var out []string
	for _, n := range names {
		n = strings.TrimSuffix(n, "/")
		if n == "" || n == "." {
			continue
		}
		dir, base := path.Split(n)
		listing, ok := dirs[dir]
		if !ok {
			entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
			if err != nil {
				dirs[dir] = nil
				continue
			}
			listing = make(map[string]bool, len(entries))
			for _, e := range entries {
				listing[e.Name()] = true
			}
			dirs[dir] = listing
		}
		if listing != nil && !listing[base] {
			out = append(out, n)
		}
	}
	return out
}

// checkRoot verifies that root is the top level of a git work tree with a .git directory.
func checkRoot(root string) error {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return fmt.Errorf("git rev-parse: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return fmt.Errorf("git rev-parse: %w", err)
	}
	top, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil || top != root {
		return errors.New(".sendaway.yml must sit at the git top level")
	}
	if fi, err := os.Lstat(filepath.Join(root, ".git")); err != nil || !fi.IsDir() {
		return errors.New("git worktrees are not supported")
	}
	return nil
}

// gitBool reads a boolean git config key; unset or unreadable is false.
func gitBool(root, key string) bool {
	out, err := exec.Command("git", "-C", root, "config", "--bool", "--get", key).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// checkDest refuses a pull into root when any element of a destination path
// is a symlink, so nothing is written outside the repository.
func checkDest(root string, files []string) error {
	checked := make(map[string]bool)
	for _, f := range files {
		parts := strings.Split(f, "/")
		for i := range parts {
			rel := strings.Join(parts[:i+1], "/")
			if checked[rel] {
				continue
			}
			checked[rel] = true
			fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				break
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("pull destination %s is a symlink; refusing to write through it", rel)
			}
		}
	}
	return nil
}

// modifiedLine is the warning for synced files the remote run changed.
func modifiedLine(paths []string) string {
	shown, more := paths, 0
	if len(paths) > modifiedShown {
		shown, more = paths[:modifiedShown], len(paths)-modifiedShown
	}
	s := fmt.Sprintf("remote run modified %d synced files (not copied back): %s", len(paths), strings.Join(shown, ", "))
	if more > 0 {
		s += fmt.Sprintf(" … and %d more", more)
	}
	return s + " — add this target to local: or list the files in pull:"
}
