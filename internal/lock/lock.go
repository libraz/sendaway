// Package lock provides an advisory file lock that records its holder.
//
// The lock is an flock(2) on a file. The kernel drops it when the holding
// process dies, so a crash never leaves a stale lock behind.
package lock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// pollInterval is how often a blocked Acquire retries and checks its context.
const pollInterval = 50 * time.Millisecond

// maxHolderRead caps how many bytes of a foreign holder record are read.
const maxHolderRead = 64 << 10

// Lock is a held lock. Release it exactly once the protected work is done.
type Lock struct {
	f *os.File
}

// Acquire takes the exclusive lock on path, creating the file if needed.
//
// If the lock is already held, onWait (when non-nil) is called once with the
// current holder bytes, which may be empty or unparsable, and Acquire then
// waits until the lock is free or ctx is done. On success the file content is
// replaced by holder. When ctx ends first, Acquire returns ctx.Err().
func Acquire(ctx context.Context, path string, holder []byte, onWait func(current []byte)) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // path is the caller-chosen lock file
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	l := &Lock{f: f}

	locked, err := tryLock(f)
	if err == nil && !locked {
		if onWait != nil {
			cur, _ := io.ReadAll(io.NewSectionReader(f, 0, maxHolderRead))
			onWait(cur)
		}
		err = waitLock(ctx, f)
	}
	if err == nil {
		err = writeHolder(f, holder)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return l, nil
}

// Release unlocks and closes the lock file. Further calls are no-ops.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	// Closing the descriptor drops the lock; the explicit unlock only makes
	// the release independent of any duplicated descriptor.
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return f.Close()
}

// tryLock attempts a non-blocking exclusive lock and reports whether it won.
func tryLock(f *os.File) (bool, error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return false, nil
		default:
			return false, fmt.Errorf("flock: %w", err)
		}
	}
}

// waitLock polls until the lock is taken. A blocking flock(2) cannot be
// interrupted by a context, so polling keeps cancellation prompt.
func waitLock(ctx context.Context, f *os.File) error {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		locked, err := tryLock(f)
		if err != nil {
			return err
		}
		if locked {
			return nil
		}
	}
}

func writeHolder(f *os.File, holder []byte) error {
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate lock file: %w", err)
	}
	if _, err := f.WriteAt(holder, 0); err != nil {
		return fmt.Errorf("write holder: %w", err)
	}
	return nil
}
