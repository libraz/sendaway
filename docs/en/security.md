# Security

sendaway copies a working tree to another machine and runs commands there. This page lists what reaches the host, what never does, and what stays on the workstation.

![A trust boundary between the workstation and the build host: the file set after the secret filter and a short environment allowlist cross it, while secret files, git credentials, ignored files and most environment variables stay on the workstation, and output frames return.](../images/trust-boundary.svg)

## What reaches the host

- The files git tracks, and untracked files git does not ignore, minus the secret set below.
- The `.git` directory, minus `config`, `FETCH_HEAD`, `hooks/`, `logs/`, `*.lock` and `objects/tmp_*`. The host receives a minimal `.git/config` (`repositoryformatversion = 0`, `bare = false`) with no remotes, URLs or credential helpers.
- The command line, the working directory and the allowlisted environment variables.

Nothing in the user configuration (host names, routes) is sent.

## Secret files

A path is withheld when any one of its elements (directory or file name) matches a pattern below, compared case-insensitively:

| Pattern | Examples |
|---|---|
| `.env`, `.env.*` | `.env`, `.env.production` |
| `*.pem`, `*.key`, `*.p12`, `*.pfx`, `*.jks`, `*.keystore` | `server.key`, `cert.pem` |
| `id_rsa*`, `id_ecdsa*`, `id_ed25519*` | `id_ed25519`, `id_rsa.pub` |
| `.netrc`, `.git-credentials`, `.pypirc` | |
| `*.tfstate`, `*.tfstate.*` | `terraform.tfstate.backup` |
| `credentials*.json`, `service-account*.json` | `credentials.json` |
| `secrets`, `.ssh`, `.aws`, `.gnupg` | everything under a directory with this name |

`.env.example`, `.env.sample` and `.env.template` are exempt, since they hold no credentials.

Two rc files are judged by content: `.npmrc` and `.yarnrc.yml`. If the file contains any of the keys `_authToken`, `_auth`, `_password`, `npmAuthToken` or `npmAuthIdent` (case-insensitive substring match), the whole file is withheld, not only the matching line. A file that cannot be read is withheld too.

Paths under `exclude:` in `.sendaway.yml` are added to the same set. The name rules are fixed and cannot be turned off.

Every withheld file is listed on stderr at the start of the run:

```
[sendaway] not sent (secret-like): .env, config/server.key
```

Files that git ignores are not sent either, and are not listed. A test that needs a withheld or ignored file fails on the host; run it locally with `sendaway local <command>` or add its target to `local:`.

## Environment variables

The command's environment on the host is built from scratch. It contains:

- `HOME`, `USER`, `LOGNAME`, `SHELL` and `PATH` as set in the host's own ssh session;
- the forwarded variables;
- `SENDAWAY_PASSTHROUGH=1`.

The forwarded variables are the allowlist plus the names in `env:` that are set locally:

```
CI NO_COLOR FORCE_COLOR CLICOLOR_FORCE
MAKEFLAGS CMAKE_BUILD_PARALLEL_LEVEL CTEST_PARALLEL_LEVEL CTEST_OUTPUT_ON_FAILURE
GOFLAGS CGO_ENABLED GOMAXPROCS GOTRACEBACK
RUST_BACKTRACE RUST_LOG
```

Two rules remove variables from that set, whether they come from the allowlist or from `env:`:

- **Secret-like names**, compared case-insensitively: names containing `TOKEN`, `SECRET`, `PASSWORD`, `PASSWD`, `CREDENTIAL`, `APIKEY`, `API_KEY`, `PRIVATE_KEY`, `AUTH`, `COOKIE` or `SESSION`, and names starting with `AWS_`, `GH_`, `GITHUB_`, `NPM_`, `ANTHROPIC_`, `OPENAI_`, `CLOUDFLARE_`, `GOOGLE_`, `AZURE_` or `SSH_`.
- **Mac-specific values**: values containing `/opt/homebrew`, `/Applications/`, `/Library/`, `/System/`, `/var/folders/` or `/private/`, and values starting with `$HOME/.` or `$HOME/Library/`. A path that only exists on the workstation would break the build on the host.

`sendaway which <command>` lists the forwarded names and, for names requested in `env:`, the dropped ones with the reason (`secret-like name` or `Mac-specific value`). Allowlist variables dropped by the value rule are not listed.

`LANG` and `LC_*` are not forwarded. If ssh sends them (an ssh configuration with `SendEnv`), they reach `remote-exec` but are discarded, because the child environment is rebuilt. Set the locale in the host's login profile.

## Connection hardening

Every ssh and rsync connection uses these options:

```
-T
BatchMode=yes            ConnectTimeout=5
ControlMaster=auto       ControlPersist=10m
ServerAliveInterval=5    ServerAliveCountMax=3
ForwardAgent=no          ForwardX11=no
ClearAllForwardings=yes  GSSAPIDelegateCredentials=no
```

Agent, X11 and port forwarding are off, so the host cannot reach back into the workstation's agent or network. `BatchMode` prevents any prompt: a password, a passphrase or an unknown host key fails the connection. The control socket directory `<state>/cm/` must be mode 0700, and sendaway refuses to use it otherwise.

The host should also refuse forwarding on its side; see [Build host](build-host.md).

## What stays local

- The state directory `${XDG_STATE_HOME:-~/.local/state}/sendaway/` is created with mode 0700; run files are mode 0600.
- Run records hold the output of every run, which may include anything the command prints. `meta.json` never holds environment variables.
- The local lock records the command line of the run that holds it. The remote lock records only the run id, start time and pid.
- The user configuration, the ssh configuration and the ssh keys never leave the workstation.

`sendaway forget` removes the project's copy and sendaway's state for it from the host (see [Command reference](cli.md#forget)). Local run records are not touched; they expire after 14 days or when more than 100 exist.

## Why there is no trust prompt

sendaway does not ask before a project's `.sendaway.yml` takes effect, and it does not ask before `prepare:` runs. Running a project's `make test` already executes that project's code; running it on the host instead moves that execution to a machine that holds no credentials. The file cannot choose a host: hosts come from the user configuration, outside the repository. What a repository can influence is bounded by the rules on this page: the secret filter, the environment allowlist and the disabled forwarding apply regardless of what `.sendaway.yml` says.

This reasoning depends on the build host being a disposable build environment. A host that holds credentials, keys to other machines or sudo for the build user undoes it. See [Build host](build-host.md#setup-checklist).
