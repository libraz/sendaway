package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/libraz/sendaway/internal/match"
	"github.com/libraz/sendaway/internal/run"
	"github.com/libraz/sendaway/internal/shim"
)

// shimMain handles an invocation under a command name. It returns only when
// the command could not be started or the remote run has ended.
func shimMain(args []string) int {
	name := filepath.Base(args[0])
	argv := append([]string{name}, args[1:]...)
	if shim.Passthrough() {
		return execLocal(argv, false, false)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(os.Stderr, err)
	}
	if _, ok := shim.FindConfig(cwd); !ok {
		return execLocal(argv, false, false)
	}
	proj, err := loadProject(cwd)
	if err != nil {
		return fail(os.Stderr, err)
	}
	if match.Decide(argv, proj.cfg.Remote, proj.cfg.Local) == match.Local {
		return execLocal(argv, true, false)
	}
	return remoteRun(proj, argv)
}

// remoteRun runs argv on the host and ends the process as the command did.
// It returns only a failure code.
func remoteRun(proj *project, argv []string) int {
	t, err := resolveTarget(proj.root)
	if err != nil {
		return fail(os.Stderr, err)
	}
	home, _ := os.UserHomeDir()
	o := run.Exec(run.ExecParams{
		Params:   t.params(proj, os.Stderr),
		Project:  proj.cfg,
		Argv:     argv,
		Cwd:      proj.cwd,
		StateDir: t.state,
		Environ:  os.Environ(),
		Home:     home,
		Stdout:   os.Stdout,
	})
	switch {
	case o.Local:
		return execLocal(argv, true, true)
	case o.Signal != 0:
		run.DieBySignal(o.Signal)
	}
	return o.Code
}

// execLocal replaces the process with the real argv[0]. marked adds
// SENDAWAY_PASSTHROUGH=1; nice lowers the priority first.
func execLocal(argv []string, marked, nice bool) int {
	path, err := realPath(argv[0])
	if err != nil {
		return fail(os.Stderr, err)
	}
	full := append([]string{path}, argv[1:]...)
	if marked {
		err = shim.ExecLocal(path, full, os.Environ(), nice)
	} else {
		err = shim.Exec(path, full, os.Environ())
	}
	return fail(os.Stderr, fmt.Errorf("exec %s: %w", path, err))
}

// realPath finds the executable behind name, skipping the shim directory.
func realPath(name string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	dir := shimDirFor(name)
	if dir == "" {
		return exec.LookPath(name)
	}
	path, err := shim.Resolve(name, dir, os.Getenv("PATH"))
	if err != nil {
		return "", err
	}
	if sameAsSelf(path) {
		return "", fmt.Errorf("%s: %w", name, shim.ErrNotFound)
	}
	return path, nil
}

// shimDirFor returns the directory holding the shim for name: the first PATH
// entry whose name is this binary, else the install-shims directory if it
// exists, else "".
func shimDirFor(name string) string {
	self, err := selfInfo()
	if err == nil {
		for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
			if entry == "" {
				continue
			}
			if fi, err := os.Stat(filepath.Join(entry, name)); err == nil && os.SameFile(fi, self) { //nolint:gosec // PATH entries are probed by design
				return entry
			}
		}
	}
	if d, err := shimDirPath(); err == nil {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	return ""
}

func selfInfo() (os.FileInfo, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return os.Stat(exe)
}

func sameAsSelf(path string) bool {
	self, err := selfInfo()
	if err != nil {
		return false
	}
	fi, err := os.Stat(path) //nolint:gosec // PATH entries are probed by design
	return err == nil && os.SameFile(fi, self)
}
