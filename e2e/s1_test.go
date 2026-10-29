//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// orderScript writes to stdout, stderr and stdout again.
const orderScript = `echo out1; echo err1 >&2; echo out2`

func TestS1ExitCodes(t *testing.T) {
	e := newEnv(t)
	for _, code := range []int{0, 1, 7, 255} {
		r := e.sendaway("run", "sh", "-c", fmt.Sprintf("exit %d", code))
		if r.code != code {
			t.Errorf("exit %d: got %s", code, r)
			continue
		}
		if want := fmt.Sprintf(" exit %d · ", code); !strings.Contains(r.trailer(), want) || !trailerRE.MatchString(r.trailer()) {
			t.Errorf("exit %d: trailer %q", code, r.trailer())
		}
	}

	// Through the make shim: make's own failure code comes back unchanged.
	e.writeFile("Makefile", "ok:\n\t@true\nfail:\n\t@exit 7\n")
	if r := e.shim("make", "ok"); r.code != 0 {
		t.Errorf("make ok: %s", r)
	}
	if r := e.shim("make", "fail"); r.code != 2 || !strings.Contains(r.stderr, "Error 7") {
		t.Errorf("make fail: want make's exit 2 after the recipe's 7, got %s", r)
	}
}

func TestS1Signal(t *testing.T) {
	e := newEnv(t)
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		r := e.sendaway("run", "sh", "-c", fmt.Sprintf("kill -%d $$", int(sig)))
		if r.signal != sig {
			t.Errorf("child killed by %d: want sendaway killed by the same signal, got %s", int(sig), r)
			continue
		}
		if want := fmt.Sprintf(" killed by signal %d · ", int(sig)); !strings.Contains(r.trailer(), want) {
			t.Errorf("signal %d: trailer %q", int(sig), r.trailer())
		}
	}
}

func TestS1OrderMerged(t *testing.T) {
	e := newEnv(t)
	// Both streams on one regular file, as an agent's shell tool provides.
	path := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cmd := e.command(t.Context(), suite.bin, []string{"run", "sh", "-c", orderScript})
	cmd.Stdout = f
	cmd.Stderr = f
	err = cmd.Run()
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	r := e.outcome(cmd, err, string(data), string(data))
	if r.code != 0 {
		t.Fatalf("run: %s", r)
	}
	if got, want := otherLines(r.stdout), []string{"out1", "err1", "out2"}; !slices.Equal(got, want) {
		t.Errorf("merged output: got %q, want %q\n%s", got, want, data)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if last := lines[len(lines)-1]; !trailerRE.MatchString(last) {
		t.Errorf("last line is not the trailer: %q", last)
	}
}

func TestS1OrderSplit(t *testing.T) {
	e := newEnv(t)
	r := e.sendaway("run", "sh", "-c", orderScript)
	mustSucceed(t, "run", r)
	if r.stdout != "out1\nout2\n" {
		t.Errorf("stdout: got %q, want %q", r.stdout, "out1\nout2\n")
	}
	if got := otherLines(r.stderr); !slices.Equal(got, []string{"err1"}) {
		t.Errorf("stderr output: got %q, want [err1]", got)
	}
	if !trailerRE.MatchString(r.trailer()) {
		t.Errorf("trailer %q", r.trailer())
	}
}
