// Package cli is the sendaway entry point: it runs as a PATH shim when the
// binary is invoked under another name, and as the sendaway command otherwise.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/libraz/sendaway/internal/config"
	"github.com/libraz/sendaway/internal/proto"
	"github.com/libraz/sendaway/internal/run"
	"github.com/libraz/sendaway/internal/shim"
	"github.com/libraz/sendaway/internal/sshx"
)

// Version is set with -ldflags; left at "dev", the module version recorded
// by go install or go build is reported instead.
var Version = "dev"

// commandName is the basename under which the binary acts as the command
// rather than as a shim.
const commandName = "sendaway"

const helpText = `sendaway runs matched build and test commands on a remote Linux host.

Usage: sendaway <command> [arguments]

Commands:
  init                     write a .sendaway.yml for the project in the current directory
  install-shims [names...] link PATH shims and print the PATH line to add
  doctor                   check shims, rsync, the control directory and each remote host
  which <cmd...>           show where a command would run, the host and the forwarded variables
  run <cmd...>             run a command on the remote host, ignoring the rules
  local <cmd...>           run a command locally, ignoring the rules
  fetch <path...>          copy root-relative paths from the host into the latest run
  runs                     list recorded runs
  last                     summarize the latest run
  shell                    open a login shell in the project directory on the host
  forget                   remove the project's copy and state from the host
  version                  print the version and protocol
  help                     print this text
`

// Main runs the process for args (os.Args) and returns its exit code. Shim
// mode normally replaces the process and does not return.
func Main(args []string) int {
	if len(args) == 0 {
		return run.ExitFailure
	}
	if filepath.Base(args[0]) != commandName {
		return shimMain(args)
	}
	return command(args[1:], os.Stdout, os.Stderr)
}

func command(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, helpText)
		return run.ExitFailure
	}
	name, rest := args[0], args[1:]
	var err error
	switch name {
	case "help", "-h", "--help":
		_, _ = io.WriteString(stdout, helpText)
	case "version":
		_, _ = fmt.Fprintf(stdout, "sendaway %s\nprotocol %d\n", version(), proto.Protocol)
	case "init":
		err = initCmd(stdout, rest)
	case "install-shims":
		err = installShimsCmd(stdout, rest)
	case "doctor":
		return doctorCmd(stdout)
	case "which":
		err = whichCmd(stdout, rest)
	case "run":
		return runCmd(rest, stderr)
	case "local":
		return localCmd(rest)
	case "fetch":
		return fetchCmd(rest, stderr)
	case "forget":
		return forgetCmd(rest, stdout, stderr)
	case "shell":
		err = shellCmd(rest)
	case "runs":
		err = runsCmd(stdout, rest, false)
	case "last":
		err = runsCmd(stdout, rest, true)
	case "remote-exec":
		err = remoteExec()
	default:
		err = fmt.Errorf("unknown command %q (see 'sendaway help')", name)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return 0
}

// fail prints the reason as a sendaway line and returns the failure code.
func fail(w io.Writer, err error) int {
	_, _ = fmt.Fprintf(w, "[sendaway] %v\n", err)
	return run.ExitFailure
}

var errUsage = errors.New("usage error")

func usage(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{errUsage}, a...)...)
}

// stateDir returns ${XDG_STATE_HOME:-~/.local/state}/sendaway.
func stateDir() (string, error) { return xdgDir("XDG_STATE_HOME", ".local/state") }

// dataDir returns ${XDG_DATA_HOME:-~/.local/share}/sendaway.
func dataDir() (string, error) { return xdgDir("XDG_DATA_HOME", ".local/share") }

func xdgDir(env, fallback string) (string, error) {
	base := os.Getenv(env)
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, filepath.FromSlash(fallback))
	}
	return filepath.Join(base, commandName), nil
}

// shimDirPath is where install-shims puts its links.
func shimDirPath() (string, error) {
	d, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "bin"), nil
}

// project is the .sendaway.yml governing the current directory.
type project struct {
	file string
	root string
	cwd  string
	cfg  *config.Project
}

var errNoProject = errors.New(config.ProjectFile + " not found above the current directory")

// loadProject finds and validates the project configuration above cwd.
func loadProject(cwd string) (*project, error) {
	file, ok := shim.FindConfig(cwd)
	if !ok {
		return nil, errNoProject
	}
	cfg, err := config.LoadProject(file)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, err
	}
	return &project{file: file, root: filepath.Dir(file), cwd: resolved, cfg: cfg}, nil
}

// target is the host a project's commands run on, with its transport.
type target struct {
	host   string
	user   *config.User
	client *sshx.Client
	state  string
}

// resolveTarget picks the host for root from the user configuration.
func resolveTarget(root string) (*target, error) {
	path, err := config.DefaultUserPath()
	if err != nil {
		return nil, err
	}
	user, err := config.LoadUser(path)
	if err != nil {
		return nil, err
	}
	host := user.HostFor(root)
	if host == "" {
		return nil, fmt.Errorf("no host configured (set host: or a matching route in %s)", path)
	}
	state, err := stateDir()
	if err != nil {
		return nil, err
	}
	return &target{
		host: host, user: user, state: state,
		client: &sshx.Client{Host: host, SSHConfig: user.SSHConfig, StateDir: state},
	}, nil
}

func (t *target) params(p *project, stderr *os.File) run.Params {
	return run.Params{Root: p.root, Host: t.host, Version: version(), Stderr: stderr, Transport: t.client}
}

func joinWords(words []string) string { return "[" + strings.Join(words, ", ") + "]" }
