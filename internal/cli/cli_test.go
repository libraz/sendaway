package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/libraz/sendaway/internal/config"
	"github.com/libraz/sendaway/internal/sshx"
)

// touch creates each name (and its parents) under dir.
func touch(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func resolvedTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestInitDetects(t *testing.T) {
	tests := []struct {
		name    string
		files   []string
		remote  [][]string
		local   [][]string
		prepare []prepareRule
	}{
		{name: "empty"},
		{
			name:   "makefile and cmake",
			files:  []string{"Makefile", "CMakeLists.txt"},
			remote: [][]string{{"make"}, {"cmake"}, {"ctest"}},
			local:  [][]string{{"make", "format"}, {"make", "fmt"}, {"make", "format-check"}, {"make", "lint"}},
		},
		{
			name:   "go",
			files:  []string{"go.mod"},
			remote: [][]string{{"go", "test"}, {"go", "build"}, {"go", "vet"}},
		},
		{
			name:   "package.json alone is not enough",
			files:  []string{"package.json"},
			remote: nil,
		},
		{
			name:    "yarn",
			files:   []string{"package.json", "yarn.lock"},
			remote:  [][]string{{"yarn", "test"}, {"yarn", "build"}},
			local:   [][]string{{"yarn", "install"}, {"yarn", "add"}, {"yarn", "remove"}, {"yarn", "dev"}, {"yarn", "lint"}, {"yarn", "format"}},
			prepare: []prepareRule{{[]string{"yarn.lock", "package.json"}, []string{"yarn", "install", "--immutable"}}},
		},
		{
			name:    "pnpm",
			files:   []string{"package.json", "pnpm-lock.yaml"},
			remote:  [][]string{{"pnpm", "test"}, {"pnpm", "build"}},
			local:   [][]string{{"pnpm", "install"}, {"pnpm", "add"}, {"pnpm", "remove"}, {"pnpm", "dev"}},
			prepare: []prepareRule{{[]string{"pnpm-lock.yaml", "package.json"}, []string{"pnpm", "install", "--frozen-lockfile"}}},
		},
		{
			name:    "bun",
			files:   []string{"package.json", "bun.lock"},
			remote:  [][]string{{"bun", "test"}, {"bun", "run", "build"}},
			local:   [][]string{{"bun", "install"}, {"bun", "add"}, {"bun", "remove"}, {"bun", "run", "dev"}},
			prepare: []prepareRule{{[]string{"bun.lock", "package.json"}, []string{"bun", "install", "--frozen-lockfile"}}},
		},
		{
			name:   "cargo",
			files:  []string{"Cargo.toml"},
			remote: [][]string{{"cargo", "test"}, {"cargo", "build"}},
			local:  [][]string{{"cargo", "fmt"}, {"cargo", "add"}},
		},
		{
			name:  "mise",
			files: []string{"mise.toml"},
			prepare: []prepareRule{
				{[]string{"mise.toml"}, []string{"mise", "trust", "--yes"}},
				{[]string{"mise.toml"}, []string{"mise", "install"}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := resolvedTemp(t)
			touch(t, dir, tc.files...)
			d := detect(dir)
			if !reflect.DeepEqual(d, detection{remote: tc.remote, local: tc.local, prepare: tc.prepare}) {
				t.Fatalf("detect = %+v", d)
			}
			cfg, err := config.ParseProject("rendered", []byte(d.render()))
			if err != nil {
				t.Fatalf("rendered file does not parse: %v\n%s", err, d.render())
			}
			if !reflect.DeepEqual(cfg.Remote, tc.remote) || !reflect.DeepEqual(cfg.Local, tc.local) || len(cfg.Prepare) != len(tc.prepare) {
				t.Fatalf("round trip lost rules: %+v", cfg)
			}
			for i, p := range tc.prepare {
				if !slices.Equal(cfg.Prepare[i].When, p.when) || !slices.Equal(cfg.Prepare[i].Run, p.run) {
					t.Fatalf("prepare[%d] = %+v, want %+v", i, cfg.Prepare[i], p)
				}
			}
		})
	}

	t.Run("refuses to overwrite", func(t *testing.T) {
		dir := resolvedTemp(t)
		touch(t, dir, "Makefile")
		var out bytes.Buffer
		if err := initIn(&out, dir); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(filepath.Join(dir, config.ProjectFile))
		if err := initIn(&out, dir); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("second init: %v", err)
		}
		after, _ := os.ReadFile(filepath.Join(dir, config.ProjectFile))
		if !bytes.Equal(before, after) {
			t.Fatal("existing file was modified")
		}
	})
}

func TestWhich(t *testing.T) {
	root := resolvedTemp(t)
	home := filepath.Join(root, "home")
	repo := filepath.Join(home, "proj")
	sub := filepath.Join(repo, "sub")
	for _, d := range []string{sub, filepath.Join(root, "bare")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	yml := "remote:\n  - [make]\nlocal:\n  - [make, format]\nenv: [MYAPP_*, MY_TOKEN]\n"
	if err := os.WriteFile(filepath.Join(repo, config.ProjectFile), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SENDAWAY_PASSTHROUGH", "")
	user := &config.User{
		Host:   "default-host",
		Routes: []config.Route{{Path: repo, Host: "proj-host"}},
	}
	environ := []string{"CI=1", "MYAPP_MODE=fast", "MYAPP_PATH=/opt/homebrew/bin", "MY_TOKEN=s3cret", "HOME=" + home, "GITHUB_TOKEN=x"}

	run := func(cwd string, argv ...string) string {
		var out bytes.Buffer
		if err := which(&out, argv, cwd, user, environ, home); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	const tail = "(commands started by a local run stay local)\n"

	got := run(sub, "make", "test")
	for _, want := range []string{
		"decision: remote (matches remote rule [make])",
		"config:   " + filepath.Join(repo, config.ProjectFile),
		"host:     proj-host",
		"env forwarded: CI, MYAPP_MODE",
		"MY_TOKEN (secret-like name)",
		"MYAPP_PATH (Mac-specific value)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "GITHUB_TOKEN") || strings.Contains(got, "s3cret") {
		t.Errorf("unrequested or secret value shown:\n%s", got)
	}
	if !strings.HasSuffix(got, tail) {
		t.Errorf("missing closing note:\n%s", got)
	}

	if got := run(sub, "make", "format"); !strings.Contains(got, "decision: local (matches local rule [make, format])") {
		t.Errorf("local rule:\n%s", got)
	}
	if got := run(sub, "ls"); !strings.Contains(got, "decision: local (no rule matches)") {
		t.Errorf("no rule:\n%s", got)
	}
	got = run(filepath.Join(root, "bare"), "make")
	if !strings.Contains(got, "decision: local (.sendaway.yml not found") || strings.Contains(got, "host:") || !strings.HasSuffix(got, tail) {
		t.Errorf("no config:\n%s", got)
	}

	t.Setenv("SENDAWAY_PASSTHROUGH", "1")
	if got := run(sub, "make"); !strings.Contains(got, "decision: local (SENDAWAY_PASSTHROUGH is set)") {
		t.Errorf("passthrough:\n%s", got)
	}
}

func TestDoctorPathHead(t *testing.T) {
	root := resolvedTemp(t)
	shimDir := filepath.Join(root, "shims")
	other := filepath.Join(root, "other")
	for _, d := range []string{shimDir, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{filepath.Join(shimDir, "make"), filepath.Join(other, "make"), filepath.Join(other, "go")} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	list := func(dirs ...string) string { return strings.Join(dirs, string(os.PathListSeparator)) }

	if f := pathHead("make", shimDir, list(shimDir, other)); !f.ok {
		t.Errorf("shim first: %v", f)
	}
	if f := pathHead("make", shimDir, list(root, shimDir, other)); !f.ok {
		t.Errorf("shim first hit after a dir without the command: %v", f)
	}
	f := pathHead("make", shimDir, list(other, shimDir))
	if f.ok || !strings.Contains(f.text, filepath.Join(other, "make")) {
		t.Errorf("shim second: %v", f)
	}
	if f := pathHead("make", shimDir, list(root)); f.ok || !strings.Contains(f.text, "add "+shimDir) {
		t.Errorf("shim absent from PATH: %v", f)
	}
	if f := pathHead("make", shimDir, list(other, "/nonexistent")); f.ok {
		t.Errorf("unexpected ok: %v", f)
	}

	state := t.TempDir()
	if f := cmFinding(state); !f.ok {
		t.Errorf("no cm dir: %v", f)
	}
	cm := filepath.Join(state, "cm")
	if err := os.Mkdir(cm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cm, 0o755); err != nil { // Mkdir is subject to the umask
		t.Fatal(err)
	}
	if f := cmFinding(state); f.ok {
		t.Errorf("cm mode 0755 accepted: %v", f)
	}
	if err := os.Chmod(cm, 0o700); err != nil {
		t.Fatal(err)
	}
	if f := cmFinding(state); !f.ok {
		t.Errorf("cm mode 0700: %v", f)
	}

	v, err := sshx.ParseRsyncVersion("rsync  version 3.5.1  protocol version 33\n")
	if f := rsyncFinding(v, err); !f.ok || !strings.Contains(f.text, "3.5.1") {
		t.Errorf("rsync 3.5.1: %v", f)
	}
	v, err = sshx.ParseRsyncVersion("openrsync: protocol version 29\nrsync version 2.6.9 compatible\n")
	if f := rsyncFinding(v, err); f.ok {
		t.Errorf("openrsync accepted: %v", f)
	}

	if f := silentFinding("h", ""); !f.ok {
		t.Errorf("silent stdout: %v", f)
	}
	if f := silentFinding("h", "Welcome!\n"); f.ok {
		t.Errorf("noisy stdout accepted: %v", f)
	}
	if h := connectHint("h", "Host key verification failed."); !strings.Contains(h, "ssh h") {
		t.Errorf("hint = %q", h)
	}
	if h := connectHint("h", "Permission denied"); h != "" {
		t.Errorf("hint = %q", h)
	}
	now := time.Unix(1000, 0)
	if f := skewFinding("h", 1001, now); !f.ok {
		t.Errorf("1s skew: %v", f)
	}
	if f := skewFinding("h", 1010, now); f.ok {
		t.Errorf("10s skew accepted: %v", f)
	}
	if f := versionFinding("h", "sendaway 1.2.3\nprotocol 1\n"); !f.ok {
		t.Errorf("version: %v", f)
	}
	if f := versionFinding("h", "sendaway 1.2.3\nprotocol 2\n"); f.ok {
		t.Errorf("protocol mismatch accepted: %v", f)
	}
	if f := versionFinding("h", "bash: sendaway: command not found"); f.ok {
		t.Errorf("missing sendaway accepted: %v", f)
	}
}

func TestShellArgv(t *testing.T) {
	state, err := os.MkdirTemp("/tmp", "sa-") //nolint:usetesting // t.TempDir exceeds the ssh control path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	c := &sshx.Client{Host: "h", StateDir: state}
	argv, err := shellArgv(c, "/Users/a b/it's")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "BatchMode") || slices.Contains(argv, "-T") || !slices.Contains(argv, "-t") {
		t.Errorf("argv = %q", argv)
	}
	if last := argv[len(argv)-1]; last != `cd '/Users/a b/it'"'"'s' && exec "$SHELL" -l` {
		t.Errorf("remote command = %q", last)
	}
}

func TestShimOverheadNoMatch(t *testing.T) {
	if raceEnabled {
		t.Skip("startup time is measured without -race; see make test")
	}
	const runs = 50
	if _, err := os.Stat("/usr/bin/true"); err != nil {
		t.Skip("/usr/bin/true not available")
	}
	tmp := resolvedTemp(t)
	bin := filepath.Join(tmp, "sendaway")
	build := exec.Command("go", "build", "-o", bin, "./cmd/sendaway")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	home := filepath.Join(tmp, "home")
	work := filepath.Join(home, "work")
	shimDir := filepath.Join(tmp, "shims")
	for _, d := range []string{work, shimDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	yml := "remote:\n  - [make]\n  - [cmake, --build]\nlocal:\n  - [make, format]\n"
	if err := os.WriteFile(filepath.Join(work, config.ProjectFile), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(shimDir, "true")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	shimmedEnv := []string{"HOME=" + home, "PATH=" + shimDir + string(os.PathListSeparator) + "/usr/bin:/bin"}
	directEnv := []string{"HOME=" + home, "PATH=/usr/bin:/bin"}

	timeRun := func(name string, env []string) time.Duration {
		cmd := exec.Command(name)
		cmd.Dir = work
		cmd.Env = env
		start := time.Now()
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return time.Since(start)
	}
	for range 3 {
		timeRun(link, shimmedEnv)
		timeRun("/usr/bin/true", directEnv)
	}
	shimmed := make([]time.Duration, 0, runs)
	direct := make([]time.Duration, 0, runs)
	for range runs {
		shimmed = append(shimmed, timeRun(link, shimmedEnv))
		direct = append(direct, timeRun("/usr/bin/true", directEnv))
	}
	diff := p50(shimmed) - p50(direct)
	t.Logf("shim p50=%v direct p50=%v diff=%v", p50(shimmed), p50(direct), diff)
	if diff >= 15*time.Millisecond {
		t.Fatalf("overhead %v >= 15ms", diff)
	}
}

func p50(d []time.Duration) time.Duration {
	s := slices.Clone(d)
	slices.Sort(s)
	return s[len(s)/2]
}
