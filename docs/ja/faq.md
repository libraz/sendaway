# FAQ とトラブルシューティング

## 制限事項

- ローカル実行が起動したコマンドは、ローカルに留まります。`local:` のルールやフォールバックは、プロセスツリー全体をワークステーションに留めます。
- 対話型コマンドと tty コマンドには対応していません。リモートのコマンドには端末がなく、`/dev/null` を読みます。
- git サブモジュール、ネストしたリポジトリ、git worktree、NFC で保存されていないファイル名には対応しておらず、検出された時点で終了コード 125 で失敗します。
- 自分で `setsid` を呼んだプロセスは、コマンドのプロセスグループを離れるため、中断されても停止されません。
- プロジェクトごとに同時に実行できるのは 1 回だけです。ほかの実行はロックを待ちます。
- sendaway はプライベートなパッケージレジストリへの認証をせず、`.npmrc` や `.yarnrc.yml` のトークンも送りません。
- 同期されるツリーは、実行開始時点の作業ツリーです。コマンドがホスト上に作成したファイルはそこに残りますが（無視されたビルドディレクトリは実行をまたいで残ります）、`pull:` か `artifacts:` に列挙しない限り戻ってきません。

## ホストに到達できない、または接続で実行が失敗する

**コマンドがローカルで実行され、`unreachable (...)` という行が出た**。ホストが応答する前にネットワーク経路が失敗しました。DNS、タイムアウト、接続拒否、経路なしなどが原因です。これがフォールバックです。ホスト、VPN、`Host` エントリを確認してください。`ssh build01 true` で同じエラーを確認できます。

**実行が `Permission denied (publickey).` で終わる**。認証の失敗では、ローカル実行へフォールバックしません。黙って別の場所で実行すると、設定の問題が隠れてしまうためです。ssh のメッセージがそのまま表示され、終了コードは 125 です。`Host` エントリの鍵がプロンプトなしでロード済み、または読み取り可能であること（`BatchMode` ではパスフレーズのプロンプトが使えません）と、ビルド用ユーザーに対して認可されていることを確認してください。

**`Host key verification failed`**。一度手で `ssh build01` を実行して鍵を受け入れてください。`sendaway doctor` も同じヒントを表示します。`REMOTE HOST IDENTIFICATION HAS CHANGED` のエラーも致命的です。ホストを確認してから、`known_hosts` を修正してください。

**`ssh control path ... is N bytes, over the 86 byte limit`**。状態ディレクトリの下にある ssh の control ソケットのパスが、システムの上限より長くなっています。`XDG_STATE_HOME` を短いディレクトリに設定してください。

**`connection lost during sync — nothing ran` または `connection lost during run — result unknown`**。ssh の接続が切れました。前者はコマンドが開始されなかったことを意味します。後者は、コマンドが完了している可能性があるため、再実行する前にホストを確認してください（`sendaway shell`）。ホストは、ワークステーションから 30 秒間連絡がなければ、自分で作業を停止します。

**ssh から `sendaway: command not found`、または doctor から `sendaway not found or unreadable`**。ホストが `sendaway` を見つけられません。非ログインの ssh が見つけられる場所、通常は `/usr/local/bin` にインストールしてください。

**`protocol mismatch`**。両端のビルドが異なるプロトコルを使っています。ローカルと同じソースから Linux 用バイナリをビルドし、入れ直してください。

## ロックの待機

`waiting for lock (held by: ...)` は、このワークステーション上の同じプロジェクトの別の実行がロックを保持していることを意味します。`waiting for remote lock (held by run <id>, ...)` は、ホストのロックが保持されていることを意味し、典型的には別のワークステーションからの実行か、`fetch` や `forget` です。待機は、保持者が終了するか死ぬと終わります。プロセスが終了すると、カーネルがロックを解放します。Ctrl-C で待機を中止できます。`sendaway runs` で記録を確認できます。

## 時計のずれ

ホストの時計がワークステーションと 2 秒を超えて異なると、`remote clock differs from local by <d>; make may misjudge mtimes` が出ます。60 秒を超えると、実行は `remote clock differs from local by <d> (limit <d>); sync the clocks` で停止します。ホストに chrony などの NTP クライアントをインストールし、`sendaway doctor` で確認してください。

## リモートで変更されたファイル

`remote run modified N synced files (not copied back)` は、コマンドがホスト上の追跡ファイルまたは同期されたファイルを変更、置換、削除し（フォーマッタ、コードジェネレータ、スナップショットの更新など）、その変更がワークステーションに届かなかったことを意味します。次の実行では、ローカルのバージョンで上書きされます。コマンドを `local:` に追加してローカルで実行するか、生成されるファイルを `pull:` に列挙して、成功後に戻ってくるようにします。ビルドツールが touch しただけのファイルのように、タイムスタンプだけが変わったファイルも、ここに現れます。

## シークレットや無視されたファイルを必要とするテスト

シークレットらしいファイル（`.env`、鍵、資格情報、トークンを含む `.npmrc`）と、git が無視するファイルは、送られません。それらを読むテストは、ホストで失敗します。対処は次のとおりです。

- テストをローカルで実行する：`sendaway local make test`、またはそのターゲットを `local:` に追加します。
- シークレットを含まないファイルなら、git で追跡する：追跡されているファイルは、無視パターンに一致していても、シークレットのルールで除外されない限り送られます。
- テンプレートを使う：`.env.example`、`.env.sample`、`.env.template` は送られます。

## MAKEFLAGS などの変数

`MAKEFLAGS`、`CMAKE_BUILD_PARALLEL_LEVEL`、`CTEST_PARALLEL_LEVEL`、`GOFLAGS`、`GOMAXPROCS` などは転送されるので、ワークステーション向けに設定した `-j2` はホストでも有効になります。ホストのコア数を使うには、シェルでそれらを unset または変更するか、プロジェクト専用のラッパーを使います。`sendaway which <command>` は、コマンドについて転送される変数と除外される変数を一覧します。

転送されない変数がホストで必要な場合は、`.sendaway.yml` の `env:` に入れます。シークレットらしい名前の変数や、ワークステーションにしかない値の変数は、その場合でも除外されたままです。

## `remote state missing` の通知

```
[sendaway] remote state missing; locally deleted files may linger — run 'sendaway forget' to start clean
```

ホストにプロジェクトのコピーはあるものの、前回の push の内容の記録がありません。手作業で作られたか、状態が削除されたことが原因です。以前の実行が送ったファイルがどれかを sendaway は判断できないため、ローカルで削除したファイルがホストに残ります。`sendaway forget` を実行すると、ホスト上のプロジェクトのディレクトリと状態が削除されます。次の実行ですべてが送られ、すべての `prepare:` ルールが再実行されます。

## rsync が拒否される：openrsync

`rsync protocol too old: rsync <version> speaks protocol 29, need 31 or newer (install rsync 3.x, e.g. Homebrew)` は、macOS 標準の rsync です。Homebrew で rsync 3.x をインストールし、PATH で `/usr/bin` より前に置いてください。`sendaway doctor` は、使用中のバージョンを報告します。

## ControlPath が長すぎる

上の `ssh control path` の項目を参照してください。

## NFD の名前と特殊なファイル名

`path "<path>" is not stored in NFC on disk; rename to NFC` は、ASCII でない文字を含むファイル名が、ローカルのファイルシステム上で、git が記録するものとは別の Unicode 正規化形式で保存されていることを意味します。macOS で作られたファイルでよく起きます。ホストでは別の名前で保存されてしまうため、sendaway はこのようなツリーの同期を拒否します。ディスク上の名前が NFC になるようにファイルをリネーム（たとえば NFC で入力した名前で作り直す）してから、そのリネームをコミットしてください。

`name differs only in case from the file on disk (case-only rename?)` は警告で、失敗ではありません。大文字小文字を区別しないファイルシステムでは、大文字小文字だけを変えたリネームの後、git とディスクで名前の大文字小文字が食い違うことがあります。ホストには、git が報告する名前が渡されます。

## shim が違うバイナリを実行する、またはループする

shim は、shim ディレクトリを飛ばして PATH を検索し、本物のコマンドを見つけます。また、自分自身は実行しません。`... : executable not found` は、PATH に shim 以外の本物のバイナリがないことを意味します。shim ディレクトリの外で `command -v <tool>` を確認してください。`sendaway` 以外の名前のバイナリは shim として動作するので、バイナリを別の名前でコピーしないでください。

## 出力より先に行が表示される

`[sendaway]` の行は stderr に出ます。sendaway と端末の間にあって stderr の順序を入れ替えるツールがあると、行の位置が前後します。[コーディングエージェント](coding-agents.md#出力フィルタ)を参照してください。
