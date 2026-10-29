package proto

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Request operations accepted in Request.Op.
const (
	OpExec   = "exec"
	OpFetch  = "fetch"
	OpForget = "forget"
)

// Hello is the body of the TypeHello frame that the remote side sends first.
type Hello struct {
	Protocol   int    `json:"protocol"`
	Version    string `json:"version"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	TimeUnixMs int64  `json:"time_unix_ms"`
}

// Request is the JSON line the shim writes after receiving Hello.
// Manifest, Pull and Artifacts hold root-relative paths.
type Request struct {
	Protocol    int               `json:"protocol"`
	Op          string            `json:"op"`
	Run         string            `json:"run"`
	Root        string            `json:"root"`
	Cwd         string            `json:"cwd"`
	Argv        []string          `json:"argv"`
	Env         map[string]string `json:"env"`
	Manifest    []string          `json:"manifest"`
	Prepare     []PrepareRule     `json:"prepare"`
	Pull        []string          `json:"pull"`
	Artifacts   []string          `json:"artifacts"`
	MergeOutput bool              `json:"merge_output"`
	SentUnixMs  int64             `json:"sent_unix_ms"`
}

// PrepareRule runs Run in the root when the contents of the files in When change.
type PrepareRule struct {
	When []string `json:"when"`
	Run  []string `json:"run"`
}

// Exit is the body of the TypeExit frame, which ends a run.
// A non-empty Error reports a sendaway-side failure, and Code is then meaningless.
type Exit struct {
	Code     int        `json:"code"`
	Signal   int        `json:"signal"`
	Modified []string   `json:"modified"`
	Collect  []string   `json:"collect"`
	Rejected []Rejected `json:"rejected"`
	Error    string     `json:"error"`
}

// Rejected is a path that pull or artifacts refused to collect, with the reason.
type Rejected struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Validate checks the fields of a request that come from the shim.
// It does not check Protocol; the remote side compares that itself.
func (r *Request) Validate() error {
	switch r.Op {
	case OpExec:
		if len(r.Argv) == 0 {
			return errors.New("exec: argv is empty")
		}
	case OpFetch, OpForget:
	default:
		return fmt.Errorf("unknown op %q", r.Op)
	}
	for i, a := range r.Argv {
		if !utf8.ValidString(a) {
			return fmt.Errorf("argv[%d]: not valid UTF-8", i)
		}
	}
	if len(r.Argv) > 0 && strings.Contains(r.Argv[0], "=") {
		return fmt.Errorf("argv[0] %q contains '='", r.Argv[0])
	}
	if err := checkPaths("manifest", r.Manifest); err != nil {
		return err
	}
	if err := checkPaths("pull", r.Pull); err != nil {
		return err
	}
	return checkPaths("artifacts", r.Artifacts)
}

// checkPaths applies checkRelPath to every entry of a path list named field.
func checkPaths(field string, paths []string) error {
	for _, p := range paths {
		if err := checkRelPath(p); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
	}
	return nil
}

// checkRelPath accepts a non-empty, root-relative, slash-separated path with no NUL and no ".." component.
func checkRelPath(p string) error {
	switch {
	case p == "":
		return errors.New("empty path")
	case strings.ContainsRune(p, 0):
		return fmt.Errorf("path %q contains NUL", p)
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("path %q is absolute", p)
	}
	if slices.Contains(strings.Split(p, "/"), "..") {
		return fmt.Errorf("path %q contains a .. component", p)
	}
	return nil
}
