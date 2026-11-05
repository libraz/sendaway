package remote

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/libraz/sendaway/internal/lock"
	"github.com/libraz/sendaway/internal/proto"
)

const (
	gib       = uint64(1) << 30
	baseFree  = 1 * gib
	copyWorth = 6 * gib
)

// spaceEnv is a state directory and project roots under one temp directory,
// with a fake free-space measure that grows as registered roots disappear.
type spaceEnv struct {
	t      *testing.T
	base   string
	srv    *Server
	ss     *session
	curDir string
	worth  map[string]uint64 // root -> bytes freed when it is removed
	calls  int
}

func newSpaceEnv(t *testing.T) *spaceEnv {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &spaceEnv{t: t, base: base, worth: map[string]uint64{}}
	e.srv = &Server{StateDir: filepath.Join(base, "state"), freeSpace: func(string) (uint64, error) {
		e.calls++
		free := baseFree
		for root, w := range e.worth {
			if _, err := os.Lstat(root); err != nil {
				free += w
			}
		}
		return free, nil
	}}
	e.ss = newSession(e.srv)
	t.Cleanup(e.ss.cancel)
	e.curDir = e.dir(e.path("proj"))
	if err := os.MkdirAll(e.curDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *spaceEnv) path(rel string) string { return filepath.Join(e.base, rel) }

func (e *spaceEnv) dir(root string) string {
	e.t.Helper()
	d, err := e.srv.repoDir(root)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func (e *spaceEnv) mkdir(p string) {
	e.t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		e.t.Fatal(err)
	}
}

// addCopy creates root and its state directory. A non-empty record is written
// as the root file with mtime used.
func (e *spaceEnv) addCopy(root, record string, used time.Time) string {
	e.t.Helper()
	e.mkdir(root)
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	dir := e.dir(root)
	e.mkdir(dir)
	e.worth[root] = copyWorth
	if record != "" {
		name := filepath.Join(dir, rootFile)
		if err := os.WriteFile(name, []byte(record), 0o600); err != nil {
			e.t.Fatal(err)
		}
		if err := os.Chtimes(name, used, used); err != nil {
			e.t.Fatal(err)
		}
	}
	return dir
}

func (e *spaceEnv) ensure(root string, minFree int64) error {
	e.t.Helper()
	return e.ss.ensureSpace(&proto.Request{Root: root, MinFree: minFree}, e.dir(root))
}

func (e *spaceEnv) notes() []string {
	var out []string
	for {
		select {
		case f := <-e.ss.out:
			out = append(out, string(f.body))
		default:
			return out
		}
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestEvictableModel(t *testing.T) {
	// Pairwise rows over current repository x relation of roots x device.
	cases := []struct {
		current  bool
		relation string
		sameDev  bool
	}{
		{false, "none", true},
		{false, "equal", false},
		{true, "none", false},
		{true, "equal", true},
		{false, "ancestor", true},
		{false, "descendant", true},
		{true, "ancestor", false},
		{true, "descendant", false},
	}
	roots := map[string]string{"none": "/p/other", "equal": "/p/proj", "ancestor": "/p", "descendant": "/p/proj/sub"}
	for _, tc := range cases {
		c := candidate{dir: "/s/repos/a", root: roots[tc.relation]}
		curDir := "/s/repos/b"
		if tc.current {
			curDir = c.dir
		}
		candDev := "dev1"
		if !tc.sameDev {
			candDev = "dev2"
		}
		want := !tc.current && tc.relation == "none" && tc.sameDev
		if got := evictable(c, curDir, "/p/proj", "dev1", candDev); got != want {
			t.Errorf("%+v: evictable = %v, want %v", tc, got, want)
		}
	}
	stateHolder := candidate{dir: "/s/repos/a", root: "/s"}
	if evictable(stateHolder, "/s/repos/b", "/p/proj", "dev1", "dev1") {
		t.Error("a copy that contains the state directory must not be evictable")
	}
	if within("/p/projx", "/p/proj") || !within("/p/proj/x", "/p/proj") || within("/p", "/p/proj") {
		t.Error("within must compare whole path components")
	}
}

func TestEvictionSkipConditions(t *testing.T) {
	// Pairwise rows over relation to the current root x root record x lock.
	cases := []struct{ relation, record, lock string }{
		{"none", "valid", "free"},
		{"none", "tampered", "held"},
		{"current", "valid", "held"},
		{"current", "tampered", "free"},
		{"ancestor", "missing", "free"},
		{"descendant", "missing", "held"},
		{"ancestor", "valid", "held"},
		{"descendant", "valid", "free"},
		{"none", "missing", "free"},
		{"current", "missing", "free"},
		{"ancestor", "tampered", "free"},
		{"descendant", "tampered", "free"},
	}
	for _, tc := range cases {
		t.Run(tc.relation+"/"+tc.record+"/"+tc.lock, func(t *testing.T) {
			e := newSpaceEnv(t)
			cur := e.path("proj")
			e.mkdir(cur)
			victim := e.path("victim")
			e.mkdir(victim)
			marker := filepath.Join(victim, "keep")
			if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			root := map[string]string{
				"none": e.path("other"), "current": cur, "ancestor": e.base, "descendant": e.path("proj/sub"),
			}[tc.relation]
			record := map[string]string{"valid": root, "tampered": victim, "missing": ""}[tc.record]
			dir := e.addCopy(root, record, time.Now().Add(-time.Hour))
			e.worth[root] = 20 * gib
			if tc.relation == "current" && dir != e.curDir {
				t.Fatalf("dir %s is not the current dir %s", dir, e.curDir)
			}
			if tc.lock == "held" {
				l, err := lock.Acquire(context.Background(), filepath.Join(dir, "lock"), []byte("{}"), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = l.Release() }()
			}

			err := e.ensure(cur, int64(10*gib))
			evict := tc.relation == "none" && tc.record == "valid" && tc.lock == "free"
			if evict != (err == nil) {
				t.Fatalf("err = %v, want success=%v", err, evict)
			}
			if evict == exists(root) || evict == exists(dir) {
				t.Errorf("evicted=%v but root exists=%v, dir exists=%v", evict, exists(root), exists(dir))
			}
			if !exists(marker) {
				t.Error("a tampered record redirected a deletion")
			}
		})
	}
}

func TestEvictionOrderAndStop(t *testing.T) {
	e := newSpaceEnv(t)
	cur := e.path("proj")
	e.mkdir(cur)
	now := time.Now()
	mid := e.path("mid")
	newest := e.path("newest")
	oldest := e.path("oldest")
	e.addCopy(mid, mid, now.Add(-5*time.Hour))
	e.addCopy(newest, newest, now.Add(-10*time.Minute))
	e.addCopy(oldest, oldest, now.Add(-72*time.Hour))

	if err := e.ensure(cur, int64(10*gib)); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"freed 6.0 GiB (removed " + oldest + ", last used 3d ago)",
		"freed 6.0 GiB (removed " + mid + ", last used 5h ago)",
	}
	if got := e.notes(); !slices.Equal(got, want) {
		t.Fatalf("notes = %q\nwant    %q", got, want)
	}
	if exists(oldest) || exists(mid) || !exists(newest) {
		t.Errorf("oldest exists=%v mid exists=%v newest exists=%v", exists(oldest), exists(mid), exists(newest))
	}
}

func TestLowDisk(t *testing.T) {
	t.Run("after removals", func(t *testing.T) {
		e := newSpaceEnv(t)
		cur := e.path("proj")
		e.mkdir(cur)
		for _, n := range []string{"a", "b"} {
			e.addCopy(e.path(n), e.path(n), time.Now().Add(-time.Hour))
		}
		err := e.ensure(cur, int64(100*gib))
		want := "remote disk low: 13.0 GiB free on " + cur + ", min_free is 100.0 GiB; removed 2 other copies — clean build outputs in this project or lower min_free"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v\nwant  %s", err, want)
		}
	})
	t.Run("without removals on a missing root", func(t *testing.T) {
		e := newSpaceEnv(t)
		err := e.ensure(e.path("new/proj"), int64(10*gib))
		want := "remote disk low: 1.0 GiB free on " + e.base + ", min_free is 10.0 GiB — clean build outputs in this project or lower min_free"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v\nwant  %s", err, want)
		}
	})
}

func TestEnsureSpaceNoCheck(t *testing.T) {
	e := newSpaceEnv(t)
	cur := e.path("proj")
	e.mkdir(cur)
	other := e.path("other")
	e.addCopy(other, other, time.Now().Add(-time.Hour))

	if err := e.ensure(cur, 0); err != nil || e.calls != 0 || !exists(other) {
		t.Fatalf("min_free 0: err %v, measured %d times, other exists %v", err, e.calls, exists(other))
	}
	if err := e.ensure(cur, int64(baseFree)); err != nil || !exists(other) {
		t.Fatalf("enough space: err %v, other exists %v", err, exists(other))
	}
}

func TestReadCandidate(t *testing.T) {
	srv := &Server{StateDir: t.TempDir()}
	good := "/srv/proj"
	goodDir, _ := srv.repoDir(good)
	otherDir, _ := srv.repoDir("/srv/other")
	cases := []struct {
		name, dir, content string
		symlink            bool
		ok                 bool
	}{
		{"valid", goodDir, good, false, true},
		{"hash mismatch", otherDir, good, false, false},
		{"trailing newline", goodDir, good + "\n", false, false},
		{"relative", goodDir, "srv/proj", false, false},
		{"unclean", goodDir, "/srv/x/../proj", false, false},
		{"slash", goodDir, "/", false, false},
		{"empty", goodDir, "", false, false},
		{"symlink record", goodDir, good, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(tc.dir, 0o700); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(tc.dir, rootFile)
			_ = os.Remove(name)
			if tc.symlink {
				real := filepath.Join(t.TempDir(), "real")
				if err := os.WriteFile(real, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, name); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(name, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			c, ok := srv.readCandidate(tc.dir)
			if ok != tc.ok || (ok && c.root != good) {
				t.Fatalf("readCandidate = %+v, %v; want ok=%v", c, ok, tc.ok)
			}
		})
	}
	if _, ok := srv.readCandidate(filepath.Join(srv.StateDir, "nope")); ok {
		t.Error("missing record accepted")
	}
}

func TestFormatAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{20 * time.Second, "0m"},
		{90 * time.Second, "2m"},
		{59 * time.Minute, "59m"},
		{time.Hour, "1h"},
		{90 * time.Minute, "2h"},
		{23 * time.Hour, "23h"},
		{24 * time.Hour, "1d"},
		{35 * time.Hour, "1d"},
		{36 * time.Hour, "2d"},
		{72 * time.Hour, "3d"},
	}
	for _, tc := range cases {
		if got := formatAge(tc.d); got != tc.want {
			t.Errorf("formatAge(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestRootRecordAndLowDiskExec(t *testing.T) {
	f := newFixture(t)
	req := f.request("true")
	req.MinFree = 1
	if e, _ := f.runExec(t, req, nil); e.Error != "" {
		t.Fatalf("exec: %+v", e)
	}
	rec := filepath.Join(f.repoDir(t), rootFile)
	b, err := os.ReadFile(rec)
	if err != nil || string(b) != f.root {
		t.Fatalf("root record = %q, %v", b, err)
	}

	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(rec, old, old); err != nil {
		t.Fatal(err)
	}
	c := f.start(t)
	c.startPings()
	c.send(proto.Request{Protocol: proto.Protocol, Op: proto.OpFetch, Run: "r-fetch", Root: f.root, SentUnixMs: time.Now().UnixMilli()})
	if e, _ := c.exit(); e.Error != "" {
		t.Fatalf("fetch: %+v", e)
	}
	if fi, err := os.Stat(rec); err != nil || time.Since(fi.ModTime()) > time.Hour {
		t.Fatalf("fetch left the record at %v, %v", fi, err)
	}

	req = f.request("true")
	req.MinFree = 1<<63 - 1
	c = f.start(t)
	c.startPings()
	c.send(req)
	if e, _ := c.exit(); !strings.HasPrefix(e.Error, "remote disk low: ") || !strings.Contains(e.Error, "min_free is ") {
		t.Fatalf("exit %+v", e)
	}
}
