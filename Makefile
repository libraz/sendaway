# Tool versions are pinned in tools/go.mod, separate from the main module.
# actionlint is built against a go.yaml.in/yaml/v4 release candidate that
# gosec (inside golangci-lint) has moved past, so it runs from its own module.
TOOL := go tool -modfile=tools/go.mod
ACTIONLINT := go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

.DEFAULT_GOAL := build

.PHONY: help build fmt format fmt-check format-check lint vet vuln test e2e

help: ## Show this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

build: ## Build bin/sendaway
	go build -o bin/sendaway ./cmd/sendaway

fmt: ## Format Go sources (gofmt -s, goimports)
	$(TOOL) golangci-lint fmt

format: fmt ## Alias of fmt

fmt-check: ## Fail if any Go source needs formatting
	$(TOOL) golangci-lint fmt --diff

format-check: fmt-check ## Alias of fmt-check

lint: ## Static analysis (golangci-lint, actionlint); changes nothing
	$(TOOL) golangci-lint run ./...
	$(ACTIONLINT)

vet: ## go vet
	go vet ./...

vuln: ## Report known vulnerabilities reachable from the code
	$(TOOL) govulncheck ./...

test: ## Unit tests with the race detector, then startup-time checks without it
	go test -race ./...
	go test -count=1 -run 'Overhead' ./...

e2e: ## End-to-end tests against an sshd container (needs Docker)
	go test -tags e2e -p 1 -count=1 ./e2e/
