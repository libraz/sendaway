# Glossary

Terms used across these pages, in alphabetical order. Each entry links to the page that defines the behaviour in full.

**artifacts** — Root-relative paths listed in `artifacts:` and copied back only when the command fails. They land in the run record's `artifacts/` directory, never in the project tree. [Configuration](configuration.md#artifacts)

**build host** — The Linux machine reached over ssh that runs the commands. It needs the `sendaway` binary in `/usr/local/bin` and no daemon. [Build host](build-host.md)

**control socket** — The ssh `ControlMaster` socket under `${XDG_STATE_HOME:-~/.local/state}/sendaway/cm/` that keeps a connection open for ten minutes so later runs reconnect quickly. It is keyed on the alias and the configuration `ssh -G` resolves for it. [How it works](how-it-works.md#4-session)

**doctor** — `sendaway doctor`, which checks the shims, the local rsync, the control directory and each configured host, printing `ok`, `warn` or `FAIL` per check. [Command reference](cli.md#doctor)

**exclude** — Root-relative paths listed in `exclude:` that are never sent. They join the secret set. [Configuration](configuration.md#exclude)

**fallback** — Running the command locally because the network path to the host failed before the host answered. Authentication and host-key failures never fall back. [How it works](how-it-works.md#10-fallback)

**frame** — A length-prefixed message on the ssh connection between the shim and `remote-exec`. Output travels as `out`, `err` and `note` frames. [How it works](how-it-works.md#6-output)

**ignored set** — Untracked paths git ignores. They are never sent, and the host's copies of them are left alone, so build directories survive between runs. [How it works](how-it-works.md#3-file-sets)

**local lock** — A per-project lock on the workstation that lets one run per project proceed at a time. Waiting prints `waiting for lock (held by: …)`. The host keeps its own **remote lock** for the same project. [How it works](how-it-works.md#2-preconditions)

**manifest** — The sorted list of files git tracks or does not ignore, minus the secret set. It is what the push sends, and the host keeps it as its record of the last push. [How it works](how-it-works.md#3-file-sets)

**min_free** — The free space the host must keep on the filesystem holding the project before a run starts. Below it, the host removes other projects' copies, least recently used first. [Configuration](configuration.md#min_free)

**passthrough** — `SENDAWAY_PASSTHROUGH`. When it is set, a shim execs the real binary without consulting the rules. sendaway sets it when a command runs locally because of a `local:` match, no match, `sendaway local` or the fallback, and for the remote command, so the commands those start stay where they are. [How it works](how-it-works.md#1-decision)

**prepare rule** — An entry under `prepare:` that runs a setup command on the host before the command, only when the content hash of its `when` files changed. [Configuration](configuration.md#prepare)

**project configuration** — `.sendaway.yml` at the git top level. It says which commands run remotely and what travels with them. [Configuration](configuration.md#project-configuration)

**project root** — The directory holding `.sendaway.yml`, which must also be the git top level. It is synced to the same absolute path on the host. [Configuration](configuration.md#finding-the-file)

**protocol** — The version of the framed protocol between the shim and `remote-exec`. Both ends must report the same number in `sendaway version`. [Command reference](cli.md#version)

**pull** — Root-relative paths listed in `pull:` and copied back into the same place in the local tree after the command exits with 0. Symlinks, native binaries and archives are refused. [Configuration](configuration.md#pull)

**push** — The two parallel rsyncs that send the manifest and the `.git` directory to the host, just before the command runs. [How it works](how-it-works.md#4-session)

**real binary** — The command the shim stands in for, found by searching PATH while skipping the shim directory. [How it works](how-it-works.md#1-decision)

**remote-exec** — `sendaway remote-exec`, the host side of a session, started over ssh for each run. It is not run by hand. [Command reference](cli.md#internal-command)

**route** — An entry under `routes:` in the user configuration that sends projects under a path to a specific host. The longest matching path wins. [Configuration](configuration.md#routes)

**rule** — An entry under `remote:` or `local:`: a program name followed by glob words matched in order against the arguments. A `local:` match wins. [Configuration](configuration.md#remote-and-local-rule-syntax)

**run** — One remote execution of a command, from the decision to the trailer. Each run has an id such as `r-20250101-120000-ab12`, which encodes its UTC start time. [How it works](how-it-works.md)

**run record** — The directory `${XDG_STATE_HOME:-~/.local/state}/sendaway/runs/<id>/` holding `output.log`, `meta.json`, and any `artifacts/` and `fetched/` files. The newest 100 records younger than 14 days are kept. [How it works](how-it-works.md#9-run-records)

**secret set** — Manifest files withheld by the secret name rules, by a token found in `.npmrc` or `.yarnrc.yml`, or by `exclude:`. They are listed in `not sent (secret-like)`. [Security](security.md#secret-files)

**shim** — A link to the `sendaway` binary under the name of a command (`make`, `go`, `yarn`). Under any name other than `sendaway`, the binary acts as a shim for that name. [Getting started](getting-started.md#6-install-the-shims)

**start line** — The first `[sendaway]` line of a run, printed once the push is done: `[sendaway] build01 linux-amd64 · sync 0.42s`. [Output and exit codes](output-and-exit-codes.md#lines-of-a-normal-run)

**trailer** — The last line of a completed run: `[sendaway] <host> exit N · <time> · run <id> · log <path>`, with `killed by signal N` in place of `exit N` after a signal death. A run that fails inside sendaway prints no trailer and exits with 125. [Output and exit codes](output-and-exit-codes.md)

**user configuration** — `~/.config/sendaway/config.yml`, which maps projects to ssh host aliases and holds `ssh_config` and `min_free`. It never leaves the workstation. [Configuration](configuration.md#user-configuration)
