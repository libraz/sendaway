package run

import (
	"io"
	"os"
	"sync"
)

// linePrefix starts every line sendaway itself prints.
const linePrefix = "[sendaway] "

// console serializes everything shown to the user and mirrors it into the
// run log. When stdout and stderr are the same file, both fields hold it.
type console struct {
	mu     sync.Mutex
	stdout *os.File
	stderr *os.File
	log    io.Writer
}

func newConsole(stdout, stderr *os.File) *console {
	return &console{stdout: stdout, stderr: stderr}
}

// setLog starts or, with nil, stops mirroring into the run log.
func (c *console) setLog(w io.Writer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log = w
}

// out writes command output destined for stdout.
func (c *console) out(b []byte) { c.write(c.stdout, b) }

// err writes command or ssh output destined for stderr.
func (c *console) err(b []byte) { c.write(c.stderr, b) }

// line writes one sendaway line to stderr.
func (c *console) line(s string) { c.write(c.stderr, []byte(linePrefix+s+"\n")) }

func (c *console) write(f *os.File, b []byte) {
	if len(b) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = f.Write(b)
	if c.log != nil {
		_, _ = c.log.Write(b)
	}
}

// sameFile reports whether a and b refer to the same open file (device and inode).
func sameFile(a, b *os.File) bool {
	fa, err := a.Stat()
	if err != nil {
		return false
	}
	fb, err := b.Stat()
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}
