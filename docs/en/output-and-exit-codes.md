# Output and exit codes

Every line sendaway adds begins with `[sendaway] ` and goes to stderr. When stdout and stderr are the same file, the lines and the command's output appear in arrival order. Lines are also written to the run's `output.log`.

Placeholders: `<host>` is the ssh alias, `<id>` a run id such as `r-20250101-120000-ab12`, `<n>` a number.

## Lines of a normal run

In the order they appear.

| Line | When |
|---|---|
| `waiting for lock (held by: <argv>, <time>, run <id>)` | Another run in the same project holds the local lock. `waiting for lock (held by unknown)` when the holder record is unreadable. |
| `waiting for remote lock (held by run <id>, <time>)` | The host's lock for the project is held, for example by another workstation. `... (held by unknown)` when unreadable. |
| `remote clock differs from local by <d>; make may misjudge mtimes` | Clock difference over 2 s. |
| `remote state missing; locally deleted files may linger — run 'sendaway forget' to start clean` | The host has a project directory but no record of the previous push. |
| `<host> <os>-<arch> · sync <s>s` | Start line, once the push has finished. `<os>-<arch>` is the host's, for example `linux-amd64`. |
| `some files vanished during sync` | rsync reported files that disappeared during the transfer. |
| `not sent (secret-like): <path>, ...` | Files withheld by the secret rules or `exclude:`. |
| `tree is <x> MiB, over 256 MiB; largest: <path> (<x> MiB), ...` | The manifest is larger than 256 MiB; names the five largest files. |
| `symlinks pointing outside the repository are sent but dangle on the host: <path>, ...` | Symlinks that leave the root. |
| `name differs only in case from the file on disk (case-only rename?): <path>, ...` | On a case-insensitive file system, a transferred name differs in case from the file on disk. |
| prepare output | Output of a `prepare:` command, each line prefixed. |
| (command output) | Unprefixed. |
| `artifacts not collected: <path> (<reason>), ...` | After a failed run, artifacts refused (`symlink`, `elf`, `macho`, `pe`, `ar`, `limit`). `artifacts not collected: <error>` if the copy failed. |
| `remote run modified <n> synced files (not copied back): <paths> … and <k> more — add this target to local: or list the files in pull:` | The command changed, replaced or deleted synced files that were not collected. At most five paths are named; `… and <k> more` appears only past five. |
| `<host> exit <n> · <s>s · run <id> · log <path>` | Trailer. `killed by signal <n>` replaces `exit <n>` when the command died by a signal. `<path>` is the run's `output.log`. |

The trailer is the last line of a completed run. It is also printed after an interrupt, with `killed by signal <n>` for the signal that interrupted the shim.

## Fallback

| Line | Meaning |
|---|---|
| `<host> unreachable (<reason>), running locally` | The network path to the host failed before it answered, and the command runs locally at lowered priority. `<reason>` is one of `Could not resolve hostname`, `Connection timed out`, `Operation timed out`, `No route to host`, `Connection refused`, `Network is unreachable`. |

## Failure lines (exit 125)

A failing run prints one of these as its last line and exits with 125. No trailer is printed.

| Line | Cause |
|---|---|
| `<file>:<line>: <message>` | Invalid `.sendaway.yml` or user configuration. |
| `no host configured (set host: or a matching route in <path>)` | No default host and no matching route. |
| `.sendaway.yml must sit at the git top level` | The configuration is in a subdirectory of the repository. |
| `git worktrees are not supported` | `.git` is not a directory. |
| `git submodule "<path>" is not supported` | The tree contains a submodule. |
| `nested git repository "<path>" is not supported` | A directory containing its own repository. |
| `path "<path>" is not stored in NFC on disk; rename to NFC` | A non-ASCII name stored in another Unicode form. |
| `argv[0] "<name>" contains '='` | The command word looks like an assignment. |
| `local lock: <error>` | The local lock could not be taken. |
| `protocol mismatch: local sendaway <v> speaks protocol <n>, remote sendaway <v> speaks protocol <n>; install the same version on both sides` | Different protocols on the two ends. |
| `connection lost during sync — nothing ran` | The connection dropped before the push finished. The command never started. |
| `connection lost during run — result unknown` | The connection dropped after the push. The command may have run. |
| `sync failed: <error>` | rsync failed. |
| `remote clock differs from local by <d> (limit <d>); sync the clocks` | Clock difference over 60 s. |
| `remote lock: <error>` | The host's lock could not be taken. |
| `prepare <command> exited <n>` | A `prepare:` command failed. |
| `pull path does not exist: <path>` | A `pull:` path is missing on the host. |
| `pull refused: <path> (<reason>), ...` | `pull:` paths that are symlinks, native binaries or archives. Nothing is copied. |
| `pull destination <path> is a symlink; refusing to write through it` | A local destination contains a symlink. |
| `pull failed: <error>` | The copy back failed. |
| `protocol error from remote-exec: <error>` | The host sent an unexpected frame. |
| `remote-exec exited <n> without a result` | The host process ended with no exit frame. |
| `remote-exec ended before the push` | The host session ended before it was ready. |
| `cancelled` | The host stopped the session (a cancel, a closed connection, or 30 s without a ping). |
| `record run: <error>` | The run record could not be written. |
| ssh's own message | The connection failed before hello for a reason that is not a fallback case, for example `Permission denied (publickey).` or `Host key verification failed.`. ssh's stderr is shown as is. When it is empty, the line is `remote-exec exited <n> without a result`. |

Commands other than a run print the same `[sendaway] <message>` form on failure, for example `[sendaway] fetch failed: <error>`, `[sendaway] no runs recorded` and `[sendaway] unknown command "<name>" (see 'sendaway help')`.

## Exit codes

| Code | Meaning |
|---|---|
| `0`-`255` | The command's own exit code, unchanged. |
| killed by a signal | sendaway dies by the same signal. If the signal is ignored in the shim's process, it exits with 128 plus the signal number. |
| `125` | sendaway failed, or the run could not be completed or collected. The last `[sendaway]` line gives the reason. |

Details:

- A command that exits normally gives that code, including 125 itself if the command chooses it. Check for the trailer to tell a command's 125 from sendaway's.
- A command killed by a signal on the host is reported as `killed by signal <n>`, and the shim dies by that signal. Its exit status is then 128 plus the signal number as the shell sees it.
- An interrupt of the shim (Ctrl-C, SIGTERM, SIGHUP) stops the remote process group, prints the trailer with `killed by signal <n>`, and ends the shim by that signal.
- A failure while collecting `pull:` paths (`pull refused`, `pull path does not exist`, `pull failed`) exits with 125 even when the command itself exited 0.
- `sendaway doctor` exits with 125 when any check is `FAIL`.
- A run that falls back to a local execution has the exit code of the local command.
