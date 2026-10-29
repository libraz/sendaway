package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/libraz/sendaway/internal/config"
	"github.com/libraz/sendaway/internal/envfilter"
	"github.com/libraz/sendaway/internal/match"
	"github.com/libraz/sendaway/internal/shim"
)

func whichCmd(out io.Writer, argv []string) error {
	if len(argv) == 0 {
		return usage("which needs a command")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	userPath, err := config.DefaultUserPath()
	if err != nil {
		return err
	}
	user, err := config.LoadUser(userPath)
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	return which(out, argv, cwd, user, os.Environ(), home)
}

// which explains where argv would run from cwd: the decision and the rule
// behind it, the host, and the variables forwarded or dropped.
func which(out io.Writer, argv []string, cwd string, user *config.User, environ []string, home string) error {
	line := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format+"\n", a...) }
	defer line("(commands started by a local run stay local)")

	line("command:  %s", strings.Join(argv, " "))
	if shim.Passthrough() {
		line("decision: local (%s is set)", shim.PassthroughEnv)
		return nil
	}
	proj, err := loadProject(cwd)
	if err != nil {
		if errors.Is(err, errNoProject) {
			line("decision: local (%v)", err)
			return nil
		}
		return err
	}
	cfg := proj.cfg
	switch {
	case match.Index(cfg.Local, argv) >= 0:
		line("decision: local (matches local rule %s)", joinWords(cfg.Local[match.Index(cfg.Local, argv)]))
	case match.Index(cfg.Remote, argv) >= 0:
		line("decision: remote (matches remote rule %s)", joinWords(cfg.Remote[match.Index(cfg.Remote, argv)]))
	default:
		line("decision: local (no rule matches)")
	}
	line("config:   %s", proj.file)
	if host := user.HostFor(proj.root); host != "" {
		line("host:     %s", host)
	} else {
		line("host:     (none configured)")
	}

	sel := envfilter.Select(environ, home, cfg.Env)
	names := make([]string, 0, len(sel.Env))
	for n := range sel.Env {
		names = append(names, n)
	}
	slices.Sort(names)
	line("env forwarded: %s", orNone(strings.Join(names, ", ")))
	dropped := make([]string, len(sel.Dropped))
	for i, d := range sel.Dropped {
		dropped[i] = fmt.Sprintf("%s (%s)", d.Name, d.Reason)
	}
	line("env dropped:   %s", orNone(strings.Join(dropped, ", ")))
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
