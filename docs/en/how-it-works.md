# How it works

This page follows one remote run from the shim's decision to the recorded result.

## 1. Decision

The shim finds `.sendaway.yml`, matches the command line against the rules (see [Configuration](configuration.md#remote-and-local-rule-syntax)) and takes one of three paths:

- No `.sendaway.yml`, or `SENDAWAY_PASSTHROUGH` set to a non-empty value: the real binary replaces the shim process, untouched.
- A `local:` match, or no match: the real binary is started with `SENDAWAY_PASSTHROUGH=1` added to its environment. Commands that process starts therefore stay local too.
- A `remote:` match: the remote run below.

The real binary is found by searching PATH while skipping the shim directory. `sendaway run <command>` forces the remote path and `sendaway local <command>` the local one, both ignoring the rules.

## 2. Preconditions

Before anything is sent, the shim checks the project root: `.sendaway.yml` must sit at the git top level, and `.git` must be a directory (git worktrees are refused). It then creates the run record directory, so even a failed run can be inspected.

The shim takes a per-project local lock (keyed by the root path). While another run in the same project holds it, the shim prints `waiting for lock (held by: <argv>, <time>, run <id>)` and waits. The lock is released when the holding process ends, however it ends.

## 3. File sets

The shim builds three sets with git, so negation patterns, `.git/info/exclude` and `core.excludesFile` behave as they do for git:

- **Manifest**: files in the work tree that git tracks or does not ignore, sorted. A tracked file that was deleted locally is not in it.
- **Ignored set**: untracked paths git ignores. They are never sent, and the host's copies of them are left alone, so build directories survive between runs.
- **Secret set**: manifest files withheld by the secret rules or by `exclude:` (see [Security](security.md#secret-files)). They are removed from the manifest and listed in `not sent (secret-like)`.

The manifest fails the run on a git submodule, a nested repository, or a file name that is not stored in NFC on disk. Symlinks that point outside the repository are sent but dangle on the host, and a warning names them. A manifest larger than 256 MiB produces a warning naming the five largest files.

## 4. Session

The run uses one ssh connection, started as `sendaway remote-exec` on the host and kept in the background for ten minutes by ssh's control master so later runs reconnect quickly. The session proceeds in this order:

1. **hello.** The host sends its protocol, version, OS and architecture. If the protocol differs, the run stops with the message `protocol mismatch: local sendaway <version> speaks protocol <n>, remote sendaway <version> speaks protocol <n>; install the same version on both sides`.
2. **request.** The shim sends one JSON line: the run id, root, working directory, command, forwarded environment, manifest, prepare rules, `pull:` and `artifacts:` paths, whether output is merged, and the send time.
3. **Remote lock.** The host takes a lock for the project root. While it is held by another session the host reports `waiting for remote lock (held by run <id>, <time>)`. The host also compares clocks here: a difference over 2 s is noted, over 60 s ends the run with `remote clock differs from local by <d> (limit <d>); sync the clocks`.
4. **Deletions.** The host deletes files from its copy that the previous run sent and this one does not, then removes parent directories that became empty. A path whose type changed (a file where a directory is needed, a directory where a file or symlink is needed) is cleared so the push can replace it. A directory that exists only on the host is never removed. Without remote state, the host notes `remote state missing; locally deleted files may linger — run 'sendaway forget' to start clean`.
5. **ready.** The host reports it is ready for the push.
6. **Push.** Two rsyncs run in parallel: one for the working tree, one for `.git`. The tree rsync is anchored at the root and excludes the ignored set, the secret set and `exclude:` entries. The `.git` rsync deletes stale files there but leaves `config`, `FETCH_HEAD`, `hooks/`, `logs/`, `*.lock` and `objects/tmp_*` alone. An rsync exit of 24 (files vanished) is tolerated and reported as `some files vanished during sync`.
7. **pushed.** The shim tells the host the push is complete and prints the start line and any warnings. The host saves the manifest as its record of what was sent, and writes a minimal `.git/config` with no remotes.
8. **Prepare.** Rules whose inputs changed run (see [Configuration](configuration.md#prepare)).
9. **Command.** The host starts the command and relays its output.

A session cancelled before `pushed` leaves the host's saved manifest as it was.

## 5. How the command is launched

The host starts the command in its own process group, with stdin on `/dev/null`, in the working directory the shim used (created if missing). It is launched through the user's login shell:

```
$SHELL -lc 'exec env -- "$@"' sendaway NAME=value ... command args...
```

The login shell reads the host profile, which sets PATH for the toolchain. The `env` call then applies the forwarded variables on top of whatever the profile set, so a forwarded `MAKEFLAGS` or `GOFLAGS` wins.

The environment the shell starts with is built from scratch: `HOME`, `USER`, `LOGNAME`, `SHELL` and `PATH` as they are in the ssh session on the host, the forwarded variables, and `SENDAWAY_PASSTHROUGH=1`. Nothing else crosses over, including locale variables that ssh might have forwarded. See [Security](security.md#environment-variables) for the allowlist.

## 6. Output

The host sends command output as framed chunks: `out` for stdout, `err` for stderr, `note` for sendaway's own messages from the host. The shim writes `out` to its stdout, `err` to its stderr, and notes as `[sendaway]` lines on stderr.

- **Merged.** When the shim's stdout and stderr are the same file (the same terminal, or `2>&1` into one file), the host puts the command's stdout and stderr on a single pipe, so their relative order is exactly as produced.
- **Split.** When they are different files, the host keeps two pipes. Each stream stays in order, but no ordering between the two is guaranteed.

Everything shown is also appended to the run's `output.log`, including sendaway's own lines. Anything between the shim and the terminal that reorders stderr (see [Coding agents](coding-agents.md#output-filters)) moves the `[sendaway]` lines relative to the output.

## 7. Interruption and liveness

The shim handles SIGINT, SIGTERM and SIGHUP. On the first one it sends a cancel frame, closes the session, waits up to 10 s for it to end, prints the trailer as `killed by signal <n>`, records the run, and then dies by the same signal.

On the host, a cancel frame sends SIGINT to the command's process group. Any other cause for stopping (the connection closing, a broken output pipe, or silence) sends SIGTERM. If the group is still running after a 5 s grace period it receives SIGKILL. The host then releases the lock.

The shim sends a ping every 5 s. The host stops the session when it hears nothing for 30 s, at any stage after the request: while waiting for the lock, during the push, in prepare or in the command. A dropped network therefore stops the remote work after at most 30 s of silence. Processes that moved themselves into a new session with `setsid` are outside the group and are not stopped.

## 8. Completion

When the command exits, the host decides what to bring back:

- Exit code 0 and no signal: the `pull:` paths.
- Anything else: the `artifacts:` paths.

It also lists the synced files whose size or modification time changed, or that no longer exist, other than the ones collected. The shim copies the collected files, then prints:

1. The modified-file warning when that list is not empty: `remote run modified N synced files (not copied back): <up to five paths> … and K more — add this target to local: or list the files in pull:`. A command that rewrites tracked files (a formatter, a code generator) belongs in `local:`, or its outputs in `pull:`.
2. The trailer: `[sendaway] <host> exit N · <seconds>s · run <id> · log <path>`, with `killed by signal N` in place of `exit N` when the command died by a signal.

A failure while collecting, such as a refused path in `pull:`, prints the reason, records the run as failed and exits with 125.

The shim then ends the way the command did: with its exit code, or by dying with the same signal. If the signal is ignored in the shim's process, it exits with 128 plus the signal number instead.

## 9. Run records

Each run has a directory `${XDG_STATE_HOME:-~/.local/state}/sendaway/runs/<id>/` (mode 0700) containing:

| File | Content |
|---|---|
| `output.log` | Everything shown during the run |
| `meta.json` | Command, working directory, root, host, times, exit code, signal, modified files, tree fingerprint, error text |
| `artifacts/` | Files collected after a failure |
| `fetched/` | Files brought back by `sendaway fetch` |

`meta.json` never contains environment variables. Run ids look like `r-20250101-120000-ab12` and encode the UTC start time.

After each recorded run, runs beyond the newest 100 and runs older than 14 days are deleted. `sendaway runs` lists the records and `sendaway last` summarizes the newest. Both mark a run with `(tree changed since this run)` when the project's current file sizes and times differ from what the run saw.

A run that fell back to local execution leaves no record.

## 10. Fallback

Before the host answers with hello, a failure of the network path makes the command run locally instead. The shim prints `[sendaway] <host> unreachable (<reason>), running locally`, drops the run record, releases the lock and starts the real command with `SENDAWAY_PASSTHROUGH=1` and a lowered priority (nice 10).

The reason is the matched ssh message: `Could not resolve hostname`, `Connection timed out`, `Operation timed out`, `No route to host`, `Connection refused` or `Network is unreachable`.

The fallback never applies to `Permission denied`, `Host key verification failed` or `REMOTE HOST IDENTIFICATION HAS CHANGED`, even when the same ssh output also contains an unreachable message. These end the run with exit 125 and ssh's own message. Failures after hello do not fall back either, because the host may already hold part of the work: a connection that drops before the push finishes reports `connection lost during sync — nothing ran`, and one that drops later reports `connection lost during run — result unknown`. Both exit with 125.
