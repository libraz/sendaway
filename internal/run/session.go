package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/libraz/sendaway/internal/proto"
	"github.com/libraz/sendaway/internal/remote"
	"github.com/libraz/sendaway/internal/sshx"
)

// pingInterval is how often the shim pings once the request is sent.
var pingInterval = remote.DefaultPingInterval

const (
	// exitWait bounds the wait for the exit frame after stdin is closed.
	exitWait = 10 * time.Second
	// stderrGrace bounds how long ssh's stderr may outlive its stdout.
	stderrGrace = 2 * time.Second
	// sshLost is ssh's exit code for a failed or dropped connection.
	sshLost = 255
	// closeStdin asks the writer to close the session's stdin.
	closeStdin proto.Type = 0
)

// interrupts records the first SIGINT, SIGTERM or SIGHUP and cancels ctx.
type interrupts struct {
	ctx    context.Context
	cancel context.CancelFunc
	ch     chan os.Signal
	quit   chan struct{}
	sig    atomic.Int64
}

func watchInterrupts() *interrupts {
	ctx, cancel := context.WithCancel(context.Background())
	in := &interrupts{ctx: ctx, cancel: cancel, ch: make(chan os.Signal, 1), quit: make(chan struct{})}
	signal.Notify(in.ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		select {
		case s := <-in.ch:
			if sig, ok := s.(syscall.Signal); ok {
				in.sig.Store(int64(sig))
			}
			cancel()
		case <-in.quit:
		}
	}()
	return in
}

// done is closed by the first interrupt.
func (in *interrupts) done() <-chan struct{} { return in.ctx.Done() }

// signal returns the interrupting signal, or 0.
func (in *interrupts) signal() syscall.Signal { return syscall.Signal(in.sig.Load()) }

func (in *interrupts) stop() {
	signal.Stop(in.ch)
	close(in.quit)
	in.cancel()
}

// stderrPump holds ssh's stderr until hello arrives and streams it afterwards.
type stderrPump struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	live bool
	con  *console
	done chan struct{}
}

func (p *stderrPump) run(r io.Reader) {
	defer close(p.done)
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			p.mu.Lock()
			if p.live {
				p.con.err(buf[:n])
			} else {
				p.buf.Write(buf[:n])
			}
			p.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// goLive shows what was held back and streams from now on.
func (p *stderrPump) goLive() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.con.err(p.buf.Bytes())
	p.buf.Reset()
	p.live = true
}

// held returns the stderr collected while not live.
func (p *stderrPump) held() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buf.String()
}

// writer is the only goroutine that writes frames to the session's stdin.
// It pings every pingInterval until stdin is closed.
type writer struct {
	ch   chan proto.Type
	done chan struct{}
}

func startWriter(w io.WriteCloser) *writer {
	wr := &writer{ch: make(chan proto.Type), done: make(chan struct{})}
	go wr.loop(w)
	return wr
}

func (wr *writer) loop(w io.WriteCloser) {
	defer close(wr.done)
	tick := time.NewTicker(pingInterval)
	defer tick.Stop()
	failed := false
	put := func(t proto.Type) {
		if !failed && proto.WriteFrame(w, t, nil) != nil {
			failed = true
		}
	}
	for {
		select {
		case t := <-wr.ch:
			if t == closeStdin {
				_ = w.Close()
				return
			}
			put(t)
		case <-tick.C:
			put(proto.TypePing)
		}
	}
}

// send queues a frame; it is dropped once stdin is closed.
func (wr *writer) send(t proto.Type) {
	select {
	case wr.ch <- t:
	case <-wr.done:
	}
}

// session is one remote-exec conversation.
type session struct {
	con  *console
	sess *sshx.Session
	kill context.CancelFunc
	errs *stderrPump
	w    *writer

	gotHello chan struct{}
	hello    proto.Hello
	ready    chan struct{}
	gotExit  chan struct{}
	exit     proto.Exit
	end      chan struct{}
	endErr   error // a protocol violation; set before end closes
}

// open starts remote-exec and the goroutines reading its stdout and stderr.
func open(tr Transport, con *console) (*session, error) {
	ctx, cancel := context.WithCancel(context.Background())
	sess, err := tr.Open(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	s := &session{
		con:      con,
		sess:     sess,
		kill:     cancel,
		errs:     &stderrPump{con: con, done: make(chan struct{})},
		gotHello: make(chan struct{}),
		ready:    make(chan struct{}),
		gotExit:  make(chan struct{}),
		end:      make(chan struct{}),
	}
	go s.errs.run(sess.Stderr)
	go s.read()
	return s, nil
}

// read consumes remote frames until stdout ends or the protocol is violated.
func (s *session) read() {
	defer close(s.end)
	var readySeen, exitSeen bool
	for first := true; ; first = false {
		t, body, err := proto.ReadFrame(s.sess.Stdout)
		if err != nil {
			if errors.Is(err, proto.ErrFrameTooLarge) {
				s.endErr = err
			}
			return
		}
		if first {
			if t != proto.TypeHello {
				s.endErr = fmt.Errorf("expected hello, got frame type 0x%02x", byte(t))
				return
			}
			if err := json.Unmarshal(body, &s.hello); err != nil {
				s.endErr = fmt.Errorf("decode hello: %w", err)
				return
			}
			s.errs.goLive()
			close(s.gotHello)
			continue
		}
		switch t {
		case proto.TypeOut:
			s.con.out(body)
		case proto.TypeErr:
			s.con.err(body)
		case proto.TypeNote:
			s.con.line(strings.ToValidUTF8(string(body), "�"))
		case proto.TypeReady:
			if !readySeen {
				readySeen = true
				close(s.ready)
			}
		case proto.TypeExit:
			if exitSeen {
				continue
			}
			if err := json.Unmarshal(body, &s.exit); err != nil {
				s.endErr = fmt.Errorf("decode exit: %w", err)
				return
			}
			exitSeen = true
			close(s.gotExit)
		default:
			s.endErr = fmt.Errorf("unexpected frame type 0x%02x", byte(t))
			return
		}
	}
}

// request writes the request line and starts the writer, which pings from now on.
func (s *session) request(req *proto.Request) {
	req.SentUnixMs = time.Now().UnixMilli()
	line, _ := json.Marshal(req)
	// A failed write surfaces as the end of the session's stdout.
	_, _ = s.sess.Stdin.Write(append(line, '\n'))
	s.w = startWriter(s.sess.Stdin)
}

// ending is how a session finished.
type ending struct {
	exit *proto.Exit // nil when stdout ended without an exit frame
	code int         // ssh's exit code
	err  error       // protocol violation
}

// failure returns the reason line for an ending that is not a usable result,
// or "". pushed tells whether the command may have started.
func (e ending) failure(pushed bool) string {
	switch {
	case e.err != nil:
		return "protocol error from remote-exec: " + e.err.Error()
	case e.exit == nil && e.code == sshLost && pushed:
		return "connection lost during run — result unknown"
	case e.exit == nil && e.code == sshLost:
		return "connection lost during sync — nothing ran"
	case e.exit == nil:
		return fmt.Sprintf("remote-exec exited %d without a result", e.code)
	case e.exit.Error != "":
		return e.exit.Error
	}
	return ""
}

// finish optionally sends cancel, closes stdin, waits up to exitWait for
// stdout to end (killing ssh after that), and reaps ssh.
func (s *session) finish(cancel bool) ending {
	go func() {
		if s.w == nil {
			_ = s.sess.Stdin.Close()
			return
		}
		if cancel {
			s.w.send(proto.TypeCancel)
		}
		s.w.send(closeStdin)
	}()
	deadline := time.NewTimer(exitWait)
	defer deadline.Stop()
	select {
	case <-s.end:
	case <-deadline.C:
		s.kill()
		<-s.end
	}
	if s.endErr != nil {
		s.kill()
	}
	grace := time.NewTimer(stderrGrace)
	defer grace.Stop()
	select {
	case <-s.errs.done:
	case <-grace.C:
	}
	code, _ := s.sess.Wait()
	s.kill()
	e := ending{code: code, err: s.endErr}
	select {
	case <-s.gotExit:
		e.exit = &s.exit
	default:
	}
	return e
}

// await waits for the exit frame or the end of the session. On an interrupt
// it cancels the remote side and returns the signal.
func (s *session) await(in *interrupts) (ending, syscall.Signal) {
	select {
	case <-s.gotExit:
	case <-s.end:
	case <-in.done():
		s.finish(true)
		return ending{}, in.signal()
	}
	return s.finish(false), 0
}

// preHello describes a session that never got past hello.
type preHello struct {
	sig    syscall.Signal // interrupted
	reason string         // network unreachable: a local fallback is allowed
	stderr string         // ssh's stderr, shown verbatim
	msg    string         // reason line, when stderr alone does not explain
}

// handshake waits for hello and checks the protocol version. It returns nil
// once a request may be sent.
func (s *session) handshake(in *interrupts, version string) *preHello {
	select {
	case <-s.gotHello:
	case <-s.end:
		e := s.finish(false)
		if e.err != nil {
			return &preHello{msg: "protocol error from remote-exec: " + e.err.Error()}
		}
		stderr := s.errs.held()
		if reason, ok := sshx.ClassifyConnectError(stderr); ok {
			return &preHello{reason: reason, stderr: stderr}
		}
		ph := &preHello{stderr: stderr}
		if strings.TrimSpace(stderr) == "" {
			ph.msg = fmt.Sprintf("remote-exec exited %d without a result", e.code)
		}
		return ph
	case <-in.done():
		s.finish(false)
		return &preHello{sig: in.signal()}
	}
	if s.hello.Protocol != proto.Protocol {
		s.finish(false)
		return &preHello{msg: fmt.Sprintf("protocol mismatch: local sendaway %s speaks protocol %d, remote sendaway %s speaks protocol %d; install the same version on both sides",
			version, proto.Protocol, s.hello.Version, s.hello.Protocol)}
	}
	return nil
}

// show prints a pre-hello failure: ssh's stderr as is, then the reason line.
func (ph *preHello) show(con *console) {
	if ph.stderr != "" {
		text := ph.stderr
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		con.err([]byte(text))
	}
	if ph.msg != "" {
		con.line(ph.msg)
	}
}

// summary is the one-line reason recorded for a pre-hello failure.
func (ph *preHello) summary() string {
	if ph.msg != "" {
		return ph.msg
	}
	lines := strings.Split(strings.TrimSpace(ph.stderr), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
