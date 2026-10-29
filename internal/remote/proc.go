package remote

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/libraz/sendaway/internal/proto"
)

// errAborted reports that the session aborted while a process group ran.
var errAborted = errors.New("aborted")

// groupPoll is how often a signalled process group is checked for exit.
const groupPoll = 20 * time.Millisecond

// passEnv are the variables the child inherits from remote-exec itself.
var passEnv = []string{"HOME", "USER", "LOGNAME", "SHELL", "PATH"}

// runRequest performs the exec op from deletions to the collection list.
func (ss *session) runRequest(req *proto.Request, dir string) (proto.Exit, error) {
	if err := ss.applyDeletions(req.Root, dir, req.Manifest); err != nil {
		return proto.Exit{}, err
	}
	ss.send(proto.TypeReady, nil)
	select {
	case <-ss.pushed:
	case <-ss.ctx.Done():
		return proto.Exit{}, errAborted
	}
	if err := writeManifest(dir, req.Manifest); err != nil {
		return proto.Exit{}, err
	}
	if err := writeGitConfig(req.Root); err != nil {
		return proto.Exit{}, err
	}
	if err := ss.prepare(req, dir); err != nil {
		return proto.Exit{}, err
	}

	before := stampFiles(req.Root, req.Manifest)
	if err := os.MkdirAll(req.Cwd, 0o755); err != nil {
		return proto.Exit{}, err
	}
	state, err := ss.runChild(req)
	if err != nil {
		return proto.Exit{}, err
	}
	exit := exitStatus(state)

	paths, mode := req.Artifacts, modeArtifacts
	if exit.Code == 0 && exit.Signal == 0 {
		paths, mode = req.Pull, modePull
	}
	exit.Collect, exit.Rejected, err = collect(req.Root, paths, mode)
	if err != nil {
		return proto.Exit{}, err
	}
	exit.Modified = modified(req.Root, req.Manifest, before, exit.Collect)
	return exit, nil
}

// exitStatus converts a wait status into the code and signal of an exit frame.
func exitStatus(state *os.ProcessState) proto.Exit {
	ws, ok := state.Sys().(syscall.WaitStatus)
	if ok && ws.Signaled() {
		return proto.Exit{Code: 128 + int(ws.Signal()), Signal: int(ws.Signal())}
	}
	return proto.Exit{Code: state.ExitCode()}
}

// launchArgv wraps argv in the login shell so the host profile sets PATH,
// then re-applies the forwarded variables over whatever the profile set.
func launchArgv(env map[string]string, argv []string) []string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	a := []string{shell, "-lc", `exec env -- "$@"`, "sendaway"}
	for _, k := range sortedKeys(env) {
		a = append(a, k+"="+env[k])
	}
	return append(a, argv...)
}

// childEnv builds the child environment from scratch: a few variables of
// remote-exec itself, the forwarded ones, and the passthrough marker.
func childEnv(fwd map[string]string) []string {
	env := make([]string, 0, len(passEnv)+len(fwd)+1)
	for _, k := range passEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for _, k := range sortedKeys(fwd) {
		env = append(env, k+"="+fwd[k])
	}
	return append(env, "SENDAWAY_PASSTHROUGH=1")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// runChild runs the requested command, streaming its output as out/err frames.
func (ss *session) runChild(req *proto.Request) (*os.ProcessState, error) {
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	readers, writers := []*os.File{outR}, []*os.File{outW}
	errW := outW
	if !req.MergeOutput {
		errR, w, err := os.Pipe()
		if err != nil {
			closeAll(readers, writers)
			return nil, err
		}
		readers, writers, errW = append(readers, errR), append(writers, w), w
	}

	var wg sync.WaitGroup
	wg.Go(func() { ss.pumpFrames(outR, proto.TypeOut) })
	if len(readers) > 1 {
		wg.Go(func() { ss.pumpFrames(readers[1], proto.TypeErr) })
	}
	state, err := ss.runGroup(req.Cwd, launchArgv(req.Env, req.Argv), childEnv(req.Env), outW, errW, writers)
	ss.drain(&wg, readers)
	return state, err
}

// runGroup starts argv in a new process group with stdin on /dev/null and
// waits for it, or for the session to abort, in which case the whole group is
// stopped. The parent's copies of the output pipe ends in owned are closed
// once the process has started.
func (ss *session) runGroup(dir string, argv, env []string, stdout, stderr *os.File, owned []*os.File) (*os.ProcessState, error) {
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // running the requested command is the purpose of remote-exec
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err := cmd.Start()
	closeAll(nil, owned)
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", argv[0], err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
		return cmd.ProcessState, nil
	case <-ss.ctx.Done():
		ss.stopGroup(cmd.Process.Pid, done)
		return nil, errAborted
	}
}

// stopGroup signals the process group, escalates to SIGKILL after KillGrace,
// and returns once the leader is reaped and the group is empty.
func (ss *session) stopGroup(pgid int, reaped <-chan struct{}) {
	grace := ss.s.killGrace()
	_ = syscall.Kill(-pgid, ss.signal())
	if waitGroupGone(pgid, reaped, grace) {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	waitGroupGone(pgid, reaped, grace)
}

// waitGroupGone polls until the leader is reaped and no process remains in
// the group, or until limit passes. It reports whether the group is gone.
func waitGroupGone(pgid int, reaped <-chan struct{}, limit time.Duration) bool {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(groupPoll)
	defer tick.Stop()
	for {
		select {
		case <-reaped:
			if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
				return true
			}
		default:
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			return false
		}
	}
}

// drain waits for the output pumps. A background process can keep a pipe
// open after the command exits, so the read ends are closed after KillGrace.
func (ss *session) drain(wg *sync.WaitGroup, readers []*os.File) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	t := time.NewTimer(ss.s.killGrace())
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
	closeAll(readers, nil)
	<-done
}

func closeAll(a, b []*os.File) {
	for _, f := range slices.Concat(a, b) {
		_ = f.Close()
	}
}

// pumpFrames forwards r as frames of type t until it ends.
func (ss *session) pumpFrames(r io.Reader, t proto.Type) {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			ss.send(t, bytes.Clone(buf[:n]))
		}
		if err != nil {
			return
		}
	}
}

// pumpNotes forwards r line by line as note frames.
func (ss *session) pumpNotes(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadSlice('\n')
		if len(line) > 0 {
			ss.note(string(bytes.TrimRight(line, "\r\n")))
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return
		}
	}
}
