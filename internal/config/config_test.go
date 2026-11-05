package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProjectConfigBoundary(t *testing.T) {
	t.Run("empty and comment-only files mean no rules", func(t *testing.T) {
		for _, content := range []string{"", "# nothing\n", "---\n", "~\n"} {
			p, err := ParseProject("f", []byte(content))
			if err != nil {
				t.Fatalf("%q: %v", content, err)
			}
			if !reflect.DeepEqual(p, &Project{}) {
				t.Fatalf("%q: got %+v", content, p)
			}
		}
	})

	t.Run("full file", func(t *testing.T) {
		src := `remote:
  - [make]
  - [go, test, "./..."]
local:
  - [make, format]
env: [TEST_FILTER, MYAPP_*]
prepare:
  - when: [yarn.lock, package.json]
    run: [yarn, install, --immutable]
pull: [dist/wasm/]
artifacts: [build/Testing/Temporary/LastTest.log]
exclude: [fixtures/private]
`
		path := writeFile(t, t.TempDir(), ProjectFile, src)
		got, err := LoadProject(path)
		if err != nil {
			t.Fatal(err)
		}
		want := &Project{
			Remote:    [][]string{{"make"}, {"go", "test", "./..."}},
			Local:     [][]string{{"make", "format"}},
			Env:       []string{"TEST_FILTER", "MYAPP_*"},
			Prepare:   []PrepareRule{{When: []string{"yarn.lock", "package.json"}, Run: []string{"yarn", "install", "--immutable"}}},
			Pull:      []string{"dist/wasm"},
			Artifacts: []string{"build/Testing/Temporary/LastTest.log"},
			Exclude:   []string{"fixtures/private"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v\nwant %+v", got, want)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := LoadProject(filepath.Join(t.TempDir(), ProjectFile))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("got %v", err)
		}
	})

	bad := []struct {
		name, src, want string
	}{
		{"unknown key", "remote:\n  - [make]\nbogus: 1\n", `f:3: unknown key "bogus" (upgrade sendaway or remove the key)`},
		{"unknown prepare key", "prepare:\n  - when: [a]\n    run: [b]\n    cwd: x\n", `f:4: unknown key "cwd"`},
		{"duplicate key", "pull: [a]\npull: [b]\n", `f:2: duplicate key "pull"`},
		{"top level list", "- a\n", "f:1: expected a mapping"},
		{"remote not a list", "remote: make\n", "f:1: remote must be a list of rules"},
		{"rule not a list", "remote:\n  - make\n", "f:2: a remote rule must be a list of words"},
		{"word not a string", "remote:\n  - [make, [a]]\n", "f:2:"},
		{"empty rule", "local:\n  - [make]\n  - []\n", "f:3: empty local rule"},
		{"empty program", "remote:\n  - [\"\"]\n", "f:2: empty program name"},
		{"bad glob", "remote:\n  - [make, \"[a\"]\n", `f:2: invalid glob "[a"`},
		{"env wrong type", "env: {a: b}\n", "f:1: env must be a list"},
		{"env with equals", "env: [A=1]\n", `f:1: invalid variable name "A=1"`},
		{"env mid star", "env: [A*B]\n", "invalid variable name"},
		{"pull dotdot", "pull: [a/../../b]\n", `f:1: path "a/../../b" contains ".."`},
		{"pull absolute", "pull: [/etc]\n", "f:1: absolute path"},
		{"artifacts dotdot", "artifacts: [../x]\n", "contains \"..\""},
		{"exclude absolute", "exclude: [/tmp/x]\n", "absolute path"},
		{"exclude root", "exclude: [.]\n", "names the project root"},
		{"exclude empty", "exclude: [\"\"]\n", "empty path"},
		{"prepare missing run", "prepare:\n  - when: [a]\n", "f:2: prepare rule needs a non-empty run"},
		{"prepare missing when", "prepare:\n  - run: [a]\n", "f:2: prepare rule needs a non-empty when"},
		{"prepare when dotdot", "prepare:\n  - when: [../a]\n    run: [b]\n", "f:2:"},
		{"syntax error", "remote: [\n", "f:"},
		{"tab indent", "remote:\n\t- [make]\n", "f:"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProject("f", []byte(tc.src))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %q, want substring %q", err, tc.want)
			}
			var ce *Error
			if !errors.As(err, &ce) || ce.File != "f" {
				t.Fatalf("not a *Error for f: %#v", err)
			}
		})
	}

	t.Run("alias is followed", func(t *testing.T) {
		p, err := ParseProject("f", []byte("pull: &p [a]\nartifacts: *p\n"))
		if err != nil || !reflect.DeepEqual(p.Artifacts, []string{"a"}) {
			t.Fatalf("got %+v, %v", p, err)
		}
	})
}

func TestUserConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Run("full file", func(t *testing.T) {
		proj := filepath.Join(home, "Projects", "formulon")
		if err := os.MkdirAll(proj, 0o755); err != nil {
			t.Fatal(err)
		}
		resolved, err := filepath.EvalSymlinks(proj)
		if err != nil {
			t.Fatal(err)
		}
		src := "host: build01\nroutes:\n  - path: ~/Projects/formulon\n    host: build02\nssh_config: ~/.ssh/config\n"
		file := writeFile(t, t.TempDir(), "config.yml", src)
		u, err := LoadUser(file)
		if err != nil {
			t.Fatal(err)
		}
		want := &User{
			Host:      "build01",
			Routes:    []Route{{Path: resolved, Host: "build02"}},
			SSHConfig: filepath.Join(home, ".ssh", "config"),
			MinFree:   DefaultMinFree,
		}
		if !reflect.DeepEqual(u, want) {
			t.Fatalf("got %+v\nwant %+v", u, want)
		}
	})

	t.Run("route path is symlink-resolved", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "real")
		link := filepath.Join(dir, "link")
		if err := os.Mkdir(real, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		resolved, _ := filepath.EvalSymlinks(real)
		u, err := ParseUser("f", []byte("routes:\n  - {path: "+link+", host: h}\n"))
		if err != nil {
			t.Fatal(err)
		}
		if u.Routes[0].Path != resolved {
			t.Fatalf("got %q, want %q", u.Routes[0].Path, resolved)
		}
		if got := u.HostFor(filepath.Join(resolved, "sub")); got != "h" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("missing and empty files", func(t *testing.T) {
		u, err := LoadUser(filepath.Join(t.TempDir(), "nope.yml"))
		if err != nil || !reflect.DeepEqual(u, &User{MinFree: DefaultMinFree}) {
			t.Fatalf("missing: %+v, %v", u, err)
		}
		u, err = ParseUser("f", nil)
		if err != nil || !reflect.DeepEqual(u, &User{MinFree: DefaultMinFree}) {
			t.Fatalf("empty: %+v, %v", u, err)
		}
		if u.HostFor("/x") != "" {
			t.Fatal("empty user must resolve to no host")
		}
	})

	t.Run("default path", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/xdg")
		got, err := DefaultUserPath()
		if err != nil || got != "/xdg/sendaway/config.yml" {
			t.Fatalf("got %q, %v", got, err)
		}
		t.Setenv("XDG_CONFIG_HOME", "")
		got, err = DefaultUserPath()
		if err != nil || got != filepath.Join(home, ".config", "sendaway", "config.yml") {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	bad := []struct{ name, src, want string }{
		{"unknown key", "host: a\nfoo: b\n", `f:2: unknown key "foo" (upgrade sendaway or remove the key)`},
		{"host dash", "host: -oProxyCommand=x\n", `f:1: host "-oProxyCommand=x" must not start with "-"`},
		{"host space", "host: \"a b\"\n", "f:1: host \"a b\" must not contain whitespace"},
		{"host tab", "host: \"a\\tb\"\n", "whitespace"},
		{"host empty", "host: \"\"\n", "must not be empty"},
		{"host list", "host: [a]\n", "f:1: host must be a string"},
		{"route host dash", "routes:\n  - {path: /a, host: -x}\n", "f:2: host"},
		{"route host space", "routes:\n  - {path: /a, host: \"x y\"}\n", "whitespace"},
		{"route missing host", "routes:\n  - path: /a\n", "f:2: a route needs a host"},
		{"route missing path", "routes:\n  - host: a\n", "f:2: a route needs a path"},
		{"route relative path", "routes:\n  - {path: a/b, host: h}\n", "f:2: route path must be absolute"},
		{"route unknown key", "routes:\n  - {path: /a, host: h, x: 1}\n", `f:2: unknown key "x"`},
		{"routes not list", "routes: {a: b}\n", "f:1: routes must be a list"},
		{"duplicate path", "routes:\n  - {path: /a/b, host: h}\n  - {path: /a//b/, host: i}\n", "f:3: duplicate route path"},
		{"min_free negative", "min_free: -5GiB\n", `f:1: min_free "-5GiB" must not be negative`},
		{"min_free unknown unit", "min_free: 20GB\n", `f:1: min_free "20GB" has an unknown unit "GB"`},
		{"min_free lower-case unit", "min_free: 20gib\n", `has an unknown unit "gib"`},
		{"min_free fraction", "min_free: 1.5GiB\n", `f:1: min_free "1.5GiB" must be an integer with a unit`},
		{"min_free bare number", "min_free: 100\n", `f:1: min_free "100" needs a unit`},
		{"min_free unit only", "min_free: GiB\n", "must be an integer with a unit"},
		{"min_free too large", "min_free: 9999999999TiB\n", "is too large"},
		{"min_free overflowing digits", "min_free: 99999999999999999999B\n", "is too large"},
		{"min_free list", "min_free: [1GiB]\n", "f:1: min_free must be a string"},
		{"min_free second line", "host: a\nmin_free: x\n", "f:2: min_free"},
		{"ssh_config list", "ssh_config: [a]\n", "f:1: ssh_config must be a string"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseUser("f", []byte(tc.src))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %q, want substring %q", err, tc.want)
			}
		})
	}
}

func TestUserMinFree(t *testing.T) {
	cases := []struct {
		name, src string
		want      int64
	}{
		{"absent", "host: h\n", DefaultMinFree},
		{"null", "min_free:\n", DefaultMinFree},
		{"zero disables", "min_free: 0\n", 0},
		{"zero with unit", "min_free: 0GiB\n", 0},
		{"bytes", "min_free: 512B\n", 512},
		{"KiB", "min_free: 4KiB\n", 4 << 10},
		{"MiB", "min_free: 512MiB\n", 512 << 20},
		{"GiB", "min_free: 20GiB\n", 20 << 30},
		{"TiB", "min_free: 2TiB\n", 2 << 40},
		{"quoted", "min_free: \"8GiB\"\n", 8 << 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := ParseUser("f", []byte(tc.src))
			if err != nil || u.MinFree != tc.want {
				t.Fatalf("MinFree = %d, %v; want %d", u.MinFree, err, tc.want)
			}
		})
	}
}

func TestFormatSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0.0 MiB"},
		{512 << 20, "512.0 MiB"},
		{1<<30 - 1, "1024.0 MiB"},
		{1 << 30, "1.0 GiB"},
		{20 << 30, "20.0 GiB"},
		{3<<30 + 1<<29, "3.5 GiB"},
	}
	for _, tc := range cases {
		if got := FormatSize(tc.in); got != tc.want {
			t.Errorf("FormatSize(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRoutesLongestComponent(t *testing.T) {
	u := &User{
		Host: "default",
		Routes: []Route{
			{Path: "/p/foo", Host: "foo"},
			{Path: "/p/foo/bar", Host: "bar"},
			{Path: "/", Host: "slash"},
		},
	}
	cases := []struct{ root, want string }{
		{"/p/foo", "foo"},
		{"/p/foo/x", "foo"},
		{"/p/foo/bar", "bar"},
		{"/p/foo/bar/baz", "bar"},
		{"/p/foobar", "slash"},
		{"/p/foo/barista", "foo"},
		{"/p", "slash"},
		{"/other", "slash"},
	}
	for _, tc := range cases {
		if got := u.HostFor(tc.root); got != tc.want {
			t.Errorf("HostFor(%q) = %q, want %q", tc.root, got, tc.want)
		}
	}

	u = &User{Host: "default", Routes: []Route{{Path: "/p/foo", Host: "foo"}}}
	for root, want := range map[string]string{
		"/p/foobar":  "default",
		"/p/fo":      "default",
		"/p":         "default",
		"/p/foo/bar": "foo",
	} {
		if got := u.HostFor(root); got != want {
			t.Errorf("HostFor(%q) = %q, want %q", root, got, want)
		}
	}

	if got := (&User{Routes: u.Routes}).HostFor("/q"); got != "" {
		t.Errorf("no default host: got %q", got)
	}

	t.Run("loaded from yaml", func(t *testing.T) {
		dir := t.TempDir()
		foo := filepath.Join(dir, "foo")
		foobar := filepath.Join(dir, "foobar")
		for _, d := range []string{foo, foobar} {
			if err := os.Mkdir(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		src := "host: d\nroutes:\n  - {path: " + foo + ", host: f}\n"
		loaded, err := ParseUser("f", []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		rf, _ := filepath.EvalSymlinks(foo)
		rfb, _ := filepath.EvalSymlinks(foobar)
		if got := loaded.HostFor(rf); got != "f" {
			t.Errorf("foo: got %q", got)
		}
		if got := loaded.HostFor(rfb); got != "d" {
			t.Errorf("foobar: got %q", got)
		}
	})
}
