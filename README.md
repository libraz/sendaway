# sendaway

[![CI](https://img.shields.io/github/actions/workflow/status/libraz/sendaway/ci.yml?branch=main&label=CI)](https://github.com/libraz/sendaway/actions)
[![Version](https://img.shields.io/github/v/release/libraz/sendaway?label=version)](https://github.com/libraz/sendaway/releases)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](https://github.com/libraz/sendaway/blob/main/LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26%2B-blue?logo=go)](https://go.dev/)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey)](https://github.com/libraz/sendaway)
[![Go Reference](https://pkg.go.dev/badge/github.com/libraz/sendaway.svg)](https://pkg.go.dev/github.com/libraz/sendaway)

**sendaway runs heavy build and test commands on a remote Linux machine, without changing the commands or the tools that call them.** It is a PATH shim: a `make test` typed by you, a script, Claude Code or Codex syncs the working tree, runs on the host, and returns the same output and exit code. A project opts in with one `.sendaway.yml`, and commands that match no rule run locally, unchanged.

> **Status:** pre-1.0. The `.sendaway.yml` schema, the output lines and the wire protocol may change between releases. Not independently audited.

## What it does

- **Transparent routing** — rules in `.sendaway.yml` match the command line (`[make]`, `[go, test]`); `local:` wins over `remote:`.
- **Faithful results** — stdout and stderr order, exit codes and fatal signals come back as a local run would produce them. Ctrl-C and SIGTERM stop the remote process group.
- **Git-defined sync** — the files sent are those git tracks or does not ignore. Secret-looking files, `.git/config` and `.npmrc` lines holding tokens are never sent.
- **Per-project hosts** — `~/.config/sendaway/config.yml` maps project paths to ssh host aliases, so hosts and addresses stay out of repositories.
- **Local fallback** — if the host is unreachable (DNS, timeout, refused), the command runs locally with a notice. Auth and host-key failures do not fall back.
- **Run records** — every run keeps its output and metadata; `sendaway last` summarizes the latest.

## Installation

```sh
go install github.com/libraz/sendaway/cmd/sendaway@latest
```

The build host needs the same version built for Linux, in `/usr/local/bin`:

```sh
GOOS=linux GOARCH=amd64 go build -o sendaway ./cmd/sendaway   # GOARCH=arm64 for an Arm host
```

`sendaway version` prints the version and protocol; both sides must agree on the protocol.

## Getting started

1. Run `ssh build01` by hand once so the host key lands in `known_hosts`. Connections use `BatchMode`, and a host key prompt fails.
2. Add a `Host build01` entry to `~/.ssh/config` with the address, user and key. sendaway reads connection details only from there.
3. Create `~/.config/sendaway/config.yml`:

   ```yaml
   host: build01                  # default ssh alias
   routes:                        # longest path-component match wins
     - path: ~/Projects/big-repo
       host: build02
   ```

4. In each project root (the git top level), run `sendaway init` to write a `.sendaway.yml` from the files it finds, then edit it:

   ```yaml
   remote: [[make], [ctest]]
   local: [[make, format], [make, lint]]
   env: [TEST_FILTER, MYAPP_*]      # extra variable names to forward
   prepare:                         # run on the host when these files change
     - when: [yarn.lock, package.json]
       run: [yarn, install, --immutable]
   pull: [dist/wasm]                # copied back on exit 0
   artifacts: [build/Testing/Temporary/LastTest.log]   # copied back on failure
   exclude: [fixtures/private]      # never sent
   ```

5. Run `sendaway install-shims`, then put the printed `export PATH=...` line at the end of `~/.zshrc`, after any `mise activate`. The default shims are make, cmake, ctest, go, cargo, yarn, npm, pnpm and bun.
6. Run `sendaway doctor`. It checks that each shim comes first on PATH, the local rsync, the control directory, and each host's reachability, version, clock and shell output.
7. If an output filter such as RTK rewrites commands, add the shim names to its exclude list (RTK: `exclude_commands`). The contract lines go to stderr, and a filter that reorders streams moves them ahead of the output.

`sendaway which make test` shows where a command would run, the host and the forwarded variables. `sendaway run <cmd>` and `sendaway local <cmd>` ignore the rules. `sendaway forget` removes the project's copy and state from the host.

## Build from source

```sh
make build    # bin/sendaway
make test     # unit tests with -race, then startup-time checks
make lint     # golangci-lint and actionlint
make e2e      # end-to-end tests against a Docker sshd
```

The binary must stay named `sendaway`: under any other name it acts as a shim for that command.

## Output lines and exit codes

Every line sendaway adds goes to stderr (the same file when stdout and stderr are one file).

| Line or code | Meaning |
|---|---|
| `[sendaway] build01 linux-amd64 · sync 0.42s` | Start line, after the sync |
| `[sendaway] not sent (secret-like): …` | Files held back by the secret filter |
| `[sendaway] remote run modified N synced files (not copied back): …` | The remote command rewrote synced files; add the target to `local:` or list the files in `pull:` |
| `[sendaway] build01 exit 1 · 42.3s · run r-… · log <path>` | Trailer; `killed by signal 15` replaces `exit N` on a signal death |
| `[sendaway] build01 unreachable (<reason>), running locally` | Fallback to a local run |
| `[sendaway] waiting for lock (held by: <argv>, <time>, run <id>)` | Another run in the same project holds the lock; `waiting for remote lock …` is the host-side form |
| `[sendaway] connection lost during sync — nothing ran` | Exit 125; the command never started |
| `[sendaway] connection lost during run — result unknown` | Exit 125; the command may have run |
| exit `0`–`255` | The command's own exit code |
| killed by signal | sendaway dies by the same signal |
| exit `125` | sendaway itself failed; the preceding line gives the reason |

## Requirements

Build host (Linux):

- Key authentication only, `AllowAgentForwarding no`, `AllowTcpForwarding no`, `ClientAliveInterval 15`.
- Time synchronization (chrony or similar). A clock difference over 2s is noted, over 60s fails the run.
- No git credentials, no keys to other hosts, and no sudo for the user sendaway runs as.
- The same absolute parent path as on the Mac (for example `/Users/<name>`), created and owned by that user.
- rsync 3.1 or later, and `sendaway` in `/usr/local/bin`, where a non-login ssh finds it.
- rc and profile files that print nothing to stdout. Output from a non-login rc breaks the framing; profile output leaks into the command's stdout.

Local: Homebrew rsync 3.x first on PATH, because macOS ships openrsync. Git and OpenSSH are required.

## Limitations

- Commands started by a local run stay local; a `local:` rule or the fallback keeps the whole process tree on the Mac.
- Variables such as `MAKEFLAGS` and `CMAKE_BUILD_PARALLEL_LEVEL` are forwarded, so a `-j2` set for your Mac applies on the host. `sendaway which` lists what is forwarded or dropped.
- Tests that need secret files must be kept local. Tests that read ignored files fail remotely unless the file is tracked.
- Interactive and tty commands, submodules, nested repositories, git worktrees and non-NFC file names are not supported (exit 125 where detected).
- Processes that call `setsid` themselves are not cleaned up on interruption.
- One run per project at a time; others wait.

## For coding agents

Paste into `AGENTS.md` or `CLAUDE.md`:

```markdown
Builds and tests here run on a remote host through sendaway shims. Use the usual commands (`make test`, `ctest`).
stderr ends with `[sendaway] <host> exit N · <time> · run <id> · log <path>`; N is the command's exit code.
Exit 125 means sendaway failed, not the command; the preceding `[sendaway]` line says why.
`sendaway last` summarizes the latest run, and the full output is in its log path.
If the trailer warns that the remote run modified files, add the target to `local:` or list the files in `pull:`.
Tests that need secret files run with `sendaway local <cmd>`.
```

## License

[Apache-2.0](LICENSE)
