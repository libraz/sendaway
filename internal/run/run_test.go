package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/libraz/sendaway/internal/config"
	"github.com/libraz/sendaway/internal/manifest"
	"github.com/libraz/sendaway/internal/remote"
	"github.com/libraz/sendaway/internal/runlog"
	"github.com/libraz/sendaway/internal/sshx"
)

// envDie makes the test binary die through DieBySignal with the given signal number.
const envDie = "RUN_TEST_DIE_SIGNAL"

const testHost = "testhost"

func TestMain(m *testing.M) {
	if v := os.Getenv(envDie); v != "" {
		n, _ := strconv.Atoi(v)
		DieBySignal(syscall.Signal(n))
	}
	pingInterval = 200 * time.Millisecond
	os.Exit(m.Run())
}

// conn is one in-process remote-exec session.
type conn struct {
	inR, outW, errW *os.File
	served          chan struct{}
	lost            atomic.Bool
}

// cut drops the connection the way a dead network would.
func (c *conn) cut() {
	c.lost.Store(true)
	_ = c.inR.Close()
	_ = c.outW.Close()
	_ = c.errW.Close()
}

// pipeTransport serves sessions with an in-process remote.Server over pipes.
// The host's copy of the root is the root itself, so Push sends nothing.
type pipeTransport struct {
	server *remote.Server

	mu       sync.Mutex
	cur      *conn
	excludes [][]string
	onPush   func(c *conn) error
}

func (tr *pipeTransport) Open(ctx context.Context) (*sshx.Session, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c := &conn{inR: inR, outW: outW, errW: errW, served: make(chan struct{})}
	tr.mu.Lock()
	tr.cur = c
	tr.mu.Unlock()
	go func() {
		_ = tr.server.Serve(inR, outW)
		_ = outW.Close()
		_ = errW.Close()
		_ = inR.Close()
		close(c.served)
	}()
	stop := context.AfterFunc(ctx, c.cut)
	return &sshx.Session{
		Stdin:  inW,
		Stdout: outR,
		Stderr: errR,
		Wait: func() (int, error) {
			<-c.served
			stop()
			_ = outR.Close()
			_ = errR.Close()
			if c.lost.Load() {
				return sshLost, nil
			}
			return 0, nil
		},
	}, nil
}

func (tr *pipeTransport) Push(_ context.Context, _ string, excludes []string) (sshx.PushResult, error) {
	tr.mu.Lock()
	tr.excludes = append(tr.excludes, excludes)
	hook, c := tr.onPush, tr.cur
	tr.mu.Unlock()
	if hook != nil {
		return sshx.PushResult{}, hook(c)
	}
	return sshx.PushResult{}, nil
}

func (tr *pipeTransport) Pull(_ context.Context, root string, files []string, dest string) error {
	if dest == root {
		return nil
	}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			return err
		}
		p := filepath.Join(dest, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// fixture is a git repository with local and remote state directories.
type fixture struct {
	root, state, remoteState, home string
	tr                             *pipeTransport
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/sh")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{root: root, state: t.TempDir(), remoteState: t.TempDir(), home: home}
	f.tr = &pipeTransport{server: &remote.Server{
		Version:     "test",
		StateDir:    f.remoteState,
		PingTimeout: 3 * time.Second,
		KillGrace:   time.Second,
	}}
	f.git(t, "init", "-q")
	f.write(t, "a.txt", "a\n")
	f.git(t, "add", "a.txt")
	return f
}

func (f *fixture) git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", f.root}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (f *fixture) write(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// runRemote runs argv through Exec against the in-process host.
func (f *fixture) runRemote(stdout, stderr *os.File, proj *config.Project, argv ...string) Outcome {
	if proj == nil {
		proj = &config.Project{}
	}
	return Exec(ExecParams{
		Params: Params{
			Root:      f.root,
			Host:      testHost,
			Version:   "test",
			Stderr:    stderr,
			Transport: f.tr,
		},
		Project:  proj,
		Argv:     argv,
		Cwd:      f.root,
		StateDir: f.state,
		Environ:  os.Environ(),
		Home:     f.home,
		Stdout:   stdout,
	})
}

// remoteManifest reads the manifest the host stored for the root.
func (f *fixture) remoteManifest(t *testing.T) ([]string, bool) {
	t.Helper()
	sum := sha256.Sum256([]byte(f.root))
	data, err := os.ReadFile(filepath.Join(f.remoteState, "repos", hex.EncodeToString(sum[:])[:16], "manifest"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for p := range bytes.SplitSeq(data, []byte{0}) {
		if len(p) > 0 {
			out = append(out, string(p))
		}
	}
	return out, true
}

func tempFile(t *testing.T, name string) *os.File {
	t.Helper()
	fh, err := os.Create(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fh.Close() })
	return fh
}

func readAll(t *testing.T, fh *os.File) string {
	t.Helper()
	data, err := os.ReadFile(fh.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// split separates sendaway's lines from command output lines.
func split(s string) (own, other []string) {
	for l := range strings.SplitSeq(strings.TrimSuffix(s, "\n"), "\n") {
		if strings.HasPrefix(l, linePrefix) {
			own = append(own, l)
		} else if l != "" {
			other = append(other, l)
		}
	}
	return own, other
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	return lines[len(lines)-1]
}

const orderScript = `echo out1; echo err1 >&2; echo out2`

func TestRunOrderMerged(t *testing.T) {
	f := newFixture(t)
	out := tempFile(t, "merged")
	o := f.runRemote(out, out, nil, "sh", "-c", orderScript)
	got := readAll(t, out)
	if o != (Outcome{}) {
		t.Fatalf("outcome = %+v\n%s", o, got)
	}
	if _, other := split(got); !slices.Equal(other, []string{"out1", "err1", "out2"}) {
		t.Fatalf("command lines = %q\n%s", other, got)
	}
}

func TestRunOrderSplit(t *testing.T) {
	f := newFixture(t)
	out, errf := tempFile(t, "stdout"), tempFile(t, "stderr")
	o := f.runRemote(out, errf, nil, "sh", "-c", orderScript)
	stderr := readAll(t, errf)
	if o != (Outcome{}) {
		t.Fatalf("outcome = %+v\n%s", o, stderr)
	}
	if got := readAll(t, out); got != "out1\nout2\n" {
		t.Fatalf("stdout = %q", got)
	}
	if _, other := split(stderr); !slices.Equal(other, []string{"err1"}) {
		t.Fatalf("stderr command lines = %q\n%s", other, stderr)
	}
	if !strings.Contains(lastLine(stderr), " exit 0 · ") {
		t.Fatalf("stderr lacks the trailer:\n%s", stderr)
	}
}

func TestExitCodes(t *testing.T) {
	f := newFixture(t)
	for _, code := range []int{0, 1, 7, 255} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			errf := tempFile(t, "stderr")
			o := f.runRemote(errf, errf, nil, "sh", "-c", "exit "+strconv.Itoa(code))
			got := readAll(t, errf)
			if o != (Outcome{Code: code}) {
				t.Fatalf("outcome = %+v, want code %d\n%s", o, code, got)
			}
			if !strings.Contains(lastLine(got), " exit "+strconv.Itoa(code)+" · ") {
				t.Fatalf("trailer = %q", lastLine(got))
			}
		})
	}
}

func TestSignalDeath(t *testing.T) {
	f := newFixture(t)
	errf := tempFile(t, "stderr")
	o := f.runRemote(errf, errf, nil, "sh", "-c", "kill -TERM $$")
	got := readAll(t, errf)
	if o.Signal != syscall.SIGTERM || o.Code != 128+int(syscall.SIGTERM) || o.Local {
		t.Fatalf("outcome = %+v, want SIGTERM\n%s", o, got)
	}
	if !strings.Contains(lastLine(got), " killed by signal 15 · ") {
		t.Fatalf("trailer = %q", lastLine(got))
	}

	t.Run("DieBySignal", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), envDie+"="+strconv.Itoa(int(syscall.SIGTERM)))
		err := cmd.Run()
		ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if err == nil || !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
			t.Fatalf("child ended with %v (%v), want death by SIGTERM", cmd.ProcessState, err)
		}
	})
}

var (
	startRe   = regexp.MustCompile(`^\[sendaway\] ` + testHost + ` [a-z0-9]+-[a-z0-9]+ · sync \d+\.\d{2}s$`)
	trailerRe = regexp.MustCompile(`^\[sendaway\] ` + testHost + ` exit 0 · \d+\.\ds · run (r-\d{8}-\d{6}-[0-9a-f]{4}) · log (\S+)$`)
)

func TestTrailer(t *testing.T) {
	f := newFixture(t)
	for i := range 7 {
		f.write(t, "m/"+strconv.Itoa(i), "x")
	}
	f.git(t, "add", "m")

	cases := []struct {
		name, script, warning string
	}{
		{"one", `echo more >> a.txt`,
			"[sendaway] remote run modified 1 synced files (not copied back): a.txt — add this target to local: or list the files in pull:"},
		{"many", `for i in 0 1 2 3 4 5 6; do echo y >> m/$i; done`,
			"[sendaway] remote run modified 7 synced files (not copied back): m/0, m/1, m/2, m/3, m/4 … and 2 more — add this target to local: or list the files in pull:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errf := tempFile(t, "stderr")
			o := f.runRemote(errf, errf, nil, "sh", "-c", tc.script)
			got := readAll(t, errf)
			if o != (Outcome{}) {
				t.Fatalf("outcome = %+v\n%s", o, got)
			}
			own, _ := split(got)
			if len(own) < 3 {
				t.Fatalf("too few sendaway lines:\n%s", got)
			}
			if !slices.ContainsFunc(own, startRe.MatchString) {
				t.Fatalf("no start line:\n%s", got)
			}
			m := trailerRe.FindStringSubmatch(lastLine(got))
			if m == nil {
				t.Fatalf("trailer = %q", lastLine(got))
			}
			if want := filepath.Join(f.state, "runs", m[1], "output.log"); m[2] != want {
				t.Fatalf("log path = %s, want %s", m[2], want)
			}
			if w := own[len(own)-2]; w != tc.warning {
				t.Fatalf("line before trailer = %q\nwant %q", w, tc.warning)
			}
		})
	}
}

func TestRecord(t *testing.T) {
	f := newFixture(t)
	m, err := manifest.Build(f.root)
	if err != nil {
		t.Fatal(err)
	}
	out := tempFile(t, "merged")
	argv := []string{"sh", "-c", "echo hello; exit 3"}
	o := f.runRemote(out, out, nil, argv...)
	shown := readAll(t, out)
	if o != (Outcome{Code: 3}) {
		t.Fatalf("outcome = %+v\n%s", o, shown)
	}

	meta, err := runlog.Open(f.state, nil).Latest()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastLine(shown), " run "+meta.ID+" ") {
		t.Fatalf("trailer %q does not name run %s", lastLine(shown), meta.ID)
	}
	want := runlog.Meta{
		ID:          meta.ID,
		Argv:        argv,
		Cwd:         f.root,
		Root:        f.root,
		Host:        testHost,
		StartUnixMS: meta.StartUnixMS,
		DurationMS:  meta.DurationMS,
		SyncMS:      meta.SyncMS,
		Code:        3,
		Fingerprint: m.Fingerprint(),
	}
	if !reflect.DeepEqual(meta, want) {
		t.Fatalf("meta = %+v\nwant %+v", meta, want)
	}
	if meta.StartUnixMS == 0 || meta.DurationMS < meta.SyncMS {
		t.Fatalf("times: start %d, duration %d, sync %d", meta.StartUnixMS, meta.DurationMS, meta.SyncMS)
	}
	logged, err := os.ReadFile(filepath.Join(f.state, "runs", meta.ID, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(logged) != shown {
		t.Fatalf("output.log differs from what was shown:\n%q\n%q", logged, shown)
	}
}

func TestExcludeComposition(t *testing.T) {
	f := newFixture(t)
	f.write(t, ".gitignore", "build/\n*.log\nout*/\n")
	f.write(t, "build/obj.o", "o")
	f.write(t, "debug.log", "d")
	f.write(t, "out[1]/f", "f")
	f.write(t, ".env", "TOKEN=x\n")
	f.write(t, "config/.npmrc", "//registry.example/:_authToken=x\n")
	f.write(t, "fixtures/private/p.txt", "p")
	f.write(t, "src/b.txt", "b")
	f.git(t, "add", ".gitignore", "config/.npmrc")

	errf := tempFile(t, "stderr")
	o := f.runRemote(errf, errf, &config.Project{Exclude: []string{"fixtures/private"}}, "true")
	got := readAll(t, errf)
	if o != (Outcome{}) {
		t.Fatalf("outcome = %+v\n%s", o, got)
	}

	if len(f.tr.excludes) != 1 {
		t.Fatalf("pushes = %d, want 1", len(f.tr.excludes))
	}
	gotEx := slices.Sorted(slices.Values(f.tr.excludes[0]))
	wantEx := []string{"/.env", "/build/", "/config/.npmrc", "/debug.log", "/fixtures/private", "/fixtures/private/p.txt", "/out[1]/"}
	if !slices.Equal(gotEx, wantEx) {
		t.Fatalf("excludes = %q\nwant %q", gotEx, wantEx)
	}
	sent, ok := f.remoteManifest(t)
	if wantSent := []string{".gitignore", "a.txt", "src/b.txt"}; !ok || !slices.Equal(sent, wantSent) {
		t.Fatalf("request manifest = %q (stored %v), want %q", sent, ok, wantSent)
	}
	notSent := "[sendaway] not sent (secret-like): .env, config/.npmrc, fixtures/private/p.txt"
	if own, _ := split(got); !slices.Contains(own, notSent) {
		t.Fatalf("missing %q in:\n%s", notSent, got)
	}
}

func TestLostBeforePushed(t *testing.T) {
	f := newFixture(t)
	f.tr.onPush = func(c *conn) error {
		c.cut()
		return errors.New("rsync tree exited 255: connection unexpectedly closed")
	}
	errf := tempFile(t, "stderr")
	o := f.runRemote(errf, errf, nil, "sh", "-c", "touch ran")
	got := readAll(t, errf)
	if o != (Outcome{Code: ExitFailure}) {
		t.Fatalf("outcome = %+v\n%s", o, got)
	}
	if want := "[sendaway] connection lost during sync — nothing ran"; lastLine(got) != want {
		t.Fatalf("last line = %q, want %q\n%s", lastLine(got), want, got)
	}
	if _, err := os.Stat(filepath.Join(f.root, "ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command ran: %v", err)
	}
	if _, stored := f.remoteManifest(t); stored {
		t.Fatal("remote stored the manifest without pushed")
	}
	meta, err := runlog.Open(f.state, nil).Latest()
	if err != nil {
		t.Fatal(err)
	}
	if meta.Code != ExitFailure || meta.Error != "connection lost during sync — nothing ran" {
		t.Fatalf("meta = %+v", meta)
	}
}
