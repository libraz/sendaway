//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"
)

const (
	// remoteToken stands in for a credential embedded in the origin URL.
	remoteToken = "ghp_e2eRemoteUrlToken0123456789"
	// langValue is sent by ssh through SendEnv LANG.
	langValue = "fr_FR.UTF-8"
)

// envScript prints the child's environment, a separator, then the
// environment of its parent, remote-exec.
const envScript = `env; echo ---; tr '\0' '\n' < /proc/$PPID/environ`

func TestS12Secrets(t *testing.T) {
	e := newEnv(t)
	e.writeFile(".gitignore", "dist\n")
	e.writeFile("main.txt", "source\n")
	e.writeFile(".env", "API_SECRET=dotenv-secret\n")
	e.writeFile(".npmrc", "//registry.npmjs.org/:_authToken=npm-secret-token\n")
	e.writeFile(".sendaway.yml", "env: [MYAPP_*, DEPLOY_TOKEN]\n")
	e.git("add", "main.txt", ".gitignore", ".npmrc", ".sendaway.yml")
	e.git("remote", "add", "origin", "https://x-access-token:"+remoteToken+"@github.com/example/repo.git")

	r := e.sendawayEnv([]string{
		"GITHUB_TOKEN=ghp_e2eEnvToken",
		"SSH_AUTH_SOCK=/tmp/e2e-agent.sock",
		"LANG=" + langValue,
		"DEPLOY_TOKEN=deploy-secret",
		"MYAPP_VISIBLE=forwarded",
	}, "run", "sh", "-c", envScript)
	mustSucceed(t, "env run", r)

	child, parent, ok := strings.Cut(r.stdout, "\n---\n")
	if !ok {
		t.Fatalf("no separator in output:\n%s", r.stdout)
	}
	if !hasVar(parent, "LANG", langValue) {
		t.Fatalf("LANG did not reach remote-exec through SendEnv, so the check below proves nothing:\n%s", parent)
	}
	if !hasVar(child, "MYAPP_VISIBLE", "forwarded") {
		t.Errorf("env: forwarding is broken, MYAPP_VISIBLE missing:\n%s", child)
	}
	for _, name := range []string{"GITHUB_TOKEN", "SSH_AUTH_SOCK", "LANG", "DEPLOY_TOKEN"} {
		for l := range strings.SplitSeq(child, "\n") {
			if strings.HasPrefix(l, name+"=") && (name != "LANG" || strings.Contains(l, langValue)) {
				t.Errorf("child environment has %s", l)
			}
		}
	}
	if !strings.Contains(r.stderr, "[sendaway] not sent (secret-like): ") {
		t.Errorf("no not-sent line: %s", r)
	}

	e.expectKinds(map[string]string{"main.txt": "file", ".env": "none", ".npmrc": "none", ".git/config": "file"})
	if out, _ := e.remoteSh(`grep -c "$1" "$ROOT/.git/config"`, remoteToken); strings.TrimSpace(out) != "0" {
		t.Errorf("remote .git/config matches the token: grep -c printed %q", out)
	}
	if !strings.Contains(e.git("config", "remote.origin.url"), remoteToken) {
		t.Fatal("local origin URL lost its token; the check above proves nothing")
	}

	// A pull destination that is a local symlink out of the repository.
	outside := t.TempDir()
	if err := os.Symlink(outside, e.path("dist")); err != nil {
		t.Fatal(err)
	}
	e.writeFile(".sendaway.yml", "pull: [dist]\n")
	r = e.sendaway("run", "sh", "-c", "mkdir -p dist && echo built > dist/app.txt")
	if r.code != 125 {
		t.Errorf("pull through a symlink: want 125, got %s", r)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Errorf("pull wrote outside the repository: %v %v", entries, err)
	}
}

// hasVar reports whether the env listing holds name=value.
func hasVar(listing, name, value string) bool {
	for l := range strings.SplitSeq(listing, "\n") {
		if l == name+"="+value {
			return true
		}
	}
	return false
}

func TestS12Forget(t *testing.T) {
	e := newEnv(t)
	e.writeFile("main.txt", "source\n")
	mustSucceed(t, "run", e.sendaway("run", "true"))
	if _, err := e.remoteSh(`test -f "$ROOT/main.txt" && test -d "$1"`, e.remoteStateDir()); err != nil {
		t.Fatalf("remote copy or state missing after a run: %v", err)
	}

	mustSucceed(t, "forget", e.sendaway("forget"))
	if _, err := e.remoteSh(`! test -e "$ROOT" && ! test -e "$1"`, e.remoteStateDir()); err != nil {
		t.Errorf("remote copy or state left after forget: %v", err)
	}
}
