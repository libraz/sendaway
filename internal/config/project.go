package config

import (
	"os"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ProjectFile is the project configuration file name.
const ProjectFile = ".sendaway.yml"

// PrepareRule runs Run at the root when the content of the When files changed.
type PrepareRule struct {
	When []string
	Run  []string
}

// Project is the decoded .sendaway.yml. The zero value means "no rules".
type Project struct {
	Remote    [][]string
	Local     [][]string
	Env       []string // names; a trailing * marks a prefix
	Prepare   []PrepareRule
	Pull      []string // root-relative, cleaned
	Artifacts []string // root-relative, cleaned
	Exclude   []string // root-relative, cleaned; no globs
}

// LoadProject reads and validates the project configuration at file.
func LoadProject(file string) (*Project, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return ParseProject(file, data)
}

// ParseProject validates data as a project configuration; name is used in
// error messages.
func ParseProject(name string, data []byte) (*Project, error) {
	p := &parser{file: name}
	root, err := p.root(data)
	if err != nil {
		return nil, err
	}
	proj := &Project{}
	if root == nil {
		return proj, nil
	}
	err = p.fields(root, func(key string, v *yaml.Node) (bool, error) {
		var err error
		switch key {
		case "remote":
			proj.Remote, err = p.rules(v, "remote")
		case "local":
			proj.Local, err = p.rules(v, "local")
		case "env":
			proj.Env, err = p.envNames(v)
		case "prepare":
			proj.Prepare, err = p.prepare(v)
		case "pull":
			proj.Pull, err = p.paths(v, "pull")
		case "artifacts":
			proj.Artifacts, err = p.paths(v, "artifacts")
		case "exclude":
			proj.Exclude, err = p.paths(v, "exclude")
		default:
			return false, nil
		}
		return true, err
	})
	if err != nil {
		return nil, err
	}
	return proj, nil
}

func (p *parser) rules(n *yaml.Node, what string) ([][]string, error) {
	if isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, p.errf(n, "%s must be a list of rules", what)
	}
	var out [][]string
	for _, c := range n.Content {
		c = deref(c)
		if c.Kind != yaml.SequenceNode {
			return nil, p.errf(c, "a %s rule must be a list of words", what)
		}
		nodes, err := p.items(c, what+" rule")
		if err != nil {
			return nil, err
		}
		if len(nodes) == 0 {
			return nil, p.errf(c, "empty %s rule", what)
		}
		if nodes[0].Value == "" {
			return nil, p.errf(nodes[0], "empty program name in %s rule", what)
		}
		rule := make([]string, len(nodes))
		for i, w := range nodes {
			// Malformed patterns are reported even when the match fails.
			if _, err := path.Match(w.Value, ""); err != nil && i > 0 {
				return nil, p.errf(w, "invalid glob %q in %s rule", w.Value, what)
			}
			rule[i] = w.Value
		}
		out = append(out, rule)
	}
	return out, nil
}

func (p *parser) envNames(n *yaml.Node) ([]string, error) {
	nodes, err := p.items(n, "env")
	if err != nil {
		return nil, err
	}
	out := make([]string, len(nodes))
	for i, c := range nodes {
		name := c.Value
		body := strings.TrimSuffix(name, "*")
		if body == "" && name != "*" || strings.ContainsAny(body, "=* \t\r\n\x00") {
			return nil, p.errf(c, "invalid variable name %q", name)
		}
		out[i] = name
	}
	return out, nil
}

func (p *parser) prepare(n *yaml.Node) ([]PrepareRule, error) {
	if isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, p.errf(n, "prepare must be a list of rules")
	}
	var out []PrepareRule
	for _, c := range n.Content {
		c = deref(c)
		if c.Kind != yaml.MappingNode {
			return nil, p.errf(c, "a prepare rule must be a mapping with when and run")
		}
		var rule PrepareRule
		err := p.fields(c, func(key string, v *yaml.Node) (bool, error) {
			var err error
			switch key {
			case "when":
				rule.When, err = p.paths(v, "when")
			case "run":
				rule.Run, err = p.words(v, "run")
			default:
				return false, nil
			}
			return true, err
		})
		if err != nil {
			return nil, err
		}
		if len(rule.When) == 0 {
			return nil, p.errf(c, "prepare rule needs a non-empty when")
		}
		if len(rule.Run) == 0 || rule.Run[0] == "" {
			return nil, p.errf(c, "prepare rule needs a non-empty run")
		}
		out = append(out, rule)
	}
	return out, nil
}
