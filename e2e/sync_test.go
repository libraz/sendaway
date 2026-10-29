//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestS4Sync(t *testing.T) {
	e := newEnv(t)
	e.writeFile(".gitignore", "*.log\n!keep.log\nbuild/\nforced/\n")
	e.writeFile("keep.log", "negated\n")
	e.writeFile("drop.log", "ignored\n")
	e.writeFile("forced/tracked.txt", "forced\n")
	e.writeFile("forced/untracked.txt", "ignored\n")
	e.writeFile("src/a.txt", "a\n")
	e.writeFile("gone.txt", "gone\n")
	e.writeFile("gonedir/x.txt", "x\n")
	e.writeFile("swap", "file first\n")
	e.writeFile("flip/inner.txt", "dir first\n")
	e.git("add", "-A")
	e.git("add", "-f", "forced/tracked.txt")
	e.git("commit", "-q", "-m", "init")

	mustSucceed(t, "first run", e.sendaway("run", "sh", "-c", "mkdir -p build && echo obj > build/out.o"))
	e.expectKinds(map[string]string{
		"keep.log":             "file",
		"drop.log":             "none",
		"forced/tracked.txt":   "file",
		"forced/untracked.txt": "none",
		"src/a.txt":            "file",
		"gone.txt":             "file",
		"gonedir/x.txt":        "file",
		"swap":                 "file",
		"flip":                 "dir",
		"build/out.o":          "file",
	})

	// Delete a file and a directory, and turn a file into a directory, without staging.
	for _, p := range []string{"gone.txt", "gonedir", "swap"} {
		if err := os.RemoveAll(e.path(p)); err != nil {
			t.Fatal(err)
		}
	}
	e.writeFile("swap/inner.txt", "dir now\n")
	if r := e.sendaway("run", "true"); r.code != 0 {
		t.Errorf("run after deleting and file to directory: %s", r)
	} else {
		e.expectKinds(map[string]string{
			"keep.log":           "file",
			"forced/tracked.txt": "file",
			"gone.txt":           "none",
			"gonedir":            "none",
			"swap":               "dir",
			"swap/inner.txt":     "file",
			"build/out.o":        "file",
		})
	}

	// Turn a tracked directory into a file, without staging.
	if err := os.RemoveAll(e.path("flip")); err != nil {
		t.Fatal(err)
	}
	e.writeFile("flip", "file now\n")
	if r := e.sendaway("run", "true"); r.code != 0 {
		t.Errorf("run after directory to file: %s", r)
	} else {
		e.expectKinds(map[string]string{"flip": "file", "build/out.o": "file"})
		if out, err := e.remoteSh(`cat "$ROOT/flip"`); err != nil || out != "file now\n" {
			t.Errorf("remote flip after the type change: %q, %v", out, err)
		}
	}
}

// genScript writes, for each kind after the directory argument, a file named
// after the kind that starts with that format's leading bytes.
const genScript = `#!/bin/sh
set -e
dir=$1; shift
mkdir -p "$dir"
for k in "$@"; do
  f=$dir/$k
  case $k in
  elf) printf '\177ELF\002\001\001' >"$f" ;;
  macho) printf '\317\372\355\376\014' >"$f" ;;
  pe) printf 'MZ\220\000' >"$f" ;;
  ar) printf '!<arch>\nx' >"$f" ;;
  zip) printf 'PK\003\004\024' >"$f" ;;
  gzip) printf '\037\213\010' >"$f" ;;
  xz) printf '\3757zXZ\000\000' >"$f" ;;
  zstd) printf '(\265/\375\044' >"$f" ;;
  bzip2) printf 'BZh91AY' >"$f" ;;
  tar) head -c 257 /dev/zero >"$f"; printf 'ustar\00000' >>"$f" ;;
  wasm) printf '\000asm\001\000\000\000' >"$f" ;;
  text) printf 'plain text\n' >"$f" ;;
  *) echo "unknown kind $k" >&2; exit 2 ;;
  esac
done
`

func TestS5Pull(t *testing.T) {
	e := newEnv(t)
	e.writeFile(".gitignore", "out/\nart/\n")
	e.writeFile("gen.sh", genScript)
	e.writeFile(".sendaway.yml", "pull: [out]\nartifacts: [art]\n")

	for _, kind := range []string{"elf", "macho", "pe", "ar", "zip", "gzip", "xz", "zstd", "bzip2", "tar"} {
		r := e.sendaway("run", "sh", "-c", "rm -rf out && sh gen.sh out "+kind)
		if r.code != 125 || !strings.Contains(r.stderr, "out/"+kind) {
			t.Errorf("pull of %s: want 125 naming out/%s, got %s", kind, kind, r)
		}
		if _, err := os.Lstat(e.path("out")); err == nil {
			t.Fatalf("pull of %s wrote out/ locally", kind)
		}
	}

	r := e.sendaway("run", "sh", "-c", "rm -rf out && sh gen.sh out wasm text")
	mustSucceed(t, "pull of wasm and text", r)
	for name, want := range map[string]string{"wasm": "\x00asm\x01\x00\x00\x00", "text": "plain text\n"} {
		got, err := os.ReadFile(e.path("out/" + name))
		if err != nil || string(got) != want {
			t.Errorf("pulled out/%s: %q, %v", name, got, err)
		}
	}

	// Artifacts on failure: native binaries are held back, archives and text come home.
	r = e.sendaway("run", "sh", "-c", "sh gen.sh art elf macho gzip text; exit 3")
	if r.code != 3 {
		t.Fatalf("artifacts run: want exit 3, got %s", r)
	}
	art := filepath.Join(e.stateDir, "runs", r.runID(t), "artifacts", "art")
	for name, want := range map[string]bool{"elf": false, "macho": false, "gzip": true, "text": true} {
		_, err := os.Stat(filepath.Join(art, name))
		if got := err == nil; got != want {
			t.Errorf("artifact %s collected=%v, want %v (%v)", name, got, want, err)
		}
	}
}

func TestS6Modified(t *testing.T) {
	e := newEnv(t)
	e.writeFile("src.txt", "source\n")
	e.writeFile("Makefile", "touch:\n\t@touch src.txt\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(e.path("src.txt"), old, old); err != nil {
		t.Fatal(err)
	}

	r := e.shim("make", "touch")
	mustSucceed(t, "make touch", r)
	lines := r.saLines()
	const want = "[sendaway] remote run modified 1 synced files (not copied back): src.txt — add this target to local: or list the files in pull:"
	if len(lines) < 2 || lines[len(lines)-2] != want {
		t.Errorf("want the line before the trailer to be\n%q\ngot %q", want, lines)
	}
	if fi, err := os.Stat(e.path("src.txt")); err != nil || !fi.ModTime().Equal(old) {
		t.Errorf("local src.txt changed: %v %v", fi, err)
	}
}

func TestS10Rerun(t *testing.T) {
	e := newEnv(t)
	const dirs, perDir = 50, 100
	for d := range dirs {
		dir := e.path(fmt.Sprintf("pkg%02d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f := range perDir {
			body := fmt.Sprintf("package pkg%02d // file %d\n", d, f)
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.go", f)), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.git("add", "-A")
	e.git("commit", "-q", "-m", "tree")

	mustSucceed(t, "initial sync", e.sendaway("run", "true"))

	now := time.Now()
	if err := os.Chtimes(e.path("pkg07/f042.go"), now, now); err != nil {
		t.Fatal(err)
	}

	cmd := e.command(t.Context(), suite.bin, []string{"run", "true"})
	pipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	begin := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var startAt, endAt time.Duration
	var stderr strings.Builder
	sc := bufio.NewScanner(pipe)
	for sc.Scan() {
		l := sc.Text()
		stderr.WriteString(l + "\n")
		switch {
		case startAt == 0 && strings.HasPrefix(l, "[sendaway] "+hostAlias+" linux-"+suite.arch+" · sync "):
			startAt = time.Since(begin)
		case endAt == 0 && trailerRE.MatchString(l):
			endAt = time.Since(begin)
		}
	}
	err = cmd.Wait()
	r := e.outcome(cmd, err, out.String(), stderr.String())
	mustSucceed(t, "rerun", r)
	t.Logf("start line %s, trailer %s", startAt.Round(time.Millisecond), endAt.Round(time.Millisecond))
	if startAt == 0 || startAt >= time.Second {
		t.Errorf("start line after %s, want < 1s\n%s", startAt, r)
	}
	if endAt == 0 || endAt >= 2*time.Second {
		t.Errorf("trailer after %s, want < 2s\n%s", endAt, r)
	}
}
