# Coding agents

Claude Code and Codex run build and test commands through a shell. Because sendaway is a PATH shim, an agent that types `make test` gets a remote run without any change to its tools or prompts. This page covers what has to be true for the shims to take effect and how the agent should read the result.

## Shims must be first on PATH in the agent's shell

The agent only reaches sendaway when `make`, `go` and the other shimmed names resolve to the shim directory in the shell the agent uses. That shell is not always the one a person types into:

- An agent that starts non-interactive shells may skip `~/.zshrc`. Put the `export PATH=...` line printed by `sendaway install-shims` in a file that shell does read, after any `mise activate` or other tool that prepends to PATH.
- Check from inside the agent's session rather than from a separate terminal: `command -v make` must print a path in `${XDG_DATA_HOME:-~/.local/share}/sendaway/bin`, and `sendaway which make test` should report `decision: remote`.
- `sendaway doctor` reports for each shim whether it comes first on PATH in the shell that runs doctor. Run it from the agent's shell as well.

A command given with an absolute path (`/usr/bin/make`) bypasses the shim, because the shim is selected by name on PATH.

## Instructions for the agent

Add this to `AGENTS.md` or `CLAUDE.md` in the project:

```markdown
Builds and tests here run on a remote host through sendaway shims. Use the usual commands (`make test`, `ctest`).
stderr ends with `[sendaway] <host> exit N · <time> · run <id> · log <path>`; N is the command's exit code.
Exit 125 means sendaway failed, not the command; the preceding `[sendaway]` line says why.
`sendaway last` summarizes the latest run, and the full output is in its log path.
If the trailer warns that the remote run modified files, add the target to `local:` or list the files in `pull:`.
Tests that need secret files run with `sendaway local <cmd>`.
```

## Reading the result

- The trailer is the last line of stderr. `exit N` is the command's own exit code, the same value the agent's shell sees. `killed by signal N` means the command died from a signal.
- Exit 125 is never a command's code. No trailer is printed; the last `[sendaway]` line says why, for example `connection lost during run — result unknown`.
- `sendaway last` prints the command, host, times, result, any modified files and the path of `output.log`. The log holds the full output of the run.
- A warning `remote run modified N synced files (not copied back)` means the command rewrote tracked files on the host and the local tree did not receive them. A formatter or code generator belongs in `local:`, or its outputs in `pull:`.
- `sendaway fetch <path>` brings a root-relative file from the host into the latest run's `fetched/` directory without touching the local tree.

## Output filters

The `[sendaway]` lines go to stderr. A tool that rewrites or compresses command output and reorders the two streams moves the trailer ahead of the command's output. Add the shim names to the filter's exclude list so those commands run unfiltered. For RTK this is `exclude_commands` in its configuration:

```toml
[hooks]
exclude_commands = ["make", "cmake", "ctest", "go", "cargo", "yarn", "npm", "pnpm", "bun"]
```

When stdout and stderr go to the same file, sendaway keeps the command's output and its own lines in arrival order (see [How it works](how-it-works.md#6-output)). A filter that separates the streams removes that guarantee.

## Background tasks and the lock

Only one run per project executes at a time. A second command waits and prints

```
[sendaway] waiting for lock (held by: make test, 12s, run r-20250101-120000-ab12)
```

until the first finishes. An agent that starts a long build in the background and then runs another command in the same project sees that wait. The wait is not a hang; use `sendaway runs` to see what is recorded, and stop the first command to release the lock. The lock is released when the holding process ends for any reason.

Stopping an agent command with Ctrl-C or a signal sends a cancel to the host, which stops the command's process group (SIGINT, then SIGKILL after a 5 s grace period). A shell that is killed outright without signalling the shim leaves the host to notice the lost connection, which it does after 30 s without a ping.

## Commands that stay local

Rules decide by command line, so an agent can run these without special handling:

- `local:` rules, which win over `remote:`, keep formatters, linters and dependency installs on the workstation.
- `sendaway local <command>` forces a local run, for tests that need secret files or ignored files.
- `sendaway run <command>` forces a remote run regardless of the rules.
