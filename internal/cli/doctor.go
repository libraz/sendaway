package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/libraz/sendaway/internal/config"
	"github.com/libraz/sendaway/internal/proto"
	"github.com/libraz/sendaway/internal/run"
	"github.com/libraz/sendaway/internal/sshx"
)

const (
	// sshProbeTimeout bounds each ssh probe of a host.
	sshProbeTimeout = 30 * time.Second
	// maxClockSkew is the host clock difference reported as a problem.
	maxClockSkew = 2 * time.Second
)

// finding is one line of the doctor report.
type finding struct {
	ok   bool
	text string
}

func (f finding) String() string {
	if f.ok {
		return "ok    " + f.text
	}
	return "FAIL  " + f.text
}

// pathHead reports whether the first executable called name on pathList lives
// in shimDir.
func pathHead(name, shimDir, pathList string) finding {
	shimInfo, err := os.Stat(shimDir)
	if err != nil {
		return finding{false, fmt.Sprintf("%s: shim directory %s: %v", name, shimDir, err)}
	}
	for _, entry := range filepath.SplitList(pathList) {
		if entry == "" {
			entry = "."
		}
		cand := filepath.Join(entry, name)
		fi, err := os.Stat(cand) //nolint:gosec // PATH entries are probed by design
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		if di, err := os.Stat(entry); err == nil && os.SameFile(di, shimInfo) { //nolint:gosec // PATH entries are probed by design
			return finding{true, name + ": the shim comes first on PATH"}
		}
		return finding{false, fmt.Sprintf("%s: %s comes before the shim; put %s first in PATH", name, cand, shimDir)}
	}
	return finding{false, fmt.Sprintf("%s: not found on PATH; add %s to PATH", name, shimDir)}
}

// rsyncFinding turns the result of sshx.DetectRsync into a finding.
func rsyncFinding(v sshx.RsyncVersion, err error) finding {
	if err != nil {
		return finding{false, err.Error()}
	}
	return finding{true, fmt.Sprintf("rsync %s (protocol %d)", v.Version, v.Protocol)}
}

// cmFinding checks that the control socket directory is private.
func cmFinding(stateDir string) finding {
	dir := filepath.Join(stateDir, "cm")
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return finding{true, dir + " does not exist yet"}
	}
	if err != nil {
		return finding{false, err.Error()}
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		return finding{false, fmt.Sprintf("%s must be a directory with mode 0700 (is %s)", dir, fi.Mode())}
	}
	return finding{true, dir + " is private (0700)"}
}

// silentFinding checks that `ssh <host> true` printed nothing on stdout.
func silentFinding(host, stdout string) finding {
	if stdout == "" {
		return finding{true, host + ": ssh prints nothing on stdout"}
	}
	return finding{false, fmt.Sprintf("%s: ssh true printed %q on stdout; rc or profile output corrupts the protocol", host, truncate(stdout, 60))}
}

// connectHint explains how to recover from an ssh failure, or returns "".
func connectHint(host, stderr string) string {
	if strings.Contains(stderr, "Host key verification failed") {
		return fmt.Sprintf("run 'ssh %s' by hand once to add the host to known_hosts", host)
	}
	return ""
}

// skewFinding compares the host clock (unix seconds) with the local one.
func skewFinding(host string, remoteUnix int64, now time.Time) finding {
	skew := time.Duration(math.Abs(float64(now.Unix()-remoteUnix))) * time.Second
	if skew > maxClockSkew {
		return finding{false, fmt.Sprintf("%s: clock differs by %s", host, skew)}
	}
	return finding{true, fmt.Sprintf("%s: clock within %s", host, maxClockSkew)}
}

// versionFinding checks the output of `sendaway version` on the host.
func versionFinding(host, out string) finding {
	var version string
	protocol := -1
	for l := range strings.Lines(out) {
		f := strings.Fields(l)
		switch {
		case len(f) == 2 && f[0] == commandName:
			version = f[1]
		case len(f) == 2 && f[0] == "protocol":
			protocol, _ = strconv.Atoi(f[1])
		}
	}
	switch {
	case version == "":
		return finding{false, fmt.Sprintf("%s: sendaway not found or unreadable (%q); install it where a non-login ssh finds it", host, truncate(strings.TrimSpace(out), 60))}
	case protocol != proto.Protocol:
		return finding{false, fmt.Sprintf("%s: sendaway %s speaks protocol %d, this one speaks %d", host, version, protocol, proto.Protocol)}
	}
	return finding{true, fmt.Sprintf("%s: sendaway %s (protocol %d)", host, version, protocol)}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func doctorCmd(out io.Writer) int {
	var findings []finding
	add := func(f finding) {
		findings = append(findings, f)
		_, _ = fmt.Fprintln(out, f)
	}
	section := func(s string) { _, _ = fmt.Fprintf(out, "\n%s\n", s) }

	shimDir, err := shimDirPath()
	if err != nil {
		return fail(os.Stderr, err)
	}
	section("shims (" + shimDir + ")")
	names := shimNames(shimDir)
	if len(names) == 0 {
		add(finding{false, "no shims installed; run 'sendaway install-shims'"})
	}
	for _, n := range names {
		add(pathHead(n, shimDir, os.Getenv("PATH")))
	}

	section("local tools")
	ctx, cancel := context.WithTimeout(context.Background(), sshProbeTimeout)
	v, rerr := sshx.DetectRsync(ctx)
	cancel()
	add(rsyncFinding(v, rerr))
	state, err := stateDir()
	if err != nil {
		return fail(os.Stderr, err)
	}
	add(cmFinding(state))

	userPath, err := config.DefaultUserPath()
	if err != nil {
		return fail(os.Stderr, err)
	}
	user, err := config.LoadUser(userPath)
	if err != nil {
		return fail(os.Stderr, err)
	}
	hosts := hostsOf(user)
	if len(hosts) == 0 {
		section("hosts")
		add(finding{false, "no host configured in " + userPath})
	}
	for _, h := range hosts {
		section("host " + h)
		client := &sshx.Client{Host: h, SSHConfig: user.SSHConfig, StateDir: state}
		for _, f := range checkHost(client, names) {
			add(f)
		}
	}

	if slices.ContainsFunc(findings, func(f finding) bool { return !f.ok }) {
		return run.ExitFailure
	}
	return 0
}

// shimNames lists the symlinks in dir.
func shimNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			names = append(names, e.Name())
		}
	}
	return names
}

// hostsOf lists the distinct hosts of the user configuration.
func hostsOf(u *config.User) []string {
	var hosts []string
	add := func(h string) {
		if h != "" && !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	add(u.Host)
	for _, r := range u.Routes {
		add(r.Host)
	}
	return hosts
}

// ssh runs a command on the client's host and returns stdout and stderr.
func ssh(c *sshx.Client, command ...string) (stdout, stderr string, err error) {
	args, err := c.SSHArgs()
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), sshProbeTimeout)
	defer cancel()
	var o, e strings.Builder
	cmd := exec.CommandContext(ctx, "ssh", append(args, command...)...) //nolint:gosec // arguments come from sendaway's own probes
	cmd.Stdout, cmd.Stderr = &o, &e
	err = cmd.Run()
	return o.String(), e.String(), err
}

// checkHost probes one host with ssh.
func checkHost(c *sshx.Client, shims []string) []finding {
	out, errOut, err := ssh(c, "true")
	if err != nil {
		text := fmt.Sprintf("%s: ssh failed: %v: %s", c.Host, err, strings.TrimSpace(errOut))
		if hint := connectHint(c.Host, errOut); hint != "" {
			text += " -- " + hint
		}
		return []finding{{false, text}}
	}
	fs := []finding{{true, c.Host + ": reachable"}, silentFinding(c.Host, out)}

	out, _, err = ssh(c, "sendaway version")
	fs = append(fs, versionFinding(c.Host, resultOr(out, err)))

	out, _, err = ssh(c, "date +%s")
	if secs, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil && perr == nil {
		fs = append(fs, skewFinding(c.Host, secs, time.Now()))
	} else {
		fs = append(fs, finding{false, c.Host + ": cannot read the clock"})
	}

	if len(shims) > 0 {
		script := `for c in ` + strings.Join(shims, " ") + `; do command -v "$c" || echo "$c: not found"; done`
		out, _, err = ssh(c, `"$SHELL" -lc `+sshx.ShellQuote(script))
		if err != nil {
			fs = append(fs, finding{false, c.Host + ": login shell probe failed: " + err.Error()})
		} else {
			for l := range strings.Lines(out) {
				l = strings.TrimSpace(l)
				fs = append(fs, finding{!strings.HasSuffix(l, ": not found"), c.Host + ": " + l})
			}
		}
	}
	return fs
}

func resultOr(out string, err error) string {
	if err != nil {
		return ""
	}
	return out
}
