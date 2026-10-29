// Package config decodes and validates the project (.sendaway.yml) and user
// (config.yml) configuration files. It imports only the standard library and
// the YAML decoder.
package config

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Error is a configuration error located in a file.
type Error struct {
	File string
	Line int // 0 when unknown
	Msg  string
}

// Error implements the error interface as "<file>:<line>: <message>".
func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Msg)
	}
	return e.File + ": " + e.Msg
}

type parser struct {
	file string
}

func (p *parser) errf(n *yaml.Node, format string, args ...any) error {
	line := 0
	if n != nil {
		line = n.Line
	}
	return &Error{File: p.file, Line: line, Msg: fmt.Sprintf(format, args...)}
}

var syntaxLine = regexp.MustCompile(`line (\d+): (.*)`)

// root parses data and returns the top-level mapping, or nil for an empty
// document.
func (p *parser) root(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		e := &Error{File: p.file, Msg: strings.TrimPrefix(err.Error(), "yaml: ")}
		if m := syntaxLine.FindStringSubmatch(err.Error()); m != nil {
			_, _ = fmt.Sscanf(m[1], "%d", &e.Line)
			e.Msg = m[2]
		}
		return nil, e
	}
	if doc.Kind == 0 {
		return nil, nil
	}
	n := deref(doc.Content[0])
	if isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, p.errf(n, "expected a mapping of keys at the top level")
	}
	return n, nil
}

func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// fields calls handle for every key of mapping m and rejects duplicate and
// unknown keys. handle reports whether it recognised the key.
func (p *parser) fields(m *yaml.Node, handle func(key string, v *yaml.Node) (bool, error)) error {
	seen := make(map[string]bool, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := deref(m.Content[i])
		if k.Kind != yaml.ScalarNode {
			return p.errf(k, "keys must be strings")
		}
		if seen[k.Value] {
			return p.errf(k, "duplicate key %q", k.Value)
		}
		seen[k.Value] = true
		known, err := handle(k.Value, deref(m.Content[i+1]))
		if err != nil {
			return err
		}
		if !known {
			return p.errf(k, "unknown key %q (upgrade sendaway or remove the key)", k.Value)
		}
	}
	return nil
}

// items returns the scalar elements of a sequence; a null value is an empty list.
func (p *parser) items(n *yaml.Node, what string) ([]*yaml.Node, error) {
	if isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, p.errf(n, "%s must be a list", what)
	}
	out := make([]*yaml.Node, 0, len(n.Content))
	for _, c := range n.Content {
		c = deref(c)
		if c.Kind != yaml.ScalarNode || isNull(c) {
			return nil, p.errf(c, "%s must contain only strings", what)
		}
		out = append(out, c)
	}
	return out, nil
}

func (p *parser) words(n *yaml.Node, what string) ([]string, error) {
	nodes, err := p.items(n, what)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(nodes))
	for i, c := range nodes {
		out[i] = c.Value
	}
	return out, nil
}

// scalar returns the string value of a scalar node.
func (p *parser) scalar(n *yaml.Node, what string) (string, error) {
	if n.Kind != yaml.ScalarNode || isNull(n) {
		return "", p.errf(n, "%s must be a string", what)
	}
	return n.Value, nil
}

// relPath validates a root-relative path and returns it cleaned.
func (p *parser) relPath(n *yaml.Node) (string, error) {
	s := n.Value
	switch {
	case s == "":
		return "", p.errf(n, "empty path")
	case strings.ContainsRune(s, 0):
		return "", p.errf(n, "path %q contains NUL", s)
	case strings.HasPrefix(s, "/"):
		return "", p.errf(n, "absolute path %q is outside the project root", s)
	}
	if containsDotDot(strings.Split(s, "/")) {
		return "", p.errf(n, "path %q contains \"..\" and leaves the project root", s)
	}
	c := path.Clean(s)
	if c == "." {
		return "", p.errf(n, "path %q names the project root itself", s)
	}
	return c, nil
}

func containsDotDot(parts []string) bool {
	return slices.Contains(parts, "..")
}

func (p *parser) paths(n *yaml.Node, what string) ([]string, error) {
	nodes, err := p.items(n, what)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(nodes))
	for i, c := range nodes {
		if out[i], err = p.relPath(c); err != nil {
			return nil, err
		}
	}
	return out, nil
}
