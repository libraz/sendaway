// Package runlog stores one directory per run under <state>/runs and prunes old ones.
package runlog

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	// MaxRuns is the number of newest runs kept.
	MaxRuns = 100
	// MaxAge is how long a run is kept.
	MaxAge = 14 * 24 * time.Hour

	idTimeLayout = "20060102-150405"
	idPrefix     = "r-"
	metaName     = "meta.json"
	logName      = "output.log"
)

// ErrNoRuns is returned by Latest when nothing has been recorded.
var ErrNoRuns = errors.New("no runs recorded")

// Meta is the content of meta.json. It never holds environment variables.
type Meta struct {
	ID          string   `json:"id"`
	Argv        []string `json:"argv"`
	Cwd         string   `json:"cwd"`
	Root        string   `json:"root"`
	Host        string   `json:"host"`
	StartUnixMS int64    `json:"start_unix_ms"`
	DurationMS  int64    `json:"duration_ms"`
	SyncMS      int64    `json:"sync_ms"`
	Code        int      `json:"code"`
	Signal      int      `json:"signal,omitempty"`
	Modified    []string `json:"modified,omitempty"`
	// Fingerprint is an opaque tree fingerprint taken before the push.
	Fingerprint string `json:"fingerprint"`
	Error       string `json:"error,omitempty"`
}

// Store manages the runs directory below a state directory.
type Store struct {
	dir string
	now func() time.Time
}

// Open returns a Store rooted at <stateDir>/runs. A nil now uses time.Now.
func Open(stateDir string, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{dir: filepath.Join(stateDir, "runs"), now: now}
}

// Run is a run directory that has been created but not yet recorded.
type Run struct {
	// ID is the run identifier.
	ID string
	// Log receives the full output; Record closes it.
	Log   *os.File
	store *Store
	dir   string
}

// Create makes a new run directory with its artifacts and fetched subdirectories
// and opens output.log for writing.
func (s *Store) Create() (*Run, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, fmt.Errorf("create runs dir: %w", err)
	}
	var suffix [2]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, fmt.Errorf("run id: %w", err)
	}
	id := idPrefix + s.now().UTC().Format(idTimeLayout) + "-" + hex.EncodeToString(suffix[:])
	dir := filepath.Join(s.dir, id)
	for _, d := range []string{dir, filepath.Join(dir, "artifacts"), filepath.Join(dir, "fetched")} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return nil, fmt.Errorf("create run dir: %w", err)
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, logName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create output log: %w", err)
	}
	return &Run{ID: id, Log: f, store: s, dir: dir}, nil
}

// Dir returns the run directory.
func (r *Run) Dir() string { return r.dir }

// ArtifactsDir returns the directory for collected artifacts.
func (r *Run) ArtifactsDir() string { return filepath.Join(r.dir, "artifacts") }

// FetchedDir returns the directory for explicitly fetched files.
func (r *Run) FetchedDir() string { return filepath.Join(r.dir, "fetched") }

// Record closes the log, writes meta.json for the run (setting m.ID) and prunes old runs.
func (r *Run) Record(m Meta) error {
	m.ID = r.ID
	closeErr := r.Log.Close()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode meta: %w", err)
	}
	tmp := filepath.Join(r.dir, metaName+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(r.dir, metaName)); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}
	if err := r.store.prune(); err != nil {
		return err
	}
	if closeErr != nil {
		return fmt.Errorf("close output log: %w", closeErr)
	}
	return nil
}

// ids returns run ids, newest first.
func (s *Store) ids() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read runs dir: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			if _, ok := idTime(e.Name()); ok {
				ids = append(ids, e.Name())
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, nil
}

// idTime extracts the start time encoded in a run id.
func idTime(id string) (time.Time, bool) {
	n := len(idPrefix) + len(idTimeLayout)
	if len(id) < n || id[:len(idPrefix)] != idPrefix {
		return time.Time{}, false
	}
	t, err := time.Parse(idTimeLayout, id[len(idPrefix):n])
	return t, err == nil
}

func (s *Store) prune() error {
	ids, err := s.ids()
	if err != nil {
		return err
	}
	cutoff := s.now().Add(-MaxAge)
	for i, id := range ids {
		t, _ := idTime(id)
		if i < MaxRuns && !t.Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.dir, id)); err != nil {
			return fmt.Errorf("prune %s: %w", id, err)
		}
	}
	return nil
}

// List returns the recorded runs, newest first. Runs without meta.json are skipped.
func (s *Store) List() ([]Meta, error) {
	ids, err := s.ids()
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, id := range ids {
		data, err := os.ReadFile(filepath.Join(s.dir, id, metaName))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read meta %s: %w", id, err)
		}
		var m Meta
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parse meta %s: %w", id, err)
		}
		out = append(out, m)
	}
	return out, nil
}

// Latest returns the newest recorded run, or ErrNoRuns.
func (s *Store) Latest() (Meta, error) {
	ms, err := s.List()
	if err != nil {
		return Meta{}, err
	}
	if len(ms) == 0 {
		return Meta{}, ErrNoRuns
	}
	return ms[0], nil
}

// Dir returns the directory of the run with the given id.
func (s *Store) Dir(id string) string { return filepath.Join(s.dir, id) }

// TreeChanged reports whether the run's stored fingerprint differs from current.
func (m Meta) TreeChanged(current string) bool { return m.Fingerprint != current }
