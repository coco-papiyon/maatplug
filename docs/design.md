# maatplug 設計書

## 1. 目的

Claude Code／Codex CLI／GitHub Copilot CLI を横断で使ったときの「指示内容」「指示後のソース変更量」「トークン消費量」を、セッション開始時の`cloc`計測とあわせて記録し、後から横断的に参照できるようにする。記録先はDBではなくフラットファイル（JSONL）とし、CLIとVSCode拡張の両方から利用できるようにする。参照用に別途Go製Webアプリを用意する。収集・書き込みは各Providerのhook／実行契機で起動する短命プロセスの中で完結させ、収集専用の常駐デーモンは持たない（詳細は3.1節）。

[maatgen](https://github.com/coco-papiyon/maatgen)（Codex/Claude Code/Copilotをプロバイダーアダプタとして扱い、Go製バックエンド＋Web UI＋VSCode拡張という構成でローカル完結・認証なしのCoding Agent Manager）の**思想のみ**を継承する。maatgenのコード・アダプタ実装・依存関係は一切利用せず、本プロジェクトはゼロから独立実装する。参考にするのは「ローカル完結・認証なし」「Providerごとにアダプタとして抽象化する」「Go＋VSCode拡張＋Webという3点セットの配布形態」という設計思想の部分のみであり、各Provider CLIの具体的な起動フラグやJSON出力仕様はmaatgenの記述を根拠にせず、実装時に各CLI自身のドキュメント／`--help`で個別に検証する。またmaatgenはSQLiteでセッション/Runを管理するのに対し、本プロジェクトは「軽量な計測専用ツール」として**DBを持たずフラットファイルのみ**で完結させる点が最大の差分。

## 2. maatgenからの継承点／差分

| 観点 | maatgen | maatplug（本設計） |
|---|---|---|
| 目的 | Agentの実行・Diff確認・Restore | 使用量（指示・差分行数・トークン）の記録・可視化 |
| 対象Provider | Codex／Claude Code／Copilot | 同左 |
| データ永続化 | SQLite | フラットファイル（JSONL） |
| 実行形態 | Web UIからAgentを起動・操作 | 既存のCLI利用に相乗り（hook／exec-wrap）して記録するだけ。Agent実行自体はしない（`exec`サブコマンドで委譲実行する経路のみ持つ） |
| 配布形態 | Agent Managerバイナリ＋Web静的ファイル＋VSIX | CLIバイナリ＋Web静的ファイル＋VSIX（同じ3点セット） |
| ローカル完結・認証なし | ○ | ○（踏襲） |
| コード・依存関係 | ― | maatgenのコード・アダプタ実装は一切利用しない。参考にするのは設計思想のみで、実装はすべて新規 |

## 3. 全体アーキテクチャ

### 3.1 基本方針：イベント駆動の短命プロセス

書き込みは、Claude Code／Codexのhook（`UserPromptSubmit`／`Stop`等）またはCopilotのexec-wrap（実行契機）でOSプロセスを1つ起動し、記録を書き込んだら終了する**短命プロセス**として設計する。maatplugは収集・書き込みのための常駐デーモンを持たない。プロセスの起動・終了ライフサイクルは各Provider本体（hook）／maatplug自身（exec-wrap）が都度管理し、maatplug側で自動起動・二重起動防止・クラッシュ時再起動といった常駐プロセス特有の管理を持つ必要がない。

`git diff`や`cloc`など外部ツールの呼び出しは、この短命プロセスの中で都度行う。

### 3.2 構成図

```
 Claude Code CLI ─┐
 Claude Code拡張  ─┼─ hooks (settings.json、engineレベルなのでCLI/拡張どちらからでも共通発火)
                   │        │ イベント発生のたびに起動→書込→終了（短命プロセス、常駐なし）
                   │        ▼
 Codex CLI/拡張 ───┤   ┌───────────────┐        ┌──────────────────────┐
 Copilot CLI ──────┴───│  maatplug      │──書込──▶│ フラットファイル store    │
   (exec-wrap:          │ (Go, 単一バイナリ) │        │ ~/.maatplug/**.jsonl    │
    実行の都度起動→終了)  │ init/hook/exec  │        │ ＝各コンシューマ共通の"契約" │
                        └───────────────┘        └───────────┬──────────┘
                                                                │読取専用（JSONLスキーマのみに依存）
                     ┌────────────────────────────────────────┼───────────────────────────┐
                     ▼                                         ▼                             ▼
        maatplug report/status                    maatplug-web (Go, go:embed)        maatplug VSCode拡張(TS)
        （CLIの一機能、任意）                        ブラウザ向けダッシュボード             拡張内でJSONLを直接
                                                    自己完結で走査・集計                   パース・集計、独自UI表示
```

書き込み（hook／exec-wrap経由の短命プロセス）と閲覧（`maatplug report`・`maatplug-web`・VSCode拡張のダッシュボード表示）は完全に分離する。閲覧側はJSONLスキーマという契約だけを頼りに、それぞれ自分自身のプロセス内で完結して読み取り・集計し、互いに依存しない（詳細は8〜10節）。

GitHub Copilot Chat（VSCode拡張）は、hookに相当する拡張ポイントが公開されておらず計測対象外である（6.4節）。

## 4. 言語選定

| コンポーネント | 言語 | 理由 |
|---|---|---|
| `maatplug`（hook／exec-wrap＝書き込み、report／status＝簡易集計） | Go | 単一静的バイナリでクロスプラットフォーム配布が容易。hookやexec-wrapの起動先として外部実行ファイルである必要があり、Webアプリとモデル定義（`internal/model`）・問い合わせロジック（`internal/query`）を共有できる。 |
| `maatplug-web`（参照用Webアプリ） | Go（指定通り） | `net/http`＋`go:embed`でフロントエンド一式を同梱し、Node不要で1バイナリに完結させる。フロントエンドはビルドチェーンを持たないvanilla JS＋Chart.js程度に留める（React/Vue等の重量フレームワークは採用しない）。 |
| VSCode拡張（ダッシュボード表示専任） | TypeScript | VSCode Extension APIの制約上これ以外の選択肢がない。収集（書き込み）ロジックは持たず、hook／exec-wrapがClaude Code拡張・ターミナルのどちらから起動されても共通に発火する前提（6.4節）に乗る。拡張自身はフラットファイルを直接読んで表示するだけの薄いクライアント。 |

→ 実質「Go（書き込み＋Web）＋TypeScript（VSCode拡張＝表示専任）」の2言語構成。収集ロジックはGo側に一本化し、VSCode拡張側での二重実装を持たない。

## 5. データモデル（フラットファイル設計）

### 5.1 ディレクトリ構成

既定ルートはOSのユーザー設定ディレクトリ配下（`%APPDATA%\maatplug` / `~/.config/maatplug`等、`MAATPLUG_HOME`で上書き可）。

```
<home>/
  config.json                          # 全体設定（有効化Provider、CLIパス、clocパス等）
  projects/
    <project-id>/                      # project-id = sha256(gitリモートURL または 絶対パス)[:12]
      project.json                     # {id, path, name, createdAt}
      sessions/
        <provider>-<session-id>.jsonl  # セッション単位の追記専用イベントログ
  logs/
    maatplug.log                       # 内部診断ログ
```

セッション単位で1ファイルに分けるのは、Claude Code自身のトランスクリプト（`~/.claude/projects/<proj>/<session-id>.jsonl`）と同じ流儀に揃えるため。追記のみで完結し、複数セッション・複数ウィンドウが並行してもファイル競合が起きない。

### 5.2 イベントスキーマ（1行1JSON）

```jsonc
{"type":"session_start","ts":"2026-09-06T10:00:00+09:00","provider":"claude","sessionId":"s1","projectId":"a1b2c3d4e5f6","cwd":"C:/repo","gitHead":"abcd123","cloc":{"files":120,"blank":800,"comment":400,"code":9500,"byLang":{"Go":6000,"TypeScript":3500}}}
{"type":"instruction","ts":"...","instructionId":"i1","text":"ログイン画面のバリデーションを直して","baseline":{"gitTree":"a1b2c3..."}}
{"type":"turn_end","ts":"...","instructionId":"i1","diff":{"filesChanged":3,"added":42,"removed":11},"tokens":{"input":5200,"output":830,"cacheRead":12000,"cacheCreation":0,"total":18030},"model":"claude-sonnet-5"}
{"type":"turn_end","ts":"...","instructionId":"i2","diff":{"filesChanged":0,"added":0,"removed":0},"tokens":null,"tokensUnavailable":true,"mergedInto":"i1"}
{"type":"session_end","ts":"...","instructionCount":7,"totalTokens":98213,"cloc":{"code":9612}}
```

- `cloc`が未インストールの場合は `"cloc":null,"clocSkipped":true` を記録し処理は継続する（要件どおりスキップのみでエラーにしない）。フィールド自体を省略するのではなく、必ず`"cloc"`キーは存在させ値を`null`にする（Web／VSCode拡張側のパース契約を安定させるため）。`tokens`／`tokensUnavailable`も同様に扱う。
- `turn_end`の`model`は提供元がその回答に使用したモデル名（例:`"claude-sonnet-5"`,`"gpt-5.6-sol"`）で、providerごとのtranscriptからベストエフォートで取得する（6.1・6.2節）。判定できなければ省略される。
- `turn_end`の`mergedInto`は、そのinstructionの`diff`／`tokens`が別のinstructionId（値として持つid）のturn_end側に記録されていることを示す（6.1節）。同一端末で作業中に追いメッセージを送った場合など、1回のStopで複数instructionをまとめて解決するときに使う。`mergedInto`が付いたturn_endの`diff`はゼロ、`tokens`は欠落（`tokensUnavailable:true`）で、集計側は二重計上を避けるためこれらを合計に加算しない。
- `abandoned`は現在の実装では新規に書き込まれることはないが、過去に書き込まれた既存セッションファイルとの読み取り互換のためスキーマ上は残っている（6.1節）。
- `instruction`の`baseline`は6.3節の方式に応じて`{"gitTree":"<tree object hash>"}`（gitリポジトリ）または`{"cloc":{...}}`（非git、指示開始時点のフルスキャン結果）のいずれかを持つ。`turn_end`の`diff`はこのbaselineと指示完了時点のスナップショットを比較した結果であり、`instruction`イベント自体には行数の差分は含まれない（baselineはあくまで比較用の内部参照値）。
- `turn_end`の`abandoned:true`は、実際に計測した結果として`diff`がゼロ・`tokens`が無いのではなく、6.1節で説明する並行resumeの曖昧状態を解消するために**測定できないまま強制的に閉じられた**ことを示す。閲覧側は`abandoned:true`の`turn_end`を「変更量ゼロのターン」と混同せず、「対応付け不能により未計測」として扱う必要がある。

## 6. プロバイダー別連携方式

3プロバイダーとも同じ`internal/adapter.Adapter`インターフェースを実装するが、実際のフック機構が現時点で異なるため、実装は2方式を使い分ける。どちらも3.1節の通り「呼ばれたら起動し、書き込んだら終了する」短命プロセスである。

### 6.1 Claude Code ── ネイティブhook方式（第一候補）

Claude Codeの`settings.json`の`hooks`機能を利用し、`SessionStart`／`UserPromptSubmit`／`Stop`／`SessionEnd`に`maatplug hook claude --event <Name>`を登録する（`maatplug init`で自動追記、確認プロンプトあり）。各イベントで`maatplug`が1回起動し、その場で処理して終了する。

- `SessionStart`: `cloc`実行→`session_start`書き込み。
- `UserPromptSubmit`: stdinで渡される指示文を記録し、6.3節の方式でベースラインを取得。**instructionIdは自前で採番する**（後述）。
- `Stop`: 現在のソース状態を再スナップショットしてベースラインとの差分を計算し`turn_end`を記録。トークン数は`transcript_path`（セッションのJSONLトランスクリプト）を読み、直近の「人間による本物の指示」行より後に現れる全assistant API応答の`usage`（input/output/cache tokens）を合算して取得する。Claude Codeは1回のAPI応答をthinking／tool_use／text等の複数`assistant`行に分け、各行へ同一usageを反復して記録することがあるため、`message.id`（無ければ`requestId`）が同じ行は1応答として1回だけ計上する。識別子が無い行は、別応答を誤ってまとめて過少計上しないよう従来どおり各行を計上する。
- `SessionEnd`: `session_end`記録。

**instructionIdの相関について**：Claude Codeのhookのstdinで、UserPromptSubmitとStopを相関させるID（`prompt_id`のようなフィールド）が渡される保証は確認できなかったため、そのようなフィールドには一切依存しない設計とした。代わりに`UserPromptSubmit`はinstructionIdを自前で採番してJSONLに書き込み、`Stop`は同じセッションのJSONLファイル自身から「まだ`turn_end`が付いていない`instruction`」をすべて検索して対応付ける（`internal/store.FindUnresolvedInstructions`）。

未解決instructionが常に高々1件である（＝1件のStopが1件のinstructionだけに対応する）のは、Claude Codeが1ターンずつ完全に順番通り処理する場合に限られる。この前提は2通りの理由で崩れうる：(1) **同一session_idを複数端末で同時にresumeした場合**（Claude Codeは同一session_idの複数端末resumeで、両端末のメッセージを同じtranscriptへ混在させる）、(2) **同一端末・同一セッションでも、Claudeがまだ作業中のうちにユーザーが追いメッセージを送信した場合**（Claude Code標準の「作業中でも次の指示を送れる」機能。これは特殊な状況ではなく通常の利用の一部）。(2)の場合、`UserPromptSubmit`が2回発火してから`Stop`は1回だけ発火する（Claudeは両方のinstructionをひとつの継続したターンとして処理する）。hookのpayloadには(1)と(2)を区別できる信頼できるフィールドが無いため、`FindUnresolvedInstructions`は区別を試みず、見つかった未解決instruction全部を（最も古いものの`baseline`とともに）返す。

呼び出し側（`Recorder.TurnEndForLastInstruction`）は、返された全instructionIdをこの1回のStopでまとめて解決する：最も古い（＝最初に送られた）instructionには実測した`diff`／`tokens`／`model`をそのまま記録し、それ以外のinstructionには`diff`をゼロ・`tokens`を欠落（`tokensUnavailable:true`）にしたうえで、実データがどのinstructionにあるかを指す`mergedInto`フィールド（そのinstructionId）を持たせる。集計（`internal/aggregate`）はabandonedと同様に`mergedInto`が付いたturn_endの`diff`／`tokens`をサマリのトークン合計・diff合計には加算しない設計とし、これにより同じ実測値が複数instructionへ二重計上されることを避ける。

これは以前の設計（未解決instructionが2件以上あれば`ambiguous`を返し呼び出し側が全件を`abandoned:true`で見捨てる）を置き換えたものである。旧設計は(2)の「追いメッセージ」ケース――同一端末・単一Stopという、実運用で最も頻繁に発生するパターン――でもデータを完全に失っていた。新設計は(2)では実測値を失わず両方のinstructionに記録できる。(1)の真の複数端末同時利用（レアケース）では依然として「どちらのinstructionの作業か」を区別できないため、最初のStopが両方をまとめて解決してしまい、もう一方の端末の実際のStopは「対応する未解決instructionが無い」として記録できなくなる、という同種のトレードオフが残る──ただしこちらも「何も記録されない」旧設計より悪化はしていない。

**同時実行への対処**：複数instructionが未解決になる状況はまさに「同一セッションに対して複数の`maatplug`プロセスがほぼ同時に動きうる」状況でもある。2つの`Stop`プロセスが「未解決instructionを読む→判定する→`turn_end`を書く」を無同期に行うと、両方が同じinstructionを未解決と読み取ってそれぞれ`turn_end`を書き、1つのinstructionIdに複数の`turn_end`が残ってしまう（複数instruction統合パスに限らず、通常の単一instruction解決パスでも同型の競合が起こり得る）。これを避けるため、`Recorder.TurnEndForLastInstruction`／`TurnEnd`の「読む→判定する→書く」全体を、セッションファイル単位のプロセス間排他ロック（`internal/store.WithSessionLock`）で囲む。ロックは`O_EXCL`で作成するロックファイル方式（Windows／POSIXどちらでもアトミック）で、一定時間（既定30秒、hookは短命プロセスなので通常はこれよりずっと早く終わる）経過したロックファイルはクラッシュ後の残骸とみなして強制的に破棄する。

`abandoned:true`の`turn_end`（内部的には`internal/store`にAPIは残っていないが、スキーマ・集計・表示側のサポートは維持している）は、上記の理由により現在の実装では新規に書き込まれることはない。過去に書き込まれた既存のセッションファイルとの読み取り互換性のためだけにスキーマ・集計・Web UIに残している。

**トランスクリプトの`user`行について**：Claude Codeはツール実行結果も`type:"user"`の行として記録する（Anthropic Messages APIでtool_resultがuser roleメッセージとして表現されるため）。実データで確認したところ、本物の人間の指示行には無く、ツール結果行にだけ存在するのが`toolUseResult`フィールドである。トークン合算の起点（「直近の人間の指示」）は`type=="user" && toolUseResultが無い`行で判定する。ツール結果行を「直近のuser行」と誤認すると、1ターン中に複数回ツールを使った場合に最初の方のassistant応答のトークンを取りこぼす。

**transcriptの書き込み待ち**：`transcript_path`は非同期書き込みのため、`Stop`発火時点でこのターンの最終応答がまだ反映されていないことがある。「一定時間トランスクリプトに変化が無ければ完了とみなす」という静止判定は、ツール呼び出し中の間延びがたまたま判定間隔と一致すると誤って「完了」と判断してしまう。そこで`Stop`のstdinに含まれる`last_assistant_message`（Claude Code自身が報告する最終応答のテキスト）と、transcript末尾のassistant行から抽出したテキストが**正規化後に完全一致**するまでポーリングする（最大約3秒）。部分一致は採用しない。ツール呼び出し前の中間応答（例:「Running tests」）が最終応答（例:「Running tests completed successfully」）の一部分と偶然重なるケースがあり、方向を問わず部分一致を許すと中間応答を最終応答と誤認して早期にポーリングを打ち切ってしまうため。一致しないまま試行回数の上限に達した場合は、その時点のtranscript内容でトークン集計を行う（＝完全一致判定が無かった場合と同じ結果に落ちるだけで、悪化はしない）。

### 6.2 Codex ── ネイティブhook方式／Copilot CLI ── exec-wrap方式

Codexは公式のlifecycle hooksを利用し、配布プラグインの`hooks/hooks.json`から`SessionStart`／`UserPromptSubmit`／`Stop`／`SessionEnd`に`maatplug hook codex --event <Name>`を登録する。Codex固有の`turn_id`が`UserPromptSubmit`と`Stop`の双方へ渡るため、これをinstructionIdとして使用し、並行ターンでも「最後の未解決instruction」の推測を行わず直接相関する。

Codexのhook payloadにはトークン使用量が含まれないため、`Stop`で渡される`transcript_path`をベストエフォートで読み取る。現行形式では`token_usage_record.payload.root_turn_id`をhookの`turn_id`と照合し、同じターンの最後の`turn_token_usage`（ターン内累積値）を採用する。旧形式では`event_msg/token_count`のスレッド累積値を、対象`task_complete`の直前とその前の`task_complete`時点との差分から求める。transcriptの形式は安定したhook APIではないため、読込失敗・該当レコードなし・未知の形式・安全に差分を計算できない場合はhook自体を失敗させず、`tokensUnavailable:true`として保存する。非同期書き込みに備えて対象turnの`task_complete`を最大約3秒待ち、完了マーカーが将来廃止された場合でもturn ID付きの使用量が得られていれば待機後にその値を採用する。`SessionStart`の`compact`は同一セッション中にも発火するため、プラグイン側のmatcherから除外し、`startup`／`resume`／`clear`のみを開始イベントとして記録する。

GitHub Copilot CLIは引き続き`maatplug exec copilot -- <指示>`によるexec-wrapを将来実装する。exec-wrap方式は「maatplug経由で叩いた実行」しか捕捉できず、ユーザーが素のCLIを直接叩いた場合は記録されない。

### 6.3 ソース変更量の計測

**主経路（gitリポジトリ）**：指示の開始時と終了時、それぞれの瞬間の作業ツリー全体（追跡済みの変更＋`.gitignore`対象外の未追跡ファイル）を一時的なgit indexに`git add -A`でステージし`git write-tree`でtree objectとして固定する（実リポジトリのindex／作業ツリーには触れない）。2つのtree hashを`git diff <before> <after> --numstat`で直接比較し、そのターンの正確な追加/削除行数・変更ファイル数を得る。

これは「HEADからの累積diffを2回読んで引き算する」方式ではない。累積差分の引き算は、同じファイルが複数ターンにまたがって編集されると壊れる：例えば2ターン目に1ターン目と同じファイルを再編集しても、HEAD比較でのfilesChangedは両方とも「1」のままなので引き算すると0になり、そのターンの変更が消える。また1ターン目で追加した行を2ターン目で削除すると、HEAD比較でのadded行数が減る方向に動き、その減少はゼロ未満にクランプされて「削除」としては計上されない。2つのtree snapshotを直接diffする方式なら、同じファイルが何度編集されようと、そのターンのdiffはgit自身の比較としてそのつど正しく求まる。

バイナリファイルの変更は`git diff --numstat`が行数の代わりに`-`を返すため行数には加算されないが、`filesChanged`には他のファイルと同様1件として計上する。

**フォールバック（非gitディレクトリ）**：tree object方式が使えないため、指示の開始時・終了時それぞれで`cloc`のフルスキャン結果を保持し、コード行数の合計の増減だけを見る粗い方式で代替する（`internal/gitdiff.DiffCloc`）。同一ターン内での「追加と削除が両方起きた」場合の内訳は区別できず、正味の増減のみが得られる。gitのtree diffほど正確ではないが、非git環境でも指示ごとの変更量がゼロに固定されてしまう事態は避けられる。

### 6.4 VSCode公式Provider拡張機能との関係

各ProviderはVSCode拡張（Claude Code拡張、GitHub Copilot Chat拡張、Codex拡張）を提供しているが、**maatplugはこれらの拡張機能に対してVSCode Extension API経由で直接連携することはしない**（そのための公開APIが存在しないため）。関係性はProviderごとに次のように異なる。

- **Claude Code**：拡張機能はCLIエンジンを起動・管理するGUIラッパーである可能性が高く、hookは`~/.claude/settings.json`等のエンジンレベル設定として解決される。そのため6.1節のhook方式は、ユーザーがターミナルの`claude`を直接使った場合でもVSCode拡張のパネルから使った場合でも、区別なく同じhookが発火して記録される見込みである（VSCode拡張向けの特別な実装は不要）。ただし実際に同一の設定ファイル・hookを共有しているかは実装着手時に要検証（12節の最優先リスク）。
- **Codex**：Claude Codeと同様、拡張機能がCodex CLIエンジンのGUIラッパーである場合は6.2節のexec-wrapに準じた検証が必要（拡張機能経由の起動をmaatplugが横取りする手段がない場合、拡張機能からの利用は捕捉対象外になる可能性がある。要検証）。
- **GitHub Copilot（最重要の制約）**：「GitHub Copilot Chat」VSCode拡張と「GitHub Copilot CLI」（`copilot`コマンド）は別製品である。Copilot Chat拡張はVSCode内で完結する独自UIであり、会話内容・トークン使用量を他の拡張機能や外部プロセスから参照する公開手段は存在しない。**maatplugが計測できるのは、ユーザーが明示的に`maatplug exec copilot`経由でCopilot CLIを呼び出した場合のみであり、VSCode上のCopilot Chatパネルでの利用は原理的に計測不可能**。これはツールの限界として利用者に明示する（後述12節にも記載）。
- **maatplug自身のVSCode拡張**：上記いずれの公式拡張とも無関係の、独立した「maatplug専用ビューア」拡張である。収集ロジックは持たず（6.1節のhookがCLI/VSCode拡張どちらの利用も共通に捕捉する前提のため）、公式拡張機能がインストールされているかどうかに関わらず動作し、あくまでmaatplugが記録したフラットファイルを表示するだけの存在（10節）。

## 7. cloc連携

- `exec.LookPath("cloc")`で存在確認。無ければ`clocSkipped:true`を記録して処理は継続（要件どおりエラーにしない）。
- gitリポジトリの場合、`cloc --vcs=git`は使わない。これは内部で素の`git ls-files`（追跡ファイルのみ）を使うため、AIが今まさに作成した未追跡ファイルを集計から取りこぼす。代わりに`git ls-files --cached --others --exclude-standard`（追跡＋未追跡だが`.gitignore`対象外）でファイル一覧を組み立て、`cloc --list-file=-`に渡す。非gitディレクトリでは対象ディレクトリを直接指定する。
- セッション終了時にも再計測し`session_end.cloc`に記録することで、セッション単位のコード規模推移を追える（指示単位の差分＝git tree diff、セッション単位の規模＝cloc、と役割を分ける）。
- 6.3節のgit diff計測（tree object snapshot方式）も同じ理由で、一時indexに`git add -A`することで追跡ファイルの変更と未追跡だが`.gitignore`対象外の新規ファイルの両方を1つのtree snapshotに含める。

## 8. CLIコマンド設計

`maatplug`の必須の役割は**書き込み経路**（`init`／`hook`／`exec`）であり、これらはClaude Codeのhookやexec-wrapの起動先として外部実行ファイルである必要があるため独立バイナリとして必須。いずれも3.1節の通り、呼ばれるたびに起動し、書き込んだら終了する短命プロセスであって常駐はしない。一方`report`／`status`は数あるJSONLコンシューマの一つに過ぎず、Webアプリ・VSCode拡張が動作するための前提ではない。

```
maatplug init                      # データディレクトリ作成／Provider検出／Claude Codeへのhooks登録（実装済み）
maatplug status                    # 現在プロジェクトの直近サマリ表示（internal/queryを利用）（実装済み）
maatplug report [--project P] [--format table|json]  # 同上、ターミナル向けの一表示形式（実装済み。--sinceは未実装）
maatplug hook claude --event <SessionStart|UserPromptSubmit|Stop|SessionEnd>  # hookから呼ばれる内部コマンド（6.1節）（実装済み）
maatplug exec <claude|codex|copilot> -- <指示...>   # exec-wrapアダプタ（6.2節）（未実装、Codex/Copilotアダプタと合わせて着手）
maatplug serve [--addr 127.0.0.1:4173] [--data-dir ...]   # Webアプリを直接起動したい場合（未実装。現状は`maatplug-web`を直接実行する、8節末尾参照）
maatplug config get/set            # （未実装）
```

Webアプリ（`maatplug-web`）は現状、`maatplug serve`のラッパー経由ではなく`cmd/maatplug-web`として直接ビルド・実行する別バイナリになっている（`maatplug-web --addr 127.0.0.1:4173 --data-dir ...`）。詳細は9節。

新形式の読み取りは`internal/query`パッケージとしてCLIとWebアプリ（どちらもGo）で共有する。プロジェクト一覧では検証済みcatalog、詳細ではRunジャーナル、期間集計ではsummaryを選び、投影の欠落・破損・遅れは正本から再構築する。これはGo内での実装共有であり、CLIとWebは互いにサブプロセスを起動しない。旧sessionファイルの互換表示も`internal/query`内へ閉じ込める。

集計結果には、指示・トークン合計・diff合計に加えて`abandonedInstructions`（6.1節の並行resume由来で`abandoned:true`となった`turn_end`の件数）を必ず含める。`abandoned`な`turn_end`はトークン・diffの合計には加算しない（合計を「実測した結果ゼロだった」と誤読させないため）。`status`はこれを`(N unmeasured/abandoned)`という注記として、`report`のtable形式は`ABANDONED`列として、JSON形式は`abandonedInstructions`フィールドとして、いずれも0件でなければ必ず可視化する。

## 9. Webアプリ設計（Go）

`maatplug-web`（`cmd/maatplug-web`）として実装済み。

- `net/http`の標準`ServeMux`（Go 1.22で追加されたメソッド・パスパラメータ構文、例: `mux.HandleFunc("GET /api/projects/{id}/sessions", ...)`）のみを使用し、外部routerライブラリへの依存を持たない。`go.mod`に追加の依存は無い。
- フロントエンドは`internal/web/frontend/`（`index.html`／`app.js`／`style.css`）に置き、`internal/web`パッケージの`//go:embed frontend`で同梱する。`go:embed`は自パッケージ配下のディレクトリしか埋め込めないため、design当初案の`web/frontend/`という最上位配置ではなく、埋め込み元の`internal/web`パッケージ配下に置く形に変更した。
- 既定は`127.0.0.1:4173`バインド・認証なし。`--addr`で変更可能。`--data-dir`で読み取り先のデータディレクトリを明示的に指定できる（省略時は`store.New()`と同じ解決規則）。
- 読み取り専用・**自己完結**。`maatplug`本体プロセスやCLIの`report`コマンドを呼び出さず、HTTPリクエストごとに`internal/query`を利用する。catalogとsummaryはファイル上の再生成可能な投影であり、常駐インメモリindexを必要としない。
- 実装済みAPI
  - `GET /api/projects` — `internal/query.ListProjects`を設定済みデータ領域ごとに実行
  - `GET /api/projects/{id}/sessions` — `internal/query.Sessions`（Provider・セッション・Run・使用量・費用・操作・欠測）
  - `GET /api/projects/{id}/sessions/{provider}/{sessionId}` — `internal/query.SessionView`による新形式の共通詳細（存在しなければ404）
  - `GET /api/v1/projects/...`、`GET /api/collection-status` — 新形式のproject／work／期間／session／Run／entity詳細と収集診断
  - `GET /api/settings`、`POST /api/settings/areas`、`DELETE /api/settings/areas/{id}` — default data directory plus additional local, mounted, or network data areas. Removing an area removes only its dashboard registration, never the area contents.
- 画面（すべて`app.js`のハッシュルーティングで実装、ビルドステップ無し）: プロジェクト一覧（`#/`） → セッション一覧（`#/project/:id`、Provider別） → セッション詳細（`#/project/:id/session/:provider/:sessionId`、指示ごとのカード表示：指示文／diff行数／トークン数、`abandoned`な指示には`unmeasured`バッジ）。clocのコード規模推移は、当初案のグラフ描画ではなく開始時／終了時の数値比較に簡略化した（Chart.js等の外部ライブラリを追加しない方針のため）。

## 10. VSCode拡張設計

収集（書き込み）ロジックは全てGo側（`maatplug`のhook／exec、常に短命プロセス）に閉じているため、拡張機能は表示専任のクライアントで実現でき、feasibility上の懸念は特にない。集計（読み取り）も拡張機能内で自己完結させ、`maatplug` CLIや`maatplug-web`のサブプロセス起動には依存しない設計とする。

- VSCode拡張を実装する場合もイベントを独自解釈せず、`internal/query`と同じ問い合わせ契約を提供するローカルAPIを利用する。新形式の帰属・料金・欠測規則をTypeScriptへ重複実装しない。
- ステータスバー: 当日のトークン使用量・指示数を表示（拡張機能内でファイル監視（`vscode.workspace.createFileSystemWatcher`相当）し、追記があれば差分だけ再計算）。
- コマンド「Maatplug: Open Dashboard」→ WebViewで拡張機能自身がレンダリングするUIを表示（外部プロセスへのHTTPリクエストは発生しない）。
- `maatplug`本体（Goバイナリ）が必要になるのは、Claude Codeへのhooks登録（`maatplug init`）や、Codex/CopilotをVSCode上から明示的に実行したい場合（`maatplug exec`をターミナルタスクとして呼ぶなど）に限られる。ダッシュボード閲覧だけならmaatplugバイナリ自体は不要（フラットファイルさえ存在すれば拡張機能単体で完結する）。収集自体はVSCode拡張が行うのではなく、6.4節の前提（Claude Codeのhookがターミナル/VSCode拡張どちらの利用も共通に捕捉する）に乗る点に注意。
- ブラウザ版ダッシュボード（`maatplug-web`）とVSCode拡張は、同じJSONLスキーマを別々に実装した独立コンシューマであり、互いに依存しない（一方が無くても他方は動作する）。

## 11. モノレポ構成案

実装済み部分（Claude版＋Codex版＋Webアプリ）は次の構成。Copilotアダプタ・VSCode拡張は未着手。

```
maatplug/
  go.mod                        # 追加の外部依存なし（標準ライブラリのみ）
  cmd/
    maatplug/                   # CLI本体（init/hook/status/report）
    maatplug-web/                # Webアプリのエントリポイント（フラグ解析のみ、ロジックはinternal/webへ）
  internal/
    provider/claude/              # Claude Code固有のhookペイロード解析・transcript集計・settings.jsonマージ
    provider/codex/               # Codex固有のhookペイロード解析・turn_id相関
    recorder/                      # Provider非依存の記録ロジック
    store/                          # JSONL読み書き・project-id算出・未解決instruction相関・セッションロック
    cloc/                            # clocラッパー（存在検知・実行・パース）
    gitdiff/                          # git tree snapshot diff計測／clocフォールバック
    gitutil/                           # cloc/gitdiffが共有するgit基本操作
    aggregate/                          # 集計ロジック（CLI・Web共有）。プロジェクト/セッション/セッション詳細
    model/                               # イベント・集計の型定義（CLI・Web共有）
    web/
      server.go                          # net/http標準ServeMuxのみで実装、go:embedでfrontendを同梱
      frontend/                           # index.html／app.js／style.css（ビルド不要、外部ライブラリ無し）
  docs/design.md
```

未着手（将来追加予定）: `internal/provider/copilot`（exec-wrap方式）、`cmd/maatplug`への`exec`/`serve`/`config`サブコマンド追加、VSCode拡張一式。

## 12. リスク・要検討事項（優先度順）

1. **【最優先検証】Claude CodeのVSCode拡張が、ターミナル版CLIと同一のhook設定（`~/.claude/settings.json`）を共有するか未確認**（6.4節）。共有しない場合、VSCode拡張経由の利用がhookで捕捉できず、計測から漏れる可能性がある。共有しないと判明した場合、VSCode拡張側にも何らかの収集手段が別途必要になり8〜10節の設計に影響する。
2. Codexのトークン数はtranscriptからベストエフォートで取得するが、transcript形式は安定APIではない。Codex更新後も既知の形式を継続して解析できることをフィクスチャテストで確認し、未知の形式では誤った値を記録せず`tokensUnavailable:true`へフォールバックする。
3. Copilotのexec-wrap方式は「maatplug経由で叩いた実行」しか捕捉できない（素のCLI直接実行は非対応）。hook方式との非対称性はユーザーに明示する。
4. **GitHub Copilot Chat（VSCode拡張）は計測不可能**（6.4節）。Copilot利用の大半がChatパネル経由であるユーザーにとっては、maatplugの計測範囲が「`maatplug exec copilot`で明示的に叩いた場合のみ」に限定される点をREADME等で明確に告知する必要がある。
5. 集計ロジックをGo（CLI／Webアプリ）とTypeScript（VSCode拡張）で二重実装する設計としたため、JSONLスキーマ変更時に両実装の同期漏れが起きるリスクがある。スキーマは`docs/design.md`を単一のSource of Truthとし、変更時は両実装のテスト（フィクスチャJSONLを共有する等）で整合性を担保する。
6. 指示文をそのままフラットファイルに保存するため、機密情報を含む場合の扱いに注意（`.gitignore`推奨、将来的にマスキング設定を検討）。
7. Windows環境でのhookスクリプト実行（パス区切り・シェル差異）は実装時に個別検証が必要。
8. 非gitディレクトリのソース変更量計測（`internal/gitdiff.DiffCloc`、6.3節）は、指示開始時と終了時のclocスキャンを比較する粗い方式であり、同一ターン内での「追加と削除が両方起きた」場合の内訳を区別できず正味の増減しか分からない。git管理下のプロジェクトに比べて精度が落ちる点は利用者に明示する。
9. 複数のinstructionが未解決のまま1回のStopに到達するケース（6.1節：同一session_idの複数端末同時resume、または同一端末での追いメッセージ）は、最も古いinstructionに実測値を記録し残りを`mergedInto`で参照させる設計で回復する。同一端末の追いメッセージ（実運用で頻発する通常のケース）ではデータは失われない。一方、真の複数端末同時resume（レアケース）では、最初のStopが両方をまとめて解決してしまうため、後から来るもう一方の端末の本当のStopは「対応する未解決instructionが無い」として記録できなくなる——この一点は旧設計（全件abandoned）から変わっていないトレードオフである。常態的に複数端末から同一セッションを使う運用には依然として向かない。
