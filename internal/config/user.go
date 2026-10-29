package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// Route sends projects under Path to Host.
type Route struct {
	Path string // absolute, symlink-resolved
	Host string
}

// User is the decoded per-user configuration. The zero value means "nothing
// configured".
type User struct {
	Host      string
	Routes    []Route
	SSHConfig string // "~" expanded; empty means ssh's default
}

// DefaultUserPath returns ${XDG_CONFIG_HOME:-~/.config}/sendaway/config.yml.
func DefaultUserPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "sendaway", "config.yml"), nil
}

// LoadUser reads and validates the user configuration at file. A missing file
// yields an empty User.
func LoadUser(file string) (*User, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return &User{}, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseUser(file, data)
}

// ParseUser validates data as a user configuration; name is used in error
// messages.
func ParseUser(name string, data []byte) (*User, error) {
	p := &parser{file: name}
	root, err := p.root(data)
	if err != nil {
		return nil, err
	}
	u := &User{}
	if root == nil {
		return u, nil
	}
	err = p.fields(root, func(key string, v *yaml.Node) (bool, error) {
		var err error
		switch key {
		case "host":
			u.Host, err = p.host(v, "host")
		case "routes":
			u.Routes, err = p.routes(v)
		case "ssh_config":
			if isNull(v) {
				return true, nil
			}
			var s string
			if s, err = p.scalar(v, "ssh_config"); err == nil {
				u.SSHConfig, err = expandHome(s)
				if err != nil {
					err = p.errf(v, "%v", err)
				}
			}
		default:
			return false, nil
		}
		return true, err
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (p *parser) host(n *yaml.Node, what string) (string, error) {
	if isNull(n) {
		return "", nil
	}
	h, err := p.scalar(n, what)
	if err != nil {
		return "", err
	}
	switch {
	case h == "":
		return "", p.errf(n, "%s must not be empty", what)
	case strings.HasPrefix(h, "-"):
		return "", p.errf(n, "%s %q must not start with \"-\"", what, h)
	case strings.ContainsFunc(h, unicode.IsSpace):
		return "", p.errf(n, "%s %q must not contain whitespace", what, h)
	}
	return h, nil
}

func (p *parser) routes(n *yaml.Node) ([]Route, error) {
	if isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, p.errf(n, "routes must be a list")
	}
	var out []Route
	seen := map[string]bool{}
	for _, c := range n.Content {
		c = deref(c)
		if c.Kind != yaml.MappingNode {
			return nil, p.errf(c, "a route must be a mapping with path and host")
		}
		var r Route
		var pathNode *yaml.Node
		err := p.fields(c, func(key string, v *yaml.Node) (bool, error) {
			var err error
			switch key {
			case "path":
				pathNode = v
				r.Path, err = p.scalar(v, "path")
			case "host":
				r.Host, err = p.host(v, "host")
			default:
				return false, nil
			}
			return true, err
		})
		if err != nil {
			return nil, err
		}
		if pathNode == nil || r.Path == "" {
			return nil, p.errf(c, "a route needs a path")
		}
		if r.Host == "" {
			return nil, p.errf(c, "a route needs a host")
		}
		if r.Path, err = resolveRoutePath(r.Path); err != nil {
			return nil, p.errf(pathNode, "%v", err)
		}
		if seen[r.Path] {
			return nil, p.errf(pathNode, "duplicate route path %q", r.Path)
		}
		seen[r.Path] = true
		out = append(out, r)
	}
	return out, nil
}

// expandHome expands a leading "~" or "~/".
func expandHome(s string) (string, error) {
	if s != "~" && !strings.HasPrefix(s, "~/") {
		return s, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, s[1:]), nil
}

// resolveRoutePath expands "~" and resolves symlinks. A path that does not
// exist is kept cleaned, since no existing root can lie under it.
func resolveRoutePath(s string) (string, error) {
	s, err := expandHome(s)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(s) {
		return "", errors.New("route path must be absolute or start with ~/")
	}
	s = filepath.Clean(s)
	if r, err := filepath.EvalSymlinks(s); err == nil {
		return r, nil
	}
	return s, nil
}

// HostFor returns the host for a normalized (symlink-resolved) project root:
// the route sharing the most leading path components with root, else the
// default host. It returns "" when neither applies.
func (u *User) HostFor(root string) string {
	best, bestLen := u.Host, -1
	for _, r := range u.Routes {
		if len(r.Path) > bestLen && hasPathPrefix(root, r.Path) {
			best, bestLen = r.Host, len(r.Path)
		}
	}
	return best
}

func hasPathPrefix(root, prefix string) bool {
	if root == prefix {
		return true
	}
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(root, prefix)
}
