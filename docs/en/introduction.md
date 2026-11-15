# Introduction

sendaway runs heavy build and test commands on a remote Linux host without changing the commands or the tools that call them. It is a PATH shim: `make test` typed by a person, a script, Claude Code or Codex is intercepted, the working tree is synced to the host, the command runs there, and the output and exit code come back as a local run would produce them.

A project opts in with one `.sendaway.yml` at its git top level. Commands that match no rule run locally, unchanged.

## Mental model

![A command typed on the Mac passes through a PATH shim and is matched against the project rules; it either runs locally or has its filtered file set synced to the build host, runs there in its own process group, and streams output frames back before files are collected and the run is recorded.](../images/run-flow.svg)

1. **Shim.** The `sendaway` binary is linked under the names of the commands it should intercept (`make`, `go`, `yarn` and so on). When the binary runs under any other name than `sendaway`, it acts as a shim for that command.
2. **Rule match.** The shim finds `.sendaway.yml` above the current directory and compares the command line with the `remote:` and `local:` rules. A `local:` match wins. A command that matches nothing, or a directory with no `.sendaway.yml`, runs the real binary directly.
3. **One-way sync just before the run.** The files git tracks, plus untracked files git does not ignore, are copied to the same absolute path on the host with rsync (a [slot](configuration.md#slots) copy uses `<root>.slot-<n>`). Secret-looking files are held back. Nothing flows from the host to the Mac during the sync.
4. **Remote process group.** The host starts the command through a login shell in its own process group, with an environment rebuilt from a short allowlist.
5. **Frames back.** Output, notes and the exit status return over the single ssh connection as length-prefixed frames. The shim writes them to its own stdout and stderr and ends with the same exit code, or dies by the same signal.
6. **Collection.** Paths listed in `pull:` are copied back after a successful run, and paths listed in `artifacts:` after a failed one.

Every run is recorded locally with its output, so a result can be read again with `sendaway last`.

## What sendaway is not

- It installs no agent or daemon on the host. The host needs the same `sendaway` binary, started on demand over ssh as `sendaway remote-exec`.
- It does not sync continuously. The tree is copied once per run, immediately before the command.
- It does not balance load across hosts. A project resolves to exactly one host through the user configuration.
- It does not distribute a compilation across machines. One command runs on one host.
- It does not run interactive or tty commands. The command has no terminal and its stdin is `/dev/null`.
- It does not support git submodules, nested repositories, git worktrees, or file names that are not stored in NFC on disk. A tree containing them fails with exit 125 where detected.
- It does not clean up processes that leave the process group by calling `setsid` themselves.
- It does not authenticate to private package registries. The host holds no credentials, and `.npmrc` or `.yarnrc.yml` files carrying tokens are not sent.

## In use

sendaway runs the builds and tests of three of the author's projects: [formulon](https://github.com/libraz/formulon), [libsonare](https://github.com/libraz/libsonare) and [midi-sketch-bach](https://github.com/libraz/midi-sketch-bach). All three are C++ code bases whose test suites take minutes to tens of minutes. The workstation is one laptop with several coding-agent sessions open, limited to 8 local compile workers in total; the hosts are Linux machines with 24 to 40 cores. The figures come from the run records of November 2026. No job was timed both locally and remotely, so they show the load the hosts took, not a speed-up.

- Sync before a run: median 0.2 s in midi-sketch-bach and 0.4 s in formulon, at most 2.3 s.
- midi-sketch-bach: 68 runs in about a day, 12.4 hours of host time, median run 7 minutes, longest 49 minutes. Local load stayed around 9 to 13 meanwhile. Four sessions share the project, and with `slots: 2` three runs proceed at once. Before slots, each job waited 10 to 30 minutes behind the project lock.
- formulon: 25 runs, median just under 2 minutes, longest 48 minutes (a clean build followed by every test tier). Eight sessions across four repositories share the laptop.
- libsonare: only `cmake` and `ctest` checks go to the host. Its heaviest work scores renders against reference audio that is not in git, and its golden values are recorded on macOS, so both stay local.

What stays local in all three: work that reads files git does not carry, results that are only valid on macOS, and targets that drive a desktop application or a browser on the Mac.

## Where to go next

- [Getting started](getting-started.md) installs sendaway and runs a first command.
- [Configuration](configuration.md) documents both configuration files.
- [How it works](how-it-works.md) follows one run step by step.
- [Security](security.md) lists what reaches the host and what stays local.
- [Build host](build-host.md) describes the Linux side.
- [Coding agents](coding-agents.md) covers Claude Code and Codex.
- [Command reference](cli.md), [output and exit codes](output-and-exit-codes.md), the [glossary](glossary.md) and the [FAQ](faq.md) are reference pages.
