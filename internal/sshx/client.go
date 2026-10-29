// Package sshx builds and runs the ssh and rsync commands sendaway uses to reach a
// build host. Every child runs in its own process group; cancelling the context
// sends SIGTERM to that group.
package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// remoteCommand is what Open runs on the host.
const remoteCommand = "sendaway remote-exec"

// rsyncWaitDelay bounds how long Wait lingers on rsync output pipes after the
// process has exited or been killed.
const rsyncWaitDelay = 5 * time.Second

// exitVanished is the rsync exit code for "some files vanished before transfer".
const exitVanished = 24

// Client reaches one host over ssh and rsync.
type Client struct {
	Host      string // ssh alias
	SSHConfig string // passed as -F when non-empty
	StateDir  string // parent of the cm/ control socket directory
}

// Session is a running `sendaway remote-exec` on the host.
type Session struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	Stderr io.Reader
	// Wait reaps ssh after Stdout and Stderr are drained. code is ssh's exit code, or
	// -1 if ssh was killed by a signal; err is set only when ssh could not be waited on
	// or the context was cancelled.
	Wait func() (code int, err error)
}

// PushResult describes a completed push.
type PushResult struct {
	// Transferred holds the names rsync reported for the working-tree transfer.
	Transferred []string
	// Vanished is true if either rsync exited 24.
	Vanished bool
}

// SSHArgs returns the ssh arguments (options then host) without the program name.
func (c *Client) SSHArgs() ([]string, error) {
	opts, err := c.options()
	if err != nil {
		return nil, err
	}
	return append(opts, c.Host), nil
}

func (c *Client) options() ([]string, error) {
	cp, err := ControlPath(c.StateDir, c.Host)
	if err != nil {
		return nil, err
	}
	return SSHOptions(cp, c.SSHConfig), nil
}

// Open starts `sendaway remote-exec` on the host.
func (c *Client) Open(ctx context.Context) (*Session, error) {
	args, err := c.SSHArgs()
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, "ssh", append(args, remoteCommand)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("ssh stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ssh stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("ssh stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ssh: %w", err)
	}
	return &Session{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Wait: func() (int, error) {
			err := cmd.Wait()
			var ee *exec.ExitError
			if errors.As(err, &ee) && ctx.Err() == nil {
				return ee.ExitCode(), nil
			}
			if err != nil {
				return -1, err
			}
			return 0, nil
		},
	}, nil
}

// Push syncs the working tree and .git of root to the same path on the host, in two
// concurrent rsyncs. excludes are literal root-relative paths (anchored with a leading
// slash, directories with a trailing slash); their rsync metacharacters are escaped here.
func (c *Client) Push(ctx context.Context, root string, excludes []string) (PushResult, error) {
	shell, err := c.rsyncShell()
	if err != nil {
		return PushResult{}, err
	}
	var list bytes.Buffer
	for _, e := range excludes {
		list.WriteString(EscapeExclude(e))
		list.WriteByte(0)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		res      PushResult
	)
	run := func(name string, args []string, stdin []byte, body bool) {
		defer wg.Done()
		code, out, errOut, err := runRsync(ctx, args, stdin)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err != nil:
			err = fmt.Errorf("rsync %s: %w", name, err)
		case code == exitVanished:
			res.Vanished = true
		case code != 0:
			err = fmt.Errorf("rsync %s exited %d: %s", name, code, strings.TrimSpace(errOut))
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
				cancel()
			}
			return
		}
		if body {
			for l := range strings.SplitSeq(out, "\n") {
				if l != "" {
					res.Transferred = append(res.Transferred, l)
				}
			}
		}
	}
	wg.Add(2)
	go run("tree", bodyPushArgs(shell, c.Host, root), list.Bytes(), true)
	go run(".git", gitPushArgs(shell, c.Host, root), nil, false)
	wg.Wait()
	if firstErr != nil {
		return PushResult{}, firstErr
	}
	return res, nil
}

// Pull copies the root-relative files from the host's root into dest, without
// following symlinks that point outside the transfer.
func (c *Client) Pull(ctx context.Context, root string, files []string, dest string) error {
	shell, err := c.rsyncShell()
	if err != nil {
		return err
	}
	var list bytes.Buffer
	for _, f := range files {
		list.WriteString(f)
		list.WriteByte(0)
	}
	code, _, errOut, err := runRsync(ctx, pullArgs(shell, c.Host, root, dest), list.Bytes())
	if err != nil {
		return fmt.Errorf("rsync pull: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("rsync pull exited %d: %s", code, strings.TrimSpace(errOut))
	}
	return nil
}

// rsyncShell returns the value of rsync's -e: the ssh command with each -o argument
// double-quoted. It has no host, because rsync appends `-l user host` itself.
func (c *Client) rsyncShell() (string, error) {
	opts, err := c.options()
	if err != nil {
		return "", err
	}
	return rsyncShellFor(opts)
}

func rsyncShellFor(opts []string) (string, error) {
	var b strings.Builder
	b.WriteString("ssh")
	for _, o := range opts {
		if strings.ContainsAny(o, "\"\n") {
			return "", fmt.Errorf("ssh option %q cannot be passed to rsync -e", o)
		}
		b.WriteString(` "`)
		b.WriteString(o)
		b.WriteByte('"')
	}
	return b.String(), nil
}

func rsyncPath(root string) string {
	return "--rsync-path=mkdir -p " + ShellQuote(root) + " && rsync"
}

func bodyPushArgs(shell, host, root string) []string {
	return []string{
		"-rlpt", "-s", "-8", "--exclude=/.git", "-0", "--exclude-from=-",
		"-e", shell, rsyncPath(root), "--out-format=%n",
		root + "/", host + ":" + root + "/",
	}
}

func gitPushArgs(shell, host, root string) []string {
	return []string{
		"-rlpt", "-s", "--delete",
		"--exclude=/config", "--exclude=/FETCH_HEAD", "--exclude=/hooks/", "--exclude=/logs/",
		"--exclude=*.lock", "--exclude=/objects/tmp_*",
		"-e", shell, rsyncPath(root),
		root + "/.git/", host + ":" + root + "/.git/",
	}
}

func pullArgs(shell, host, root, dest string) []string {
	return []string{
		"-lpt", "-s", "--safe-links", "-0", "--files-from=-",
		"-e", shell,
		host + ":" + root + "/", dest + "/",
	}
}

// runRsync runs rsync and returns its exit code with captured stdout and stderr.
// err is non-nil only when rsync could not run to a normal exit.
func runRsync(ctx context.Context, args []string, stdin []byte) (code int, stdout, stderr string, err error) {
	cmd := command(ctx, "rsync", args...)
	cmd.WaitDelay = rsyncWaitDelay
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ctx.Err() == nil {
		return ee.ExitCode(), out.String(), errBuf.String(), nil
	}
	if err != nil {
		return -1, "", errBuf.String(), err
	}
	return 0, out.String(), errBuf.String(), nil
}

// command builds a child in its own process group that receives SIGTERM, as a group,
// when ctx is cancelled.
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return cmd
}
