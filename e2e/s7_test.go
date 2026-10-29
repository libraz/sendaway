//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// markerScript leaves a file in the directory it runs in, so the side the
// command ran on can be told afterwards.
const markerScript = "echo ran > marker; exit 3"

func TestS7Unreachable(t *testing.T) {
	e := newEnv(t)
	e.setHost(closedPort(t), suite.key, suite.knownHosts)
	r := e.sendaway("run", "sh", "-c", markerScript)
	if r.code != 3 {
		t.Fatalf("want the local command's exit 3, got %s", r)
	}
	if !strings.Contains(r.stderr, "[sendaway] "+hostAlias+" unreachable (") || !strings.Contains(r.stderr, "running locally") {
		t.Errorf("no fallback line: %s", r)
	}
	if _, err := os.Stat(e.path("marker")); err != nil {
		t.Errorf("command did not run locally: %v", err)
	}
}

func TestS7AuthFail(t *testing.T) {
	e := newEnv(t)
	other := filepath.Join(t.TempDir(), "id_other")
	if err := keygen(other); err != nil {
		t.Fatal(err)
	}
	e.setHost(suite.port, other, suite.knownHosts)
	r := e.sendaway("run", "sh", "-c", markerScript)
	expectNoFallback(t, e, r, "Permission denied")
}

func TestS7HostKey(t *testing.T) {
	e := newEnv(t)
	other := filepath.Join(t.TempDir(), "host_key")
	if err := keygen(other); err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(other + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(pub))
	known := filepath.Join(t.TempDir(), "known_hosts")
	line := fmt.Sprintf("[127.0.0.1]:%d %s %s\n", suite.port, fields[0], fields[1])
	if err := os.WriteFile(known, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	e.setHost(suite.port, suite.key, known)
	r := e.sendaway("run", "sh", "-c", markerScript)
	expectNoFallback(t, e, r, "Host key verification failed")
}

// expectNoFallback checks that r failed with 125 and ssh's message, and that
// nothing ran locally.
func expectNoFallback(t *testing.T, e *env, r result, sshMsg string) {
	t.Helper()
	if r.code != 125 || !strings.Contains(r.stderr, sshMsg) {
		t.Errorf("want 125 with %q: %s", sshMsg, r)
	}
	if strings.Contains(r.stderr, "running locally") {
		t.Errorf("fell back to a local run: %s", r)
	}
	if _, err := os.Stat(e.path("marker")); err == nil {
		t.Errorf("command ran locally")
	}
}

func TestS7LostDuringRun(t *testing.T) {
	e := newEnv(t)
	p := e.start("run", "sh", "-c", "sleep 305")
	pgid := findGroup(t, "sleep 305", 1)

	socks, err := filepath.Glob(filepath.Join(e.stateDir, "cm", "*"))
	if err != nil || len(socks) != 1 {
		t.Fatalf("want one control socket, got %v %v", socks, err)
	}
	if _, err := run(nil, "ssh", "-F", e.sshConfig, "-o", "ControlPath="+socks[0], "-O", "exit", hostAlias); err != nil {
		t.Fatal(err)
	}

	r := p.wait(60 * time.Second)
	if r.code != 125 || !strings.Contains(r.stderr, lostLine) {
		t.Errorf("want 125 and %q: %s", lostLine, r)
	}
	waitGroupGone(t, pgid, blackholeLimit)
}

// closedPort returns a local TCP port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
