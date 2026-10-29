package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/libraz/sendaway/internal/lock"
	"github.com/libraz/sendaway/internal/proto"
)

// envServe switches the test binary into a remote-exec server whose state
// directory is the variable's value.
const envServe = "REMOTE_TEST_SERVE_STATE"

const (
	testPingTimeout = 3 * time.Second
	waitLimit       = 15 * time.Second
	sleeper         = `sleep 300 & echo "$$ $!"; sleep 300`
)

func TestMain(m *testing.M) {
	if state := os.Getenv(envServe); state != "" {
		s := &Server{
			Version:      "test",
			StateDir:     state,
			PingInterval: time.Second,
			PingTimeout:  testPingTimeout,
			KillGrace:    time.Second,
		}
		if err := s.Serve(os.Stdin, os.Stdout); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "remote-exec:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fixture is a repository root and remote state directory.
type fixture struct {
	root, state, home string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &fixture{root: root, state: t.TempDir(), home: t.TempDir()}
}

func (f *fixture) repoDir(t *testing.T) string {
	t.Helper()
	d, err := (&Server{StateDir: f.state}).repoDir(f.root)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f *fixture) request(argv ...string) proto.Request {
	return proto.Request{
		Protocol:   proto.Protocol,
		Op:         proto.OpExec,
		Run:        "r-test",
		Root:       f.root,
		Cwd:        f.root,
		Argv:       argv,
		SentUnixMs: time.Now().UnixMilli(),
	}
}

func (f *fixture) write(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(f.root, rel))
	return err == nil
}

// client drives a remote-exec subprocess over real pipes.
type client struct {
	t      *testing.T
	cmd    *exec.Cmd
	in     *os.File
	out    *os.File
	frames chan frame
	exited chan struct{}

	mu       sync.Mutex
	stopPing chan struct{}
}

func (f *fixture) start(t *testing.T, extraEnv ...string) *client {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0])
	cmd.Env = append([]string{
		envServe + "=" + f.state,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + f.home,
		"USER=" + os.Getenv("USER"),
		"LOGNAME=" + os.Getenv("LOGNAME"),
		"SHELL=/bin/sh",
	}, extraEnv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = inR.Close()
	_ = outW.Close()
	c := &client{t: t, cmd: cmd, in: inW, out: outR, frames: make(chan frame, 4096), exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(c.exited)
	}()
	go func() {
		defer close(c.frames)
		for {
			typ, body, err := proto.ReadFrame(outR)
			if err != nil {
				return
			}
			c.frames <- frame{typ, body}
		}
	}()
	t.Cleanup(func() {
		c.stopPings()
		_ = inW.Close()
		select {
		case <-c.exited:
		case <-time.After(waitLimit):
			_ = cmd.Process.Kill()
			<-c.exited
		}
		_ = outR.Close()
	})

	typ, body := c.next()
	if typ != proto.TypeHello {
		t.Fatalf("first frame type 0x%02x, want hello", typ)
	}
	var h proto.Hello
	if err := json.Unmarshal(body, &h); err != nil || h.Protocol != proto.Protocol {
		t.Fatalf("hello = %s, %v", body, err)
	}
	return c
}

func (c *client) send(req proto.Request) {
	c.t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		c.t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) frame(t proto.Type) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return proto.WriteFrame(c.in, t, nil)
}

func (c *client) startPings() {
	c.stopPing = make(chan struct{})
	stop := c.stopPing
	go func() {
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				if c.frame(proto.TypePing) != nil {
					return
				}
			}
		}
	}()
}

func (c *client) stopPings() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopPing != nil {
		close(c.stopPing)
		c.stopPing = nil
	}
}

func (c *client) next() (proto.Type, []byte) {
	c.t.Helper()
	select {
	case f, ok := <-c.frames:
		if !ok {
			c.t.Fatal("remote-exec closed stdout")
		}
		return f.typ, f.body
	case <-time.After(waitLimit):
		c.t.Fatal("timed out waiting for a frame")
	}
	return 0, nil
}

// until reads frames up to and including one of type want.
func (c *client) until(want proto.Type) []frame {
	c.t.Helper()
	var got []frame
	for {
		typ, body := c.next()
		got = append(got, frame{typ, body})
		if typ == want {
			return got
		}
		if typ == proto.TypeExit {
			c.t.Fatalf("exit before frame 0x%02x: %s", want, body)
		}
	}
}

func (c *client) exit() (proto.Exit, []frame) {
	c.t.Helper()
	got := c.until(proto.TypeExit)
	var e proto.Exit
	if err := json.Unmarshal(got[len(got)-1].body, &e); err != nil {
		c.t.Fatal(err)
	}
	return e, got
}

func notes(fs []frame) []string {
	var out []string
	for _, f := range fs {
		if f.typ == proto.TypeNote {
			out = append(out, string(f.body))
		}
	}
	return out
}

func text(fs []frame, t proto.Type) string {
	var b strings.Builder
	for _, f := range fs {
		if f.typ == t {
			b.Write(f.body)
		}
	}
	return b.String()
}

// runExec runs a full exec session with pings. beforePushed runs after
// ready, in place of the push.
func (f *fixture) runExec(t *testing.T, req proto.Request, beforePushed func(), extraEnv ...string) (proto.Exit, []frame) {
	t.Helper()
	c := f.start(t, extraEnv...)
	c.startPings()
	c.send(req)
	got := c.until(proto.TypeReady)
	if beforePushed != nil {
		beforePushed()
	}
	if err := c.frame(proto.TypePushed); err != nil {
		t.Fatal(err)
	}
	e, rest := c.exit()
	return e, append(got, rest...)
}

// startSleeper starts a child with a background grandchild and returns its
// process group after checking that the grandchild shares it.
func (f *fixture) startSleeper(t *testing.T, c *client) int {
	t.Helper()
	c.send(f.request("sh", "-c", sleeper))
	c.until(proto.TypeReady)
	if err := c.frame(proto.TypePushed); err != nil {
		t.Fatal(err)
	}
	var out string
	for !strings.Contains(out, "\n") {
		typ, body := c.next()
		if typ == proto.TypeExit {
			t.Fatalf("exit before child output: %s", body)
		}
		if typ == proto.TypeOut {
			out += string(body)
		}
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		t.Fatalf("child output %q", out)
	}
	pgid, _ := strconv.Atoi(fields[0])
	bg, _ := strconv.Atoi(fields[1])
	if got, err := syscall.Getpgid(bg); err != nil || got != pgid {
		t.Fatalf("grandchild %d pgid = %d, %v; want %d", bg, got, err, pgid)
	}
	return pgid
}

func assertGroupGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			t.Fatalf("process group %d still has members", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (f *fixture) assertLockFree(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	l, err := lock.Acquire(ctx, filepath.Join(f.repoDir(t), "lock"), nil, nil)
	if err != nil {
		t.Fatalf("remote lock still held: %v", err)
	}
	_ = l.Release()
}

func assertCancelled(t *testing.T, e proto.Exit) {
	t.Helper()
	if e.Error != errCancelled {
		t.Fatalf("exit = %+v, want error %q", e, errCancelled)
	}
}

func TestCancelKillsGroup(t *testing.T) {
	f := newFixture(t)
	c := f.start(t)
	c.startPings()
	pgid := f.startSleeper(t, c)
	if err := c.frame(proto.TypeCancel); err != nil {
		t.Fatal(err)
	}
	e, _ := c.exit()
	assertCancelled(t, e)
	assertGroupGone(t, pgid)
	f.assertLockFree(t)
}

func TestStdinEOFKillsGroup(t *testing.T) {
	f := newFixture(t)
	c := f.start(t)
	c.startPings()
	pgid := f.startSleeper(t, c)
	c.stopPings()
	c.mu.Lock()
	_ = c.in.Close()
	c.mu.Unlock()
	e, _ := c.exit()
	assertCancelled(t, e)
	assertGroupGone(t, pgid)
	f.assertLockFree(t)
}

func TestPingTimeout(t *testing.T) {
	f := newFixture(t)
	c := f.start(t)
	pgid := f.startSleeper(t, c)
	started := time.Now()
	e, _ := c.exit()
	assertCancelled(t, e)
	if waited := time.Since(started); waited < testPingTimeout/2 {
		t.Fatalf("aborted after %s, before the ping timeout", waited)
	}
	assertGroupGone(t, pgid)
	f.assertLockFree(t)
}

func TestPingTimeoutBeforePushed(t *testing.T) {
	f := newFixture(t)
	c := f.start(t)
	req := f.request("true")
	req.Manifest = []string{"a"}
	c.send(req)
	c.until(proto.TypeReady)
	e, _ := c.exit()
	assertCancelled(t, e)
	f.assertLockFree(t)
	if _, err := os.Stat(filepath.Join(f.repoDir(t), manifestFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest state written before pushed: %v", err)
	}
}

func TestSIGPIPESurvives(t *testing.T) {
	f := newFixture(t)
	c := f.start(t)
	c.startPings()
	c.send(f.request("sh", "-c", `sleep 300 & echo "$$ $!"; while :; do echo x; sleep 0.1; done`))
	c.until(proto.TypeReady)
	if err := c.frame(proto.TypePushed); err != nil {
		t.Fatal(err)
	}
	var out string
	for !strings.Contains(out, "\n") {
		if typ, body := c.next(); typ == proto.TypeOut {
			out += string(body)
		}
	}
	fields := strings.Fields(out)
	pgid, _ := strconv.Atoi(fields[0])

	_ = c.out.Close()
	select {
	case <-c.exited:
	case <-time.After(waitLimit):
		t.Fatal("remote-exec did not exit after stdout closed")
	}
	if ws, ok := c.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		t.Fatalf("remote-exec killed by %v", ws.Signal())
	}
	assertGroupGone(t, pgid)
	f.assertLockFree(t)
}

func TestDeleteByManifest(t *testing.T) {
	f := newFixture(t)
	first := []string{"a", "d/x", "d/y/z", "e/w", "f"}
	req := f.request("true")
	req.Manifest = first
	e, got := f.runExec(t, req, func() {
		for _, p := range first {
			f.write(t, p, p)
		}
	})
	if e.Error != "" || e.Code != 0 {
		t.Fatalf("first run: %+v", e)
	}
	if !slices.Contains(notes(got), noStateNote) {
		t.Fatalf("notes = %q, want the missing-state note", notes(got))
	}

	// Files the host created itself, and a directory where a file will go.
	f.write(t, "d/keep", "host")
	f.write(t, "build/out", "host")
	f.write(t, "d2/inner", "host")

	second := []string{"a", "d2", "f/g"}
	req = f.request("true")
	req.Manifest = second
	e, got = f.runExec(t, req, func() {
		for _, gone := range []string{"d/x", "d/y", "e", "f", "d2"} {
			if f.exists(gone) {
				t.Errorf("%s still exists after deletion", gone)
			}
		}
		for _, kept := range []string{"a", "d/keep", "build/out"} {
			if !f.exists(kept) {
				t.Errorf("%s was deleted", kept)
			}
		}
		f.write(t, "d2", "file")
		f.write(t, "f/g", "file")
	})
	if e.Error != "" || e.Code != 0 {
		t.Fatalf("second run: %+v", e)
	}
	if n := notes(got); len(n) != 0 {
		t.Fatalf("unexpected notes %q", n)
	}
	stored, ok, err := readManifest(f.repoDir(t))
	if err != nil || !ok || !slices.Equal(stored, second) {
		t.Fatalf("stored manifest = %q, %v, %v; want %q", stored, ok, err, second)
	}
}

func TestPrepareHash(t *testing.T) {
	f := newFixture(t)
	f.write(t, "lock.txt", "v1")
	f.write(t, "other.txt", "o")
	rule := proto.PrepareRule{
		When: []string{"lock.txt"},
		Run:  []string{"sh", "-c", `echo run >> count; printf 'line1\nline2\n'`},
	}
	run := func(rules ...proto.PrepareRule) (proto.Exit, []string) {
		req := f.request("sh", "-c", "echo ran > child")
		req.Manifest = []string{"lock.txt", "other.txt"}
		req.Prepare = rules
		e, got := f.runExec(t, req, nil)
		return e, notes(got)
	}
	runs := func() int {
		b, _ := os.ReadFile(filepath.Join(f.root, "count"))
		return strings.Count(string(b), "\n")
	}

	e, n := run(rule)
	if e.Error != "" || runs() != 1 || !slices.Contains(n, "line1") || !slices.Contains(n, "line2") {
		t.Fatalf("first: exit %+v, runs %d, notes %q", e, runs(), n)
	}
	inner := sha256.Sum256([]byte("v1"))
	outer := sha256.Sum256(inner[:])
	var state map[string]string
	b, _ := os.ReadFile(filepath.Join(f.repoDir(t), prepareFile))
	if err := json.Unmarshal(b, &state); err != nil || state["lock.txt"] != hex.EncodeToString(outer[:]) {
		t.Fatalf("prepare.json = %s, %v", b, err)
	}

	if e, n = run(rule); e.Error != "" || runs() != 1 || slices.Contains(n, "line1") {
		t.Fatalf("unchanged: exit %+v, runs %d, notes %q", e, runs(), n)
	}

	f.write(t, "lock.txt", "v2")
	if e, _ = run(rule); e.Error != "" || runs() != 2 {
		t.Fatalf("changed: exit %+v, runs %d", e, runs())
	}

	if err := os.Remove(filepath.Join(f.root, "child")); err != nil {
		t.Fatal(err)
	}
	failing := proto.PrepareRule{When: []string{"other.txt"}, Run: []string{"sh", "-c", "echo boom; exit 3"}}
	e, n = run(rule, failing)
	if !strings.Contains(e.Error, "exited 3") || !slices.Contains(n, "boom") {
		t.Fatalf("failing: exit %+v, notes %q", e, n)
	}
	if f.exists("child") {
		t.Fatal("command ran after a failed prepare")
	}
	b, _ = os.ReadFile(filepath.Join(f.repoDir(t), prepareFile))
	if strings.Contains(string(b), "other.txt") {
		t.Fatalf("failed rule hash stored: %s", b)
	}
}

func TestModified(t *testing.T) {
	f := newFixture(t)
	req := f.request("sh", "-c", "echo changed >> a; echo changed >> b; exit 1")
	req.Manifest = []string{"a", "b", "c"}
	req.Artifacts = []string{"b"}
	e, _ := f.runExec(t, req, func() {
		for _, p := range req.Manifest {
			f.write(t, p, "orig")
		}
		f.write(t, ".git/config", "[remote \"origin\"]\n\turl = https://token@example.com/x\n")
	})
	if e.Error != "" || e.Code != 1 {
		t.Fatalf("exit = %+v", e)
	}
	if !slices.Equal(e.Modified, []string{"a"}) || !slices.Equal(e.Collect, []string{"b"}) {
		t.Fatalf("modified = %q, collect = %q", e.Modified, e.Collect)
	}
	if b, _ := os.ReadFile(filepath.Join(f.root, ".git", "config")); string(b) != gitConfig {
		t.Fatalf(".git/config = %q", b)
	}
}

func TestCollect(t *testing.T) {
	f := newFixture(t)
	elf := "\x7fELF\x02\x01\x01"
	run := func(argv string, pull, artifacts []string) proto.Exit {
		req := f.request("sh", "-c", argv)
		req.Pull, req.Artifacts = pull, artifacts
		e, _ := f.runExec(t, req, nil)
		return e
	}

	f.write(t, "dist/text.txt", "hello")
	f.write(t, "dist/mod.wasm", "\x00asm\x01\x00\x00\x00")
	if e := run("true", []string{"dist"}, nil); e.Error != "" || len(e.Rejected) != 0 ||
		!slices.Equal(e.Collect, []string{"dist/mod.wasm", "dist/text.txt"}) {
		t.Fatalf("clean pull: %+v", e)
	}

	f.write(t, "dist/bin", elf)
	f.write(t, "dist/a.gz", "\x1f\x8b\x08")
	if err := os.Symlink("text.txt", filepath.Join(f.root, "dist/link")); err != nil {
		t.Fatal(err)
	}
	want := []proto.Rejected{{Path: "dist/a.gz", Reason: "archive"}, {Path: "dist/bin", Reason: "elf"}, {Path: "dist/link", Reason: "symlink"}}
	if e := run("true", []string{"dist"}, nil); e.Error != "" || len(e.Collect) != 0 || !slices.Equal(e.Rejected, want) {
		t.Fatalf("refused pull: %+v", e)
	}

	f.write(t, "logs/a.gz", "\x1f\x8b\x08")
	f.write(t, "logs/bin", elf)
	f.write(t, "logs/t.txt", "log")
	for _, big := range []string{"logs/big1", "logs/big2"} {
		f.write(t, big, "")
		if err := os.Truncate(filepath.Join(f.root, big), 40<<20); err != nil {
			t.Fatal(err)
		}
	}
	want = []proto.Rejected{{Path: "logs/big2", Reason: "limit"}, {Path: "logs/bin", Reason: "elf"}}
	e := run("exit 2", []string{"dist"}, []string{"logs", "missing"})
	if e.Error != "" || e.Code != 2 || !slices.Equal(e.Collect, []string{"logs/a.gz", "logs/big1", "logs/t.txt"}) ||
		!slices.Equal(e.Rejected, want) {
		t.Fatalf("artifacts: %+v", e)
	}

	if e := run("true", []string{"nope"}, nil); !strings.Contains(e.Error, "does not exist") {
		t.Fatalf("missing pull: %+v", e)
	}
}

func TestChildEnvScrubbed(t *testing.T) {
	f := newFixture(t)
	req := f.request("sh", "-c", "env; pwd -P; echo to-stderr >&2")
	req.Cwd = filepath.Join(f.root, "sub")
	req.Env = map[string]string{"FORCE_COLOR": "1"}
	e, got := f.runExec(t, req, nil, "LANG=xx_LEAK.UTF-8", "SSH_AUTH_SOCK=/tmp/LEAK", "GITHUB_TOKEN=LEAK")
	if e.Error != "" || e.Code != 0 {
		t.Fatalf("exit = %+v", e)
	}
	out := text(got, proto.TypeOut)
	if strings.Contains(out, "LEAK") {
		t.Fatalf("server environment leaked into the child:\n%s", out)
	}
	lines := strings.Split(out, "\n")
	for _, want := range []string{"FORCE_COLOR=1", "SENDAWAY_PASSTHROUGH=1", "HOME=" + f.home, req.Cwd} {
		if !slices.Contains(lines, want) {
			t.Errorf("child output lacks %q:\n%s", want, out)
		}
	}
	if errOut := text(got, proto.TypeErr); errOut != "to-stderr\n" {
		t.Fatalf("stderr frames = %q", errOut)
	}
}

func TestFetchOp(t *testing.T) {
	f := newFixture(t)
	f.write(t, "dist/text.txt", "hello")
	f.write(t, "dist/bin", "\x7fELF")
	fetch := func(pull []string, skew time.Duration, held *lock.Lock) (proto.Exit, []frame) {
		c := f.start(t)
		c.startPings()
		c.send(proto.Request{
			Protocol: proto.Protocol, Op: proto.OpFetch, Run: "r-fetch", Root: f.root,
			Pull: pull, SentUnixMs: time.Now().Add(-skew).UnixMilli(),
		})
		var got []frame
		if held != nil {
			got = c.until(proto.TypeNote)
			_ = held.Release()
		}
		e, rest := c.exit()
		return e, append(got, rest...)
	}

	dir := f.repoDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	holder := fmt.Sprintf(`{"run":"r-other","started_unix_ms":%d,"pid":1}`, time.Now().UnixMilli())
	held, err := lock.Acquire(context.Background(), filepath.Join(dir, "lock"), []byte(holder), nil)
	if err != nil {
		t.Fatal(err)
	}
	e, got := fetch([]string{"dist/text.txt"}, 5*time.Second, held)
	n := notes(got)
	if e.Error != "" || !slices.Equal(e.Collect, []string{"dist/text.txt"}) || len(n) != 2 ||
		!strings.HasPrefix(n[0], "waiting for remote lock (held by run r-other, ") ||
		!strings.HasPrefix(n[1], "remote clock differs from local by ") {
		t.Fatalf("exit %+v, notes %q", e, n)
	}
	for _, fr := range got {
		if fr.typ == proto.TypeReady {
			t.Fatal("fetch sent ready")
		}
	}

	e, _ = fetch([]string{"dist"}, 0, nil)
	if e.Error != "" || len(e.Collect) != 0 || !slices.Equal(e.Rejected, []proto.Rejected{{Path: "dist/bin", Reason: "elf"}}) {
		t.Fatalf("refused fetch: %+v", e)
	}

	if e, _ = fetch([]string{"dist"}, 2*time.Minute, nil); !strings.Contains(e.Error, "remote clock differs") {
		t.Fatalf("skewed fetch: %+v", e)
	}
}

func TestForgetOp(t *testing.T) {
	f := newFixture(t)
	req := f.request("true")
	req.Manifest = []string{"a"}
	if e, _ := f.runExec(t, req, func() { f.write(t, "a", "a") }); e.Error != "" {
		t.Fatalf("exec: %+v", e)
	}
	dir := f.repoDir(t)
	if _, err := os.Stat(filepath.Join(dir, manifestFile)); err != nil {
		t.Fatal(err)
	}

	c := f.start(t)
	c.startPings()
	c.send(proto.Request{Protocol: proto.Protocol, Op: proto.OpForget, Run: "r-forget", Root: f.root})
	if e, _ := c.exit(); e.Error != "" {
		t.Fatalf("forget: %+v", e)
	}
	for _, p := range []string{f.root, dir} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived forget: %v", p, err)
		}
	}
}
