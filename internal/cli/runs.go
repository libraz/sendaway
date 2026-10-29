package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/libraz/sendaway/internal/manifest"
	"github.com/libraz/sendaway/internal/runlog"
)

const treeChangedNote = "(tree changed since this run)"

// outputLogName is the file name of a run's recorded output inside its directory.
const outputLogName = "output.log"

func runsCmd(out io.Writer, args []string, latestOnly bool) error {
	if len(args) > 0 {
		return usage("this command takes no arguments")
	}
	state, err := stateDir()
	if err != nil {
		return err
	}
	store := runlog.Open(state, nil)
	if latestOnly {
		m, err := store.Latest()
		if err != nil {
			return err
		}
		writeLast(out, store, m, currentFingerprint)
		return nil
	}
	ms, err := store.List()
	if err != nil {
		return err
	}
	writeRuns(out, ms, currentFingerprint)
	return nil
}

// currentFingerprint computes the fingerprint of the tree at root now; ok is
// false when the tree cannot be read.
func currentFingerprint(root string) (string, bool) {
	m, err := manifest.Build(root)
	if err != nil {
		return "", false
	}
	return m.Fingerprint(), true
}

// changed reports whether the tree at m.Root differs from the one the run saw.
func changed(m runlog.Meta, fp func(string) (string, bool)) bool {
	cur, ok := fp(m.Root)
	return ok && m.TreeChanged(cur)
}

func result(m runlog.Meta) string {
	switch {
	case m.Error != "":
		return "failed"
	case m.Signal != 0:
		return fmt.Sprintf("signal %d", m.Signal)
	}
	return fmt.Sprintf("exit %d", m.Code)
}

func duration(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(100 * time.Millisecond).String()
}

func writeRuns(out io.Writer, ms []runlog.Meta, fp func(string) (string, bool)) {
	if len(ms) == 0 {
		_, _ = fmt.Fprintln(out, "no runs recorded")
		return
	}
	type current struct {
		fp string
		ok bool
	}
	seen := make(map[string]current)
	cached := func(root string) (string, bool) {
		if c, ok := seen[root]; ok {
			return c.fp, c.ok
		}
		f, ok := fp(root)
		seen[root] = current{f, ok}
		return f, ok
	}
	for _, m := range ms {
		s := fmt.Sprintf("%s  %s  %s  %s  %s", m.ID, m.Host, result(m), duration(m.DurationMS), strings.Join(m.Argv, " "))
		if changed(m, cached) {
			s += "  " + treeChangedNote
		}
		_, _ = fmt.Fprintln(out, s)
	}
}

func writeLast(out io.Writer, store *runlog.Store, m runlog.Meta, fp func(string) (string, bool)) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format+"\n", a...) }
	p("run:      %s", m.ID)
	p("command:  %s", strings.Join(m.Argv, " "))
	p("cwd:      %s", m.Cwd)
	p("host:     %s", m.Host)
	p("started:  %s", time.UnixMilli(m.StartUnixMS).Format(time.RFC3339))
	p("duration: %s (sync %s)", duration(m.DurationMS), duration(m.SyncMS))
	p("result:   %s", result(m))
	if m.Error != "" {
		p("error:    %s", m.Error)
	}
	if len(m.Modified) > 0 {
		p("modified: %s", strings.Join(m.Modified, ", "))
	}
	p("log:      %s", filepath.Join(store.Dir(m.ID), outputLogName))
	if changed(m, fp) {
		p("%s", treeChangedNote)
	}
}
