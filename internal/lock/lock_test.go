package lock

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const (
	envHelperPath   = "LOCK_TEST_HELPER_PATH"
	helperHolder    = `{"run":"child"}`
	helperReadyLine = "held"
)

// TestMain doubles as the child process that holds the lock.
func TestMain(m *testing.M) {
	if p := os.Getenv(envHelperPath); p != "" {
		l, err := Acquire(context.Background(), p, []byte(helperHolder), nil)
		if err != nil {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString(helperReadyLine + "\n")
		// Hold until the parent closes stdin or kills us.
		_, _ = os.Stdin.Read(make([]byte, 1))
		_ = l.Release()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestLockExcludesOtherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), envHelperPath+"="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// A held stdin pipe keeps the child alive; /dev/null would EOF at once.
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != helperReadyLine+"\n" {
		t.Fatalf("child not ready: %q, %v", line, err)
	}

	waited := make(chan []byte, 1)
	done := make(chan *Lock, 1)
	go func() {
		l, err := Acquire(context.Background(), path, []byte("parent"), func(cur []byte) { waited <- cur })
		if err != nil {
			t.Error(err)
		}
		done <- l
	}()

	select {
	case cur := <-waited:
		if string(cur) != helperHolder {
			t.Fatalf("holder seen = %q, want %q", cur, helperHolder)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onWait not called")
	}
	select {
	case <-done:
		t.Fatal("acquired while child holds the lock")
	case <-time.After(200 * time.Millisecond):
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	var l *Lock
	select {
	case l = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("not acquired after child died")
	}
	if l == nil {
		t.FailNow()
	}
	defer func() { _ = l.Release() }()
	if b, _ := os.ReadFile(path); string(b) != "parent" {
		t.Fatalf("file = %q, want parent", b)
	}
}

func TestHolderInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.lock")

	first, err := Acquire(context.Background(), path, []byte("a-much-longer-holder-record"), nil)
	if err != nil {
		t.Fatal(err)
	}

	// A second acquirer sees the holder bytes, and ctx cancellation returns promptly.
	var seen []byte
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Acquire(ctx, path, []byte("b"), func(cur []byte) { seen = cur }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation not prompt")
	}
	if string(seen) != "a-much-longer-holder-record" {
		t.Fatalf("seen = %q", seen)
	}

	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	// A shorter record fully replaces the longer one.
	second, err := Acquire(context.Background(), path, []byte("b"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Release() }()
	if b, _ := os.ReadFile(path); string(b) != "b" {
		t.Fatalf("file = %q, want b", b)
	}
}
