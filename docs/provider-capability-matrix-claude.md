# プロバイダー能力マトリクス（Phase 0: Claude Code / Codex 実データ検証）

`docs/proposal-command-audit-and-pricing.md`10.2節「Phase 0: Provider仕様と実データの検証」に対応する調査結果。共通モデル（3節以降）を実在しないProviderフィールドに依存させないため、Claude Code・Codex双方について、コードだけでなく**実際のtranscriptファイルの中身**を直接サンプリングして確認した。

## 0. 調査概要

### 0.1 対象・方法

| 項目 | 内容 |
|---|---|
| 対象CLI版 | Claude Code `2.1.263`／Codex CLI `0.153.4`（いずれも本調査機で`--version`実行して確認、2026-09-10時点） |
| サンプルしたtranscript | Claude Code: このリポジトリ向けセッション1本（約1,500行）。行内の`version`フィールドは`2.1.251`（セッション実施時点のバージョン。調査機の現行版とは異なり、セッションごとにバージョンが変わり得ることを示す一例）<br>Codex: この開発機の別プロジェクト向けセッション1本（約7,800行、`session_meta.cli_version`で確認） |
| 参照した既存コード | `internal/provider/claude/{claude,settings,transcript}.go`、`internal/provider/codex/{codex,transcript}.go`、`internal/usage/codex.go` |
| 除外した工程 | 公式ドキュメントとの逐条突き合わせ（`--help`以外）、VSCode拡張版との比較、複数バージョン・複数セッションでの横断確認。いずれも5節に「未確認」として明記する |

### 0.2 分類語彙

情報の確度を次の5段階で示す（`docs/proposal-command-audit-and-pricing.md`10.2節item6の想定に合わせた語彙）。

- **直接観測**: 実際のtranscript／hookペイロードのフィールドとして、今回のサンプルで値の存在を確認した。
- **Provider申告**: Providerが値として記録しているが、その正しさ（実際の環境と一致するか）を第三者的には検証していない（例: CLIバージョン文字列自体はProviderの自己申告）。
- **推定可能**: 専用フィールドは無いが、他の直接観測できるフィールドから導出できる。
- **未取得**: 存在する可能性はあるが、今回サンプルした1セッションには当該レコードが現れず確認できなかった。
- **取得手段なし**: hookペイロード・transcriptいずれの構造を見ても、該当情報を運ぶ場所が無いと判断した。

### 0.3 注意事項

実際のコマンド文字列・ファイルパス・指示内容など、調査対象セッションの作業内容そのものはこのドキュメントに転記しない。フィールド名・型・出現件数・分類値（`parsed_cmd[].type`の`"read"`のような列挙値）のみを記載する。

---

## 1. セッション・会話

| 項目 | Claude Code | Codex |
|---|---|---|
| セッションID | 直接観測（hookペイロード`session_id`、`internal/provider/claude/claude.go`の`hookCommon`） | 直接観測（`session_meta.session_id`／`.id`） |
| クライアント（CLI）バージョン | 直接観測（transcriptの`user`/`assistant`行に`version`キー。今回のサンプルは`2.1.251`） | 直接観測（`session_meta.cli_version`） |
| セッション全体のcwd | 直接観測（hookペイロード`cwd`。transcript側にも`user`/`assistant`行に`cwd`） | 直接観測（hookペイロード`cwd`、`session_meta.cwd`、`turn_context.payload.cwd`） |
| 起動元／エントリポイント | 直接観測（`entrypoint`キー。今回のサンプルは`"sdk-cli"` — Claude Agent SDK経由のCLI実行だったことを示す） | 直接観測（`session_meta.originator`／`.source`／`.thread_source`） |
| 権限モード／サンドボックス設定 | 直接観測（`permissionMode`キー。今回のサンプルは`"bypassPermissions"`） | 直接観測（`turn_context.payload.sandbox_policy`、`.approval_policy`、`.permission_profile`） |
| gitブランチ | 直接観測（`gitBranch`キー） | 未取得（同等のキーは今回未確認） |
| モデル・プロバイダーの既定値 | 未取得（セッション単位の既定モデルを表す専用フィールドは無く、ターンごとの`message.model`から推定可能） | 直接観測（`session_meta.model_provider`） |

---

## 2. 指示（ユーザー発話）とターンの対応

| 項目 | Claude Code | Codex |
|---|---|---|
| 指示本文 | 直接観測（`user`行の`message.content`。ただし`tool_result`も`type:"user"`で紛れ込むため、`isRealPrompt()`相当の除外が必要 — 既存の`internal/provider/claude/transcript.go:41-43`で対応済み） | 直接観測（`UserPromptSubmit`hookペイロード`prompt`） |
| ターンを一意に識別するID | **取得手段なし** — hookペイロードにIDが無く、`internal/store.FindUnresolvedInstructions`によるヒューリスティックな相関に依存（`docs/design.md`より） | 直接観測 — `turn_id`がhookペイロード自体（`UserPromptSubmit`／`Stop`両方）に含まれる（`internal/provider/codex/codex.go:38,49`） |
| 指示送信時刻 | 直接観測（行の`timestamp`） | 直接観測（行の`timestamp`） |
| 添付資料 | 直接観測 — `attachment`という専用の行種別が存在（今回のサンプルで327件） | 未取得（今回のサンプルセッションに添付操作が無かった） |

---

## 3. モデル呼び出し・使用量

既存コード（`internal/provider/claude/transcript.go`、`internal/provider/codex/transcript.go`）がすでにトークン数・モデル名の抽出に使っている部分は概ね正確（両アダプターのテストで検証済み）。今回追加で確認できた点のみ記す。

| 項目 | Claude Code | Codex |
|---|---|---|
| 応答の一意識別 | 直接観測 — `message.id`優先、無ければ`requestId`（`assistantUsageKey`、`transcript.go:246-258`） | 直接観測 — `root_turn_id`キー（`token_usage_record`） |
| 使用量の記録方式 | 直接観測 — 各応答行が完全な`usage`を繰り返し持つ（重複排除が必要） | 直接観測 — **2系統を実データで確認**: 新しい`token_usage_record`（ターン単位の確定値）と、古い`event_msg/token_count`（累積値。`task_complete`前後の差分計算が必要、既存の`subtractUsage`ロジックが対応） |
| 実際に使用されたモデル名 | 直接観測（`message.model`、応答ごと） | 直接観測（`turn_context.payload.model`、ターンごと） |
| モデル切り替え設定 | 未取得（今回のサンプルに複数モデル混在は無かった） | 直接観測 — `turn_context.payload.collaboration_mode.settings.model`／`.reasoning_effort`（設定として存在） |

---

## 4. ツール呼び出し・コマンド実行

Claude・Codexで情報量の差が最も大きい領域。両者とも今回のサンプルで実際にコマンド実行の記録を直接確認できた。

### 4.1 Claude Code

- `assistant`行の`message.content[]`に`type:"tool_use"`ブロック（今回のサンプルで`Bash`82件、`Edit`90件、`Write`42件、`Read`67件等）。`Bash`の`input`は`{"command": "...", "description": "..."}`の2キー構造（直接観測）。
- 対応する結果は`user`行の`message.content[].type:"tool_result"`（`tool_use_id`で紐付け）。加えてClaude Code独自の便宜フィールド`toolUseResult`が同じ`user`行に付き、`Bash`の場合`{"stdout","stderr","interrupted","isImage","noOutputExpected"}`（直接観測）。
- **終了コード相当のフィールドは無い**（`interrupted`はあるが正常/異常終了の判定はstdout/stderrの内容次第、取得手段なし）。
- **コマンド単体の所要時間フィールドは無い**（`tool_use`行と`tool_result`行のタイムスタンプ差分から推定可能）。
- 実行時cwdは個々のコマンド単位では記録されない（セッション全体のcwdのみ、未取得〜取得手段なし寄り）。
- Codexのような「コマンド種別の事前分類」は無い（生のコマンド文字列のみ、取得手段なし）。
- `internal/provider/claude/transcript.go`は現状これらを一切デコードしておらず（`Content`は`json.RawMessage`のまま、`assistantText()`がテキストブロックのみ抽出）、コード上は未使用。

### 4.2 Codex

Codexは低レベル・高レベルの2種類のレコードを実データで確認した。

**(a) `response_item`/`custom_tool_call`（+`custom_tool_call_output`）— 低レベル**（サンプルで874件ずつ）。`payload.name`は`"exec"`（874件）と`"wait"`（15件）の2種のみ観測。`payload.input`は`const r = await tools.exec_command({"cmd":"...","workdir":"...","yield_time_ms":N,"max_output_tokens":N});`という**JS関数呼び出し風の文字列**で、内部にJSON的なオブジェクトが埋め込まれる独特な形式（直接観測）。

**(b) `event_msg`/`item_completed`（`item.type=="CommandExecution"`）— 高レベル・パース済み**（サンプルで547件）。こちらの方が実装で使いやすい。実データで確認したフィールド:

```
item.command       // argv配列
item.cwd            // コマンド実行時点のcwd
item.parsed_cmd      // Codex自身によるコマンドの意味分類（下記）
item.source           // 呼び出し経路（例: "unified_exec_startup"）
item.status            // "completed" / "failed" 等
item.stdout / item.stderr / item.aggregated_output / item.formatted_output
item.exit_code         // 数値。実データで 0（成功）・1（失敗）を確認
item.duration          // {secs, nanos}。コマンド1回ごとの所要時間
```

`item.parsed_cmd`は配列で、各要素`{"type","cmd","name"?,"path"?}`。実データで`type`の値として`"read"`（ファイル読取系。`name`/`path`も付与される）と`"unknown"`（分類不能）を確認した（`"write"`等の他の分類値は今回未確認）。

| 項目 | Claude Code | Codex |
|---|---|---|
| コマンド文字列 | 直接観測（`tool_use.input.command`） | 直接観測（`CommandExecution.command`／`.parsed_cmd[].cmd`） |
| stdout/stderr | 直接観測（`toolUseResult`） | 直接観測（`CommandExecution`直下） |
| 終了コード | 取得手段なし | **直接観測** |
| 所要時間（コマンド単位） | 推定可能（行タイムスタンプ差分） | **直接観測**（`duration.{secs,nanos}`） |
| 実行時cwd（コマンド単位） | 取得手段なし（セッション全体のcwdのみ） | **直接観測** |
| コマンド種別の事前分類 | 取得手段なし | **直接観測**（`parsed_cmd[].type`。ただし観測できた値は`"read"`/`"unknown"`のみ） |

---

## 5. ファイル変更

| 項目 | Claude Code | Codex |
|---|---|---|
| 変更操作の記録 | 直接観測 — `Edit`/`Write`の`tool_use.input`に変更内容が含まれる（今回のサンプルで`Write`42件・`Edit`90件）。ただしdiff集計自体は既存のgit経由パイプライン（`internal/gitdiff`）に依存している | **直接観測・ネイティブ** — `item_completed`の`item.type=="FileChange"`（サンプルで297件）。`item.changes`が「変更ファイルパス → `{type, content}`」のマップで、**gitに依存せずCodex自身が変更ファイル一覧と種別を記録している** |
| 言語別内訳への転用可能性 | 既存の`gitdiff.DiffTrees`が`git diff --numstat`のパス（現状は捨てている）から拡張子推定で導出可能（`docs/proposal-command-audit-and-pricing.md`3.2節） | `FileChange.changes`のパスキーから同様に拡張子推定で導出可能。gitが無い環境でも使える点でClaudeより優位 |

---

## 6. ネットワーク・通信

| 項目 | Claude Code | Codex |
|---|---|---|
| セッション／ターン単位のネットワーク許可設定 | 取得手段なし | **直接観測** — `turn_context.payload.sandbox_policy.network_access`（真偽値。今回のサンプルは全ターン`false`） |
| 実際の通信先（コマンドから抽出） | 推定可能（`tool_use.input.command`の文字列からbest-effortでcurl/wget/ssh等を抽出する実装が必要、`docs/proposal-command-audit-and-pricing.md`2.2/2.3節で検討済み） | 推定可能（`CommandExecution.command`／`parsed_cmd[].cmd`から同様に抽出） |

`sandbox_policy.network_access`はコマンド文字列からの抽出より前段の、安価で確実な絞り込み条件として使える（Codexのみ）。

---

## 7. 承認（approval）

| 項目 | Claude Code | Codex |
|---|---|---|
| セッション単位の権限設定 | 直接観測（`permissionMode`。今回のサンプルは`"bypassPermissions"`のため、承認が発生しないモードだった） | 直接観測（`turn_context.payload.approval_policy`／`.approvals_reviewer`） |
| 承認済みコマンドの一覧 | 未取得 | 直接観測（`world_state.payload.state.permissions.approved_command_prefixes`） |
| 個別の承認要求／許可／拒否イベント | 未取得（今回のサンプルは`bypassPermissions`のため発生し得なかった） | 未取得（今回のサンプルはsandbox内実行でネットワーク・承認とも不要だったため発生しなかった） |

両Providerとも、**承認が実際に発生するセッション**をサンプルできていない。この項目は今回の調査で最も確度が低く、別途承認が発生する条件（Claude: `permissionMode`が`default`等、Codex: `approval_policy`が`on-request`等）のセッションを確保して再調査する必要がある。

---

## 8. サブエージェント

| 項目 | Claude Code | Codex |
|---|---|---|
| メカニズムの存在 | 直接観測 — `isSidechain`フラグが`user`/`assistant`行に存在（既存コードも`transcript.go`の`isRealPrompt`/`isAssistantResponse`でこのフラグを参照済み） | 直接観測 — `multi_agent_mode`／`multi_agent_version`フィールドが`world_state`/`turn_context`に存在 |
| 実際の委任データ | **未取得** — 今回のサンプルはどちらも`isSidechain:true`（Claude）／サブエージェント委任（Codex）を含まないセッションだった | **未取得**（同上） |

両Providerともメカニズム自体はコード上・スキーマ上確認できたが、実際にサブエージェントが動いたセッションのサンプルが取れておらず、フィールドの中身（例: 委任元指示との対応関係）は未検証。

---

## 9. 再試行・中断・継続

| 項目 | Claude Code | Codex |
|---|---|---|
| ターン中断イベント | 未取得（今回のサンプルに該当イベントなし） | **直接観測** — `event_msg/turn_aborted`（サンプルで3件）。`reason`／`started_at`／`completed_at`／`duration_ms`を保持 |
| コンテキスト圧縮（要約） | 未取得 | **直接観測・2種類** — トップレベル`type:"compacted"`（`message`・`replacement_history`・`window_number`・`window_id`等を持つ実質的な内容、サンプルで6件）と、`item_completed`の`item.type:"ContextCompaction"`（マーカーのみ、内容フィールドなし） |

Claude Code側は、hookイベントの構成（`SessionStart`/`UserPromptSubmit`/`Stop`/`SessionEnd`のみ）からも、中断・要約に相当する情報を拾う経路がそもそも用意されていない。この2項目はCodexとの非対称が特に大きい。

---

## 10. 比較まとめ

| 項目 | Claude Code | Codex |
|---|---|---|
| ターンID（hookペイロード） | 取得手段なし | 直接観測 |
| コマンド文字列 | 直接観測 | 直接観測 |
| stdout/stderr | 直接観測 | 直接観測 |
| 終了コード | 取得手段なし | 直接観測 |
| コマンド所要時間 | 推定可能 | 直接観測 |
| コマンド単位cwd | 取得手段なし | 直接観測 |
| コマンド種別分類 | 取得手段なし | 直接観測 |
| ファイル変更のネイティブ記録 | 未取得（tool_use inputに一部あり） | 直接観測 |
| ネットワーク許可設定 | 取得手段なし | 直接観測 |
| 承認関連イベント | 未取得 | 未取得 |
| サブエージェント機構 | 直接観測（実データ未取得） | 直接観測（実データ未取得） |
| 中断イベント | 未取得 | 直接観測 |
| コンテキスト圧縮 | 未取得 | 直接観測 |
| フォーマットの安定性保証 | 非公式（`docs/design.md`上も明記なし） | 明示的に非保証（`internal/provider/codex/transcript.go:46-48,130-133`） |
| maatplug既存コードでの利用状況 | text blockのみ使用。tool_use/tool_result/attachment等は未使用 | token/model用の一部フィールドのみ使用。コマンド・ファイル変更・承認・中断・圧縮関連レコードは完全未使用 |

**総括**: 今回のサンプルで確認できた範囲では、情報の豊富さは一貫してCodexの方が上回る（終了コード・所要時間・コマンド分類・ネットワーク設定・ファイル変更・中断・コンテキスト圧縮のいずれも、Codexは構造化された専用レコードを持つ一方、Claude Codeは同等のものを持たないか、生のテキストから推定するしかない）。一方でフォーマットの安定性保証はCodexの方が明確に弱い（コード内コメントで非保証と明記）。**「情報量はCodexが優位、将来のバージョン変更リスクもCodexが高い」という2軸で捉えるべきで、「Codexは難しいから後回し」という単純な評価は実データと整合しない。**

---

## 11. 提案ドキュメントへの示唆

- 3節（共通データモデル）の`EventEnvelope`／操作イベント設計は、Codexの`CommandExecution`／`FileChange`のような構造化レコードをそのまま取り込める形にしておくと情報の目減りが少ない。Claude側は同じスキーマ上で多くの項目が「取得手段なし」のまま埋まる非対称を許容する必要がある（10.5節「片方のProviderにしかない情報が、もう片方で偽の既定値にならない」という完了条件と整合）。
- 7節（監査設計）のうち、ネットワーク通信の監査はCodexの`sandbox_policy.network_access`が使える一方、Claudeには相当する情報が無い。Claude側の監査精度は構造的にCodexより低くなる前提を置く必要がある。
- 8.3節の受入条件のうち「拒否・起動成功・部分失敗・非同期完了」「同じ大きなツール出力を複数回入力、一部を切り詰め」等は、今回のサンプルでは承認発生・部分失敗ケースを確保できておらず、fixture作成時に意図的にこれらの状況を含むセッションを再現する必要がある。
- 10.5節（Phase 3 Providerアダプター）の「Phase 0で利用可能と確認できたツール・承認・サブエージェントの通知を取り込む」に対応する取り込み候補は、本ドキュメント4〜9節の「直接観測」項目。「未取得」項目は今回のサンプル不足によるものか、本当に存在しないのかを再調査してから実装対象を確定する。

---

## 12. 未確認・要フォロー事項（本調査のスコープ外）

- **公式資料との突き合わせ**: `--version`の確認のみ行い、Claude Code／Codexそれぞれの公式hooksリファレンス・transcriptスキーマ文書との逐条照合は実施していない。
- **CLIとVSCode拡張の一致確認**: 未実施。`docs/design.md`12節が挙げている既知の要検証事項のまま。
- **成果物としてのfixture／README**: `docs/proposal-command-audit-and-pricing.md`10.2節が求める`internal/provider/claude/testdata/`・`internal/provider/codex/testdata/`への匿名化fixtureと、その説明README作成は本ドキュメントの範囲に含めていない（別途対応が必要）。
- **サンプル数の少なさ**: 各Provider1セッションのみ。承認発生・サブエージェント委任・複数モデル混在・複数バージョンにまたがる比較・カウンターリセット等のエッジケースは未確認。
- **`parsed_cmd[].type`の全列挙値**: 今回`"read"`／`"unknown"`のみ観測。`"write"`等、他の分類値が存在するかは追加サンプルが必要。
- **Codexの`response_item`/`function_call`（旧形式、サンプル中15件のみ）**: 最新CLIで継続して出力されるか、廃止予定かは未確認。
