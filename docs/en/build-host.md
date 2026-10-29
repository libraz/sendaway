# Build host

The build host is a Linux machine reached over ssh. It runs `sendaway remote-exec` on demand and starts the project's commands. It needs no daemon.

## Requirements

- Key authentication only. `AllowAgentForwarding no`, `AllowTcpForwarding no` and `ClientAliveInterval 15` in `sshd_config`.
- Time synchronization (chrony or similar). A clock difference over 2 s is noted during a run, and over 60 s fails it.
- No git credentials, no keys to other hosts, and no sudo for the user sendaway runs as.
- The same absolute parent path as on the workstation (for example `/Users/<name>`), created and owned by that user. A project at `/Users/alice/Projects/big-repo` is synced to `/Users/alice/Projects/big-repo` on the host.
- rsync 3.1 or later, and `sendaway` in `/usr/local/bin`, where a non-login ssh finds it.
- Login profile and rc files that print nothing to stdout.
- The project's toolchain on PATH in the login profile.

The sendaway on the host must speak the same protocol as the local one. `sendaway version` prints both on either side.

## Setup checklist

1. **Create a dedicated user** for builds, with key authentication only. This user owns every synced tree. Give it no sudo rights, no git credentials and no ssh keys to other machines. The trust model in [Security](security.md#why-there-is-no-trust-prompt) assumes the host holds nothing worth stealing.
2. **Create the parent path** the workstation uses, owned by that user. The sync creates missing directories below it with `mkdir -p`, so it needs only the parent. If the workstation path is `/Users/alice/Projects/...`, create `/Users/alice` as root once and give it to the build user.
3. **Harden sshd** with `PasswordAuthentication no`, `AllowAgentForwarding no`, `AllowTcpForwarding no` and `ClientAliveInterval 15`. The client already disables forwarding; the server settings make that independent of any one client's configuration.
4. **Synchronize the clock** with chrony or another NTP client. Clock skew makes `make` misjudge file times; sendaway notes a difference over 2 s and refuses one over 60 s.
5. **Install rsync** 3.1 or later through the distribution's package manager. Check with `rsync --version`: the line `protocol version` must read 31 or higher.
6. **Install sendaway** at `/usr/local/bin/sendaway`, built for the host's architecture (see [Getting started](getting-started.md#2-build-the-linux-binary-for-the-host)). `ssh build01 sendaway version` must work without a login shell.
7. **Put the toolchain on PATH in the login profile.** The command runs under `$SHELL -lc`, so a login shell reads the profile (for bash, `~/.bash_profile` or `~/.profile`) before the command starts. Compilers, `mise`, `cargo`, `go`, `node` and package managers must be found there. A PATH set only in an interactive rc file is not seen.
8. **Keep rc and profile files quiet.** Output from a non-login rc file corrupts the protocol framing on stdout. Output from the login profile appears in the command's own stdout. Guard banners, `echo` lines and `motd`-style output behind an interactive-terminal test.
9. **Add the ssh Host entry** on the workstation, run `ssh build01` once for the host key, and add the host to the user configuration (see [Getting started](getting-started.md)).
10. **Route heavy projects separately.** A host runs one run per project at a time, and runs of different projects compete for the same CPUs. Give a project that builds for a long time its own host with a `routes:` entry (see [Configuration](configuration.md#routes)).
11. **Run `sendaway doctor`** on the workstation.

## What doctor checks on the host

For each host in the user configuration (the default host and every route host) `sendaway doctor` runs these probes over ssh and prints `ok` or `FAIL` for each:

| Check | Passing line |
|---|---|
| `ssh <host> true` succeeds | `<host>: reachable` |
| That command prints nothing on stdout | `<host>: ssh prints nothing on stdout` |
| `sendaway version` runs and reports the local protocol | `<host>: sendaway <version> (protocol <n>)` |
| `date +%s` is within 2 s of the local clock | `<host>: clock within 2s` |
| Each installed shim name resolves in the login shell (`$SHELL -lc 'command -v <name>'`) | `<host>: <path>` |

A failed connection stops the checks for that host and prints the ssh error. A `Host key verification failed` error adds the hint `run 'ssh <host>' by hand once to add the host to known_hosts`.

The shim probe covers every shim in the shim directory, not only the commands a project uses. A shim for a tool that is not installed on the host reports `<host>: <name>: not found` as a failure. Either install the tool on the host or delete its link from the shim directory.

doctor does not check rsync on the host, the parent path, sshd settings or sudo rights. Those are covered by the checklist above.
