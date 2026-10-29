package secret

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func noRead(string) ([]byte, error) {
	return nil, errors.New("unexpected read")
}

func TestSecretPath(t *testing.T) {
	cases := []struct {
		name     string
		rel      string
		excludes []string
		secret   bool
	}{
		{"dotenv", ".env", nil, true},
		{"nested dotenv", "app/.env", nil, true},
		{"dotenv upper case", ".ENV", nil, true},
		{"dotenv variant", ".env.local", nil, true},
		{"dotenv example", ".env.example", nil, false},
		{"dotenv sample upper case", ".env.SAMPLE", nil, false},
		{"dotenv template", ".env.template", nil, false},
		{"pem upper case", "keys/server.PEM", nil, true},
		{"key", "a.key", nil, true},
		{"p12", "b.p12", nil, true},
		{"pfx", "c.pfx", nil, true},
		{"jks", "d.jks", nil, true},
		{"keystore", "e.keystore", nil, true},
		{"ssh key", "id_ed25519_work", nil, true},
		{"ssh key pub", "id_rsa.pub", nil, true},
		{"netrc", ".netrc", nil, true},
		{"git credentials", ".git-credentials", nil, true},
		{"pypirc", ".pypirc", nil, true},
		{"tfstate", "infra/terraform.tfstate", nil, true},
		{"tfstate backup", "terraform.tfstate.backup", nil, true},
		{"credentials json", "credentials.json", nil, true},
		{"service account", "service-account-prod.json", nil, true},
		{"secrets dir", "secrets/x.txt", nil, true},
		{"secrets dir upper case", "Secrets/x", nil, true},
		{"ssh dir", ".ssh/config", nil, true},
		{"aws dir", ".aws/credentials", nil, true},
		{"gnupg dir", "x/.gnupg/a", nil, true},
		{"source", "src/main.go", nil, false},
		{"environment name", "config/environment.ts", nil, false},
		{"exclude path", "fixtures/private/a.txt", []string{"fixtures/private"}, true},
		{"exclude itself", "fixtures/private", []string{"fixtures/private"}, true},
		{"exclude trailing slash", "fixtures/private/a", []string{"fixtures/private/"}, true},
		{"exclude prefix sibling", "fixtures/privateer.txt", []string{"fixtures/private"}, false},
		{"exclude other", "fixtures/x", []string{"fixtures/private"}, false},
		{"exclude root", "src/a.go", []string{"."}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Check(c.rel, c.excludes, noRead) != ""
			if got != c.secret {
				t.Errorf("Check(%q) secret = %v, want %v", c.rel, got, c.secret)
			}
		})
	}
}

func TestSecretRC(t *testing.T) {
	cases := []struct {
		name    string
		rel     string
		content string
		secret  bool
	}{
		{"npmrc auth token", ".npmrc", "registry=https://r.example/\n//r.example/:_authToken=abc\n", true},
		{"npmrc legacy auth", ".npmrc", "_auth=dXNlcjpwYXNz\n", true},
		{"npmrc password", ".npmrc", "//r.example/:_password=c2VjcmV0\n", true},
		{"yarnrc auth token", ".yarnrc.yml", "npmAuthToken: abc\n", true},
		{"yarnrc ident", ".yarnrc.yml", "npmAuthIdent: user:pass\n", true},
		{"plain registry", ".npmrc", "registry=https://registry.npmjs.org/\n", false},
		{"comment mentioning token", ".npmrc", "# token is read from the CI environment\n", false},
		{"empty", ".npmrc", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, c.rel), []byte(c.content), 0o600); err != nil {
				t.Fatal(err)
			}
			read := func(rel string) ([]byte, error) {
				return os.ReadFile(filepath.Join(dir, rel))
			}
			got := Check(c.rel, nil, read) != ""
			if got != c.secret {
				t.Errorf("Check(%q) secret = %v, want %v", c.rel, got, c.secret)
			}
		})
	}

	failing := func(string) ([]byte, error) { return nil, errors.New("gone") }
	if Check(".npmrc", nil, failing) == "" {
		t.Error("unreadable .npmrc was not treated as secret")
	}
}
