# ビルドホスト

ビルドホストは、ssh 経由で到達する Linux マシンです。`sendaway remote-exec` を必要なときに起動し、プロジェクトのコマンドを実行します。デーモンは不要です。

## 要件

- 鍵認証のみ。`sshd_config` で `AllowAgentForwarding no`、`AllowTcpForwarding no`、`ClientAliveInterval 15` を設定します。
- 時刻同期（chrony など）。時計の差が 2 秒を超えると実行中に通知され、60 秒を超えると実行が失敗します。
- sendaway を実行するユーザーに、git の資格情報、他ホストへの鍵、sudo 権限を持たせません。
- ワークステーションと同じ絶対パスの親ディレクトリ（例：`/Users/<name>`）。そのユーザーが作成し、所有します。`/Users/alice/Projects/big-repo` にあるプロジェクトは、ホスト上の `/Users/alice/Projects/big-repo` へ同期されます。
- rsync 3.1 以降。`sendaway` は `/usr/local/bin` に置きます。非ログインの ssh はそこで見つけます。
- 標準出力に何も出力しないログインプロファイルと rc ファイル。
- ログインプロファイルで PATH に通したプロジェクトのツールチェーン。

ホスト側の sendaway は、ローカルと同じプロトコルである必要があります。どちら側でも `sendaway version` で両方を確認できます。

## セットアップのチェックリスト

1. **専用ユーザーを作成します**。ビルド専用のユーザーを、鍵認証のみで作ります。このユーザーが同期されたツリーをすべて所有します。sudo 権限、git の資格情報、他のマシンへの ssh 鍵は与えません。[セキュリティ](security.md#信頼確認がない理由)の信頼モデルは、ホストに盗む価値のあるものが何もないことを前提にしています。
2. **親パスを作成します**。ワークステーションが使うパスを、そのユーザー所有で作ります。同期は、その下の存在しないディレクトリを `mkdir -p` で作るので、必要なのは親だけです。ワークステーションのパスが `/Users/alice/Projects/...` なら、`/Users/alice` を root で一度作り、ビルド用ユーザーに渡します。
3. **sshd を堅牢化します**。`PasswordAuthentication no`、`AllowAgentForwarding no`、`AllowTcpForwarding no`、`ClientAliveInterval 15` を設定します。クライアントはすでに転送を無効にしていますが、サーバー側の設定により、特定のクライアントの設定に依存しなくなります。
4. **時計を同期します**。chrony などの NTP クライアントを使います。時計がずれると `make` がファイルの時刻を誤判定します。sendaway は 2 秒を超える差を通知し、60 秒を超える差では実行を拒否します。
5. **rsync をインストールします**。3.1 以降を、ディストリビューションのパッケージマネージャで入れます。`rsync --version` で確認し、`protocol version` の行が 31 以上である必要があります。
6. **sendaway をインストールします**。ホストのアーキテクチャ向けにビルドしたものを `/usr/local/bin/sendaway` に置きます（[はじめに](getting-started.md#2-ホスト用の-linux-バイナリをビルドする)を参照）。`ssh build01 sendaway version` がログインシェルなしで動く必要があります。
7. **ログインプロファイルでツールチェーンを PATH に通します**。コマンドは `$SHELL -lc` の下で動くため、コマンドの開始前にログインシェルがプロファイル（bash では `~/.bash_profile` か `~/.profile`）を読みます。コンパイラ、`mise`、`cargo`、`go`、`node`、パッケージマネージャは、そこで見つかる必要があります。対話用の rc ファイルだけで設定した PATH は見えません。
8. **rc ファイルとプロファイルを静かに保ちます**。非ログインの rc ファイルが出力すると、stdout 上のプロトコルのフレームが壊れます。ログインプロファイルの出力は、コマンド自身の stdout に混ざります。バナー、`echo`、`motd` のような出力は、対話端末かどうかの判定で囲みます。
9. **ssh の Host エントリを追加します**。ワークステーション側にエントリを書き、`ssh build01` を一度実行してホスト鍵を登録し、ホストをユーザー設定に加えます（[はじめに](getting-started.md)を参照）。
10. **重いプロジェクトは別に振り分けます**。ホストはプロジェクトごとに同時に 1 回しか実行せず、別のプロジェクトの実行は同じ CPU を取り合います。ビルドに長くかかるプロジェクトには、`routes:` のエントリで専用のホストを割り当てます（[設定](configuration.md#routes)を参照）。
11. **`sendaway doctor` を実行します**。ワークステーション側で実行します。

## doctor がホストで確認する項目

`sendaway doctor` は、ユーザー設定の各ホスト（デフォルトのホストとルートのホストすべて）について、次のプローブを ssh 経由で実行し、それぞれ `ok` か `FAIL` を表示します。

| 確認項目 | 成功時の行 |
|---|---|
| `ssh <host> true` が成功する | `<host>: reachable` |
| そのコマンドが stdout に何も出力しない | `<host>: ssh prints nothing on stdout` |
| `sendaway version` が動き、ローカルと同じプロトコルを報告する | `<host>: sendaway <version> (protocol <n>)` |
| `date +%s` がローカルの時計と 2 秒以内に収まる | `<host>: clock within 2s` |
| インストール済みの各 shim 名がログインシェルで解決できる（`$SHELL -lc 'command -v <name>'`） | `<host>: <path>` |

接続に失敗すると、そのホストの確認を中止し、ssh のエラーを表示します。`Host key verification failed` のエラーには、`run 'ssh <host>' by hand once to add the host to known_hosts` というヒントが付きます。

shim のプローブは、プロジェクトが使うコマンドだけでなく、shim ディレクトリにあるすべての shim が対象です。ホストにインストールされていないツールの shim は、失敗として `<host>: <name>: not found` を報告します。そのツールをホストにインストールするか、shim ディレクトリからそのリンクを削除してください。

doctor は、ホストの rsync、親パス、sshd の設定、sudo 権限を確認しません。これらは上のチェックリストの対象です。
