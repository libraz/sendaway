package shim

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const reexecEnv = "SHIM_TEST_REEXEC"

// TestMain lets the test binary act as a minimal shim when re-executed
// through a symlink with reexecEnv set.
func TestMain(m *testing.M) {
	if os.Getenv(reexecEnv) == "1" {
		reexecMain()
		os.Exit(125)
	}
	os.Exit(m.Run())
}

func reexecMain() {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	if _, ok := FindConfig(cwd); ok {
		return
	}
	exe, err := Resolve("true", filepath.Dir(os.Args[0]), os.Getenv("PATH"))
	if err != nil {
		return
	}
	_ = Exec(exe, []string{"true"}, os.Environ())
}

func writeExe(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestResolveSkipsShimDir(t *testing.T) {
	root := t.TempDir()
	shimDir := filepath.Join(root, "shims")
	realDir := filepath.Join(root, "real")
	for _, d := range []string{shimDir, realDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExe(t, filepath.Join(shimDir, "make"))
	writeExe(t, filepath.Join(realDir, "make"))

	// Reach the shim dir through a symlink to prove SameFile is used.
	link := filepath.Join(root, "link")
	if err := os.Symlink(shimDir, link); err != nil {
		t.Fatal(err)
	}
	pathList := link + string(os.PathListSeparator) + realDir
	got, err := Resolve("make", shimDir, pathList)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(realDir, "make"); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}

	if _, err := Resolve("make", shimDir, link); err == nil {
		t.Fatal("expected not found when only the shim dir is on PATH")
	}

	// A non-executable file is not a match.
	if err := os.WriteFile(filepath.Join(realDir, "plain"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("plain", shimDir, realDir); err == nil {
		t.Fatal("non-executable file must not match")
	}
}

func TestFindConfig(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	repo := filepath.Join(home, "repo")
	deep := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	if p, ok := FindConfig(deep); ok {
		t.Fatalf("unexpected config %s", p)
	}

	cfg := filepath.Join(repo, ConfigName)
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if p, ok := FindConfig(deep); !ok || p != cfg {
		t.Fatalf("got %q %v, want %q", p, ok, cfg)
	}

	// Symlinked cwd resolves to the real directory.
	link := filepath.Join(base, "link")
	if err := os.Symlink(deep, link); err != nil {
		t.Fatal(err)
	}
	if p, ok := FindConfig(link); !ok || p != cfg {
		t.Fatalf("via symlink: got %q %v, want %q", p, ok, cfg)
	}

	// A config in the parent of $HOME is never reached.
	if err := os.WriteFile(filepath.Join(base, ConfigName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	if p, ok := FindConfig(deep); ok {
		t.Fatalf("search crossed the stop dir: %s", p)
	}
}

func p50(d []time.Duration) time.Duration {
	slices.Sort(d)
	return d[len(d)/2]
}

func TestOverhead(t *testing.T) {
	if raceEnabled {
		t.Skip("startup time is measured without -race; see make test")
	}
	const runs = 50
	if _, err := os.Stat("/usr/bin/true"); err != nil {
		t.Skip("/usr/bin/true not available")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	work := filepath.Join(home, "work")
	shimDir := filepath.Join(root, "shims")
	for _, d := range []string{work, shimDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(shimDir, "true")
	if err := os.Symlink(self, link); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HOME=" + home,
		"PATH=" + shimDir + string(os.PathListSeparator) + "/usr/bin:/bin",
		reexecEnv + "=1",
	}

	timeRun := func(name string, e []string) time.Duration {
		cmd := exec.Command(name)
		cmd.Dir = work
		cmd.Env = e
		start := time.Now()
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return time.Since(start)
	}
	direct := []string{"HOME=" + home, "PATH=/usr/bin:/bin"}

	for range 3 {
		timeRun(link, env)
		timeRun("/usr/bin/true", direct)
	}
	shimmed := make([]time.Duration, 0, runs)
	baseline := make([]time.Duration, 0, runs)
	for range runs {
		shimmed = append(shimmed, timeRun(link, env))
		baseline = append(baseline, timeRun("/usr/bin/true", direct))
	}
	diff := p50(shimmed) - p50(baseline)
	t.Logf("shim p50=%v direct p50=%v diff=%v", p50(shimmed), p50(baseline), diff)
	if diff >= 15*time.Millisecond {
		t.Fatalf("overhead %v >= 15ms", diff)
	}
}
