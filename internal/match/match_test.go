package match

import "testing"

// Parameter model for TestMatchRules (pairwise over these factors):
//
//	words    : 0, 1, 2, 3          (rule words after the program name)
//	glob     : none, star, class   (glob syntax in the words)
//	leading  : none, flag, flag+value (options before the first rule word)
//	varval   : absent, present     (VAR=val element in argv)
//	prog     : bare, absolute path, different program
//	sides    : local-only, remote-only, both, neither
//
// Rows below cover every pair of values at least once; a few extra rows pin
// order, repetition, and malformed globs.

type matchCase struct {
	name   string
	rule   []string
	argv   []string
	expect bool
}

func TestMatchRules(t *testing.T) {
	cases := []matchCase{
		{"zero words bare", []string{"make"}, []string{"make"}, true},
		{"zero words with args", []string{"make"}, []string{"make", "-j4", "test"}, true},
		{"zero words absolute path", []string{"make"}, []string{"/usr/bin/make", "test"}, true},
		{"zero words other prog", []string{"make"}, []string{"cmake", "test"}, false},
		{"zero words varval", []string{"make"}, []string{"make", "CC=gcc"}, true},
		{"one word plain", []string{"go", "test"}, []string{"go", "test", "./..."}, true},
		{"one word leading flag", []string{"go", "test"}, []string{"go", "-v", "test"}, true},
		{"one word leading flag value", []string{"go", "test"}, []string{"go", "-C", "dir", "test"}, true},
		{"one word varval", []string{"make", "test"}, []string{"make", "CC=gcc", "test"}, true},
		{"one word absent", []string{"make", "test"}, []string{"make", "build"}, false},
		{"one word star", []string{"make", "test*"}, []string{"make", "test-unit"}, true},
		{"one word star leading flag", []string{"make", "test*"}, []string{"make", "-j4", "test-unit"}, true},
		{"one word class", []string{"make", "t[ae]st"}, []string{"make", "test"}, true},
		{"one word class miss", []string{"make", "t[ae]st"}, []string{"make", "tost"}, false},
		{"one word star absolute path", []string{"make", "*"}, []string{"/opt/bin/make", "x"}, true},
		{"one word star no args", []string{"make", "*"}, []string{"make"}, false},
		{"two words in order", []string{"yarn", "run", "build"}, []string{"yarn", "run", "build"}, true},
		{"two words gap", []string{"yarn", "run", "build"}, []string{"yarn", "run", "--silent", "build"}, true},
		{"two words reversed", []string{"yarn", "run", "build"}, []string{"yarn", "build", "run"}, false},
		{"two words leading flag value varval", []string{"bun", "run", "build"}, []string{"bun", "--cwd", "x", "FOO=1", "run", "build"}, true},
		{"two words star", []string{"cargo", "t*", "--release"}, []string{"cargo", "test", "--release"}, true},
		{"two words star class", []string{"cargo", "*", "[a-c]*"}, []string{"cargo", "build", "bin"}, true},
		{"two words star class miss", []string{"cargo", "*", "[a-c]*"}, []string{"cargo", "build", "zip"}, false},
		{"two words one arg", []string{"cargo", "*", "*"}, []string{"cargo", "build"}, false},
		{"three words", []string{"go", "test", "-run", "X*"}, []string{"go", "-C", "d", "test", "-run", "XY"}, true},
		{"three words repeated arg", []string{"go", "a", "a", "a"}, []string{"go", "a", "a"}, false},
		{"three words repeated arg ok", []string{"go", "a", "a", "a"}, []string{"go", "a", "b", "a", "a"}, true},
		{"three words other prog", []string{"go", "a", "b", "c"}, []string{"gofmt", "a", "b", "c"}, false},
		{"three words varval class", []string{"make", "[a-z]*", "V=*", "t"}, []string{"make", "x", "V=1", "t"}, true},
		{"prog is not a glob", []string{"m*"}, []string{"make"}, false},
		{"argv0 word not matched as arg", []string{"make", "make"}, []string{"make"}, false},
		{"malformed glob", []string{"make", "["}, []string{"make", "["}, false},
		{"empty rule", nil, []string{"make"}, false},
		{"empty argv", []string{"make"}, nil, false},
		{"slash blocks star", []string{"make", "*"}, []string{"make", "a/b"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Matches(tc.rule, tc.argv); got != tc.expect {
				t.Fatalf("Matches(%q, %q) = %v, want %v", tc.rule, tc.argv, got, tc.expect)
			}
		})
	}
}

func TestMatchPrecedence(t *testing.T) {
	remote := [][]string{{"make"}, {"go", "test"}}
	local := [][]string{{"make", "format"}, {"make", "fmt*"}}
	cases := []struct {
		name string
		argv []string
		want Side
	}{
		{"remote only", []string{"make", "test"}, Remote},
		{"remote second rule", []string{"go", "-v", "test"}, Remote},
		{"both match local wins", []string{"make", "format"}, Local},
		{"both match local glob wins", []string{"make", "-j2", "fmt-check"}, Local},
		{"neither defaults local", []string{"ls", "-l"}, Local},
		{"empty argv defaults local", nil, Local},
		{"local only", []string{"cargo"}, Local},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.argv, remote, local); got != tc.want {
				t.Fatalf("Decide(%q) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
	t.Run("no rules", func(t *testing.T) {
		if got := Decide([]string{"make"}, nil, nil); got != Local {
			t.Fatalf("Decide with no rules = %v, want local", got)
		}
	})
	t.Run("index", func(t *testing.T) {
		if got := Index(remote, []string{"go", "test"}); got != 1 {
			t.Fatalf("Index = %d, want 1", got)
		}
		if got := Index(remote, []string{"ls"}); got != -1 {
			t.Fatalf("Index = %d, want -1", got)
		}
	})
	t.Run("string", func(t *testing.T) {
		if Local.String() != "local" || Remote.String() != "remote" {
			t.Fatal("unexpected Side strings")
		}
	})
}
