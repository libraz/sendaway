# コマンドリファレンス

```
sendaway <command> [arguments]
```

バイナリが `sendaway` として動作するのは、その名前で起動された場合だけです。それ以外の名前（shim のリンク）で起動されると、そのコマンドの shim として動作し、下の表は無視されます。

| コマンド | 目的 |
|---|---|
| [`init`](#init) | カレントディレクトリ用の `.sendaway.yml` を書く |
| [`install-shims`](#install-shims) | PATH shim をリンクし、PATH の行を表示する |
| [`doctor`](#doctor) | shim、rsync、control ディレクトリ、各ホストを確認する |
| [`which`](#which) | コマンドの実行先を表示する |
| [`run`](#run) | ルールを無視して、ホストでコマンドを実行する |
| [`local`](#local) | ルールを無視して、ローカルでコマンドを実行する |
| [`fetch`](#fetch) | ホストのパスを最新の実行にコピーする |
| [`runs`](#runs) | 記録された実行を一覧する |
| [`last`](#last) | 最新の実行を要約する |
| [`shell`](#shell) | ホスト上のプロジェクトディレクトリでログインシェルを開く |
| [`forget`](#forget) | プロジェクトのコピーと状態をホストから削除する |
| [`version`](#version) | バージョンとプロトコルを表示する |
| [`help`](#help) | コマンド一覧を表示する |

終了コードの一般則：成功なら 0、sendaway が失敗したら 125 です。`run` はコマンド自身の終了コードかシグナルで終わります。失敗時は stderr に `[sendaway] <reason>` を表示します。引数の数が違う場合は `[sendaway] usage error: <detail>` を表示します。詳細は[出力と終了コード](output-and-exit-codes.md)にあります。

ホストと通信するコマンド（`run`、`fetch`、`shell`、`forget`）には、カレントディレクトリより上にある `.sendaway.yml` と、[ユーザー設定](configuration.md#ユーザー設定)で決まるホストが必要です。ない場合は `.sendaway.yml not found above the current directory` か `no host configured (set host: or a matching route in <path>)` で失敗します。

## init

```
sendaway init
```

カレントディレクトリにあるマーカーファイルから `.sendaway.yml` を書き、`wrote <path> (N remote, N local, N prepare rules)` と表示します。使われないキーは、コメントアウトした空のリストとして書かれます。ファイルがすでにあれば `<path> already exists` で失敗し、上書きはしません。

| マーカーファイル | 書かれるルール |
|---|---|
| `Makefile` | remote `[make]`、local `[make, format]`、`[make, fmt]`、`[make, format-check]`、`[make, lint]` |
| `CMakeLists.txt` | remote `[cmake]`、`[ctest]` |
| `go.mod` | remote `[go, test]`、`[go, build]`、`[go, vet]` |
| `package.json` と `yarn.lock` | remote `[yarn, test]`、`[yarn, build]`、local `install`、`add`、`remove`、`dev`、`lint`、`format`、prepare は `yarn.lock`、`package.json` に対する `yarn install --immutable` |
| `package.json` と `pnpm-lock.yaml` | `pnpm` で同様。prepare は `pnpm install --frozen-lockfile` |
| `package.json` と `bun.lock` | remote `[bun, test]`、`[bun, run, build]`、local `install`、`add`、`remove`、`[bun, run, dev]`、prepare は `bun install --frozen-lockfile` |
| `Cargo.toml` | remote `[cargo, test]`、`[cargo, build]`、local `[cargo, fmt]`、`[cargo, add]` |
| `mise.toml` | `mise.toml` に対する prepare `mise trust --yes` と `mise install` |

終了コードは 0 です。失敗した場合や引数が渡された場合は 125 です。

## install-shims

```
sendaway install-shims [names...]
```

各名前を、実行中のバイナリへ `${XDG_DATA_HOME:-~/.local/share}/sendaway/bin` にリンクし（既存のリンクは置き換えます）、リンクした数と PATH の行を表示します。

```
linked 9 shims in /Users/you/.local/share/sendaway/bin

Add this line at the end of ~/.zshrc, after any 'mise activate':

export PATH="/Users/you/.local/share/sendaway/bin:$PATH"
```

名前を省略すると `make cmake ctest go cargo yarn npm pnpm bun` をリンクします。空の名前、`sendaway`、`/` を含む名前は `invalid shim name` で拒否されます。ディレクトリ内にある、シンボリックリンクでない既存のファイルは置き換えません。終了コードは 0 です。失敗した場合は 125 です。

## doctor

```
sendaway doctor
```

結果をセクションごとに表示します。各行は `ok` か `FAIL` で始まります。

- `shims (<dir>)`：インストール済みの各 shim が PATH の先頭にあるかどうか。メッセージは `<name>: the shim comes first on PATH`、`<name>: <path> comes before the shim; put <dir> first in PATH`、`<name>: not found on PATH; add <dir> to PATH` です。shim が 1 つもなければ `no shims installed; run 'sendaway install-shims'` と表示されます。
- `local tools`：ローカルの rsync（`rsync <version> (protocol <n>)`。プロトコルが 31 未満なら失敗）と、`cm` control ディレクトリ（`<dir> is private (0700)`、`<dir> does not exist yet`、モードが 0700 でなければ失敗）。
- `host <name>`：ユーザー設定にある重複しない各ホストについて、[ビルドホスト](build-host.md#doctor-がホストで確認する項目)に挙げた確認項目。ホストが設定されていなければ `no host configured in <path>` と表示されます。

ssh のプローブは、それぞれ 30 秒でタイムアウトします。すべての行が `ok` なら終了コードは 0、1 行でも `FAIL` があれば 125 です。

## which

```
sendaway which <command> [arguments...]
```

何も実行せずに、カレントディレクトリからこのコマンドラインがどこで実行されるかを説明します。

```
command:  make test
decision: remote (matches remote rule [make])
config:   /Users/you/Projects/app/.sendaway.yml
host:     build01
env forwarded: CI, MAKEFLAGS
env dropped:   MYAPP_TOKEN (secret-like name)
(commands started by a local run stay local)
```

`decision` は次のいずれかです。`local (matches local rule [..])`、`remote (matches remote rule [..])`、`local (no rule matches)`、`local (SENDAWAY_PASSTHROUGH is set)`、`local (.sendaway.yml not found above the current directory)`。最後の 2 つの場合は、`command:` と `decision:` の行だけが表示されます（それに末尾の行が付きます）。ホストが決まらなければ、`host:` は `(none configured)` と表示されます。一覧が空なら `(none)` と表示されます。終了コードは 0 です。設定エラーの場合やコマンドがない場合は 125 です。

## run

```
sendaway run <command> [arguments...]
```

ルールにかかわらず、ホストでコマンドを実行します。全体の流れは[動作の仕組み](how-it-works.md)にあります。プロジェクトが必要です。終了コードはコマンドのもので、sendaway が失敗した場合は `125` です。コマンドがシグナルで死んだ場合は、sendaway も同じシグナルで終了します。ホストに到達できない場合は、shim と同じようにローカル実行へフォールバックします。

## local

```
sendaway local <command> [arguments...]
```

ルールにかかわらず、本物のコマンドをローカルで実行します。`SENDAWAY_PASSTHROUGH=1` を設定するので、コマンドが起動するコマンドもローカルに留まります。コマンドの検索では、shim ディレクトリを飛ばします。プロセスはコマンドに置き換えられるため、終了コードとシグナルはコマンドのものになります。プロジェクトの外でも動作します。コマンドが見つからない場合や起動できない場合は、終了コード 125 です。

## fetch

```
sendaway fetch <path...>
```

ホスト上のプロジェクトのコピーから、ルート相対のパスを、最新の記録済み実行の `fetched/` ディレクトリ（`${XDG_STATE_HOME:-~/.local/state}/sendaway/runs/<id>/fetched/`）へ、相対的な配置を保ったままコピーします。ローカルのツリーは変更されません。最新の実行とは、このマシンで最も新しい記録であり、このプロジェクトのものとは限りません。

`pull:` と同じ検査が適用されます。ディレクトリは再帰的にコピーされ、シンボリックリンク、ネイティブバイナリ、アーカイブは拒否され（`pull refused: <path> (<reason>)`）、存在しないパスはエラーです。成功時は何も表示しません。終了コードは 0 です。失敗した場合は 125 で、実行が 1 つも記録されていなければ `no runs recorded` になります。fetch はプロジェクトのホストのロックを待つので、実行中のコマンドの後ろに並びます。

## runs

```
sendaway runs
```

記録された実行を、新しい順に 1 行ずつ一覧します。

```
r-20250101-120000-ab12  build01  exit 1  42.3s  make test
```

フィールドは、実行 ID、ホスト、結果（`exit N`、`signal N`、sendaway が失敗した場合は `failed`）、所要時間、コマンドラインです。プロジェクトのツリーが、その実行が見たものと現在異なっていれば、行末に `  (tree changed since this run)` が付きます。記録がなければ `no runs recorded` と表示して、終了コード 0 で終わります。

## last

```
sendaway last
```

最新の実行を要約します。

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

`error:` は sendaway の内部で失敗した実行に、`modified:` はホストが同期されたファイルを変更した場合に、最後の行はその実行以降にツリーが変わった場合に表示されます。終了コードは 0 です。記録がなければ `no runs recorded` とともに 125 です。

## shell

```
sendaway shell
```

プロジェクトのホストへの対話的な ssh セッションでプロセスを置き換え、プロジェクトのディレクトリ（`cd <root> && exec "$SHELL" -l`）で tty つきで開きます。接続のオプションは実行時と共通ですが、`BatchMode` と `-T` は含まれないので、パスワードのプロンプトも使えます。同期は行われず、ホストには前回の実行が残したままのツリーが見えます。終了ステータスは ssh のものです。失敗した実行のディレクトリをホスト上で調べるために使います。

## forget

```
sendaway forget
```

ホストのプロジェクトのディレクトリと、それに対する sendaway のリモート状態（保存されたマニフェストと prepare のハッシュ）を、ホストのプロジェクトロックを取得してから削除します。成功すると `[sendaway] <host>: removed <root> and its state` と表示します。次の実行ではツリー全体が再び送られ、すべての `prepare:` ルールが再実行されます。ローカルの実行記録には触れません。終了コードは 0 です。失敗した場合は 125 です。

## version

```
sendaway version
```

stdout に 2 行を表示します。

```
sendaway <version>
protocol <n>
```

両端が同じプロトコルを報告する必要があります。終了コードは 0 です。

## help

```
sendaway help
```

`-h` と `--help` でも同じです。コマンド一覧を stdout に表示して、終了コード 0 で終わります。引数なしで `sendaway` を実行すると、同じテキストを stderr に表示して、終了コード 125 で終わります。未知のコマンドは `[sendaway] unknown command "<name>" (see 'sendaway help')` を表示して 125 で終わります。

## 内部コマンド

`sendaway remote-exec` は、セッションのホスト側です。sendaway が ssh 経由で起動し、stdin と stdout でフレーム形式のプロトコルを話します。手で実行することは想定されていません。
