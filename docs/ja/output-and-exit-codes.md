# 出力と終了コード

sendaway が追加する行はすべて `[sendaway] ` で始まり、stderr に出ます。stdout と stderr が同じファイルの場合、これらの行とコマンドの出力は到着した順に現れます。行は実行の `output.log` にも書き込まれます。

プレースホルダー：`<host>` は ssh の別名、`<id>` は `r-20250101-120000-ab12` のような実行 ID、`<n>` は数値です。

## 通常の実行で出る行

現れる順に並べています。

| 行 | 出る場面 |
|---|---|
| `waiting for lock (held by: <argv>, <time>, run <id>)` | 同じプロジェクトの別の実行がローカルロックを保持しているとき。保持者の記録が読めない場合は `waiting for lock (held by unknown)`。 |
| `waiting for remote lock (held by run <id>, <time>)` | ホスト側のプロジェクトのロックが、たとえば別のワークステーションによって保持されているとき。読めない場合は `... (held by unknown)`。 |
| `remote clock differs from local by <d>; make may misjudge mtimes` | 時計の差が 2 秒を超えたとき。 |
| `remote state missing; locally deleted files may linger — run 'sendaway forget' to start clean` | ホストにプロジェクトのディレクトリはあるが、前回の push の記録がないとき。 |
| `<host> <os>-<arch> · sync <s>s` | push の完了後に 1 回出る開始行。`<os>-<arch>` はホストのもので、たとえば `linux-amd64`。 |
| `some files vanished during sync` | 転送中にファイルが消えたと rsync が報告したとき。 |
| `not sent (secret-like): <path>, ...` | シークレットのルールまたは `exclude:` で送られなかったファイル。 |
| `tree is <x> MiB, over 256 MiB; largest: <path> (<x> MiB), ...` | マニフェストが 256 MiB を超えたとき。大きい順に 5 つのファイル名を挙げる。 |
| `symlinks pointing outside the repository are sent but dangle on the host: <path>, ...` | ルートの外へ出るシンボリックリンク。 |
| `name differs only in case from the file on disk (case-only rename?): <path>, ...` | 大文字小文字を区別しないファイルシステムで、転送された名前の大文字小文字がディスク上のファイルと異なるとき。 |
| prepare の出力 | `prepare:` コマンドの出力。各行に接頭辞が付く。 |
| （コマンドの出力） | 接頭辞なし。 |
| `artifacts not collected: <path> (<reason>), ...` | 失敗した実行の後、artifacts が拒否されたとき（`symlink`、`elf`、`macho`、`pe`、`ar`、`limit`）。コピー自体が失敗した場合は `artifacts not collected: <error>`。 |
| `remote run modified <n> synced files (not copied back): <paths> … and <k> more — add this target to local: or list the files in pull:` | コマンドが、回収されていない同期済みファイルを変更、置換、削除したとき。挙げるパスは最大 5 つで、`… and <k> more` は 5 つを超える場合にだけ付く。 |
| `<host> exit <n> · <s>s · run <id> · log <path>` | トレーラー。コマンドがシグナルで死んだ場合は、`exit <n>` が `killed by signal <n>` に置き換わる。`<path>` は実行の `output.log`。 |

トレーラーは、完了した実行の最後の行です。中断された後にも出力され、その場合は shim を中断したシグナルについて `killed by signal <n>` となります。

## フォールバック

| 行 | 意味 |
|---|---|
| `<host> unreachable (<reason>), running locally` | ホストが応答する前にネットワーク経路が失敗したため、コマンドを下げた優先度でローカル実行する。`<reason>` は `Could not resolve hostname`、`Connection timed out`、`Operation timed out`、`No route to host`、`Connection refused`、`Network is unreachable` のいずれか。 |

## 失敗の行（終了コード 125）

失敗した実行は、次のいずれかを最後の行として表示し、終了コード 125 で終わります。トレーラーは出力されません。

| 行 | 原因 |
|---|---|
| `<file>:<line>: <message>` | `.sendaway.yml` かユーザー設定が不正。 |
| `no host configured (set host: or a matching route in <path>)` | 既定のホストがなく、一致するルートもない。 |
| `.sendaway.yml must sit at the git top level` | 設定がリポジトリのサブディレクトリにある。 |
| `git worktrees are not supported` | `.git` がディレクトリでない。 |
| `git submodule "<path>" is not supported` | ツリーにサブモジュールがある。 |
| `nested git repository "<path>" is not supported` | 独自のリポジトリを含むディレクトリがある。 |
| `path "<path>" is not stored in NFC on disk; rename to NFC` | ASCII でない名前が別の Unicode 形式で保存されている。 |
| `argv[0] "<name>" contains '='` | コマンドの単語が代入のように見える。 |
| `local lock: <error>` | ローカルロックを取得できなかった。 |
| `protocol mismatch: local sendaway <v> speaks protocol <n>, remote sendaway <v> speaks protocol <n>; install the same version on both sides` | 両端のプロトコルが異なる。 |
| `connection lost during sync — nothing ran` | push が終わる前に接続が切れた。コマンドは開始していない。 |
| `connection lost during run — result unknown` | push の後に接続が切れた。コマンドは実行された可能性がある。 |
| `sync failed: <error>` | rsync が失敗した。 |
| `remote clock differs from local by <d> (limit <d>); sync the clocks` | 時計の差が 60 秒を超えた。 |
| `remote lock: <error>` | ホストのロックを取得できなかった。 |
| `prepare <command> exited <n>` | `prepare:` のコマンドが失敗した。 |
| `pull path does not exist: <path>` | `pull:` のパスがホストにない。 |
| `pull refused: <path> (<reason>), ...` | `pull:` のパスがシンボリックリンク、ネイティブバイナリ、アーカイブである。何もコピーされない。 |
| `pull destination <path> is a symlink; refusing to write through it` | ローカルの宛先にシンボリックリンクが含まれる。 |
| `pull failed: <error>` | 回収のコピーが失敗した。 |
| `protocol error from remote-exec: <error>` | ホストが想定外のフレームを送った。 |
| `remote-exec exited <n> without a result` | ホストのプロセスが、exit フレームなしで終了した。 |
| `remote-exec ended before the push` | ホストのセッションが、準備できる前に終了した。 |
| `cancelled` | ホストがセッションを停止した（cancel、接続の切断、ping が 30 秒途絶えた場合）。 |
| `record run: <error>` | 実行記録を書き込めなかった。 |
| ssh 自身のメッセージ | hello の前に、フォールバックの対象でない理由で接続が失敗した。たとえば `Permission denied (publickey).` や `Host key verification failed.`。ssh の stderr がそのまま表示される。空の場合、行は `remote-exec exited <n> without a result` になる。 |

実行以外のコマンドも、失敗時は同じ `[sendaway] <message>` の形で表示します。たとえば `[sendaway] fetch failed: <error>`、`[sendaway] no runs recorded`、`[sendaway] unknown command "<name>" (see 'sendaway help')` です。

## 終了コード

| コード | 意味 |
|---|---|
| `0`-`255` | コマンド自身の終了コード。そのまま返る。 |
| シグナルで死んだ場合 | sendaway も同じシグナルで終了する。shim のプロセスでそのシグナルが無視されている場合は、128 にシグナル番号を足した値で終了する。 |
| `125` | sendaway が失敗した、または実行を完了または回収できなかった。最後の `[sendaway]` 行に理由が書かれる。 |

補足：

- コマンドが正常に終了した場合は、その終了コードが返ります。コマンド自身が 125 を選んだ場合も同じです。コマンドの 125 と sendaway の 125 は、トレーラーの有無で区別できます。
- ホスト上でシグナルによって死んだコマンドは `killed by signal <n>` と報告され、shim もそのシグナルで死にます。シェルから見た終了ステータスは、128 にシグナル番号を足した値です。
- shim への割り込み（Ctrl-C、SIGTERM、SIGHUP）は、リモートのプロセスグループを停止し、`killed by signal <n>` のトレーラーを表示して、shim をそのシグナルで終了させます。
- `pull:` のパスの回収中に失敗した場合（`pull refused`、`pull path does not exist`、`pull failed`）は、コマンド自身が 0 で終了していても、終了コードは 125 です。
- `sendaway doctor` は、1 つでも `FAIL` があれば 125 で終了します。
- ローカル実行へフォールバックした実行の終了コードは、ローカルのコマンドのものです。
