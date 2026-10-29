package runlog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordAndPrune(t *testing.T) {
	state := t.TempDir()
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := Open(state, func() time.Time { return clock })

	record := func(host string) Meta {
		t.Helper()
		r, err := s.Create()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Log.WriteString("hello\n"); err != nil {
			t.Fatal(err)
		}
		for _, d := range []string{r.Dir(), r.ArtifactsDir(), r.FetchedDir()} {
			fi, err := os.Stat(d)
			if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
				t.Fatalf("bad dir %s: %v %v", d, fi, err)
			}
		}
		m := Meta{Argv: []string{"make"}, Host: host, Fingerprint: "fp"}
		if err := r.Record(m); err != nil {
			t.Fatal(err)
		}
		return Meta{ID: r.ID}
	}

	first := record("a")
	if got, _ := os.ReadFile(filepath.Join(s.Dir(first.ID), logName)); string(got) != "hello\n" {
		t.Fatalf("output.log = %q", got)
	}
	latest, err := s.Latest()
	if err != nil || latest.ID != first.ID || latest.Host != "a" {
		t.Fatalf("latest = %+v, %v", latest, err)
	}

	// Count limit: 100 newest are kept.
	for range MaxRuns + 5 {
		clock = clock.Add(time.Second)
		record("b")
	}
	ms, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != MaxRuns {
		t.Fatalf("kept %d runs, want %d", len(ms), MaxRuns)
	}
	if _, err := os.Stat(s.Dir(first.ID)); !os.IsNotExist(err) {
		t.Fatalf("oldest run not pruned: %v", err)
	}
	for i := 1; i < len(ms); i++ {
		if ms[i-1].ID <= ms[i].ID {
			t.Fatal("not newest first")
		}
	}

	// Age limit: runs older than 14 days go on the next record.
	clock = clock.Add(MaxAge + time.Hour)
	record("c")
	ms, _ = s.List()
	if len(ms) != 1 || ms[0].Host != "c" {
		t.Fatalf("after age prune: %d runs", len(ms))
	}
}

func TestTreeChanged(t *testing.T) {
	s := Open(t.TempDir(), nil)
	if _, err := s.Latest(); !errors.Is(err, ErrNoRuns) {
		t.Fatalf("Latest on empty = %v", err)
	}
	r, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Record(Meta{Fingerprint: "abc"}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if m.TreeChanged("abc") {
		t.Error("same fingerprint reported as changed")
	}
	if !m.TreeChanged("abd") {
		t.Error("different fingerprint not reported as changed")
	}
}
