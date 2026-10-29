package proto

import "testing"

func TestRequestValidate(t *testing.T) {
	base := func() Request {
		return Request{
			Op:        OpExec,
			Argv:      []string{"make", "test"},
			Manifest:  []string{"a", "b/c"},
			Pull:      []string{"dist/wasm"},
			Artifacts: []string{"build/Testing/LastTest.log"},
		}
	}
	cases := []struct {
		name   string
		mutate func(*Request)
		ok     bool
	}{
		{"valid", func(*Request) {}, true},
		{"fetch without argv", func(r *Request) {
			r.Op = OpFetch
			r.Argv = nil
		}, true},
		{"forget without argv", func(r *Request) {
			r.Op = OpForget
			r.Argv = nil
		}, true},
		{"empty op", func(r *Request) { r.Op = "" }, false},
		{"unknown op", func(r *Request) { r.Op = "run" }, false},
		{"exec without argv", func(r *Request) { r.Argv = nil }, false},
		{"argv not UTF-8", func(r *Request) { r.Argv = []string{"make", "\xff"} }, false},
		{"argv[0] contains =", func(r *Request) { r.Argv = []string{"FOO=1", "make"} }, false},
		{"argv[1] contains =", func(r *Request) { r.Argv = []string{"make", "FOO=1"} }, true},
		{"manifest empty path", func(r *Request) { r.Manifest = []string{""} }, false},
		{"manifest absolute", func(r *Request) { r.Manifest = []string{"/etc/passwd"} }, false},
		{"manifest dot-dot", func(r *Request) { r.Manifest = []string{"a/../../x"} }, false},
		{"manifest NUL", func(r *Request) { r.Manifest = []string{"a\x00b"} }, false},
		{"manifest dots inside a name", func(r *Request) { r.Manifest = []string{"a..b/c..", "./x"} }, true},
		{"pull dot-dot", func(r *Request) { r.Pull = []string{".."} }, false},
		{"pull absolute", func(r *Request) { r.Pull = []string{"/tmp"} }, false},
		{"artifacts NUL", func(r *Request) { r.Artifacts = []string{"x\x00"} }, false},
		{"artifacts dot-dot", func(r *Request) { r.Artifacts = []string{"build/../../etc"} }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base()
			tc.mutate(&r)
			err := r.Validate()
			if (err == nil) != tc.ok {
				t.Fatalf("Validate() error = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}
