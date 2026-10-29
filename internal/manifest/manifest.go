// Package manifest computes the set of files a work tree sends to the build
// host, the set git ignores, and a fingerprint of the former.
//
// The file set comes from git, so negation patterns, .git/info/exclude and
// core.excludesFile behave exactly as they do for git. The package knows
// nothing about secrets or user-configured exclusions.
package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LargeTreeBytes is the manifest size above which callers should warn.
const LargeTreeBytes = 256 << 20

// gitlinkMode is the index mode of a submodule entry.
const gitlinkMode = "160000"

// Entry is one file of the manifest.
type Entry struct {
	// Path is slash-separated and relative to the root.
	Path    string
	Size    int64
	ModTime time.Time
}

// Manifest is the sorted set of files present in the work tree that git
// either tracks or does not ignore.
type Manifest struct {
	// Entries is sorted by Path.
	Entries []Entry
	// OutsideLinks lists the symlinks whose target lies outside the root.
	OutsideLinks []string
}

// Build lists the manifest of the git work tree at root. It fails when the
// tree contains a nested repository, a gitlink, or a path whose bytes differ
// from what git reports (for example an NFD name on macOS).
func Build(root string) (*Manifest, error) {
	staged, err := gitZ(root, "ls-files", "-z", "--stage")
	if err != nil {
		return nil, err
	}
	for _, rec := range staged {
		meta, path, ok := strings.Cut(rec, "\t")
		if ok && strings.HasPrefix(meta, gitlinkMode+" ") {
			return nil, fmt.Errorf("git submodule %q is not supported", path)
		}
	}

	listed, err := gitZ(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	sort.Strings(listed)

	m := &Manifest{}
	dirs := map[string]map[string]struct{}{}
	prev := ""
	for _, p := range listed {
		if p == prev {
			continue
		}
		prev = p
		if strings.HasSuffix(p, "/") {
			return nil, fmt.Errorf("nested git repository %q is not supported", p)
		}
		fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat %q: %w", p, err)
		}
		if fi.IsDir() {
			continue
		}
		if err := checkExactName(root, p, dirs); err != nil {
			return nil, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(filepath.Join(root, filepath.FromSlash(p)))
			if err != nil {
				return nil, fmt.Errorf("readlink %q: %w", p, err)
			}
			if escapesRoot(root, p, target) {
				m.OutsideLinks = append(m.OutsideLinks, p)
			}
		}
		m.Entries = append(m.Entries, Entry{Path: p, Size: fi.Size(), ModTime: fi.ModTime()})
	}
	return m, nil
}

// Ignored lists the untracked paths git ignores under root. Directories that
// are ignored as a whole keep their trailing slash.
func Ignored(root string) ([]string, error) {
	return gitZ(root, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
}

// Paths returns the entry paths in order.
func (m *Manifest) Paths() []string {
	out := make([]string, len(m.Entries))
	for i, e := range m.Entries {
		out[i] = e.Path
	}
	return out
}

// TotalSize returns the summed size of all entries in bytes.
func (m *Manifest) TotalSize() int64 {
	var total int64
	for _, e := range m.Entries {
		total += e.Size
	}
	return total
}

// Largest returns up to n entries, biggest first.
func (m *Manifest) Largest(n int) []Entry {
	out := append([]Entry(nil), m.Entries...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Fingerprint returns an opaque digest of every entry's path, size and mtime.
func (m *Manifest) Fingerprint() string {
	h := sha256.New()
	for _, e := range m.Entries {
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", e.Path, e.Size, e.ModTime.UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

// gitZ runs git in root and splits its NUL-terminated output.
func gitZ(root string, args ...string) ([]string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...) //nolint:gosec // fixed git subcommands; root is the caller's work tree
	out, err := cmd.Output()
	if err != nil {
		msg := err.Error()
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("git %s: %s", args[0], msg)
	}
	out = bytes.TrimSuffix(out, []byte{0})
	if len(out) == 0 {
		return nil, nil
	}
	return strings.Split(string(out), "\x00"), nil
}

// checkExactName verifies that every non-ASCII component of path appears
// byte for byte in its parent's directory listing.
func checkExactName(root, path string, dirs map[string]map[string]struct{}) error {
	dir := root
	for part := range strings.SplitSeq(path, "/") {
		if !isASCII(part) {
			names, ok := dirs[dir]
			if !ok {
				entries, err := os.ReadDir(dir)
				if err != nil {
					return fmt.Errorf("read dir for %q: %w", path, err)
				}
				names = make(map[string]struct{}, len(entries))
				for _, e := range entries {
					names[e.Name()] = struct{}{}
				}
				dirs[dir] = names
			}
			if _, ok := names[part]; !ok {
				return fmt.Errorf("path %q is not stored in NFC on disk; rename to NFC", path)
			}
		}
		dir = filepath.Join(dir, part)
	}
	return nil
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// escapesRoot reports whether a symlink at rel pointing to target resolves
// outside root, judged lexically.
func escapesRoot(root, rel, target string) bool {
	var r string
	if filepath.IsAbs(target) {
		var err error
		r, err = filepath.Rel(root, filepath.Clean(target))
		if err != nil {
			return true
		}
	} else {
		r = filepath.Join(filepath.Dir(filepath.FromSlash(rel)), target)
	}
	return r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator))
}
