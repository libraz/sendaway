// Package remote implements remote-exec, the build-host side of a sendaway
// session. It serves one session over the stdin/stdout of an ssh channel:
// hello, request, deletions, the wait for the push, prepare, the command in
// its own process group, and the list of files to bring back.
//
// Liveness covers every stage after the request arrives. A cancel frame,
// stdin EOF, a ping gap longer than PingTimeout or a failed stdout write
// aborts the session: any running process group is stopped (SIGINT for a
// cancel frame, SIGTERM otherwise, SIGKILL after KillGrace), the manifest
// state is left as it was, the repository lock is released and an exit frame
// carrying "cancelled" is attempted.
//
// Outgoing frames go through a single writer goroutine; the request handler,
// output pumps and lock notes only enqueue.
package remote

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/libraz/sendaway/internal/lock"
	"github.com/libraz/sendaway/internal/proto"
)

// Production timings used when the corresponding Server field is zero.
const (
	DefaultPingInterval = 5 * time.Second
	DefaultPingTimeout  = 30 * time.Second
	DefaultKillGrace    = 5 * time.Second
)

const (
	// skewNote and skewLimit bound the clock difference between shim and host.
	skewNote  = 2 * time.Second
	skewLimit = 60 * time.Second

	errCancelled = "cancelled"

	// queueLen is the capacity of the outgoing frame queue.
	queueLen = 64
)

// Server serves remote-exec sessions. The zero value uses production timings
// and the default state directory.
type Server struct {
	// Version is reported in the hello frame.
	Version string
	// StateDir is the sendaway state directory; per-repository state lives in
	// StateDir/repos/<hash>. Empty means ${XDG_STATE_HOME:-~/.local/state}/sendaway.
	StateDir string
	// PingInterval is the cadence at which a client pings. The server never
	// pings; clients paired with this Server read it through Interval.
	PingInterval time.Duration
	// PingTimeout is how long the server waits for any client frame before
	// aborting the session.
	PingTimeout time.Duration
	// KillGrace is how long a signalled process group may take to exit before
	// it is sent SIGKILL.
	KillGrace time.Duration
}

// Interval returns PingInterval, or DefaultPingInterval when it is zero.
func (s *Server) Interval() time.Duration { return orDefault(s.PingInterval, DefaultPingInterval) }

func (s *Server) pingTimeout() time.Duration { return orDefault(s.PingTimeout, DefaultPingTimeout) }

func (s *Server) killGrace() time.Duration { return orDefault(s.KillGrace, DefaultKillGrace) }

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// repoDir returns the state directory for a repository root.
func (s *Server) repoDir(root string) (string, error) {
	base := s.StateDir
	if base == "" {
		state := os.Getenv("XDG_STATE_HOME")
		if state == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			state = filepath.Join(home, ".local", "state")
		}
		base = filepath.Join(state, "sendaway")
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(base, "repos", hex.EncodeToString(sum[:])[:16]), nil
}

// Serve runs one session: it writes frames to out (fd 1 in production) and
// reads the request line and client frames from in. It returns nil once an
// exit frame has been written, otherwise the reason it could not be.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	// A closed stdout must surface as EPIPE, not kill the process. Ignoring
	// SIGPIPE instead would be inherited by the child.
	sigpipe := make(chan os.Signal, 1)
	signal.Notify(sigpipe, syscall.SIGPIPE)
	defer signal.Stop(sigpipe)

	ss := newSession(s)
	defer ss.cancel()
	go ss.writeLoop(out)

	ss.sendJSON(proto.TypeHello, proto.Hello{
		Protocol:   proto.Protocol,
		Version:    s.Version,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		TimeUnixMs: time.Now().UnixMilli(),
	})

	br := bufio.NewReader(in)
	req, err := readRequest(br)
	if err != nil {
		if werr := ss.finish(proto.Exit{Error: err.Error()}); werr != nil {
			return errors.Join(err, werr)
		}
		return err
	}
	go ss.readLoop(br)
	go ss.watchdog()

	exit, l := ss.handle(req)
	if ss.aborted() {
		_ = l.Release()
		return ss.finish(proto.Exit{Error: errCancelled})
	}
	err = ss.finish(exit)
	_ = l.Release()
	return err
}

// readRequest reads the JSON request line that precedes the client frames.
func readRequest(br *bufio.Reader) (*proto.Request, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > proto.MaxFrameLen {
			return nil, errors.New("request line too long")
		}
		if err == nil {
			break
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, fmt.Errorf("read request: %w", err)
		}
	}
	var req proto.Request
	if err := json.Unmarshal(line, &req); err != nil {
		return nil, fmt.Errorf("decode request: %w", err)
	}
	return &req, nil
}

type frame struct {
	typ  proto.Type
	body []byte
}

// session is the state of one Serve call.
type session struct {
	s *Server

	out        chan frame
	writerDone chan struct{}
	writeErr   error // set before writerDone closes

	// ctx is cancelled when the session aborts; abortSig is the signal for a
	// running process group and is set before the cancel.
	ctx      context.Context
	cancel   context.CancelFunc
	abortSig atomic.Int64

	alive      chan struct{}
	pushed     chan struct{}
	pushedOnce sync.Once
}

func newSession(s *Server) *session {
	ctx, cancel := context.WithCancel(context.Background())
	return &session{
		s:          s,
		out:        make(chan frame, queueLen),
		writerDone: make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
		alive:      make(chan struct{}, 1),
		pushed:     make(chan struct{}),
	}
}

// abort ends the session; the first caller's signal wins.
func (ss *session) abort(sig syscall.Signal) {
	if ss.abortSig.CompareAndSwap(0, int64(sig)) {
		ss.cancel()
	}
}

func (ss *session) aborted() bool { return ss.abortSig.Load() != 0 }

func (ss *session) signal() syscall.Signal { return syscall.Signal(ss.abortSig.Load()) }

// writeLoop is the only writer of out. After a write error it aborts the
// session and discards the rest of the queue.
func (ss *session) writeLoop(w io.Writer) {
	bw := bufio.NewWriterSize(w, 64<<10)
	var err error
	for f := range ss.out {
		if err != nil {
			continue
		}
		err = proto.WriteFrame(bw, f.typ, f.body)
		if err == nil && len(ss.out) == 0 {
			err = bw.Flush()
		}
		if err != nil {
			ss.abort(syscall.SIGTERM)
		}
	}
	if err == nil {
		err = bw.Flush()
	}
	ss.writeErr = err
	close(ss.writerDone)
}

// send enqueues a frame. Frames sent after an abort are dropped.
func (ss *session) send(t proto.Type, body []byte) {
	select {
	case ss.out <- frame{t, body}:
	case <-ss.ctx.Done():
	}
}

func (ss *session) sendJSON(t proto.Type, v any) {
	body, _ := json.Marshal(v)
	ss.send(t, body)
}

// note sends one line of text as a note frame.
func (ss *session) note(line string) {
	ss.send(proto.TypeNote, []byte(strings.ToValidUTF8(line, "�")))
}

// finish queues the exit frame, closes the queue and waits for the writer.
// After an abort it gives up KillGrace later, so a stalled stdout cannot hold
// the process.
func (ss *session) finish(e proto.Exit) error {
	body, _ := json.Marshal(e)
	giveUp := make(chan struct{})
	stop := context.AfterFunc(ss.ctx, func() {
		time.AfterFunc(ss.s.killGrace(), func() { close(giveUp) })
	})
	defer stop()
	errStalled := errors.New("exit frame not sent: stdout stalled")
	select {
	case ss.out <- frame{proto.TypeExit, body}:
		close(ss.out)
	case <-giveUp:
		return errStalled
	}
	select {
	case <-ss.writerDone:
		return ss.writeErr
	case <-giveUp:
		return errStalled
	}
}

// readLoop consumes client frames until stdin ends or the client cancels.
func (ss *session) readLoop(r io.Reader) {
	for {
		t, _, err := proto.ReadFrame(r)
		if err != nil {
			ss.abort(syscall.SIGTERM)
			return
		}
		switch t {
		case proto.TypePing:
		case proto.TypeCancel:
			ss.abort(syscall.SIGINT)
			return
		case proto.TypePushed:
			ss.pushedOnce.Do(func() { close(ss.pushed) })
		default:
			ss.abort(syscall.SIGTERM)
			return
		}
		select {
		case ss.alive <- struct{}{}:
		default:
		}
	}
}

// watchdog aborts the session when no client frame arrives for PingTimeout.
func (ss *session) watchdog() {
	timeout := ss.s.pingTimeout()
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		select {
		case <-ss.alive:
			t.Reset(timeout)
		case <-t.C:
			ss.abort(syscall.SIGTERM)
			return
		case <-ss.ctx.Done():
			return
		}
	}
}

// lockHolder is the remote lock record. It never carries argv or environment.
type lockHolder struct {
	Run           string `json:"run"`
	StartedUnixMs int64  `json:"started_unix_ms"`
	PID           int    `json:"pid"`
}

func (ss *session) lockWait(cur []byte) {
	var h lockHolder
	if json.Unmarshal(cur, &h) != nil || h.Run == "" {
		ss.note("waiting for remote lock (held by unknown)")
		return
	}
	held := time.Since(time.UnixMilli(h.StartedUnixMs)).Round(time.Second)
	ss.note(fmt.Sprintf("waiting for remote lock (held by run %s, %s)", h.Run, held))
}

func errExit(err error) proto.Exit { return proto.Exit{Error: err.Error()} }

// handle runs the request up to, but not including, the exit frame. It
// returns the held lock, if any, for the caller to release.
func (ss *session) handle(req *proto.Request) (proto.Exit, *lock.Lock) {
	if req.Protocol != proto.Protocol {
		return errExit(fmt.Errorf("protocol mismatch: request %d, remote-exec %d", req.Protocol, proto.Protocol)), nil
	}
	if err := checkRequest(req); err != nil {
		return errExit(err), nil
	}
	dir, err := ss.s.repoDir(req.Root)
	if err == nil {
		err = os.MkdirAll(dir, 0o700)
	}
	if err != nil {
		return errExit(fmt.Errorf("state dir: %w", err)), nil
	}
	holder, _ := json.Marshal(lockHolder{Run: req.Run, StartedUnixMs: time.Now().UnixMilli(), PID: os.Getpid()})
	l, err := lock.Acquire(ss.ctx, filepath.Join(dir, "lock"), holder, ss.lockWait)
	if err != nil {
		return errExit(fmt.Errorf("remote lock: %w", err)), nil
	}

	if req.Op == proto.OpForget {
		if err := os.RemoveAll(req.Root); err != nil {
			return errExit(err), l
		}
		if err := os.RemoveAll(dir); err != nil {
			return errExit(err), l
		}
		return proto.Exit{}, l
	}

	skew := time.Since(time.UnixMilli(req.SentUnixMs)).Round(100 * time.Millisecond)
	if skew.Abs() > skewLimit {
		return errExit(fmt.Errorf("remote clock differs from local by %s (limit %s); sync the clocks", skew, skewLimit)), l
	}
	if skew.Abs() > skewNote {
		ss.note(fmt.Sprintf("remote clock differs from local by %s; make may misjudge mtimes", skew))
	}

	if req.Op == proto.OpFetch {
		files, rejected, err := collect(req.Root, req.Pull, modePull)
		if err != nil {
			return errExit(err), l
		}
		return proto.Exit{Collect: files, Rejected: rejected}, l
	}
	exit, err := ss.runRequest(req, dir)
	if err != nil {
		return errExit(err), l
	}
	return exit, l
}

// checkRequest validates the request beyond proto.Request.Validate.
func checkRequest(req *proto.Request) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(req.Root) || filepath.Clean(req.Root) != req.Root || req.Root == "/" {
		return fmt.Errorf("root %q must be a clean absolute path below /", req.Root)
	}
	if req.Op != proto.OpExec {
		return nil
	}
	if !filepath.IsAbs(req.Cwd) || filepath.Clean(req.Cwd) != req.Cwd ||
		(req.Cwd != req.Root && !strings.HasPrefix(req.Cwd, req.Root+"/")) {
		return fmt.Errorf("cwd %q is not inside root", req.Cwd)
	}
	for k, v := range req.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return fmt.Errorf("env: invalid variable %q", k)
		}
	}
	for i, r := range req.Prepare {
		if len(r.When) == 0 || len(r.Run) == 0 {
			return fmt.Errorf("prepare[%d]: when and run must not be empty", i)
		}
		for _, w := range r.When {
			if !validRel(w) {
				return fmt.Errorf("prepare[%d]: invalid when path %q", i, w)
			}
		}
	}
	return nil
}

// validRel accepts a non-empty root-relative path without NUL or ".." components.
func validRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return false
	}
	return !slices.Contains(strings.Split(p, "/"), "..")
}
