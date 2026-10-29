package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// defaultShims are the command names install-shims links by default.
var defaultShims = []string{"make", "cmake", "ctest", "go", "cargo", "yarn", "npm", "pnpm", "bun"}

func installShimsCmd(out io.Writer, names []string) error {
	if len(names) == 0 {
		names = defaultShims
	}
	dir, err := shimDirPath()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	if err := installShims(dir, exe, names); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "linked %d shims in %s\n\nAdd this line at the end of ~/.zshrc, after any 'mise activate':\n\nexport PATH=%q\n",
		len(names), dir, dir+":$PATH")
	return nil
}

// installShims links each name in dir to exe, replacing existing links.
func installShims(dir, exe string, names []string) error {
	for _, n := range names {
		if n == "" || n == commandName || strings.ContainsRune(n, '/') {
			return usage("invalid shim name %q", n)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, n := range names {
		link := filepath.Join(dir, n)
		if fi, err := os.Lstat(link); err == nil {
			if fi.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("%s exists and is not a symlink", link)
			}
			if err := os.Remove(link); err != nil {
				return err
			}
		}
		if err := os.Symlink(exe, link); err != nil {
			return err
		}
	}
	return nil
}
