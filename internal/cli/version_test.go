package cli

import "testing"

func TestResolveVersion(t *testing.T) {
	cases := []struct {
		ldflags, module, want string
	}{
		{"v1.2.3", "v0.1.0", "v1.2.3"},
		{"dev", "v0.1.0", "v0.1.0"},
		{"dev", "(devel)", "dev"},
		{"dev", "", "dev"},
	}
	for _, c := range cases {
		if got := resolveVersion(c.ldflags, c.module); got != c.want {
			t.Errorf("resolveVersion(%q, %q) = %q, want %q", c.ldflags, c.module, got, c.want)
		}
	}
}
