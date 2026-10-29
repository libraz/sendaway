package sshx

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// maxControlPath is the longest ControlPath ssh accepts on macOS once it appends
// its temporary-socket suffix.
const maxControlPath = 86

// SSHOptions returns the ssh options shared by every connection, without the program
// name and without the host. sshConfig is passed as -F when non-empty.
func SSHOptions(controlPath, sshConfig string) []string {
	opts := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + controlPath,
		"-o", "ControlPersist=10m",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=3",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "ClearAllForwardings=yes",
		"-o", "GSSAPIDelegateCredentials=no",
	}
	if sshConfig != "" {
		opts = append(opts, "-F", sshConfig)
	}
	return opts
}

// ControlPath returns <stateDir>/cm/<sha256(host)[:16]>. It creates the cm directory
// with mode 0700, and fails if an existing one has other permissions or if the
// resulting path is too long for ssh.
func ControlPath(stateDir, host string) (string, error) {
	sum := sha256.Sum256([]byte(host))
	dir := filepath.Join(stateDir, "cm")
	path := filepath.Join(dir, hex.EncodeToString(sum[:])[:16])
	if len(path) > maxControlPath {
		return "", fmt.Errorf("ssh control path %q is %d bytes, over the %d byte limit; use a shorter state directory", path, len(path), maxControlPath)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		return "", fmt.Errorf("%s has mode %04o, want 0700", dir, perm)
	}
	return path, nil
}
