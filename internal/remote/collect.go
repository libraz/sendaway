package remote

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/libraz/sendaway/internal/magic"
	"github.com/libraz/sendaway/internal/proto"
)

// Rejection reasons decided here rather than by content.
const (
	reasonSymlink = "symlink"
	reasonLimit   = "limit"
)

// artifactsLimit caps the total size of collected artifacts.
const artifactsLimit = 64 << 20

const (
	modePull      = magic.Pull
	modeArtifacts = magic.Artifacts
)

// collect walks the root-relative paths without following symlinks and
// returns the files to bring back and the ones refused. In pull mode a
// missing path is an error and any refusal empties the collection.
func collect(root string, paths []string, mode magic.Mode) ([]string, []proto.Rejected, error) {
	c := collector{root: root, mode: mode, seen: make(map[string]bool)}
	for _, p := range paths {
		if c.underSymlink(p) {
			c.reject(p, reasonSymlink)
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(p))
		info, err := os.Lstat(full)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if mode == modePull {
				return nil, nil, fmt.Errorf("pull path does not exist: %s", p)
			}
			continue
		case err != nil:
			return nil, nil, err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			c.reject(p, reasonSymlink)
		case info.IsDir():
			if err := filepath.WalkDir(full, c.walk); err != nil {
				return nil, nil, err
			}
		case info.Mode().IsRegular():
			if err := c.add(p, info.Size()); err != nil {
				return nil, nil, err
			}
		}
	}
	if mode == modePull && len(c.rejected) > 0 {
		c.files = nil
	}
	return c.files, c.rejected, nil
}

type collector struct {
	root     string
	mode     magic.Mode
	seen     map[string]bool
	total    int64
	files    []string
	rejected []proto.Rejected
}

// underSymlink reports whether a proper ancestor of p inside root is a symlink.
func (c *collector) underSymlink(p string) bool {
	parts := strings.Split(p, "/")
	for i := 1; i < len(parts); i++ {
		info, err := os.Lstat(filepath.Join(c.root, filepath.FromSlash(strings.Join(parts[:i], "/"))))
		if err != nil {
			return false
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

func (c *collector) walk(full string, d fs.DirEntry, err error) error {
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(c.root, full)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	switch {
	case d.Type()&fs.ModeSymlink != 0:
		c.reject(rel, reasonSymlink)
	case d.Type().IsRegular():
		info, err := d.Info()
		if err != nil {
			return err
		}
		return c.add(rel, info.Size())
	}
	return nil
}

func (c *collector) reject(rel, reason string) {
	if c.seen[rel] {
		return
	}
	c.seen[rel] = true
	c.rejected = append(c.rejected, proto.Rejected{Path: rel, Reason: reason})
}

// add classifies a regular file and either accepts or rejects it.
func (c *collector) add(rel string, size int64) error {
	if c.seen[rel] {
		return nil
	}
	reason, err := magic.ClassifyFile(filepath.Join(c.root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	switch {
	case magic.Refused(c.mode, reason):
		c.reject(rel, reason)
	case c.mode == modeArtifacts && c.total+size > artifactsLimit:
		c.reject(rel, reasonLimit)
	default:
		c.seen[rel] = true
		c.total += size
		c.files = append(c.files, rel)
	}
	return nil
}
