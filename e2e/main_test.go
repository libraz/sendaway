//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	// hostAlias is the ssh alias the user config names.
	hostAlias = "e2e"
	// sshUser owns the copies on the container.
	sshUser = "sa"
	// remoteHome is sshUser's home directory in the container.
	remoteHome = "/home/sa"
	// modulePkg is the sendaway main package.
	modulePkg = "github.com/libraz/sendaway/cmd/sendaway"
)

// suite is the container and binaries shared by every test in the package.
var suite struct {
	bin        string // local sendaway binary
	container  string // container name
	port       int    // host port forwarded to the container's sshd
	key        string // private key authorized in the container
	knownHosts string // known_hosts holding the container's host keys
	arch       string // container GOARCH
}

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	dir, err := os.MkdirTemp("", "sendaway-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()

	var undo []func()
	defer func() {
		for _, f := range slices.Backward(undo) {
			f()
		}
	}()
	if err := setup(dir, &undo); err != nil {
		fmt.Fprintln(os.Stderr, "e2e setup:", err)
		return 1
	}
	return m.Run()
}

// setup builds both binaries and the image, and starts the container. Every
// resource it creates is undone by the functions it appends to undo.
func setup(dir string, undo *[]func()) error {
	arch, err := dockerArch()
	if err != nil {
		return err
	}
	suite.arch = arch

	suite.bin = filepath.Join(dir, "darwin", "sendaway")
	if _, err := run(nil, "go", "build", "-o", suite.bin, modulePkg); err != nil {
		return err
	}
	linuxBin := filepath.Join(dir, "linux", "sendaway")
	cross := append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	if _, err := run(cross, "go", "build", "-o", linuxBin, modulePkg); err != nil {
		return err
	}

	suite.key = filepath.Join(dir, "id_ed25519")
	if err := keygen(suite.key); err != nil {
		return err
	}

	id := randomHex(4)
	tag := "sendaway-e2e:" + id
	*undo = append(*undo, func() { _, _ = run(nil, "docker", "rmi", "-f", tag) })
	if _, err := run(nil, "docker", "build", "-q", "-t", tag, "."); err != nil {
		return err
	}

	suite.container = "sendaway-e2e-" + id
	*undo = append(*undo, func() { _, _ = run(nil, "docker", "rm", "-f", suite.container) })
	if _, err := run(nil, "docker", "run", "-d", "--rm",
		"--name", suite.container,
		"--cpus=2",
		"--init", // reaps processes orphaned by a cancelled group
		"--cap-add=NET_ADMIN",
		"-p", "127.0.0.1::22",
		"-v", linuxBin+":/usr/local/bin/sendaway:ro",
		tag); err != nil {
		return err
	}

	if suite.port, err = sshPort(); err != nil {
		return err
	}
	pub, err := os.ReadFile(suite.key + ".pub")
	if err != nil {
		return err
	}
	install := `install -d -m 0700 -o sa -g sa ` + remoteHome + `/.ssh && ` +
		`cat > ` + remoteHome + `/.ssh/authorized_keys && ` +
		`chown sa:sa ` + remoteHome + `/.ssh/authorized_keys && chmod 0600 ` + remoteHome + `/.ssh/authorized_keys`
	if _, err := dockerExec("root", nil, bytes.NewReader(pub), "sh", "-c", install); err != nil {
		return err
	}

	suite.knownHosts = filepath.Join(dir, "known_hosts")
	return scanHostKeys(suite.knownHosts)
}

// dockerArch maps the daemon's architecture to a GOARCH.
func dockerArch() (string, error) {
	out, err := run(nil, "docker", "info", "--format", "{{.Architecture}}")
	if err != nil {
		return "", err
	}
	switch a := strings.TrimSpace(out); a {
	case "aarch64", "arm64":
		return "arm64", nil
	case "x86_64", "amd64":
		return "amd64", nil
	default:
		return "", fmt.Errorf("unsupported docker architecture %q", a)
	}
}

// sshPort returns the host port published for the container's port 22.
func sshPort() (int, error) {
	out, err := run(nil, "docker", "port", suite.container, "22/tcp")
	if err != nil {
		return 0, err
	}
	first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	i := strings.LastIndex(first, ":")
	if i < 0 {
		return 0, fmt.Errorf("unexpected docker port output %q", out)
	}
	return strconv.Atoi(first[i+1:])
}

// scanHostKeys waits for sshd and writes its host keys to path.
func scanHostKeys(path string) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		out, err := run(nil, "ssh-keyscan", "-T", "3", "-p", strconv.Itoa(suite.port), "127.0.0.1")
		if err == nil && strings.Contains(out, "ssh-ed25519") {
			return os.WriteFile(path, []byte(out), 0o600)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sshd did not answer on port %d: %w", suite.port, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// keygen writes a passphrase-less ed25519 key pair at path and path.pub.
func keygen(path string) error {
	_, err := run(nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "sendaway-e2e", "-f", path)
	return err
}

// run executes a command and returns its stdout; the error carries stderr.
// A nil env inherits the test process environment.
func run(env []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}

// dockerExec runs argv in the container as user with the extra variables in env.
func dockerExec(user string, env []string, stdin io.Reader, argv ...string) (string, error) {
	args := []string{"exec", "-u", user}
	if stdin != nil {
		args = append(args, "-i")
	}
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	args = append(append(args, suite.container), argv...)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = stdin
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return out.String(), fmt.Errorf("in container: %s: exit %d: %s", strings.Join(argv, " "), ee.ExitCode(), strings.TrimSpace(errOut.String()))
		}
		return out.String(), err
	}
	return out.String(), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
