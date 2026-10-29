# Command reference

```
sendaway <command> [arguments]
```

The binary acts as `sendaway` only under that name. Under any other name (the shim links) it acts as a shim for that command and ignores the table below.

| Command | Purpose |
|---|---|
| [`init`](#init) | Write a `.sendaway.yml` for the current directory |
| [`install-shims`](#install-shims) | Link PATH shims and print the PATH line |
| [`doctor`](#doctor) | Check shims, rsync, the control directory and each host |
| [`which`](#which) | Show where a command would run |
| [`run`](#run) | Run a command on the host, ignoring the rules |
| [`local`](#local) | Run a command locally, ignoring the rules |
| [`fetch`](#fetch) | Copy paths from the host into the latest run |
| [`runs`](#runs) | List recorded runs |
| [`last`](#last) | Summarize the latest run |
| [`shell`](#shell) | Open a login shell in the project directory on the host |
| [`forget`](#forget) | Remove the project's copy and state from the host |
| [`version`](#version) | Print the version and protocol |
| [`help`](#help) | Print the command list |

Exit codes in general: 0 on success, 125 when sendaway fails. `run` ends with the command's own exit code or signal. A failure prints `[sendaway] <reason>` on stderr. A wrong number of arguments prints `[sendaway] usage error: <detail>`. Details are in [Output and exit codes](output-and-exit-codes.md).

Commands that talk to the host (`run`, `fetch`, `shell`, `forget`) need a `.sendaway.yml` above the current directory and a host from the [user configuration](configuration.md#user-configuration). They fail with `.sendaway.yml not found above the current directory` or `no host configured (set host: or a matching route in <path>)`.

## init

```
sendaway init
```

Writes `.sendaway.yml` in the current directory from the marker files found there, and prints `wrote <path> (N remote, N local, N prepare rules)`. Unused keys are written as commented empty lists. Fails with `<path> already exists` if the file exists; it never overwrites.

| Marker files | Rules written |
|---|---|
| `Makefile` | remote `[make]`; local `[make, format]`, `[make, fmt]`, `[make, format-check]`, `[make, lint]` |
| `CMakeLists.txt` | remote `[cmake]`, `[ctest]` |
| `go.mod` | remote `[go, test]`, `[go, build]`, `[go, vet]` |
| `package.json` and `yarn.lock` | remote `[yarn, test]`, `[yarn, build]`; local `install`, `add`, `remove`, `dev`, `lint`, `format`; prepare `yarn install --immutable` on `yarn.lock`, `package.json` |
| `package.json` and `pnpm-lock.yaml` | the same with `pnpm`; prepare `pnpm install --frozen-lockfile` |
| `package.json` and `bun.lock` | remote `[bun, test]`, `[bun, run, build]`; local `install`, `add`, `remove`, `[bun, run, dev]`; prepare `bun install --frozen-lockfile` |
| `Cargo.toml` | remote `[cargo, test]`, `[cargo, build]`; local `[cargo, fmt]`, `[cargo, add]` |
| `mise.toml` | prepare `mise trust --yes` and `mise install` on `mise.toml` |

Exit 0, or 125 on failure or when arguments are given.

## install-shims

```
sendaway install-shims [names...]
```

Links each name to the running binary in `${XDG_DATA_HOME:-~/.local/share}/sendaway/bin`, replacing existing links, and prints the number linked and the PATH line:

```
linked 9 shims in /Users/you/.local/share/sendaway/bin

Add this line at the end of ~/.zshrc, after any 'mise activate':

export PATH="/Users/you/.local/share/sendaway/bin:$PATH"
```

Without names it links `make cmake ctest go cargo yarn npm pnpm bun`. A name that is empty, `sendaway` or contains `/` is rejected (`invalid shim name`), and an existing file in the directory that is not a symlink is not replaced. Exit 0, or 125 on failure.

## doctor

```
sendaway doctor
```

Prints findings in sections, each line starting with `ok` or `FAIL`:

- `shims (<dir>)`: for each installed shim, whether it comes first on PATH. Messages: `<name>: the shim comes first on PATH`, `<name>: <path> comes before the shim; put <dir> first in PATH`, `<name>: not found on PATH; add <dir> to PATH`. With no shims: `no shims installed; run 'sendaway install-shims'`.
- `local tools`: the local rsync (`rsync <version> (protocol <n>)`, failing when the protocol is below 31) and the `cm` control directory (`<dir> is private (0700)`, `<dir> does not exist yet`, or a failure if the mode is not 0700).
- `host <name>` for each distinct host in the user configuration: the checks listed in [Build host](build-host.md#what-doctor-checks-on-the-host). With no host configured: `no host configured in <path>`.

Each ssh probe times out after 30 s. Exit 0 when every line is `ok`, 125 when any is `FAIL`.

## which

```
sendaway which <command> [arguments...]
```

Explains where the command line would run from the current directory, without running anything:

```
command:  make test
decision: remote (matches remote rule [make])
config:   /Users/you/Projects/app/.sendaway.yml
host:     build01
env forwarded: CI, MAKEFLAGS
env dropped:   MYAPP_TOKEN (secret-like name)
(commands started by a local run stay local)
```

`decision` is one of `local (matches local rule [..])`, `remote (matches remote rule [..])`, `local (no rule matches)`, `local (SENDAWAY_PASSTHROUGH is set)` or `local (.sendaway.yml not found above the current directory)`. In the last two cases only the `command:` and `decision:` lines are printed (plus the closing line). `host:` reads `(none configured)` when no host resolves. An empty list prints `(none)`. Exit 0, or 125 on a configuration error or without a command.

## run

```
sendaway run <command> [arguments...]
```

Runs the command on the host regardless of the rules, with the full sequence in [How it works](how-it-works.md). Needs a project. The exit code is the command's, `125` for a sendaway failure, and sendaway dies by the signal when the command did. If the host is unreachable it falls back to a local run like a shim does.

## local

```
sendaway local <command> [arguments...]
```

Runs the real command locally regardless of the rules, with `SENDAWAY_PASSTHROUGH=1` set so commands it starts stay local. The shim directory is skipped when looking up the command. The process is replaced by the command, so its exit code and signals are the command's. Works outside a project. Exit 125 if the command cannot be found or started.

## fetch

```
sendaway fetch <path...>
```

Copies root-relative paths from the host's copy of the project into the `fetched/` directory of the most recent recorded run (`${XDG_STATE_HOME:-~/.local/state}/sendaway/runs/<id>/fetched/`), keeping their relative layout. The local tree is not changed. The most recent run is the newest record on this machine, which is not necessarily one of this project.

The same checks as `pull:` apply: directories are copied recursively; symlinks, native binaries and archives are refused (`pull refused: <path> (<reason>)`); a missing path is an error. Prints nothing on success. Exit 0, or 125 on failure, including `no runs recorded` when no run exists. Fetch waits for the host's lock for the project, so it queues behind a running command.

## runs

```
sendaway runs
```

Lists recorded runs, newest first, one per line:

```
r-20250101-120000-ab12  build01  exit 1  42.3s  make test
```

The fields are the run id, host, result (`exit N`, `signal N`, or `failed` when sendaway failed), the duration and the command line. A run whose project tree now differs from what it saw is followed by `  (tree changed since this run)`. With no records it prints `no runs recorded` and exits 0.

## last

```
sendaway last
```

Summarizes the newest run:

```
run:      r-20250101-120000-ab12
command:  make test
cwd:      /Users/you/Projects/app
host:     build01
started:  2025-01-01T12:00:00+09:00
duration: 42.3s (sync 400ms)
result:   exit 1
modified: src/gen.c, src/gen.h
log:      /Users/you/.local/state/sendaway/runs/r-20250101-120000-ab12/output.log
(tree changed since this run)
```

`error:` appears for a run that failed inside sendaway, `modified:` when the host changed synced files, and the final line when the tree changed since the run. Exit 0, or 125 with `no runs recorded` when there is none.

## shell

```
sendaway shell
```

Replaces the process with an interactive ssh session to the project's host, in the project directory (`cd <root> && exec "$SHELL" -l`), with a tty. The connection shares the options of a run, minus `BatchMode` and `-T`, so a password prompt works. Nothing is synced; the host shows the tree as the last run left it. Exit status is ssh's. Use it to inspect a failed run's directory on the host.

## forget

```
sendaway forget
```

Removes the project's directory on the host and sendaway's remote state for it (the saved manifest and prepare hashes), after taking the host's lock for the project. On success it prints `[sendaway] <host>: removed <root> and its state`. The next run sends the whole tree again and re-runs every `prepare:` rule. Local run records are not touched. Exit 0, or 125 on failure.

## version

```
sendaway version
```

Prints two lines to stdout:

```
sendaway <version>
protocol <n>
```

Both ends must report the same protocol. Exit 0.

## help

```
sendaway help
```

Also `-h` and `--help`. Prints the command list to stdout and exits 0. Running `sendaway` with no arguments prints the same text to stderr and exits 125. An unknown command prints `[sendaway] unknown command "<name>" (see 'sendaway help')` and exits 125.

## Internal command

`sendaway remote-exec` is the host side of a session. It is started by sendaway over ssh and speaks the framed protocol on stdin and stdout. It is not meant to be run by hand.
