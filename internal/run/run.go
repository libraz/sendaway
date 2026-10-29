// Package run is the shim side of a remote operation. Exec checks the work
// tree, builds the file set, opens a remote-exec session through a Transport,
// pushes once the host is ready, relays output and the exit status, collects
// pull or artifacts files and records the run.
//
// Command output, notes and sendaway's own lines all pass through one
// console, so when stdout and stderr are the same file they land in arrival
// order, and output.log receives exactly what was shown. Frames to the host
// (ping, pushed, cancel) are written by a single goroutine.
//
// Nothing here exits the process: callers act on the returned Outcome, using
// DieBySignal when it carries a signal.
package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/libraz/sendaway/internal/config"
	"github.com/libraz/sendaway/internal/envfilter"
	"github.com/libraz/sendaway/internal/lock"
	"github.com/libraz/sendaway/internal/proto"
	"github.com/libraz/sendaway/internal/runlog"
	"github.com/libraz/sendaway/internal/sshx"
)

// ExitFailure is the exit code of a sendaway-side failure. The line printed
// just before it gives the reason.
const ExitFailure = 125

// Transport reaches the build host.
type Transport interface {
	// Open starts `sendaway remote-exec` on the host.
	Open(ctx context.Context) (*sshx.Session, error)
	// Push syncs root to the same path on the host, skipping the anchored,
	// unescaped exclude entries.
	Push(ctx context.Context, root string, excludes []string) (sshx.PushResult, error)
	// Pull copies root-relative files from the host's root into dest.
	Pull(ctx context.Context, root string, files []string, dest string) error
}

var _ Transport = (*sshx.Client)(nil)

// Outcome is how the caller must end the process.
type Outcome struct {
	// Code is the exit code when Signal is 0 and Local is false.
	Code int
	// Signal, when non-zero, means the process must die by it (DieBySignal).
	Signal syscall.Signal
	// Local means the host was unreachable before hello: the fallback line is
	// printed and the local lock released, so the command runs locally.
	Local bool
}

// Params is what every remote operation needs.
type Params struct {
	// Root is the symlink-resolved repository root.
	Root string
	// Host is the ssh alias shown in sendaway's lines.
	Host string
	// Version is the local sendaway version, shown on a protocol mismatch.
	Version   string
	Stderr    *os.File
	Transport Transport
}

// ExecParams describes a remote run of Argv.
type ExecParams struct {
	Params
	Project *config.Project
	Argv    []string
	// Cwd is the symlink-resolved working directory inside Root.
	Cwd string
	// StateDir is the local sendaway state directory (runs, locks).
	StateDir string
	// Environ and Home feed the environment allowlist.
	Environ []string
	Home    string
	Stdout  *os.File
}

// DieBySignal ends the process by sig with its default disposition. If sig
// is ignored, it exits with 128+sig instead.
func DieBySignal(sig syscall.Signal) {
	signal.Reset(sig)
	_ = syscall.Kill(os.Getpid(), sig)
	time.Sleep(time.Second)
	os.Exit(128 + int(sig))
}

// localHolder is the local lock record.
type localHolder struct {
	Argv          []string `json:"argv"`
	StartedUnixMs int64    `json:"started_unix_ms"`
	Run           string   `json:"run"`
}

// execution is the state of one Exec call.
type execution struct {
	p     ExecParams
	start time.Time
	con   *console
	merge bool
	in    *interrupts
	rec   *runlog.Run
	meta  runlog.Meta
}

// Exec runs p.Argv on the host and returns how the process must end. Every
// failure is reported on stderr before an Outcome with ExitFailure.
func Exec(p ExecParams) Outcome {
	x := &execution{p: p, start: time.Now()}
	x.merge = sameFile(p.Stdout, p.Stderr)
	errFile := p.Stderr
	if x.merge {
		errFile = p.Stdout
	}
	x.con = newConsole(p.Stdout, errFile)
	return x.run()
}

func (x *execution) run() Outcome {
	p := x.p
	if err := (&proto.Request{Op: proto.OpExec, Argv: p.Argv}).Validate(); err != nil {
		return x.fail(err.Error())
	}
	if err := checkRoot(p.Root); err != nil {
		return x.fail(err.Error())
	}
	x.in = watchInterrupts()
	defer x.in.stop()

	rec, err := runlog.Open(p.StateDir, nil).Create()
	if err != nil {
		return x.fail(err.Error())
	}
	x.rec = rec
	x.con.setLog(rec.Log)
	x.meta = runlog.Meta{Argv: p.Argv, Cwd: p.Cwd, Root: p.Root, Host: p.Host, StartUnixMS: x.start.UnixMilli()}

	l, err := x.lock()
	if err != nil {
		if sig := x.in.signal(); sig != 0 {
			return x.interrupted(sig)
		}
		return x.failRecorded("local lock: " + err.Error())
	}
	defer func() { _ = l.Release() }()

	pl, err := buildPlan(p.Root, p.Project.Exclude)
	if err != nil {
		return x.failRecorded(err.Error())
	}
	x.meta.Fingerprint = pl.fingerprint
	req := x.request(pl)
	if err := req.Validate(); err != nil {
		return x.failRecorded(err.Error())
	}
	if sig := x.in.signal(); sig != 0 {
		return x.interrupted(sig)
	}
	return x.remote(pl, req)
}

// lock takes the per-repository local lock, printing a line while it waits.
func (x *execution) lock() (*lock.Lock, error) {
	dir := filepath.Join(x.p.StateDir, "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(x.p.Root))
	name := filepath.Join(dir, hex.EncodeToString(sum[:])[:16]+".lock")
	holder, _ := json.Marshal(localHolder{Argv: x.p.Argv, StartedUnixMs: x.start.UnixMilli(), Run: x.rec.ID})
	return lock.Acquire(x.in.ctx, name, holder, func(cur []byte) {
		var h localHolder
		if json.Unmarshal(cur, &h) != nil || h.Run == "" {
			x.con.line("waiting for lock (held by unknown)")
			return
		}
		held := time.Since(time.UnixMilli(h.StartedUnixMs)).Round(time.Second)
		x.con.line(fmt.Sprintf("waiting for lock (held by: %s, %s, run %s)", strings.Join(h.Argv, " "), held, h.Run))
	})
}

func (x *execution) request(pl *plan) *proto.Request {
	proj := x.p.Project
	prepare := make([]proto.PrepareRule, len(proj.Prepare))
	for i, r := range proj.Prepare {
		prepare[i] = proto.PrepareRule{When: r.When, Run: r.Run}
	}
	return &proto.Request{
		Protocol:    proto.Protocol,
		Op:          proto.OpExec,
		Run:         x.rec.ID,
		Root:        x.p.Root,
		Cwd:         x.p.Cwd,
		Argv:        x.p.Argv,
		Env:         envfilter.Select(x.p.Environ, x.p.Home, proj.Env).Env,
		Manifest:    pl.send,
		Prepare:     prepare,
		Pull:        proj.Pull,
		Artifacts:   proj.Artifacts,
		MergeOutput: x.merge,
	}
}

type pushDone struct {
	res sshx.PushResult
	err error
}

// remote runs the session: hello, request, push, the command and collection.
func (x *execution) remote(pl *plan, req *proto.Request) Outcome {
	s, err := open(x.p.Transport, x.con)
	if err != nil {
		return x.failRecorded(err.Error())
	}
	if ph := s.handshake(x.in, x.p.Version); ph != nil {
		switch {
		case ph.sig != 0:
			return x.interrupted(ph.sig)
		case ph.reason != "":
			x.con.line(fmt.Sprintf("%s unreachable (%s), running locally", x.p.Host, ph.reason))
			x.discard()
			return Outcome{Local: true}
		}
		ph.show(x.con)
		return x.record(ExitFailure, 0, ph.summary(), nil)
	}
	s.request(req)

	select {
	case <-s.ready:
	case <-s.gotExit:
		return x.ended(s.finish(false), false)
	case <-s.end:
		return x.ended(s.finish(false), false)
	case <-x.in.done():
		s.finish(true)
		return x.interrupted(x.in.signal())
	}

	pushCtx, stopPush := context.WithCancel(context.Background())
	defer stopPush()
	pushed := make(chan pushDone, 1)
	syncStart := time.Now()
	go func() {
		res, err := x.p.Transport.Push(pushCtx, x.p.Root, pl.excludes)
		pushed <- pushDone{res, err}
	}()
	var pr pushDone
	select {
	case pr = <-pushed:
	case <-s.gotExit:
		stopPush()
		<-pushed
		return x.ended(s.finish(false), false)
	case <-s.end:
		stopPush()
		<-pushed
		return x.ended(s.finish(false), false)
	case <-x.in.done():
		stopPush()
		s.finish(true)
		<-pushed
		return x.interrupted(x.in.signal())
	}
	syncTime := time.Since(syncStart)
	x.meta.SyncMS = syncTime.Milliseconds()
	if pr.err != nil {
		e := s.finish(false)
		msg := "sync failed: " + pr.err.Error()
		if e.exit == nil || e.err != nil {
			x.con.line(msg)
			return x.ended(e, false)
		}
		return x.failRecorded(msg)
	}
	s.w.send(proto.TypePushed)
	x.startLines(s, pl, pr.res, syncTime)

	e, sig := s.await(x.in)
	if sig != 0 {
		return x.interrupted(sig)
	}
	if msg := e.failure(true); msg != "" {
		return x.failRecorded(msg)
	}
	return x.complete(e.exit)
}

// startLines prints the start line and the lines that follow it.
func (x *execution) startLines(s *session, pl *plan, res sshx.PushResult, syncTime time.Duration) {
	x.con.line(fmt.Sprintf("%s %s-%s · sync %.2fs", x.p.Host, s.hello.OS, s.hello.Arch, syncTime.Seconds()))
	if res.Vanished {
		x.con.line("some files vanished during sync")
	}
	if len(pl.notSent) > 0 {
		x.con.line("not sent (secret-like): " + strings.Join(pl.notSent, ", "))
	}
	for _, w := range pl.warnings {
		x.con.line(w)
	}
	if pl.ignoreCase {
		if names := caseMismatches(x.p.Root, res.Transferred); len(names) > 0 {
			x.con.line("name differs only in case from the file on disk (case-only rename?): " + strings.Join(names, ", "))
		}
	}
}

// ended reports a session that finished without a usable result.
func (x *execution) ended(e ending, pushed bool) Outcome {
	msg := e.failure(pushed)
	if msg == "" {
		msg = "remote-exec ended before the push"
	}
	return x.failRecorded(msg)
}

// complete collects files, prints the closing lines and records the run.
func (x *execution) complete(ex *proto.Exit) Outcome {
	collectErr := x.collect(ex)
	if sig := x.in.signal(); sig != 0 {
		return x.interrupted(sig)
	}
	if len(ex.Modified) > 0 {
		x.con.line(modifiedLine(ex.Modified))
	}
	if collectErr != "" {
		x.con.line(collectErr)
		return x.record(ExitFailure, 0, collectErr, ex.Modified)
	}
	x.trailer(ex.Code, ex.Signal)
	return x.record(ex.Code, ex.Signal, "", ex.Modified)
}

// collect brings back pull files after success or artifacts after failure.
// It returns a reason line when the run must fail.
func (x *execution) collect(ex *proto.Exit) string {
	if ex.Code == 0 && ex.Signal == 0 {
		if len(ex.Rejected) > 0 {
			return "pull refused: " + rejectedList(ex.Rejected)
		}
		if len(ex.Collect) == 0 {
			return ""
		}
		if err := checkDest(x.p.Root, ex.Collect); err != nil {
			return err.Error()
		}
		if err := x.p.Transport.Pull(x.in.ctx, x.p.Root, ex.Collect, x.p.Root); err != nil {
			return "pull failed: " + err.Error()
		}
		return ""
	}
	if len(ex.Rejected) > 0 {
		x.con.line("artifacts not collected: " + rejectedList(ex.Rejected))
	}
	if len(ex.Collect) > 0 {
		if err := x.p.Transport.Pull(x.in.ctx, x.p.Root, ex.Collect, x.rec.ArtifactsDir()); err != nil {
			x.con.line("artifacts not collected: " + err.Error())
		}
	}
	return ""
}

func rejectedList(rs []proto.Rejected) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = fmt.Sprintf("%s (%s)", r.Path, r.Reason)
	}
	return strings.Join(parts, ", ")
}

// trailer prints the closing line of a run.
func (x *execution) trailer(code, sig int) {
	status := fmt.Sprintf("exit %d", code)
	if sig != 0 {
		status = fmt.Sprintf("killed by signal %d", sig)
	}
	x.con.line(fmt.Sprintf("%s %s · %.1fs · run %s · log %s",
		x.p.Host, status, time.Since(x.start).Seconds(), x.rec.ID, x.rec.Log.Name()))
}

// interrupted ends a run the shim was told to stop.
func (x *execution) interrupted(sig syscall.Signal) Outcome {
	x.trailer(128+int(sig), int(sig))
	return x.record(128+int(sig), int(sig), "", nil)
}

// fail reports a failure that happens before the run directory exists.
func (x *execution) fail(msg string) Outcome {
	x.con.line(msg)
	return Outcome{Code: ExitFailure}
}

// failRecorded reports a failure and records it.
func (x *execution) failRecorded(msg string) Outcome {
	x.con.line(msg)
	return x.record(ExitFailure, 0, msg, nil)
}

// record writes meta.json and returns the matching outcome.
func (x *execution) record(code, sig int, errText string, modified []string) Outcome {
	x.con.setLog(nil)
	x.meta.DurationMS = time.Since(x.start).Milliseconds()
	x.meta.Code = code
	x.meta.Signal = sig
	x.meta.Error = errText
	x.meta.Modified = modified
	if err := x.rec.Record(x.meta); err != nil {
		x.con.line("record run: " + err.Error())
	}
	return Outcome{Code: code, Signal: syscall.Signal(sig)}
}

// discard drops the run directory of a run that falls back to local execution.
func (x *execution) discard() {
	x.con.setLog(nil)
	_ = x.rec.Log.Close()
	_ = os.RemoveAll(x.rec.Dir())
}
