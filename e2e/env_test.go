//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// cmdTimeout bounds a single sendaway invocation.
const cmdTimeout = 3 * time.Minute

// trailerRE matches the closing line of a remote run.
var trailerRE = regexp.MustCompile(`^\[sendaway\] ` + hostAlias +
	` (exit \d+|killed by signal \d+) · \d+\.\ds · run (r-\d{8}-\d{6}-[0-9a-f]{4}) · log (\S+)$`)

// env is one test's project, local sendaway directories and ssh setup.
type env struct {
	t         *testing.T
	root      string // symlink-resolved repository root
	state     string // XDG_STATE_HOME
	stateDir  string // sendaway's state directory below it
	config    string // XDG_CONFIG_HOME
	sshConfig string
	shimDir   string
	environ   []string
}

// newEnv creates a git repository with a .sendaway.yml routing make to the
// container, the user configuration, the make shim, and the same root path in
// the container owned by the ssh user.
func newEnv(t *testing.T) *env {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A t.TempDir path is too long for the ssh control socket below it.
	state, err := os.MkdirTemp("/tmp", "sa-") //nolint:usetesting // must stay short for the ssh ControlPath limit
	if err != nil {
		t.Fatal(err)
	}
	e := &env{
		t:        t,
		root:     root,
		state:    state,
		stateDir: filepath.Join(state, "sendaway"),
		config:   t.TempDir(),
	}
	data := t.TempDir()
	e.shimDir = filepath.Join(data, "sendaway", "bin")
	e.environ = baseEnviron(map[string]string{
		"XDG_STATE_HOME":      state,
		"XDG_CONFIG_HOME":     e.config,
		"XDG_DATA_HOME":       data,
		"PATH":                e.shimDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
	})
	t.Cleanup(e.cleanup)

	e.sshConfig = filepath.Join(e.config, "ssh_config")
	e.setHost(suite.port, suite.key, suite.knownHosts)
	e.write("sendaway/config.yml", "host: "+hostAlias+"\nssh_config: "+e.sshConfig+"\n", e.config)

	e.git("init", "-q", "-b", "main")
	e.git("config", "user.name", "e2e")
	e.git("config", "user.email", "e2e@example.invalid")
	e.git("config", "commit.gpgsign", "false")
	e.writeFile(".sendaway.yml", "remote:\n  - [make]\n")

	if r := e.sendaway("install-shims", "make"); r.code != 0 {
		t.Fatalf("install-shims: %s", r)
	}

	mk := `mkdir -p "$1" && chown sa:sa "$1" "$(dirname "$1")"`
	if _, err := dockerExec("root", nil, nil, "sh", "-c", mk, "sh", root); err != nil {
		t.Fatal(err)
	}
	return e
}

// baseEnviron is the test process environment without variables that would
// make runs depend on the machine, with set applied on top.
func baseEnviron(set map[string]string) []string {
	environ := os.Environ()
	out := make([]string, 0, len(environ)+len(set))
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "XDG_"), strings.HasPrefix(k, "GIT_"), strings.HasPrefix(k, "SENDAWAY_"),
			k == "LANG", strings.HasPrefix(k, "LC_"), k == "PATH":
			continue
		}
		out = append(out, kv)
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}

// setHost rewrites the ssh_config entry for hostAlias.
func (e *env) setHost(port int, key, knownHosts string) {
	e.t.Helper()
	cfg := fmt.Sprintf(`Host %s
  HostName 127.0.0.1
  Port %d
  User %s
  IdentityFile "%s"
  IdentitiesOnly yes
  UserKnownHostsFile "%s"
  GlobalKnownHostsFile /dev/null
  StrictHostKeyChecking yes
  SendEnv LANG
`, hostAlias, port, sshUser, key, knownHosts)
	if err := os.WriteFile(e.sshConfig, []byte(cfg), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) cleanup() {
	if socks, _ := filepath.Glob(filepath.Join(e.stateDir, "cm", "*")); len(socks) > 0 {
		for _, s := range socks {
			_, _ = run(nil, "ssh", "-F", e.sshConfig, "-o", "ControlPath="+s, "-O", "exit", hostAlias)
		}
	}
	_ = os.RemoveAll(e.state)
	_, _ = dockerExec("root", nil, nil, "rm", "-rf", filepath.Dir(e.root), e.remoteStateDir())
}

// remoteStateDir is the container's per-repository state directory.
func (e *env) remoteStateDir() string {
	sum := sha256.Sum256([]byte(e.root))
	return remoteHome + "/.local/state/sendaway/repos/" + hex.EncodeToString(sum[:])[:16]
}

// write creates base/rel with content, making parent directories.
func (e *env) write(rel, content, base string) {
	e.t.Helper()
	p := filepath.Join(base, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// writeFile creates a file in the repository.
func (e *env) writeFile(rel, content string) {
	e.t.Helper()
	e.write(rel, content, e.root)
}

// path returns the absolute local path of a repository-relative path.
func (e *env) path(rel string) string { return filepath.Join(e.root, filepath.FromSlash(rel)) }

// git runs git in the repository and fails the test on error.
func (e *env) git(args ...string) string {
	e.t.Helper()
	cmd := exec.CommandContext(e.t.Context(), "git", args...)
	cmd.Dir = e.root
	cmd.Env = e.environ
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// result is how a sendaway process ended and what it printed.
type result struct {
	stdout, stderr string
	code           int            // -1 when killed by a signal
	signal         syscall.Signal // 0 unless killed by a signal
}

func (r result) String() string {
	return fmt.Sprintf("code=%d signal=%d\n--- stdout\n%s--- stderr\n%s", r.code, r.signal, r.stdout, r.stderr)
}

// saLines returns the lines sendaway printed on stderr.
func (r result) saLines() []string { return sendawayLines(r.stderr) }

// trailer returns the last sendaway line.
func (r result) trailer() string {
	l := r.saLines()
	if len(l) == 0 {
		return ""
	}
	return l[len(l)-1]
}

// runID returns the run id from the trailer, failing the test without one.
func (r result) runID(t *testing.T) string {
	t.Helper()
	m := trailerRE.FindStringSubmatch(r.trailer())
	if m == nil {
		t.Fatalf("no trailer line:\n%s", r)
	}
	return m[2]
}

func sendawayLines(s string) []string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		if strings.HasPrefix(l, "[sendaway] ") {
			out = append(out, l)
		}
	}
	return out
}

// otherLines returns the lines of s that sendaway did not print.
func otherLines(s string) []string {
	var out []string
	for l := range strings.SplitSeq(strings.TrimSuffix(s, "\n"), "\n") {
		if l != "" && !strings.HasPrefix(l, "[sendaway] ") {
			out = append(out, l)
		}
	}
	return out
}

// command prepares the binary at path with args in the repository.
func (e *env) command(ctx context.Context, path string, args []string, extra ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = e.root
	cmd.Env = append(append([]string(nil), e.environ...), extra...)
	cmd.WaitDelay = 10 * time.Second
	return cmd
}

// sendaway runs `sendaway args...` to completion.
func (e *env) sendaway(args ...string) result {
	e.t.Helper()
	return e.runTo(suite.bin, args, nil)
}

// sendawayEnv runs `sendaway args...` with extra variables.
func (e *env) sendawayEnv(extra []string, args ...string) result {
	e.t.Helper()
	return e.runTo(suite.bin, args, extra)
}

// shim runs a command through its installed shim, as a caller on PATH would.
func (e *env) shim(name string, args ...string) result {
	e.t.Helper()
	return e.runTo(filepath.Join(e.shimDir, name), args, nil)
}

// runTo runs the binary at path to completion, killing it after cmdTimeout.
func (e *env) runTo(path string, args, extra []string) result {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.t.Context(), cmdTimeout)
	defer cancel()
	cmd := e.command(ctx, path, args, extra...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	return e.outcome(cmd, err, out.String(), errOut.String())
}

func (e *env) outcome(cmd *exec.Cmd, err error, stdout, stderr string) result {
	e.t.Helper()
	if _, ok := errors.AsType[*exec.ExitError](err); err != nil && !ok {
		e.t.Fatalf("%s: %v\n%s", strings.Join(cmd.Args, " "), err, stderr)
	}
	r := result{stdout: stdout, stderr: stderr, code: cmd.ProcessState.ExitCode()}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		r.signal = ws.Signal()
	}
	return r
}

// proc is a sendaway process running in the background.
type proc struct {
	e       *env
	cmd     *exec.Cmd
	out     bytes.Buffer
	err     bytes.Buffer
	exited  chan struct{} // closed once Wait returns
	waitErr error
}

// start launches `sendaway args...` without waiting. The process is killed
// when the test ends if it is still running.
func (e *env) start(args ...string) *proc {
	e.t.Helper()
	p := &proc{e: e, exited: make(chan struct{})}
	p.cmd = e.command(context.WithoutCancel(e.t.Context()), suite.bin, args)
	p.cmd.Stdout = &p.out
	p.cmd.Stderr = &p.err
	if err := p.cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	go func() {
		p.waitErr = p.cmd.Wait()
		close(p.exited)
	}()
	e.t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		<-p.exited
	})
	return p
}

// signal sends sig to the sendaway process.
func (p *proc) signal(sig syscall.Signal) {
	p.e.t.Helper()
	if err := p.cmd.Process.Signal(sig); err != nil {
		p.e.t.Fatal(err)
	}
}

// wait returns the result once the process ends, failing after limit.
func (p *proc) wait(limit time.Duration) result {
	p.e.t.Helper()
	select {
	case <-p.exited:
	case <-time.After(limit):
		_ = p.cmd.Process.Kill()
		<-p.exited
		p.e.t.Fatalf("sendaway still running after %s:\n%s", limit, p.e.outcome(p.cmd, p.waitErr, p.out.String(), p.err.String()))
	}
	return p.e.outcome(p.cmd, p.waitErr, p.out.String(), p.err.String())
}

// remoteSh runs a shell script in the container as the ssh user with ROOT
// set to the repository root and the remaining arguments as "$@".
func (e *env) remoteSh(script string, args ...string) (string, error) {
	argv := append([]string{"sh", "-c", script, "sh"}, args...)
	return dockerExec(sshUser, []string{"ROOT=" + e.root}, nil, argv...)
}

// remoteKinds reports, for each repository-relative path, whether the
// container's copy is a file, dir, link or none.
func (e *env) remoteKinds(paths ...string) map[string]string {
	e.t.Helper()
	const script = `cd "$ROOT" || exit 1
for p in "$@"; do
  if [ -L "$p" ]; then k=link; elif [ -d "$p" ]; then k=dir; elif [ -f "$p" ]; then k=file; else k=none; fi
  printf '%s\t%s\n' "$p" "$k"
done`
	out, err := e.remoteSh(script, paths...)
	if err != nil {
		e.t.Fatal(err)
	}
	kinds := make(map[string]string)
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if p, k, ok := strings.Cut(sc.Text(), "\t"); ok {
			kinds[p] = k
		}
	}
	return kinds
}

// expectKinds fails the test for every path whose remote kind differs.
func (e *env) expectKinds(want map[string]string) {
	e.t.Helper()
	paths := make([]string, 0, len(want))
	for p := range want {
		paths = append(paths, p)
	}
	got := e.remoteKinds(paths...)
	for _, p := range paths {
		if got[p] != want[p] {
			e.t.Errorf("remote %s: got %s, want %s", p, got[p], want[p])
		}
	}
}

// mustSucceed fails the test unless r exited 0.
func mustSucceed(t *testing.T, what string, r result) {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("%s: %s", what, r)
	}
}
