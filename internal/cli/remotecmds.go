package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/libraz/sendaway/internal/remote"
	"github.com/libraz/sendaway/internal/run"
	"github.com/libraz/sendaway/internal/runlog"
	"github.com/libraz/sendaway/internal/sshx"
)

// currentProject loads the project above the current directory.
func currentProject() (*project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return loadProject(cwd)
}

func runCmd(argv []string, stderr *os.File) int {
	if len(argv) == 0 {
		return fail(stderr, usage("run needs a command"))
	}
	proj, err := currentProject()
	if err != nil {
		return fail(stderr, err)
	}
	return remoteRun(proj, argv)
}

func localCmd(argv []string) int {
	if len(argv) == 0 {
		return fail(os.Stderr, usage("local needs a command"))
	}
	return execLocal(argv, true, false)
}

func fetchCmd(paths []string, stderr *os.File) int {
	if len(paths) == 0 {
		return fail(stderr, usage("fetch needs at least one path"))
	}
	proj, err := currentProject()
	if err != nil {
		return fail(stderr, err)
	}
	t, err := resolveTarget(proj.root)
	if err != nil {
		return fail(stderr, err)
	}
	m, err := runlog.Open(t.state, nil).Latest()
	if err != nil {
		return fail(stderr, err)
	}
	dest := filepath.Join(runlog.Open(t.state, nil).Dir(m.ID), "fetched")
	return finish(run.Fetch(t.params(proj, stderr), paths, dest))
}

func forgetCmd(args []string, _ io.Writer, stderr *os.File) int {
	if len(args) > 0 {
		return fail(stderr, usage("forget takes no arguments"))
	}
	proj, err := currentProject()
	if err != nil {
		return fail(stderr, err)
	}
	t, err := resolveTarget(proj.root)
	if err != nil {
		return fail(stderr, err)
	}
	if code := finish(run.Forget(t.params(proj, stderr))); code != 0 {
		return code
	}
	_, _ = fmt.Fprintf(stderr, "[sendaway] %s: removed %s and its state\n", t.host, proj.root)
	return 0
}

// finish converts an Outcome of fetch or forget into an exit code.
func finish(o run.Outcome) int {
	if o.Signal != 0 {
		run.DieBySignal(o.Signal)
	}
	return o.Code
}

func shellCmd(args []string) error {
	if len(args) > 0 {
		return usage("shell takes no arguments")
	}
	proj, err := currentProject()
	if err != nil {
		return err
	}
	t, err := resolveTarget(proj.root)
	if err != nil {
		return err
	}
	argv, err := shellArgv(t.client, proj.root)
	if err != nil {
		return err
	}
	path, err := exec.LookPath("ssh")
	if err != nil {
		return err
	}
	return fmt.Errorf("exec ssh: %w", syscall.Exec(path, argv, os.Environ()))
}

// shellArgv builds the ssh command line (argv[0] included) for an interactive
// shell in root: the shared options minus BatchMode and -T, plus -t.
func shellArgv(c *sshx.Client, root string) ([]string, error) {
	args, err := c.SSHArgs()
	if err != nil {
		return nil, err
	}
	argv := []string{"ssh", "-t"}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-T":
		case args[i] == "-o" && i+1 < len(args) && args[i+1] == "BatchMode=yes":
			i++
		default:
			argv = append(argv, args[i])
		}
	}
	return append(argv, "cd "+sshx.ShellQuote(root)+` && exec "$SHELL" -l`), nil
}

func remoteExec() error {
	return (&remote.Server{Version: Version}).Serve(os.Stdin, os.Stdout)
}
