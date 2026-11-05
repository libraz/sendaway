# 用語集

各ページで使う用語を、英字、五十音の順に並べています。それぞれ、動作を詳しく定めているページへのリンクを付けています。

**artifacts** — `artifacts:` に列挙するプロジェクトルート相対のパスで、コマンドが失敗したときだけ回収します。回収先は実行記録の `artifacts/` ディレクトリで、プロジェクトのツリーには書き込みません。[設定](configuration.md#artifacts)

**doctor** — `sendaway doctor` のことです。shim、手元の rsync、制御ディレクトリ、設定された各ホストを検査し、項目ごとに `ok`、`warn`、`FAIL` を表示します。[コマンドリファレンス](cli.md#doctor)

**exclude** — `exclude:` に列挙するプロジェクトルート相対のパスで、送信しません。シークレットセットに加わります。[設定](configuration.md#exclude)

**min_free** — 実行を始める前に、ホストがプロジェクトのあるファイルシステムに確保しておく空き容量です。下回ると、ホストは他プロジェクトのコピーを、最後に使われた時刻が古い順に削除します。[設定](configuration.md#min_free)

**passthrough** — `SENDAWAY_PASSTHROUGH` のことです。設定されていると、shim は規則を見ずに実体のバイナリを exec します。sendaway は、`local:` に一致したとき、どの規則にも一致しないとき、`sendaway local`、フォールバックで手元実行するときと、遠隔のコマンドにこれを設定します。そのため、それらが起動したコマンドは同じ場所に留まります。[動作の仕組み](how-it-works.md#1-判断)

**prepare ルール** — `prepare:` の各項目です。`when` に挙げたファイルの内容ハッシュが変わったときだけ、コマンドの前にホストで準備のコマンドを実行します。[設定](configuration.md#prepare)

**pull** — `pull:` に列挙するプロジェクトルート相対のパスで、コマンドが終了コード 0 で終わった後、手元のツリーの同じ場所へコピーします。シンボリックリンク、ネイティブバイナリ、アーカイブは拒否します。[設定](configuration.md#pull)

**push** — 並行して動く 2 つの rsync で、マニフェストと `.git` ディレクトリを、コマンドの直前にホストへ送ります。[動作の仕組み](how-it-works.md#4-セッション)

**remote-exec** — `sendaway remote-exec` のことで、セッションのホスト側です。実行のたびに ssh 経由で起動され、手で実行するものではありません。[コマンドリファレンス](cli.md#内部コマンド)

**shim** — コマンド名（`make`、`go`、`yarn` など）で作った `sendaway` バイナリへのリンクです。バイナリは `sendaway` 以外の名前で起動されると、その名前のコマンドの shim として動きます。[はじめに](getting-started.md#6-shim-をインストールする)

**開始行** — 実行の最初の `[sendaway]` 行で、push が終わった時点で `[sendaway] build01 linux-amd64 · sync 0.42s` の形で表示します。[出力と終了コード](output-and-exit-codes.md#通常の実行で出る行)

**シークレットセット** — マニフェストのうち、秘密らしい名前の規則、`.npmrc` や `.yarnrc.yml` 内のトークン、`exclude:` によって送らないファイルです。`not sent (secret-like)` の行に列挙します。[セキュリティ](security.md#シークレットファイル)

**実行（run）** — 1 つのコマンドの 1 回の遠隔実行で、振り分けの判断からトレーラーまでを指します。実行には `r-20250101-120000-ab12` のような ID が付き、UTC の開始時刻を含みます。[動作の仕組み](how-it-works.md)

**実行記録** — `${XDG_STATE_HOME:-~/.local/state}/sendaway/runs/<id>/` のディレクトリで、`output.log`、`meta.json`、必要に応じて `artifacts/` と `fetched/` を持ちます。新しい 100 件のうち 14 日以内のものを残します。[動作の仕組み](how-it-works.md#9-実行記録)

**実体のバイナリ** — shim が代わりを務めるコマンドです。shim のディレクトリを飛ばして PATH を探して見つけます。[動作の仕組み](how-it-works.md#1-判断)

**制御ソケット** — `${XDG_STATE_HOME:-~/.local/state}/sendaway/cm/` に置く ssh の `ControlMaster` ソケットです。接続を 10 分間保ち、後続の実行がすぐにつながるようにします。別名と、`ssh -G` がその別名について解決した設定をキーにします。[動作の仕組み](how-it-works.md#4-セッション)

**トレーラー** — 完了した実行の最後の行で、`[sendaway] <host> exit N · <time> · run <id> · log <path>` の形です。シグナルで終わったときは `exit N` の代わりに `killed by signal N` が入ります。sendaway の内部で失敗した実行はトレーラーを出さず、終了コード 125 で終わります。[出力と終了コード](output-and-exit-codes.md)

**ビルドホスト** — コマンドを実行する、ssh で接続する Linux マシンです。`/usr/local/bin` に `sendaway` バイナリがあればよく、デーモンは要りません。[ビルドホスト](build-host.md)

**フォールバック** — ホストが応答する前にネットワーク経路が失敗したため、コマンドを手元で実行することです。認証の失敗とホスト鍵の不一致ではフォールバックしません。[動作の仕組み](how-it-works.md#10-フォールバック)

**フレーム** — shim と `remote-exec` の間の ssh 接続を流れる、長さ付きのメッセージです。出力は `out`、`err`、`note` のフレームで届きます。[動作の仕組み](how-it-works.md#6-出力)

**プロジェクト設定** — git のトップレベルに置く `.sendaway.yml` です。どのコマンドを遠隔で実行し、何を一緒に送るかを決めます。[設定](configuration.md#プロジェクト設定)

**プロジェクトルート** — `.sendaway.yml` のあるディレクトリで、git のトップレベルでもある必要があります。ホスト上の同じ絶対パスへ同期します。[設定](configuration.md#ファイルの探索)

**プロトコル** — shim と `remote-exec` の間のフレーム化されたプロトコルの版です。両端の `sendaway version` が同じ番号を表示する必要があります。[コマンドリファレンス](cli.md#version)

**マニフェスト** — git が追跡している、または無視していないファイルを並べたリストから、シークレットセットを除いたものです。push が送るのはこれで、ホストは直前のpush の記録として保存します。[動作の仕組み](how-it-works.md#3-ファイルセット)

**無視セット** — git が無視する未追跡のパスです。送信せず、ホスト上のそれらのコピーにも手を付けないので、ビルドディレクトリは実行をまたいで残ります。[動作の仕組み](how-it-works.md#3-ファイルセット)

**ユーザー設定** — `~/.config/sendaway/config.yml` で、プロジェクトを ssh のホスト別名に対応づけ、`ssh_config` と `min_free` を持ちます。ワークステーションの外には出ません。[設定](configuration.md#ユーザー設定)

**ルート（route）** — ユーザー設定の `routes:` の各項目で、あるパス以下のプロジェクトを特定のホストへ送ります。一致するパスのうち最も長いものが勝ちます。[設定](configuration.md#routes)

**ルール** — `remote:` または `local:` の各項目で、プログラム名と、引数に順に照合する glob の語からなります。`local:` の一致が優先されます。[設定](configuration.md#remote-と-local-のルール書式)

**ローカルロック** — ワークステーション上のプロジェクト単位のロックで、1 つのプロジェクトで同時に進む実行を 1 つにします。待つ間は `waiting for lock (held by: …)` を表示します。ホストも同じプロジェクトについて独自の**リモートロック**を持ちます。[動作の仕組み](how-it-works.md#2-事前条件)
