# FAQ and troubleshooting

## Limitations

- Commands started by a local run stay local. A `local:` rule or the fallback keeps the whole process tree on the workstation.
- Interactive and tty commands are not supported. The remote command has no terminal and reads `/dev/null`.
- Git submodules, nested repositories, git worktrees and file names not stored in NFC are not supported and fail with exit 125 where detected.
- A process that calls `setsid` itself leaves the command's process group and is not stopped on interruption.
- One run per project at a time. Others wait for the lock.
- sendaway does not authenticate to private package registries, and does not send tokens in `.npmrc` or `.yarnrc.yml`.
- The synced tree is the working tree as of the start of the run. Files created on the host by the command stay there (ignored build directories survive between runs) but do not return unless listed in `pull:` or `artifacts:`.

## The host is unreachable, or the run fails at connection

**The command ran locally and a line says `unreachable (...)`.** The network path failed before the host answered: DNS, a timeout, a refused connection, no route. This is the fallback. Check the host, the VPN and the `Host` entry; `ssh build01 true` shows the same error.

**The run ends with `Permission denied (publickey).`** Authentication failures never fall back to a local run, since silently running somewhere else would hide a configuration problem. The ssh message is printed as is and the exit code is 125. Check that the key in the `Host` entry is loaded or readable without a prompt (`BatchMode` forbids passphrase prompts) and is authorized for the build user.

**`Host key verification failed`.** Run `ssh build01` by hand once and accept the key. `sendaway doctor` prints this hint as well. A `REMOTE HOST IDENTIFICATION HAS CHANGED` error is also fatal: verify the host, then fix `known_hosts`.

**`ssh control path ... is N bytes, over the 86 byte limit`.** The ssh control socket path under the state directory is too long for the system. Set `XDG_STATE_HOME` to a shorter directory.

**`connection lost during sync — nothing ran` or `connection lost during run — result unknown`.** The ssh connection dropped. The first message means the command never started; for the second, check the host (`sendaway shell`) before rerunning, since the command may have completed. The host stops the work itself after 30 s without hearing from the workstation.

**`sendaway: command not found` from ssh, or `sendaway not found or unreadable` from doctor.** The host cannot find `sendaway`. Install it where a non-login ssh finds it, normally `/usr/local/bin`.

**`protocol mismatch`.** The two ends run builds with different protocols. Build the Linux binary from the same source as the local one and reinstall it.

## Waiting for a lock

`waiting for lock (held by: ...)` means another run in the same project, on this workstation, holds the lock. `waiting for remote lock (held by run <id>, ...)` means the host's lock is held, typically by a run from another workstation or by `fetch` and `forget`. The wait ends when the holder finishes or dies; the kernel releases the lock when the process exits. Ctrl-C ends the wait. `sendaway runs` shows what is recorded.

## Clock skew

A host whose clock differs by more than 2 s from the workstation produces `remote clock differs from local by <d>; make may misjudge mtimes`. Beyond 60 s the run stops with `remote clock differs from local by <d> (limit <d>); sync the clocks`. Install chrony or another NTP client on the host and check `sendaway doctor`.

## Files modified remotely

`remote run modified N synced files (not copied back)` means the command changed, replaced or deleted tracked or synced files on the host (a formatter, a code generator, a snapshot update) and the changes did not reach the workstation. The next run overwrites them with the local version. Either run the command locally by adding it to `local:`, or list the generated files in `pull:` so they come back after a successful run. A file reported as modified only by its timestamp, such as one touched by a build tool, shows up here too.

## Tests that need secrets or ignored files

Secret-like files (`.env`, keys, credentials, token-bearing `.npmrc`) are never sent, and neither are files git ignores. A test that reads one fails on the host. Options:

- Run the test locally: `sendaway local make test`, or add the target to `local:`.
- Track the file in git, if it holds no secret. A tracked file is sent even if it matches an ignore pattern, unless a secret rule withholds it.
- Use a template. `.env.example`, `.env.sample` and `.env.template` are sent.

## MAKEFLAGS and other variables

`MAKEFLAGS`, `CMAKE_BUILD_PARALLEL_LEVEL`, `CTEST_PARALLEL_LEVEL`, `GOFLAGS`, `GOMAXPROCS` and a few others are forwarded, so a `-j2` set for the workstation applies on the host as well. To use the host's cores, unset or change them in the shell, or use a project-specific wrapper. `sendaway which <command>` lists which variables are forwarded or dropped for a command.

A variable you need on the host that is not forwarded goes into `env:` in `.sendaway.yml`. Variables with secret-like names or workstation-only values stay dropped even then.

## `remote state missing` note

```
[sendaway] remote state missing; locally deleted files may linger — run 'sendaway forget' to start clean
```

The host has a copy of the project but no record of what the last push contained. It was created by hand or its state was removed. sendaway cannot tell which files there were sent by an earlier run, so files deleted locally stay on the host. Run `sendaway forget` to delete the project directory and state on the host; the next run sends everything and re-runs every `prepare:` rule.

## rsync rejected: openrsync

`rsync protocol too old: rsync <version> speaks protocol 29, need 31 or newer (install rsync 3.x, e.g. Homebrew)` is macOS's built-in rsync. Install rsync 3.x with Homebrew and put it ahead of `/usr/bin` on PATH. `sendaway doctor` reports the version in use.

## ControlPath too long

See the `ssh control path` entry above.

## NFD names and unusual file names

`path "<path>" is not stored in NFC on disk; rename to NFC` means a file name with non-ASCII characters is stored on the local file system in a different Unicode normalization form than git records, a common outcome of files created on macOS. sendaway refuses to sync such a tree because the host would store a different name. Rename the file so its on-disk name is NFC (for example by recreating it from an NFC-typed name), then commit the rename.

`name differs only in case from the file on disk (case-only rename?)` is a warning, not a failure. On a case-insensitive file system, git and the disk can disagree on the case of a name after a case-only rename; the host receives the name git reports.

## A shim runs the wrong binary or loops

The shim finds the real command by searching PATH and skipping the shim directory, and it refuses to run itself. `... : executable not found` means no real binary exists on PATH besides the shim. Check `command -v <tool>` outside the shim directory. A binary named anything other than `sendaway` acts as a shim, so do not copy the binary under another name.

## Output lines appear before the output

The `[sendaway]` lines are on stderr. A tool between sendaway and the terminal that reorders stderr moves them. See [Coding agents](coding-agents.md#output-filters).
