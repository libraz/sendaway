package remote

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/libraz/sendaway/internal/config"
	"github.com/libraz/sendaway/internal/lock"
	"github.com/libraz/sendaway/internal/proto"
)

// rootFile in a repository state directory records the root it belongs to; its
// mtime is the time of the last exec or fetch.
const rootFile = "root"

// maxRootFile bounds the size of a root record that is read back.
const maxRootFile = 4096

// devID is the st_dev of a path. Its Go type differs per platform, so values
// are only compared for equality.
type devID any

// candidate is another project copy on this host that may be evicted.
type candidate struct {
	dir  string // state directory
	root string // project root recorded in dir
	used time.Time
}

// freeBytes returns the bytes available to unprivileged users on the
// filesystem holding path.
func (s *Server) freeBytes(path string) (uint64, error) {
	if s.freeSpace != nil {
		return s.freeSpace(path)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return blockBytes(st.Bavail, st.Bsize), nil
}

// blockBytes multiplies a block count by statfs's block size, whose type is
// int64 on linux and uint32 on darwin.
func blockBytes[T int64 | uint32](blocks uint64, size T) uint64 {
	if size <= 0 {
		return 0
	}
	return blocks * uint64(size)
}

// nearestExisting returns p, or its closest ancestor that exists.
func nearestExisting(p string) string {
	for p != "/" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		p = filepath.Dir(p)
	}
	return p
}

func deviceOf(path string) (devID, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return nil, err
	}
	return st.Dev, nil
}

// recordRoot stores root in dir; the new file's mtime marks the use.
func recordRoot(dir, root string) error {
	return writeFileAtomic(filepath.Join(dir, rootFile), []byte(root))
}

// within reports whether p is base or lies below it.
func within(p, base string) bool {
	return p == base || strings.HasPrefix(p, strings.TrimSuffix(base, "/")+"/")
}

// evictable reports whether a copy may be removed to make room for root: it is
// not the current repository, not nested with root, does not hold the state
// directory, and is on root's device.
func evictable(c candidate, curDir, root string, rootDev, candDev devID) bool {
	return c.dir != curDir && !within(c.root, root) && !within(root, c.root) &&
		!within(curDir, c.root) && candDev == rootDev
}

// readCandidate loads the root record of a state directory. A record that is
// not a plain file, not a clean absolute path below /, or whose hash does not
// name dir is rejected, so a tampered record cannot redirect a deletion.
func (s *Server) readCandidate(dir string) (candidate, bool) {
	name := filepath.Join(dir, rootFile)
	fi, err := os.Lstat(name)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxRootFile {
		return candidate{}, false
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return candidate{}, false
	}
	root := string(b)
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return candidate{}, false
	}
	if want, err := s.repoDir(root); err != nil || want != dir {
		return candidate{}, false
	}
	return candidate{dir: dir, root: root, used: fi.ModTime()}, true
}

// candidates lists the evictable copies, least recently used first.
func (s *Server) candidates(curDir, root string, rootDev devID) []candidate {
	repos := filepath.Dir(curDir)
	entries, err := os.ReadDir(repos)
	if err != nil {
		return nil
	}
	var out []candidate
	for _, e := range entries {
		c, ok := s.readCandidate(filepath.Join(repos, e.Name()))
		if !ok {
			continue
		}
		dev, err := deviceOf(nearestExisting(c.root))
		if err != nil || !evictable(c, curDir, root, rootDev, dev) {
			continue
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b candidate) int {
		return cmp.Or(a.used.Compare(b.used), cmp.Compare(a.dir, b.dir))
	})
	return out
}

// formatAge renders d in days from 24h, else hours, else minutes.
func formatAge(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int((d+12*time.Hour)/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int((d+30*time.Minute)/time.Hour))
	}
	return fmt.Sprintf("%dm", int((d+30*time.Second)/time.Minute))
}

// ensureSpace evicts least-recently-used other copies until the filesystem of
// req.Root has req.MinFree bytes available, or fails when it cannot.
func (ss *session) ensureSpace(req *proto.Request, dir string) error {
	if req.MinFree <= 0 {
		return nil
	}
	minFree := uint64(req.MinFree)
	probe := nearestExisting(req.Root)
	free, err := ss.s.freeBytes(probe)
	if err != nil {
		ss.note(fmt.Sprintf("cannot measure free space on %s: %v", probe, err))
		return nil
	}
	if free >= minFree {
		return nil
	}
	rootDev, err := deviceOf(probe)
	if err != nil {
		ss.note(fmt.Sprintf("cannot stat %s: %v", probe, err))
		return nil
	}

	removed := 0
	for _, c := range ss.s.candidates(dir, req.Root, rootDev) {
		if !ss.evict(c) {
			continue
		}
		removed++
		before := free
		if free, err = ss.s.freeBytes(probe); err != nil {
			ss.note(fmt.Sprintf("cannot measure free space on %s: %v", probe, err))
			return nil
		}
		ss.note(fmt.Sprintf("freed %s (removed %s, last used %s ago)",
			config.FormatSize(free-min(free, before)), c.root, formatAge(time.Since(c.used))))
		if free >= minFree {
			return nil
		}
	}
	var clause string
	if removed > 0 {
		clause = fmt.Sprintf("; removed %d other %s", removed, plural(removed, "copy", "copies"))
	}
	return fmt.Errorf("remote disk low: %s free on %s, min_free is %s%s — clean build outputs in this project or lower min_free",
		config.FormatSize(free), probe, config.FormatSize(minFree), clause)
}

// evict removes a copy and its state unless its lock is held, and reports
// whether it did. Failures are noted and leave the copy for the next run.
func (ss *session) evict(c candidate) bool {
	holder, _ := json.Marshal(lockHolder{Run: "evict", StartedUnixMs: time.Now().UnixMilli(), PID: os.Getpid()})
	l, ok, err := lock.TryAcquire(filepath.Join(c.dir, "lock"), holder)
	if err != nil {
		ss.note(fmt.Sprintf("cannot lock %s: %v", c.root, err))
		return false
	}
	if !ok {
		return false
	}
	defer func() { _ = l.Release() }()
	for _, p := range []string{c.root, c.dir} {
		if err := os.RemoveAll(p); err != nil {
			ss.note(fmt.Sprintf("cannot remove %s: %v", c.root, err))
			return false
		}
	}
	return true
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
