package sshx

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// minRsyncProtocol is the oldest rsync protocol sendaway relies on. Apple's
// openrsync speaks protocol 29 and is rejected by this bound.
const minRsyncProtocol = 31

// ErrRsyncTooOld is wrapped by the error ParseRsyncVersion returns for a protocol
// older than the minimum.
var ErrRsyncTooOld = errors.New("rsync protocol too old")

// RsyncVersion is the identity reported by `rsync --version`.
type RsyncVersion struct {
	Version  string
	Protocol int
}

var (
	rsyncVersionRE  = regexp.MustCompile(`rsync\s+version\s+(\S+)`)
	rsyncProtocolRE = regexp.MustCompile(`protocol version (\d+)`)
)

// ParseRsyncVersion parses the output of `rsync --version`. When the protocol is
// below 31 it returns the parsed version together with an error wrapping ErrRsyncTooOld.
func ParseRsyncVersion(out string) (RsyncVersion, error) {
	pm := rsyncProtocolRE.FindStringSubmatch(out)
	vm := rsyncVersionRE.FindStringSubmatch(out)
	if pm == nil || vm == nil {
		return RsyncVersion{}, fmt.Errorf("unrecognized rsync --version output %q", out)
	}
	proto, err := strconv.Atoi(pm[1])
	if err != nil {
		return RsyncVersion{}, fmt.Errorf("parse rsync protocol %q: %w", pm[1], err)
	}
	v := RsyncVersion{Version: vm[1], Protocol: proto}
	if proto < minRsyncProtocol {
		return v, fmt.Errorf("%w: rsync %s speaks protocol %d, need %d or newer (install rsync 3.x, e.g. Homebrew)",
			ErrRsyncTooOld, v.Version, proto, minRsyncProtocol)
	}
	return v, nil
}

// DetectRsync runs `rsync --version` and parses it.
func DetectRsync(ctx context.Context) (RsyncVersion, error) {
	out, err := command(ctx, "rsync", "--version").Output()
	if err != nil {
		return RsyncVersion{}, fmt.Errorf("run rsync --version: %w", err)
	}
	return ParseRsyncVersion(string(out))
}
