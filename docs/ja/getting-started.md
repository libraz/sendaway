# はじめに

このページでは、Mac（または Linux）のワークステーションと Linux のビルドホスト 1 台から始めて、最初のリモート `make test` を実行するところまでを説明します。ホスト側の詳細は[ビルドホスト](build-host.md)にあります。

## 1. sendaway をローカルにインストールする

```sh
go install github.com/libraz/sendaway/cmd/sendaway@latest
```

必要な Go ツールチェーンのバージョンは、リポジトリの `go.mod` にある `go` ディレクティブのとおりです。Git と OpenSSH のインストールも必要です。macOS では、Homebrew の rsync 3.x を、システム標準の rsync より前に PATH へ置いてください。macOS 標準の rsync（openrsync）はプロトコル 29 を話すため拒否されます。sendaway にはプロトコル 31 以降が必要です。

インストールを確認します。

```sh
sendaway version
```

出力は `sendaway <version>` と `protocol <n>` の 2 行です。接続の両側が同じプロトコルを報告する必要があります。

## 2. ホスト用の Linux バイナリをビルドする

ホストには、同じバージョンの sendaway を Linux 向けにビルドし、`/usr/local/bin/sendaway` としてインストールします。

```sh
GOOS=linux GOARCH=amd64 go build -o sendaway ./cmd/sendaway   # GOARCH=arm64 for an Arm host
scp sendaway build01:/tmp/sendaway
ssh build01 'sudo install -m 0755 /tmp/sendaway /usr/local/bin/sendaway'
```

ファイル名は `sendaway` のままにします。別の名前でインストールすると、その名前の shim になります。

## 3. ssh の Host エントリを追加する

sendaway は、接続情報を ssh の設定からのみ読みます。`~/.ssh/config` にエントリを追加します。

```
Host build01
    HostName 192.0.2.10
    User builder
    IdentityFile ~/.ssh/id_ed25519
```

一度、手で `ssh build01` を実行します。接続は `BatchMode` で行われるため、未知のホスト鍵を対話的に受け入れることができません。鍵が `known_hosts` に入るまで、実行は `Host key verification failed` で失敗します。

## 4. ユーザー設定を書く

`~/.config/sendaway/config.yml` を作成します（環境変数 `XDG_CONFIG_HOME` が設定されている場合は `$XDG_CONFIG_HOME/sendaway/config.yml`）。

```yaml
host: build01                  # default ssh alias
routes:                        # longest path-component match wins
  - path: ~/Projects/big-repo
    host: build02
```

スキーマ全体は[設定](configuration.md#ユーザー設定)にあります。

## 5. プロジェクトを初期化する

git のトップレベルであるプロジェクトのルートで、次を実行します。

```sh
sendaway init
```

`init` はマーカーファイル（`Makefile`、`CMakeLists.txt`、`go.mod`、ロックファイルのある `package.json`、`Cargo.toml`、`mise.toml`）を探し、対応する `remote:`、`local:`、`prepare:` のルールを書いた `.sendaway.yml` を作成します。既存のファイルは上書きしません。生成された内容は編集してください。すべてのキーは[設定](configuration.md#プロジェクト設定)で説明しています。

## 6. shim をインストールする

```sh
sendaway install-shims
```

デフォルトの shim（make、cmake、ctest、go、cargo、yarn、npm、pnpm、bun）を `${XDG_DATA_HOME:-~/.local/share}/sendaway/bin` にリンクし、シェルの起動ファイルへ追加する行を表示します。

```
export PATH="/Users/you/.local/share/sendaway/bin:$PATH"
```

この行は `~/.zshrc` の末尾、`mise activate` がある場合はその後ろに置きます。そうすると、mise が PATH に置くツールの shim より、sendaway の shim ディレクトリが前に来ます。追加したら新しいシェルを開きます。ほかのコマンドを shim にするには、名前を渡します（`sendaway install-shims ninja just`）。

## 7. doctor を実行する

```sh
sendaway doctor
```

`doctor` は次を確認します。各 shim が PATH の先頭にあること、ローカルの rsync、control ディレクトリ、そして設定済みの各ホストについて、到達性、ssh が stdout に何も出力しないこと、リモートの sendaway のバージョンとプロトコル、時計、各 shim 名がホストのログインシェルで解決できることです。どの行も `ok` か `FAIL` で始まります。先に進む前に、すべての `FAIL` を解消してください。1 つでも失敗すると、終了コードは 125 です。

## 8. 最初のコマンドを実行する

プロジェクトの中で、次を実行します。

```sh
make test
```

`sendaway which make test` を使うと、事前にコマンドの実行先、ホスト、転送される変数を確認できます。

### 出力の読み方

sendaway は、コマンド自身の出力の合間に、`[sendaway]` で始まる行を追加します。すべて stderr に出ます。最初の行は、同期が終わった時点で現れます。

```
[sendaway] build01 linux-amd64 · sync 0.42s
```

ホスト名、ホストの OS とアーキテクチャ、同期にかかった時間を表します。最後の行はトレーラーです。

```
[sendaway] build01 exit 0 · 42.3s · run r-20250101-120000-ab12 · log /Users/you/.local/state/sendaway/runs/r-20250101-120000-ab12/output.log
```

`exit N` はコマンド自身の終了コードで、シェルにも同じコードが返ります。コマンドがシグナルで死んだ場合、トレーラーは `exit N` の代わりに `killed by signal N` となります。終了コード 125 は sendaway 自体の失敗を表します。この場合トレーラーは出力されず、最後の `[sendaway]` 行に理由が書かれます。すべての行は[出力と終了コード](output-and-exit-codes.md)に一覧があります。

次に読むページは[設定](configuration.md)です。エージェントでの利用は[コーディングエージェント](coding-agents.md)を参照してください。
