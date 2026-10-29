package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/libraz/sendaway/internal/config"
)

// prepareRule mirrors a prepare entry of .sendaway.yml.
type prepareRule struct {
	when []string
	run  []string
}

// detection is what init found in a project directory.
type detection struct {
	remote  [][]string
	local   [][]string
	prepare []prepareRule
}

// detectors map marker files to the rules they imply.
var detectors = []struct {
	files   []string
	remote  [][]string
	local   [][]string
	prepare []prepareRule
}{
	{
		files:  []string{"Makefile"},
		remote: [][]string{{"make"}},
		local:  [][]string{{"make", "format"}, {"make", "fmt"}, {"make", "format-check"}, {"make", "lint"}},
	},
	{files: []string{"CMakeLists.txt"}, remote: [][]string{{"cmake"}, {"ctest"}}},
	{files: []string{"go.mod"}, remote: [][]string{{"go", "test"}, {"go", "build"}, {"go", "vet"}}},
	{
		files:   []string{"package.json", "yarn.lock"},
		remote:  [][]string{{"yarn", "test"}, {"yarn", "build"}},
		local:   [][]string{{"yarn", "install"}, {"yarn", "add"}, {"yarn", "remove"}, {"yarn", "dev"}, {"yarn", "lint"}, {"yarn", "format"}},
		prepare: []prepareRule{{[]string{"yarn.lock", "package.json"}, []string{"yarn", "install", "--immutable"}}},
	},
	{
		files:   []string{"package.json", "pnpm-lock.yaml"},
		remote:  [][]string{{"pnpm", "test"}, {"pnpm", "build"}},
		local:   [][]string{{"pnpm", "install"}, {"pnpm", "add"}, {"pnpm", "remove"}, {"pnpm", "dev"}},
		prepare: []prepareRule{{[]string{"pnpm-lock.yaml", "package.json"}, []string{"pnpm", "install", "--frozen-lockfile"}}},
	},
	{
		files:   []string{"package.json", "bun.lock"},
		remote:  [][]string{{"bun", "test"}, {"bun", "run", "build"}},
		local:   [][]string{{"bun", "install"}, {"bun", "add"}, {"bun", "remove"}, {"bun", "run", "dev"}},
		prepare: []prepareRule{{[]string{"bun.lock", "package.json"}, []string{"bun", "install", "--frozen-lockfile"}}},
	},
	{
		files:  []string{"Cargo.toml"},
		remote: [][]string{{"cargo", "test"}, {"cargo", "build"}},
		local:  [][]string{{"cargo", "fmt"}, {"cargo", "add"}},
	},
	{
		files: []string{"mise.toml"},
		prepare: []prepareRule{
			{[]string{"mise.toml"}, []string{"mise", "trust", "--yes"}},
			{[]string{"mise.toml"}, []string{"mise", "install"}},
		},
	},
}

// detect inspects dir for the files that imply rules.
func detect(dir string) detection {
	var d detection
	for _, det := range detectors {
		found := true
		for _, f := range det.files {
			if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
				found = false
				break
			}
		}
		if !found {
			continue
		}
		d.remote = append(d.remote, det.remote...)
		d.local = append(d.local, det.local...)
		d.prepare = append(d.prepare, det.prepare...)
	}
	return d
}

// render writes the detection as a .sendaway.yml with the unused keys left
// as commented empty lists.
func (d detection) render() string {
	var b strings.Builder
	rules := func(key, comment string, rs [][]string) {
		if len(rs) == 0 {
			fmt.Fprintf(&b, "%s: []  # %s\n", key, comment)
			return
		}
		fmt.Fprintf(&b, "%s:  # %s\n", key, comment)
		for _, r := range rs {
			fmt.Fprintf(&b, "  - %s\n", joinWords(r))
		}
	}
	rules("remote", "commands that run on the remote host", d.remote)
	rules("local", "commands that stay local; wins over remote", d.local)
	b.WriteString("env: []  # extra variable names to forward; a trailing * is a prefix\n")
	if len(d.prepare) > 0 {
		b.WriteString("prepare:  # run on the host when the listed files change\n")
		for _, p := range d.prepare {
			fmt.Fprintf(&b, "  - when: %s\n    run: %s\n", joinWords(p.when), joinWords(p.run))
		}
	}
	b.WriteString("pull: []  # paths copied back after a successful run\n")
	b.WriteString("artifacts: []  # paths copied back after a failed run\n")
	b.WriteString("exclude: []  # paths never sent to the host\n")
	return b.String()
}

// initCmd writes .sendaway.yml in the current directory unless it exists.
func initCmd(out io.Writer, args []string) error {
	if len(args) > 0 {
		return usage("init takes no arguments")
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	return initIn(out, dir)
}

func initIn(out io.Writer, dir string) error {
	path := filepath.Join(dir, config.ProjectFile)
	d := detect(dir)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s already exists", path)
	}
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, d.render()); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "wrote %s (%d remote, %d local, %d prepare rules)\n", path, len(d.remote), len(d.local), len(d.prepare))
	return nil
}
