package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/libraz/sendaway/internal/proto"
)

const prepareFile = "prepare.json"

// prepare runs each rule whose when-files changed since its last success.
// A failing rule is an error and keeps its previous hash.
func (ss *session) prepare(req *proto.Request, dir string) error {
	if len(req.Prepare) == 0 {
		return nil
	}
	hashes := readPrepareState(dir)
	for _, r := range req.Prepare {
		key := strings.Join(r.When, ",")
		sum, err := whenHash(req.Root, r.When)
		if err != nil {
			return fmt.Errorf("prepare: %w", err)
		}
		if hashes[key] == sum {
			continue
		}
		if err := ss.runPrepare(req, r.Run); err != nil {
			return err
		}
		hashes[key] = sum
		data, _ := json.Marshal(hashes)
		if err := writeFileAtomic(filepath.Join(dir, prepareFile), data); err != nil {
			return err
		}
	}
	return nil
}

// readPrepareState loads the stored hashes. An unreadable file reads as
// empty, which only re-runs the rules.
func readPrepareState(dir string) map[string]string {
	hashes := make(map[string]string)
	data, err := os.ReadFile(filepath.Join(dir, prepareFile))
	if err == nil && json.Unmarshal(data, &hashes) != nil {
		clear(hashes)
	}
	return hashes
}

// whenHash is the sha256 over the concatenated sha256 of each file's content.
func whenHash(root string, when []string) (string, error) {
	h := sha256.New()
	for _, w := range when {
		sum, err := fileHash(filepath.Join(root, filepath.FromSlash(w)))
		if err != nil {
			return "", err
		}
		h.Write(sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileHash(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// runPrepare runs one prepare command in the root like the child, with its
// merged output sent as notes.
func (ss *session) runPrepare(req *proto.Request, run []string) error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	wg.Go(func() { ss.pumpNotes(r) })
	state, err := ss.runGroup(req.Root, launchArgv(req.Env, run), childEnv(req.Env), w, w, []*os.File{w})
	ss.drain(&wg, []*os.File{r})
	if err != nil {
		if errors.Is(err, errAborted) {
			return err
		}
		return fmt.Errorf("prepare %s: %w", strings.Join(run, " "), err)
	}
	if e := exitStatus(state); e.Code != 0 {
		return fmt.Errorf("prepare %s exited %d", strings.Join(run, " "), e.Code)
	}
	return nil
}
