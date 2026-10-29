//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestS8Runs(t *testing.T) {
	e := newEnv(t)
	e.writeFile("notes.txt", "first\n")
	r := e.sendaway("run", "sh", "-c", "echo hello-s8; exit 3")
	if r.code != 3 {
		t.Fatalf("want exit 3, got %s", r)
	}
	id := r.runID(t)
	dir := filepath.Join(e.stateDir, "runs", id)

	logPath := filepath.Join(dir, "output.log")
	if m := trailerRE.FindStringSubmatch(r.trailer()); m[3] != logPath {
		t.Errorf("trailer log path %q, want %q", m[3], logPath)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "hello-s8\n") || !strings.Contains(string(log), r.trailer()+"\n") {
		t.Errorf("output.log lacks the output or the trailer:\n%s", log)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		ID          string   `json:"id"`
		Argv        []string `json:"argv"`
		Root        string   `json:"root"`
		Host        string   `json:"host"`
		Code        int      `json:"code"`
		Fingerprint string   `json:"fingerprint"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ID != id || meta.Code != 3 || meta.Host != hostAlias || meta.Root != e.root || meta.Fingerprint == "" ||
		!slices.Equal(meta.Argv, []string{"sh", "-c", "echo hello-s8; exit 3"}) {
		t.Errorf("meta.json: %s", raw)
	}

	last := e.sendaway("last")
	mustSucceed(t, "last", last)
	for _, want := range []string{"run:      " + id, "result:   exit 3", "log:      " + logPath} {
		if !strings.Contains(last.stdout, want) {
			t.Errorf("last lacks %q:\n%s", want, last.stdout)
		}
	}
	if strings.Contains(last.stdout, "tree changed since this run") {
		t.Errorf("last reports a change on an unchanged tree:\n%s", last.stdout)
	}

	e.writeFile("notes.txt", "first\nsecond\n")
	last = e.sendaway("last")
	mustSucceed(t, "last after an edit", last)
	if !strings.Contains(last.stdout, "(tree changed since this run)") {
		t.Errorf("last misses the tree change:\n%s", last.stdout)
	}
}
