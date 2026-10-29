package manifest

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// isolate keeps the user's git configuration out of the test.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	isolate(t)
	root := t.TempDir()
	git(t, root, "init", "-q")
	return root
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func TestManifestNegation(t *testing.T) {
	root := newRepo(t)
	write(t, root, ".gitignore", "*.log\n!keep.log\nbuild/\n")
	write(t, root, ".git/info/exclude", "local.txt\n")
	write(t, root, "a.txt", "aaa")
	write(t, root, "gone.txt", "x")
	write(t, root, "build/tracked.txt", "t")
	write(t, root, "build/out.o", "o")
	write(t, root, "keep.log", "k")
	write(t, root, "debug.log", "d")
	write(t, root, "local.txt", "l")
	write(t, root, "untracked.txt", "u")
	write(t, root, "sub/dir/deep.txt", "deep")
	write(t, root, "new\nline.txt", "n")
	git(t, root, "add", "a.txt", "gone.txt")
	git(t, root, "add", "-f", "build/tracked.txt")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"abs-link":     "/etc/hosts",
		"sub/rel-link": "../../outside",
		"in-link":      "a.txt",
		"sub/up-link":  "../a.txt",
	} {
		if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(link))); err != nil {
			t.Fatal(err)
		}
	}

	m, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		".gitignore", "a.txt", "abs-link", "build/tracked.txt", "in-link", "keep.log",
		"new\nline.txt", "sub/dir/deep.txt", "sub/rel-link", "sub/up-link", "untracked.txt",
	}
	if got := m.Paths(); !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
	if want := []string{"abs-link", "sub/rel-link"}; !reflect.DeepEqual(m.OutsideLinks, want) {
		t.Fatalf("outside links = %q, want %q", m.OutsideLinks, want)
	}
	var a Entry
	for _, e := range m.Entries {
		if e.Path == "a.txt" {
			a = e
		}
	}
	if a.Size != 3 {
		t.Fatalf("a.txt size = %d, want 3", a.Size)
	}
	big := m.Largest(1)
	if len(big) != 1 || m.TotalSize() < a.Size || big[0].Size < a.Size {
		t.Fatalf("size accounting off: total=%d largest=%v", m.TotalSize(), big)
	}
}

func TestManifestTypeChange(t *testing.T) {
	root := newRepo(t)
	write(t, root, "flip/inner.txt", "i")
	git(t, root, "add", "flip/inner.txt")
	if err := os.RemoveAll(filepath.Join(root, "flip")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "flip", "now a file")

	m, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := m.Paths(), []string{"flip"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestManifestRejects(t *testing.T) {
	t.Run("nested repo", func(t *testing.T) {
		root := newRepo(t)
		write(t, root, "a.txt", "a")
		write(t, root, "inner/f.txt", "f")
		git(t, filepath.Join(root, "inner"), "init", "-q")
		_, err := Build(root)
		if err == nil || !strings.Contains(err.Error(), "nested git repository") {
			t.Fatalf("err = %v, want nested repository error", err)
		}
	})

	t.Run("gitlink", func(t *testing.T) {
		root := newRepo(t)
		write(t, root, "a.txt", "a")
		git(t, root, "update-index", "--add", "--cacheinfo",
			"160000,0123456789abcdef0123456789abcdef01234567,sub")
		_, err := Build(root)
		if err == nil || !strings.Contains(err.Error(), "submodule") {
			t.Fatalf("err = %v, want submodule error", err)
		}
	})

	t.Run("nfd name", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("git precomposes names only on macOS")
		}
		root := newRepo(t)
		git(t, root, "config", "core.precomposeunicode", "true")
		write(t, root, "cafe\u0301.txt", "x")
		_, err := Build(root)
		if err == nil || !strings.Contains(err.Error(), "rename to NFC") {
			t.Fatalf("err = %v, want NFC hint", err)
		}
	})

	t.Run("nfc name passes", func(t *testing.T) {
		root := newRepo(t)
		write(t, root, "caf\u00e9.txt", "x")
		m, err := Build(root)
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Paths(); len(got) != 1 || got[0] != "caf\u00e9.txt" {
			t.Fatalf("paths = %q", got)
		}
	})
}

func TestIgnoredExcludes(t *testing.T) {
	root := newRepo(t)
	write(t, root, ".gitignore", "build/\n*.log\n!keep.log\nign/\n")
	write(t, root, ".git/info/exclude", "local/\n")
	write(t, root, "build/x.o", "x")
	write(t, root, "build/sub/y", "y")
	write(t, root, "debug.log", "d")
	write(t, root, "keep.log", "k")
	write(t, root, "local/z", "z")
	write(t, root, "src/main.c", "m")
	write(t, root, "src/tmp.log", "t")
	write(t, root, "ign/tracked.txt", "t")
	write(t, root, "ign/other.txt", "o")
	git(t, root, "add", "-f", "ign/tracked.txt")

	got, err := Ignored(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"build/", "debug.log", "ign/other.txt", "local/", "src/tmp.log"}
	if !reflect.DeepEqual(sorted(got), want) {
		t.Fatalf("ignored = %q, want %q", sorted(got), want)
	}
}

func TestFingerprint(t *testing.T) {
	root := newRepo(t)
	write(t, root, ".gitignore", "*.log\n")
	write(t, root, "a.txt", "aaa")
	write(t, root, "b.txt", "bbb")

	fp := func() string {
		t.Helper()
		m, err := Build(root)
		if err != nil {
			t.Fatal(err)
		}
		return m.Fingerprint()
	}
	base := fp()
	if base == "" || fp() != base {
		t.Fatal("fingerprint is empty or unstable")
	}

	write(t, root, "ignored.log", "i")
	if fp() != base {
		t.Fatal("ignored file changed the fingerprint")
	}

	mtime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(root, "a.txt"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	afterMtime := fp()
	if afterMtime == base {
		t.Fatal("mtime change did not change the fingerprint")
	}

	write(t, root, "a.txt", "aaaa")
	if err := os.Chtimes(filepath.Join(root, "a.txt"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	afterSize := fp()
	if afterSize == afterMtime {
		t.Fatal("size change did not change the fingerprint")
	}

	write(t, root, "c.txt", "c")
	if fp() == afterSize {
		t.Fatal("new file did not change the fingerprint")
	}
}
