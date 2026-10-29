# コーディングエージェント

Claude Code と Codex は、シェルを通じてビルドやテストのコマンドを実行します。sendaway は PATH shim なので、エージェントが `make test` と入力すれば、ツールやプロンプトを何も変えずにリモート実行になります。このページでは、shim を有効にするために必要な条件と、エージェントが結果をどう読むべきかを説明します。

## shim はエージェントのシェルで PATH の先頭にあること

エージェントが sendaway に到達するのは、エージェントが使うシェルで `make`、`go` などの shim 化した名前が shim ディレクトリに解決される場合だけです。そのシェルは、人間が入力するシェルと同じとは限りません。

- 非対話シェルを起動するエージェントは、`~/.zshrc` を読み込まないことがあります。`sendaway install-shims` が表示する `export PATH=...` の行は、そのシェルが読むファイルの、`mise activate` など PATH の先頭に追加するツールより後ろに置きます。
- 別の端末ではなく、エージェントのセッションの中から確認します。`command -v make` が `${XDG_DATA_HOME:-~/.local/share}/sendaway/bin` 内のパスを表示し、`sendaway which make test` が `decision: remote` を報告するはずです。
- `sendaway doctor` は、各 shim が、doctor を実行したシェルで PATH の先頭にあるかどうかを報告します。エージェントのシェルからも実行してください。

絶対パスで指定したコマンド（`/usr/bin/make`）は、shim をすり抜けます。shim は PATH 上の名前で選ばれるためです。

## エージェントへの指示

プロジェクトの `AGENTS.md` か `CLAUDE.md` に、次を追加します。

```markdown
Builds and tests here run on a remote host through sendaway shims. Use the usual commands (`make test`, `ctest`).
stderr ends with `[sendaway] <host> exit N · <time> · run <id> · log <path>`; N is the command's exit code.
Exit 125 means sendaway failed, not the command; the preceding `[sendaway]` line says why.
`sendaway last` summarizes the latest run, and the full output is in its log path.
If the trailer warns that the remote run modified files, add the target to `local:` or list the files in `pull:`.
Tests that need secret files run with `sendaway local <cmd>`.
```

## 結果の読み方

- トレーラーは stderr の最後の行です。`exit N` はコマンド自身の終了コードで、エージェントのシェルが見る値と同じです。`killed by signal N` は、コマンドがシグナルで死んだことを表します。
- 終了コード 125 がコマンドのコードであることはありません。トレーラーは出力されず、最後の `[sendaway]` 行に理由が書かれます（例：`connection lost during run — result unknown`）。
- `sendaway last` は、コマンド、ホスト、時刻、結果、変更されたファイル、`output.log` のパスを表示します。ログには実行の出力がすべて入っています。
- `remote run modified N synced files (not copied back)` という警告は、コマンドがホスト上の追跡ファイルを書き換え、その内容がローカルのツリーに届いていないことを意味します。フォーマッタやコードジェネレータは `local:` に、その出力は `pull:` に入れます。
- `sendaway fetch <path>` は、ローカルのツリーに触れずに、ホストのルート相対のファイルを最新の実行の `fetched/` ディレクトリへ持ち帰ります。

## 出力フィルタ

`[sendaway]` の行は stderr に出ます。コマンドの出力を書き換えたり圧縮したりして、2 つのストリームの順序を入れ替えるツールがあると、トレーラーがコマンドの出力より前に来ます。そのようなフィルタには、shim の名前を除外リストに加えて、これらのコマンドがフィルタされずに動くようにします。RTK の場合は、設定の `exclude_commands` です。

```toml
[hooks]
exclude_commands = ["make", "cmake", "ctest", "go", "cargo", "yarn", "npm", "pnpm", "bun"]
```

stdout と stderr が同じファイルへ向かう場合、sendaway はコマンドの出力と自身の行を、到着した順に保ちます（[動作の仕組み](how-it-works.md#6-出力)を参照）。ストリームを分離するフィルタは、この保証を失わせます。

## バックグラウンドタスクとロック

プロジェクトごとに同時に実行されるのは 1 回だけです。2 つ目のコマンドは待機し、次のように表示します。

```
[sendaway] waiting for lock (held by: make test, 12s, run r-20250101-120000-ab12)
```

最初の実行が終わるまで、この表示が続きます。エージェントが長いビルドをバックグラウンドで始め、同じプロジェクトで別のコマンドを実行すると、この待機が起きます。待機は固まっているわけではありません。`sendaway runs` で記録を確認でき、ロックを解放するには最初のコマンドを止めます。ロックは、保持しているプロセスがどのような理由で終了しても解放されます。

エージェントのコマンドを Ctrl-C やシグナルで止めると、cancel がホストに送られ、ホストがコマンドのプロセスグループを停止します（SIGINT、猶予の 5 秒の後に SIGKILL）。shim にシグナルを送らず、シェルごと強制終了した場合は、ping が 30 秒途絶えた時点で、ホストが切断に気付きます。

## ローカルに留まるコマンド

ルールはコマンドラインで判断するので、エージェントはこれらを特別な処理なしで実行できます。

- `local:` のルールは `remote:` より優先され、フォーマッタ、リンタ、依存関係のインストールをワークステーション上に留めます。
- `sendaway local <command>` は、シークレットのファイルや無視されたファイルを必要とするテストのために、ローカル実行を強制します。
- `sendaway run <command>` は、ルールにかかわらずリモート実行を強制します。
