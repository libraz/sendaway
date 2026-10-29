package remote

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	manifestFile = "manifest"
	gitConfig    = "[core]\n\trepositoryformatversion = 0\n\tbare = false\n"
	noStateNote  = "remote state missing; locally deleted files may linger — run 'sendaway forget' to start clean"
)

// applyDeletions removes what the previous run sent and this one does not,
// then clears entries whose type changed so the push can replace them.
func (ss *session) applyDeletions(root, dir string, cur []string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return &fs.PathError{Op: "sync", Path: root, Err: syscall.ENOTDIR}
	}
	prev, ok, err := readManifest(dir)
	if err != nil {
		return err
	}
	if ok {
		if err := deleteRemoved(root, prev, cur); err != nil {
			return err
		}
	} else {
		ss.note(noStateNote)
	}
	return removeTypeChanges(root, cur)
}

// deleteRemoved deletes the files of prev missing from cur and then every
// parent directory that became empty, up to but excluding root and .git.
func deleteRemoved(root string, prev, cur []string) error {
	keep := make(map[string]bool, len(cur))
	for _, p := range cur {
		keep[p] = true
	}
	for _, p := range prev {
		if keep[p] {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(p))
		info, err := os.Lstat(full)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return err
		case info.IsDir():
			// A directory created on the host is not a file this tree sent.
			continue
		default:
			if err := os.Remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		removeEmptyParents(root, p)
	}
	return nil
}

// removeEmptyParents removes the empty ancestors of rel inside root.
func removeEmptyParents(root, rel string) {
	for d := path.Dir(rel); d != "."; d = path.Dir(d) {
		if d == ".git" || strings.HasPrefix(d, ".git/") {
			return
		}
		// Rmdir, unlike os.Remove, never deletes a file standing in the way.
		if syscall.Rmdir(filepath.Join(root, filepath.FromSlash(d))) != nil {
			return
		}
	}
}

// removeTypeChanges deletes a non-directory standing where cur needs a
// directory, and a directory standing where cur has a file or symlink.
func removeTypeChanges(root string, cur []string) error {
	checked := make(map[string]bool)
	for _, p := range cur {
		parts := strings.Split(p, "/")
		for i := 1; i < len(parts); i++ {
			pre := strings.Join(parts[:i], "/")
			if checked[pre] {
				continue
			}
			checked[pre] = true
			full := filepath.Join(root, filepath.FromSlash(pre))
			info, err := os.Lstat(full)
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
			if err != nil {
				return err
			}
			if !info.IsDir() {
				if err := os.Remove(full); err != nil {
					return err
				}
				break
			}
		}
		full := filepath.Join(root, filepath.FromSlash(p))
		if info, err := os.Lstat(full); err == nil && info.IsDir() {
			if err := os.RemoveAll(full); err != nil {
				return err
			}
		}
	}
	return nil
}

// readManifest reads the manifest state file. ok is false when it does not exist.
func readManifest(dir string) (paths []string, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	for p := range bytes.SplitSeq(data, []byte{0}) {
		if len(p) > 0 {
			paths = append(paths, string(p))
		}
	}
	return paths, true, nil
}

// writeManifest stores the pushed path set, NUL-terminated per entry.
func writeManifest(dir string, paths []string) error {
	var b bytes.Buffer
	for _, p := range paths {
		b.WriteString(p)
		b.WriteByte(0)
	}
	return writeFileAtomic(filepath.Join(dir, manifestFile), b.Bytes())
}

// writeGitConfig replaces .git/config with a minimal one carrying no remotes.
func writeGitConfig(root string) error {
	return os.WriteFile(filepath.Join(root, ".git", "config"), []byte(gitConfig), 0o600)
}

// writeFileAtomic writes data to a temporary file beside name and renames it into place.
func writeFileAtomic(name string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".tmp-*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), name)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}

// stamp is the size and mtime of a synced file.
type stamp struct {
	size  int64
	mtime time.Time
}

// stampFiles records the manifest files that exist; vanished ones are skipped.
func stampFiles(root string, paths []string) map[string]stamp {
	m := make(map[string]stamp, len(paths))
	for _, p := range paths {
		if info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p))); err == nil {
			m[p] = stamp{info.Size(), info.ModTime()}
		}
	}
	return m
}

// modified lists the stamped files whose size or mtime changed, or that are
// gone, leaving out the paths about to be collected.
func modified(root string, paths []string, before map[string]stamp, collected []string) []string {
	skip := make(map[string]bool, len(collected))
	for _, p := range collected {
		skip[p] = true
	}
	var out []string
	for _, p := range paths {
		st, ok := before[p]
		if !ok || skip[p] {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil || info.Size() != st.size || !info.ModTime().Equal(st.mtime) {
			out = append(out, p)
		}
	}
	return out
}
