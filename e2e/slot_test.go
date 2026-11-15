//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// TestSlots syncs a slot to its own copy over rsync, runs there, pulls back
// into the local tree, and forgets only that copy.
func TestSlots(t *testing.T) {
	e := newEnv(t)
	e.writeFile(".gitignore", "out/\n")
	e.writeFile(".sendaway.yml", "remote:\n  - [make]\npull: [out]\nslots: 1\n")
	e.writeFile("src.txt", "source\n")
	slot := []string{"SENDAWAY_SLOT=1"}
	slotRoot := e.root + ".slot-1"

	mustSucceed(t, "main run", e.sendaway("run", "sh", "-c", "mkdir -p out && echo main > out/main.txt"))
	r := e.sendawayEnv(slot, "run", "sh", "-c", "pwd; mkdir -p out && cat src.txt > out/copy.txt")
	mustSucceed(t, "slot run", r)
	if r.stdout != slotRoot+"\n" {
		t.Errorf("slot run cwd = %q, want %q", r.stdout, slotRoot)
	}
	if lines := r.saLines(); len(lines) < 2 || !strings.HasPrefix(lines[0], "[sendaway] "+hostAlias+" slot 1 ") ||
		!strings.HasPrefix(r.trailer(), "[sendaway] "+hostAlias+" slot 1 ") {
		t.Errorf("start and closing lines must name the slot: %q", lines)
	}
	if got, err := os.ReadFile(e.path("out/copy.txt")); err != nil || string(got) != "source\n" {
		t.Errorf("pulled out/copy.txt from the slot: %q, %v", got, err)
	}
	if _, err := e.remoteSh(`test -f "$ROOT.slot-1/src.txt" && test -d "$ROOT.slot-1/.git" && ! test -e "$ROOT.slot-1/out/main.txt" && ! test -e "$ROOT/out/copy.txt"`); err != nil {
		t.Errorf("slot copy is not separate from the main copy: %v", err)
	}

	if r := e.sendawayEnv([]string{"SENDAWAY_SLOT=2"}, "run", "true"); r.code != 125 ||
		!strings.Contains(r.stderr, "SENDAWAY_SLOT=2 is not enabled: slots is 1 in .sendaway.yml") {
		t.Errorf("unenabled slot: want 125 with the slots message, got %s", r)
	}

	mustSucceed(t, "forget slot", e.sendawayEnv(slot, "forget"))
	sum := sha256.Sum256([]byte(slotRoot))
	slotState := remoteHome + "/.local/state/sendaway/repos/" + hex.EncodeToString(sum[:])[:16]
	if _, err := e.remoteSh(`! test -e "$ROOT.slot-1" && ! test -e "$1" && test -f "$ROOT/src.txt" && test -d "$2"`,
		slotState, e.remoteStateDir()); err != nil {
		t.Errorf("forget in a slot must remove only that copy and its state: %v", err)
	}
}
