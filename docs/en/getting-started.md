# Getting started

This page takes a Mac (or Linux) workstation and one Linux build host from nothing to a first remote `make test`. The host side is detailed in [Build host](build-host.md).

## 1. Install sendaway locally

```sh
go install github.com/libraz/sendaway/cmd/sendaway@latest
```

The Go toolchain version required is the `go` directive in the repository's `go.mod`. Git and OpenSSH must be installed. On macOS, put a Homebrew rsync 3.x ahead of the system one on PATH: the rsync that ships with macOS (openrsync) speaks protocol 29 and is rejected, because sendaway needs protocol 31 or newer.

Check the install:

```sh
sendaway version
```

The output has two lines, `sendaway <version>` and `protocol <n>`. Both sides of a connection must report the same protocol.

## 2. Build the Linux binary for the host

The host needs the same sendaway version built for Linux, installed as `/usr/local/bin/sendaway`:

```sh
GOOS=linux GOARCH=amd64 go build -o sendaway ./cmd/sendaway   # GOARCH=arm64 for an Arm host
scp sendaway build01:/tmp/sendaway
ssh build01 'sudo install -m 0755 /tmp/sendaway /usr/local/bin/sendaway'
```

The file name must stay `sendaway`. Installing it under another name turns it into a shim for that name.

## 3. Add an ssh Host entry

sendaway reads connection details only from the ssh configuration. Add an entry to `~/.ssh/config`:

```
Host build01
    HostName 192.0.2.10
    User builder
    IdentityFile ~/.ssh/id_ed25519
```

Run `ssh build01` by hand once. Connections use `BatchMode`, so an unknown host key cannot be accepted interactively and the run fails with `Host key verification failed` until the key is in `known_hosts`.

## 4. Write the user configuration

Create `~/.config/sendaway/config.yml` (`$XDG_CONFIG_HOME/sendaway/config.yml` when that variable is set):

```yaml
host: build01                  # default ssh alias
routes:                        # longest path-component match wins
  - path: ~/Projects/big-repo
    host: build02
```

The full schema is in [Configuration](configuration.md#user-configuration).

## 5. Initialize a project

In the project root, which must be the git top level, run:

```sh
sendaway init
```

`init` looks for marker files (`Makefile`, `CMakeLists.txt`, `go.mod`, `package.json` with a lockfile, `Cargo.toml`, `mise.toml`) and writes a `.sendaway.yml` with matching `remote:`, `local:` and `prepare:` rules. It refuses to overwrite an existing file. Edit the result: [Configuration](configuration.md#project-configuration) describes every key.

## 6. Install the shims

```sh
sendaway install-shims
```

This links the default shims (make, cmake, ctest, go, cargo, yarn, npm, pnpm and bun) into `${XDG_DATA_HOME:-~/.local/share}/sendaway/bin` and prints a line to add to the shell startup file:

```
export PATH="/Users/you/.local/share/sendaway/bin:$PATH"
```

Put that line at the end of `~/.zshrc`, after any `mise activate`, so the shim directory ends up ahead of the tool shims mise puts on PATH. Open a new shell afterwards. To shim other commands, pass the names: `sendaway install-shims ninja just`.

## 7. Run doctor

```sh
sendaway doctor
```

`doctor` checks that each shim comes first on PATH, the local rsync, the control directory, and for each configured host: reachability, that ssh prints nothing on stdout, the remote sendaway version and protocol, the clock, and that each shim name resolves in the host's login shell. Every line starts with `ok` or `FAIL`. Fix every `FAIL` before continuing; the exit code is 125 when any check fails.

## 8. Run a first command

From inside the project:

```sh
make test
```

`sendaway which make test` shows beforehand where the command would run, the host and the forwarded variables.

### Reading the output

Between the command's own output, sendaway adds lines that start with `[sendaway]`, all on stderr. The first appears once the sync is done:

```
[sendaway] build01 linux-amd64 · sync 0.42s
```

It names the host, the host's OS and architecture, and the sync time. The last line is the trailer:

```
[sendaway] build01 exit 0 · 42.3s · run r-20250101-120000-ab12 · log /Users/you/.local/state/sendaway/runs/r-20250101-120000-ab12/output.log
```

`exit N` is the command's own exit code, and the shell sees the same code. If the command died from a signal the trailer reads `killed by signal N` instead. Exit code 125 means sendaway itself failed; no trailer is printed and the last `[sendaway]` line gives the reason. All lines are listed in [Output and exit codes](output-and-exit-codes.md).

The next page to read is [Configuration](configuration.md); for use with an agent see [Coding agents](coding-agents.md).
