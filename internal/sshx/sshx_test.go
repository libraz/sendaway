package sshx

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func shortStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sa-") //nolint:usetesting // t.TempDir() overflows the 86-byte macOS ssh ControlPath limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestSSHArgs(t *testing.T) {
	want := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=/s/cm/abc",
		"-o", "ControlPersist=10m",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=3",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "ClearAllForwardings=yes",
		"-o", "GSSAPIDelegateCredentials=no",
	}
	if got := SSHOptions("/s/cm/abc", ""); !reflect.DeepEqual(got, want) {
		t.Errorf("options without -F:\n got %q\nwant %q", got, want)
	}
	if got := SSHOptions("/s/cm/abc", "/c/ssh config"); !reflect.DeepEqual(got, append(slices.Clone(want), "-F", "/c/ssh config")) {
		t.Errorf("options with -F: %q", got)
	}

	c := &Client{Host: "build01", SSHConfig: "/c/cfg", StateDir: shortStateDir(t)}
	args, err := c.SSHArgs()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(args); args[n-1] != "build01" || args[n-2] != "/c/cfg" || args[n-3] != "-F" {
		t.Errorf("host must be last, after -F, with no --: %q", args)
	}
	for _, a := range args {
		if a == "--" {
			t.Errorf("unexpected -- in %q", args)
		}
	}

	shell, err := rsyncShellFor(SSHOptions("/s/cm/abc", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(shell, `ssh "-T" "-o" "BatchMode=yes"`) {
		t.Errorf("rsync -e shell: %s", shell)
	}
	if !strings.Contains(shell, `"ControlPath=/s/cm/abc"`) {
		t.Errorf("rsync -e shell must quote each option: %s", shell)
	}
	if _, err := rsyncShellFor([]string{`a"b`}); err == nil {
		t.Error("option with a double quote must be rejected")
	}

	root := "/Users/me/it's"
	body := bodyPushArgs("SHELL", "h", root)
	wantBody := []string{
		"-rlpt", "-s", "-8", "--exclude=/.git", "-0", "--exclude-from=-",
		"-e", "SHELL", `--rsync-path=mkdir -p '/Users/me/it'"'"'s' && rsync`, "--out-format=%n",
		root + "/", "h:" + root + "/",
	}
	if !reflect.DeepEqual(body, wantBody) {
		t.Errorf("body args:\n got %q\nwant %q", body, wantBody)
	}
	git := gitPushArgs("SHELL", "h", "/r")
	if git[len(git)-2] != "/r/.git/" || git[len(git)-1] != "h:/r/.git/" || !contains(git, "--delete") || !contains(git, "--exclude=/objects/tmp_*") {
		t.Errorf("git args: %q", git)
	}
	pull := pullArgs("SHELL", "h", "/r", "/d")
	if !contains(pull, "--safe-links") || !contains(pull, "--files-from=-") || pull[len(pull)-2] != "h:/r/" || pull[len(pull)-1] != "/d/" {
		t.Errorf("pull args: %q", pull)
	}
}

func contains(s []string, v string) bool {
	return slices.Contains(s, v)
}

func TestShellQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", `''`},
		{"/Users/me/proj", `'/Users/me/proj'`},
		{"a b", `'a b'`},
		{"it's", `'it'"'"'s'`},
		{"$HOME `x` \"y\" \\", "'$HOME `x` \"y\" \\'"},
		{"'", `''"'"''`},
		{"a\nb", "'a\nb'"},
	}
	for _, tt := range tests {
		if got := ShellQuote(tt.in); got != tt.want {
			t.Errorf("ShellQuote(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestRsyncVersion(t *testing.T) {
	gnu := "rsync  version 3.5.1  protocol version 33\nCopyright (C) 1996-2025 by Andrew Tridgell, Wayne Davison, and others.\n"
	v, err := ParseRsyncVersion(gnu)
	if err != nil || v.Version != "3.5.1" || v.Protocol != 33 {
		t.Errorf("gnu: %+v, %v", v, err)
	}
	v, err = ParseRsyncVersion("rsync  version 3.1.0  protocol version 31\n")
	if err != nil || v.Protocol != 31 {
		t.Errorf("boundary 31: %+v, %v", v, err)
	}
	open := "openrsync: protocol version 29\nrsync version 2.6.9 compatible\n"
	v, err = ParseRsyncVersion(open)
	if !errors.Is(err, ErrRsyncTooOld) || v.Protocol != 29 || v.Version != "2.6.9" {
		t.Errorf("openrsync: %+v, %v", v, err)
	}
	if _, err := ParseRsyncVersion("rsync  version 2.6.9  protocol version 30\n"); !errors.Is(err, ErrRsyncTooOld) {
		t.Errorf("protocol 30 must be rejected: %v", err)
	}
	if _, err := ParseRsyncVersion("command not found"); err == nil || errors.Is(err, ErrRsyncTooOld) {
		t.Errorf("garbage: %v", err)
	}
}

func TestClassifyConnectError(t *testing.T) {
	tests := []struct {
		stderr   string
		fallback bool
	}{
		{"ssh: Could not resolve hostname build01: nodename nor servname provided, or not known", true},
		{"ssh: connect to host 10.0.0.1 port 22: Connection timed out", true},
		{"ssh: connect to host 10.0.0.1 port 22: Operation timed out", true},
		{"ssh: connect to host 10.0.0.1 port 22: No route to host", true},
		{"ssh: connect to host 10.0.0.1 port 22: Connection refused", true},
		{"ssh: connect to host 10.0.0.1 port 22: Network is unreachable", true},
		{"me@host: Permission denied (publickey).", false},
		{"Host key verification failed.", false},
		{"@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@", false},
		{"bash: sendaway: command not found", false},
		{"Permission denied (publickey).\nconnect to host x port 22: Connection refused", false},
		{"", false},
	}
	for _, tt := range tests {
		reason, got := ClassifyConnectError(tt.stderr)
		if got != tt.fallback {
			t.Errorf("ClassifyConnectError(%q) fallback = %v, want %v", tt.stderr, got, tt.fallback)
		}
		if got && reason == "" || !got && reason != "" {
			t.Errorf("ClassifyConnectError(%q) reason = %q with fallback %v", tt.stderr, reason, got)
		}
	}
	if reason, _ := ClassifyConnectError("ssh: connect to host h port 22: Connection refused"); reason != "Connection refused" {
		t.Errorf("reason = %q", reason)
	}
}

func TestControlPath(t *testing.T) {
	state := shortStateDir(t)
	p, err := ControlPath(state, "build01")
	if err != nil {
		t.Fatal(err)
	}
	if dir, base := filepath.Dir(p), filepath.Base(p); dir != filepath.Join(state, "cm") || len(base) != 16 {
		t.Errorf("path = %s", p)
	}
	if fi, err := os.Stat(filepath.Join(state, "cm")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("cm dir: %v, %v", fi, err)
	}
	if p2, err := ControlPath(state, "build01"); err != nil || p2 != p {
		t.Errorf("second call: %s, %v", p2, err)
	}
	if p3, err := ControlPath(state, "build02"); err != nil || p3 == p {
		t.Errorf("distinct hosts must differ: %s, %v", p3, err)
	}

	if err := os.Chmod(filepath.Join(state, "cm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ControlPath(state, "build01"); err == nil {
		t.Error("cm with mode 0755 must be rejected")
	}

	long := filepath.Join(shortStateDir(t), strings.Repeat("x", 80))
	if _, err := ControlPath(long, "build01"); err == nil {
		t.Error("over-long ControlPath must be rejected")
	}
	if _, err := os.Stat(long); !os.IsNotExist(err) {
		t.Errorf("nothing should be created on a length failure: %v", err)
	}

	// Exactly at the limit is accepted: "/cm/" plus 16 hex digits is 20 bytes.
	atLimit := "/tmp/" + strings.Repeat("y", maxControlPath-20-len("/tmp/"))
	t.Cleanup(func() { _ = os.RemoveAll(atLimit) })
	if p, err := ControlPath(atLimit, "h"); err != nil || len(p) != maxControlPath {
		t.Errorf("at limit: len %d, %v", len(p), err)
	}
}

func TestRsyncExcludeEscape(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/plain/dir/", "/plain/dir/"},
		{"/secrets[old]/", `/secrets\[old\]/`},
		{"/a*b?c", `/a\*b\?c`},
		{`/back\slash`, `/back\\slash`},
		{`/\*[]?`, `/\\\*\[\]\?`},
		{"/日本語 name", "/日本語 name"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := EscapeExclude(tt.in); got != tt.want {
			t.Errorf("EscapeExclude(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
