//go:build e2e

package e2e

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	// cancelLimit is how long the remote group may outlive an interrupt.
	cancelLimit = 10 * time.Second
	// blackholeLimit is how long it may outlive a dead link.
	blackholeLimit = 40 * time.Second
	// lostLine is printed when the link drops after the command started.
	lostLine = "[sendaway] connection lost during run — result unknown"
)

// remoteGroups returns, for each process group in the container, the
// arguments of its members.
func remoteGroups() (map[int][]string, error) {
	out, err := dockerExec("root", nil, nil, "ps", "-eo", "pgid=,args=")
	if err != nil {
		return nil, err
	}
	groups := make(map[int][]string)
	for l := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		pgid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		groups[pgid] = append(groups[pgid], strings.Join(f[1:], " "))
	}
	return groups, nil
}

// findGroup waits until count processes running args share a process group
// in the container and returns that group.
func findGroup(t *testing.T, args string, count int) int {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		groups, err := remoteGroups()
		if err != nil {
			t.Fatal(err)
		}
		for pgid, members := range groups {
			n := 0
			for _, m := range members {
				if m == args {
					n++
				}
			}
			if n >= count {
				return pgid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no process group with %d × %q in the container: %v", count, args, groups)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitGroupGone polls until no process is left in pgid and returns how long
// that took, failing the test after limit.
func waitGroupGone(t *testing.T, pgid int, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for {
		groups, err := remoteGroups()
		if err != nil {
			t.Fatal(err)
		}
		members, ok := groups[pgid]
		if !ok {
			return time.Since(start)
		}
		if time.Since(start) > limit {
			t.Fatalf("process group %d still has %d processes after %s: %q", pgid, len(members), limit, members)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// startSleepers runs a child and a grandchild sleeping for secs on the host
// and returns the sendaway process and the remote group.
func startSleepers(t *testing.T, e *env, secs int) (*proc, int) {
	t.Helper()
	sleep := fmt.Sprintf("sleep %d", secs)
	p := e.start("run", "sh", "-c", sleep+" & "+sleep)
	return p, findGroup(t, sleep, 2)
}

func testCancel(t *testing.T, sig syscall.Signal, secs int) {
	t.Helper()
	e := newEnv(t)
	p, pgid := startSleepers(t, e, secs)
	p.signal(sig)
	took := waitGroupGone(t, pgid, cancelLimit)
	t.Logf("remote group %d gone %s after signal %d", pgid, took.Round(time.Millisecond), int(sig))
	r := p.wait(30 * time.Second)
	if r.signal != sig {
		t.Errorf("sendaway should die by signal %d: %s", int(sig), r)
	}
}

func TestS3CancelINT(t *testing.T)  { testCancel(t, syscall.SIGINT, 301) }
func TestS3CancelTERM(t *testing.T) { testCancel(t, syscall.SIGTERM, 302) }
func TestS3CancelKILL(t *testing.T) { testCancel(t, syscall.SIGKILL, 303) }

func TestS3Blackhole(t *testing.T) {
	e := newEnv(t)
	p, pgid := startSleepers(t, e, 304)
	blackhole(t)
	took := waitGroupGone(t, pgid, blackholeLimit)
	t.Logf("remote group %d gone %s after the blackhole", pgid, took.Round(time.Millisecond))
	r := p.wait(60 * time.Second)
	if r.code != 125 || !strings.Contains(r.stderr, lostLine) {
		t.Errorf("want 125 and %q: %s", lostLine, r)
	}
}

// blackholeRules drop the ssh traffic in both directions inside the container.
var blackholeRules = [][]string{
	{"INPUT", "-p", "tcp", "--dport", "22", "-j", "DROP"},
	{"OUTPUT", "-p", "tcp", "--sport", "22", "-j", "DROP"},
}

// blackhole silently drops the client's ssh traffic until the test ends.
func blackhole(t *testing.T) {
	t.Helper()
	for _, r := range blackholeRules {
		if _, err := dockerExec("root", nil, nil, append([]string{"iptables", "-I"}, r...)...); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := dockerExec("root", nil, nil, append([]string{"iptables", "-D"}, r...)...); err != nil {
				t.Errorf("remove blackhole rule: %v", err)
			}
		})
	}
}
