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

// finding is one line of the doctor report. A warning is ok: it is shown but
// does not fail doctor.
type finding struct {
	ok   bool
	warn bool
	text string
}

func (f finding) String() string {
	switch {
	case f.warn:
		return "warn  " + f.text
	case f.ok:
		return "ok    " + f.text
	}
	return "FAIL  " + f.text
}

// pathHead reports whether the first executable called name on pathList lives
// in shimDir.
func pathHead(name, shimDir, pathList string) finding {
	shimInfo, err := os.Stat(shimDir)
	if err != nil {
		return finding{text: fmt.Sprintf("%s: shim directory %s: %v", name, shimDir, err)}
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
			return finding{ok: true, text: name + ": the shim comes first on PATH"}
		}
		return finding{text: fmt.Sprintf("%s: %s comes before the shim; put %s first in PATH", name, cand, shimDir)}
	}
	return finding{text: fmt.Sprintf("%s: not found on PATH; add %s to PATH", name, shimDir)}
}

// rsyncFinding turns the result of sshx.DetectRsync into a finding.
func rsyncFinding(v sshx.RsyncVersion, err error) finding {
	if err != nil {
		return finding{text: err.Error()}
	}
	return finding{ok: true, text: fmt.Sprintf("rsync %s (protocol %d)", v.Version, v.Protocol)}
}

// cmFinding checks that the control socket directory is private.
func cmFinding(stateDir string) finding {
	dir := filepath.Join(stateDir, "cm")
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return finding{ok: true, text: dir + " does not exist yet"}
	}
	if err != nil {
		return finding{text: err.Error()}
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		return finding{text: fmt.Sprintf("%s must be a directory with mode 0700 (is %s)", dir, fi.Mode())}
	}
	return finding{ok: true, text: dir + " is private (0700)"}
}

// silentFinding checks that `ssh <host> true` printed nothing on stdout.
func silentFinding(host, stdout string) finding {
	if stdout == "" {
		return finding{ok: true, text: host + ": ssh prints nothing on stdout"}
	}
	return finding{text: fmt.Sprintf("%s: ssh true printed %q on stdout; rc or profile output corrupts the protocol", host, truncate(stdout, 60))}
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
		return finding{text: fmt.Sprintf("%s: clock differs by %s", host, skew)}
	}
	return finding{ok: true, text: fmt.Sprintf("%s: clock within %s", host, maxClockSkew)}
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
		return finding{text: fmt.Sprintf("%s: sendaway not found or unreadable (%q); install it where a non-login ssh finds it", host, truncate(strings.TrimSpace(out), 60))}
	case protocol != proto.Protocol:
		return finding{text: fmt.Sprintf("%s: sendaway %s speaks protocol %d, this one speaks %d", host, version, protocol, proto.Protocol)}
	}
	return finding{ok: true, text: fmt.Sprintf("%s: sendaway %s (protocol %d)", host, version, protocol)}
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
		add(finding{text: "no shims installed; run 'sendaway install-shims'"})
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
		add(finding{text: "no host configured in " + userPath})
	}
	for _, h := range hosts {
		section("host " + h)
		client := &sshx.Client{Host: h, SSHConfig: user.SSHConfig, StateDir: state}
		for _, f := range checkHost(client, names) {
			add(f)
		}
	}

	if cwd, err := os.Getwd(); err == nil {
		if proj, err := loadProject(cwd); err == nil {
			if h := user.HostFor(proj.root); h != "" {
				section("project disk on " + h)
				client := &sshx.Client{Host: h, SSHConfig: user.SSHConfig, StateDir: state}
				if slot, err := selectSlot(os.Getenv(slotEnv), proj.cfg.Slots); err != nil {
					add(finding{text: err.Error()})
				} else {
					add(checkDisk(client, slotRoot(proj.root, slot), user.MinFree))
				}
			}
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
		return []finding{{text: text}}
	}
	fs := []finding{{ok: true, text: c.Host + ": reachable"}, silentFinding(c.Host, out)}

	out, _, err = ssh(c, "sendaway version")
	fs = append(fs, versionFinding(c.Host, resultOr(out, err)))

	out, _, err = ssh(c, "date +%s")
	if secs, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil && perr == nil {
		fs = append(fs, skewFinding(c.Host, secs, time.Now()))
	} else {
		fs = append(fs, finding{text: c.Host + ": cannot read the clock"})
	}

	if len(shims) > 0 {
		script := `for c in ` + strings.Join(shims, " ") + `; do command -v "$c" || echo "$c: not found"; done`
		out, _, err = ssh(c, `"$SHELL" -lc `+sshx.ShellQuote(script))
		if err != nil {
			fs = append(fs, finding{text: c.Host + ": login shell probe failed: " + err.Error()})
		} else {
			for l := range strings.Lines(out) {
				l = strings.TrimSpace(l)
				fs = append(fs, toolFinding(c.Host, l))
			}
		}
	}
	return fs
}

// toolFinding turns one line of the login-shell probe into a finding. A shim
// whose tool the host lacks is a warning: a host serves only the projects
// routed to it, and those may never send that command.
func toolFinding(host, line string) finding {
	if name, ok := strings.CutSuffix(line, ": not found"); ok {
		return finding{ok: true, warn: true, text: fmt.Sprintf("%s: %s: not found; a run that sends %s here fails", host, name, name)}
	}
	return finding{ok: true, text: host + ": " + line}
}

// diskScript prints the nearest existing ancestor of $1, then df -Pk for it.
const diskScript = `p=$1; while [ ! -e "$p" ] && [ "$p" != / ]; do p=$(dirname "$p"); done; echo "$p"; df -Pk "$p"`

// checkDisk reports the free space on the host where root lives or would be created.
func checkDisk(c *sshx.Client, root string, minFree int64) finding {
	out, errOut, err := ssh(c, "sh -c "+sshx.ShellQuote(diskScript)+" sh "+sshx.ShellQuote(root))
	if err != nil {
		return finding{text: fmt.Sprintf("%s: cannot read free space: %v: %s", c.Host, err, strings.TrimSpace(errOut))}
	}
	return diskFinding(c.Host, minFree, out)
}

// diskFinding turns the output of diskScript into a finding; free space below
// minFree is a warning.
func diskFinding(host string, minFree int64, out string) finding {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var avail int64 = -1
	if len(lines) == 3 {
		if f := strings.Fields(lines[2]); len(f) >= 6 {
			if kib, err := strconv.ParseInt(f[3], 10, 64); err == nil && kib >= 0 && kib <= math.MaxInt64>>10 {
				avail = kib << 10
			}
		}
	}
	if avail < 0 {
		return finding{text: fmt.Sprintf("%s: cannot read free space (%q)", host, truncate(strings.TrimSpace(out), 60))}
	}
	limit := "off"
	if minFree > 0 {
		limit = config.FormatSize(minFree)
	}
	return finding{
		ok: true, warn: avail < minFree,
		text: fmt.Sprintf("%s: %s free at %s (min_free %s)", host, config.FormatSize(avail), lines[0], limit),
	}
}

func resultOr(out string, err error) string {
	if err != nil {
		return ""
	}
	return out
}
