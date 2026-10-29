package run

import (
	"syscall"

	"github.com/libraz/sendaway/internal/proto"
)

// Fetch brings the root-relative paths from the host's copy of p.Root into
// dest, applying the same checks as pull.
func Fetch(p Params, paths []string, dest string) Outcome {
	con := newConsole(p.Stderr, p.Stderr)
	in := watchInterrupts()
	defer in.stop()
	ex, o := call(p, con, in, &proto.Request{Op: proto.OpFetch, Pull: paths})
	if ex == nil {
		return o
	}
	if len(ex.Rejected) > 0 {
		con.line("pull refused: " + rejectedList(ex.Rejected))
		return Outcome{Code: ExitFailure}
	}
	if len(ex.Collect) == 0 {
		return Outcome{}
	}
	if err := p.Transport.Pull(in.ctx, p.Root, ex.Collect, dest); err != nil {
		if sig := in.signal(); sig != 0 {
			return Outcome{Code: 128 + int(sig), Signal: sig}
		}
		con.line("fetch failed: " + err.Error())
		return Outcome{Code: ExitFailure}
	}
	return Outcome{}
}

// Forget removes the host's copy of p.Root and its remote state.
func Forget(p Params) Outcome {
	con := newConsole(p.Stderr, p.Stderr)
	in := watchInterrupts()
	defer in.stop()
	if ex, o := call(p, con, in, &proto.Request{Op: proto.OpForget}); ex == nil {
		return o
	}
	return Outcome{}
}

// call runs a request that has no push phase and returns its exit frame, or
// nil and the outcome to end with.
func call(p Params, con *console, in *interrupts, req *proto.Request) (*proto.Exit, Outcome) {
	failed := func(msg string) (*proto.Exit, Outcome) {
		con.line(msg)
		return nil, Outcome{Code: ExitFailure}
	}
	killed := func(sig syscall.Signal) (*proto.Exit, Outcome) {
		return nil, Outcome{Code: 128 + int(sig), Signal: sig}
	}
	req.Protocol = proto.Protocol
	req.Root = p.Root
	req.Cwd = p.Root
	if err := req.Validate(); err != nil {
		return failed(err.Error())
	}
	if err := checkRoot(p.Root); err != nil {
		return failed(err.Error())
	}
	s, err := open(p.Transport, con)
	if err != nil {
		return failed(err.Error())
	}
	if ph := s.handshake(in, p.Version); ph != nil {
		if ph.sig != 0 {
			return killed(ph.sig)
		}
		ph.show(con)
		return nil, Outcome{Code: ExitFailure}
	}
	s.request(req)
	e, sig := s.await(in)
	if sig != 0 {
		return killed(sig)
	}
	if msg := e.failure(true); msg != "" {
		return failed(msg)
	}
	return e.exit, Outcome{}
}
