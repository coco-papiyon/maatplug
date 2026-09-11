# プロバイダー能力マトリクス（Phase 0 統合版）

`docs/proposal-command-audit-and-pricing.md`10.2節「Phase 0: Provider仕様と実データの検証」の成果物。[`provider-capability-matrix-claude.md`](provider-capability-matrix-claude.md)（このセッションが実施）と[`provider-capability-matrix-codex.md`](provider-capability-matrix-codex.md)（Codexが実施）という2本の独立調査を突き合わせ、記述が食い違っていた箇所を実機の追加調査で解消した最終版。2本の先行ドキュメントは調査記録としてそのまま残し、本書はそれらの統合・訂正版として扱う。

## 0. 統合方針

- 判定語彙は両ドキュメント共通の5段階（**直接観測**／**Provider申告**／**推定可能**／**未取得**／**取得手段なし**、定義は1節参照）をそのまま採用する。
- 2本のドキュメントの記述が一致している項目は、より詳しい方（多くはCodex側）の記述をベースに採用する。
- 2本の記述が食い違っている項目は、本書のために追加で実機調査を行い、どちらが正しいか・なぜ食い違ったかを2節に記録した上でマトリクスに反映した。
- 調査環境（統合後）:

| 対象 | 版 | 確認方法 |
|---|---|---|
| Claude Code CLI | `2.1.263` | `--version`、および本書のため実際に一時hook設定で`claude -p`を起動しstdin payloadを直接確認（2節参照） |
| Claude Code VS Code拡張 | `2.1.266`（同梱CLI `2.1.266`） | `-codex.md`調査による。本書での再確認は未実施 |
| Codex CLI | `0.153.0`／`0.153.4` | `-codex.md`調査、および本書のため実rollout file 2種（`0.151.0-alpha.7.2`と`0.153.0`）を追加比較（2節参照） |
| Codex VS Code拡張 | `26.903.61454`（同梱CLI `0.153.4`） | `-codex.md`調査による。本書での再確認は未実施 |

## 1. 判定語彙（再掲）

| 判定 | 意味 |
|---|---|
| **直接観測** | 実際のCLI出力・hook payload・transcript・インストール物から値と型を確認した |
| **Provider申告** | 公式資料・生成スキーマ・`--help`が仕様として示すが、該当イベントを実行時に発生させて確認してはいない |
| **推定可能** | 専用フィールドはないが、複数の観測値・差分・順序・テキストから導出できる |
| **未取得** | 取得経路はあるが、今回の対象版・シナリオでは値を直接採取できなかった |
| **取得手段なし** | 調査した出力だけでは取得できない。別の収集源（OS/VCS/請求系）が必要 |

---

## 2. 再調査で解消した相違点

`docs/proposal-command-audit-and-pricing.md`10.2節「実施内容」item 4（暫定値→確定値、複数経路での重複）とは別に、**2本のPhase 0ドキュメント自体の記述が食い違っていた3項目**を、本書作成のために追加調査して解消した。ここが今回の統合作業でもっとも重要な部分である。

### 2.1 Claude Codeの指示（ターン）相関ID — `-codex.md`が正しかった

- `-claude.md`の記述: 「取得手段なし — hookペイロードにIDが無く、`internal/store.FindUnresolvedInstructions`によるヒューリスティックな相関に依存」
- `-codex.md`の記述: 「**直接観測** `prompt_id`。現行設計にある『Claude Code hookにはprompt IDがない』という前提は、2.1.263では成立しない」

**再調査**: 一時的な`.claude/settings.json`でhookをファイルへの`cat`にリダイレクトし、実際に`claude -p "Reply with exactly the word OK. Do not use any tools." --output-format json`を実行してstdin payloadを直接確認した（作業ディレクトリ・設定ファイルは調査後に削除済み）。結果、`UserPromptSubmit`・`Stop`・`SessionEnd`のいずれにも`prompt_id`（例: `54f38939-1a1e-4acb-846e-7b5541bb95fe`、UUID形式）が入っており、同一ターン内で値が一致することを確認した。`internal/provider/claude/testdata/phase0/hook-lifecycle.jsonl`（`-codex.md`が作成したfixture）の内容とも一致する。

**結論**: `-codex.md`が正しい。`-claude.md`の記述は誤り（当時のリポジトリコードとdesign.mdの記述のみに基づき、実hookペイロードそのものを確認していなかったため）。

**実装への示唆**: maatplugの`internal/provider/claude/claude.go`は現状`prompt_id`を一切デコードしておらず（`hookCommon`／各ハンドラーの構造体に無い）、`docs/design.md`が明記する「UserPromptSubmitとStopはID無しでFindUnresolvedInstructionsのみで相関する」という設計判断は、少なくとも2.1.263では前提が崩れている。`prompt_id`を読み取って一次的な相関キーとして採用し、既存のヒューリスティックは失敗時のフォールバックに格下げすることを検討する価値がある。

### 2.2 Codexコマンド実行の終了コード・所要時間・コマンド種別分類 — バージョン差が原因で両者とも部分的に正しかった

- `-claude.md`の記述: `exit_code`・`duration`・`parsed_cmd`（コマンド種別分類）いずれも「直接観測」（`event_msg`/`item_completed`の`item.type=="CommandExecution"`から）
- `-codex.md`の記述: 終了コードは「hookでは未取得、App Server command itemはProvider申告」、所要時間は「Provider申告、欠測時は推定可能」

**再調査**: `-claude.md`がサンプルした実rollout fileのCLIバージョンを確認したところ`0.151.0-alpha.7.2`だった。これは現行（`0.153.0`系）より古いプレリリース版である。同じ開発機に残っていた`0.153.0`の実rollout file（`session_meta.cli_version`で確認済み、1,260行規模）を追加で調査したところ、**`event_msg`/`item_completed`という仕組み自体が1件も現れず**、`item.type=="CommandExecution"`・`exit_code`・`duration`・`parsed_cmd`のいずれも確認できなかった。`0.153.0`のコマンド実行は低レベルの`response_item`/`custom_tool_call`（`name:"exec"`）のみで表現されており、その出力テキストの先頭に`"Script completed\nWall time X.X seconds\nOutput:\n"`という非構造化の所要時間表記が残るのみだった。

**結論**: 両ドキュメントとも自分がサンプルしたバージョンについては正しかったが、**バージョンをまたいで一般化した記述はどちらも不正確**だった。現行版（`0.153.0`系）を基準にすると:

| 項目 | `0.151.0-alpha.7.2` | `0.153.0` |
|---|---|---|
| 終了コード | 直接観測（`CommandExecution.exit_code`） | **未取得**（相当する構造化フィールドが無い） |
| 所要時間（コマンド単位） | 直接観測（`duration.{secs,nanos}`） | **推定可能**（`custom_tool_call_output`のテキスト先頭の`"Wall time X.X seconds"`をパースする必要がある） |
| コマンド種別事前分類 | 直接観測（`parsed_cmd[].type`） | **取得手段なし**（相当する構造が無い） |

このバージョン間差異を裏付ける実例を`internal/provider/codex/testdata/phase0/command-execution-legacy-0.151.jsonl`（旧版）と`exec-events.jsonl`（新版、既存fixture）として残した。

**実装への示唆**: `internal/provider/codex/transcript.go:46-48`のコメント「transcript形式は安定APIではない」は、努力目標的な注意書きではなく、**同一開発機の2バージョン間で実際に起きていた事実**である。終了コード・コマンド種別分類に依存する設計（例: 監査での「破壊的操作」自動判定）は、利用可能なら使う「ボーナス情報」として扱い、前提条件にしてはならない。

### 2.3 Codexファイル変更のネイティブ記録 — 存在はするが記述が古かった

- `-claude.md`の記述: 「直接観測・ネイティブ — `item_completed`の`item.type=="FileChange"`。`item.changes`が`{パス: {type, content}}`」
- `-codex.md`の記述: 「App Server `fileChange`は**Provider申告**。VCS差分から推定可能」（実データでの直接観測とまでは踏み込んでいない）

**再調査**: 2.2節と同じ`0.153.0`の実rollout fileを調べたところ、ファイル変更のネイティブ記録**自体は現行版にも存在した**。ただしイベント種別が`item_completed`/`FileChange`ではなく`event_msg`/`patch_apply_end`に変わっており、`changes`マップの値も`{type, content}`ではなく`{type, unified_diff, move_path}`（`content`全文ではなく差分形式）になっていた。

**結論**: 「gitに依存せずCodex自身がファイル変更を記録している」という`-claude.md`の主張の**方向性は正しい**が、参照していたフィールド名（`item_completed`/`FileChange`/`content`）は古いバージョンのものだった。`-codex.md`の「Provider申告」判定は、実データでの確認が不足していたための過小評価だった。現行版での正しい判定は**直接観測**（`event_msg`/`patch_apply_end`、`changes: {パス: {type, unified_diff, move_path}}`）。実例を`internal/provider/codex/testdata/phase0/file-change-0.153.jsonl`として追加した。

---

## 3. 統合マトリクス

以下は2節の訂正を反映した最終版。個々のセルの詳細な理由づけは元ドキュメント（`-claude.md`／`-codex.md`）の対応節を参照。

### 3.1 セッション・指示・応答・使用量

| 収集項目 | Claude Code | Codex |
|---|---|---|
| セッションID | 直接観測（hook `session_id`、transcript `sessionId`） | 直接観測（hook `session_id`、`session_meta.session_id`） |
| 指示（ターン）ID | **直接観測** `prompt_id`（2.1節で再確認。UserPromptSubmit/Stop/SessionEndに共通） | 直接観測（hook `turn_id`、transcript `turn_id`/`root_turn_id`） |
| 指示本文 | 直接観測（hook `prompt`、transcript `message.content`） | 直接観測（hook `prompt`） |
| クライアント（CLI）バージョン | 直接観測（transcript行の`version`キー） | 直接観測（`session_meta.cli_version`） |
| モデル・実行モデル | 直接観測（`message.model`） | 直接観測（`turn_context.payload.model`） |
| 応答（API呼び出し）単位usage | 直接観測（`message.usage`、同一`message.id`の反復に注意） | 直接観測（`token_usage_record`。ただし2.2節の通りバージョンにより有無が変わる。旧版の代替として`event_msg/token_count`＋`task_complete`の累積差分方式もある） |
| ターン／セッション累積usage | 推定可能（応答単位usageの積算） | 直接観測（`turn_token_usage`／`thread_token_usage`、`token_usage_record`がある版のみ） |
| Providerのコスト見積り | 直接観測（`claude -p`のresult `total_cost_usd`／`modelUsage[].costUSD`） | 取得手段なし（ローカルには無し）。料金表から推定可能 |
| 実請求・subscription消費・返金 | 取得手段なし | 取得手段なし |

### 3.2 ツール呼び出し・コマンド実行

| 収集項目 | Claude Code | Codex（`0.153.0`系、現行） |
|---|---|---|
| コマンド文字列 | 直接観測（`tool_use.input.command`、`name=="Bash"`） | 直接観測（`response_item`/`custom_tool_call`の`payload.input`。JS関数呼び出し風文字列に`cmd`/`workdir`等が埋め込まれる） |
| stdout/stderr | 直接観測（`toolUseResult.stdout`/`.stderr`） | 直接観測（`custom_tool_call_output.output[].text`） |
| 終了コード | 取得手段なし | **未取得**（2.2節で訂正。構造化フィールドは現行版に無い） |
| 所要時間（コマンド単位） | 推定可能（`tool_use`/`tool_result`行のタイムスタンプ差） | **推定可能**（2.2節で訂正。出力テキスト先頭の`"Wall time X.X seconds"`をパース） |
| 実行時cwd（コマンド単位） | 取得手段なし（セッション全体のcwdのみ） | 未取得（低レベルレコードに専用フィールドなし。`turn_context.payload.cwd`はターン単位） |
| コマンド種別の事前分類 | 取得手段なし | **取得手段なし**（2.2節で訂正。現行版に`parsed_cmd`相当の構造なし） |
| Provider側の危険性・承認判断 | 未取得（今回のサンプルは`permissionMode:"bypassPermissions"`で承認が発生しないモード） | 未取得（今回のサンプルは承認が発生しなかった） |

### 3.3 ファイル変更

| 収集項目 | Claude Code | Codex（`0.153.0`系、現行） |
|---|---|---|
| ネイティブな変更記録 | 直接観測（`Edit`/`Write`の`tool_use.input`。ただしdiff集計自体は既存の`internal/gitdiff`（git経由）に依存） | **直接観測**（2.3節で訂正・再確認。`event_msg`/`patch_apply_end`、`changes: {パス: {type, unified_diff, move_path}}`。gitに依存しない） |
| 言語別内訳への転用可能性 | `gitdiff.DiffTrees`が読み捨てている`git diff --numstat`のパスから拡張子推定で導出可能 | `patch_apply_end.changes`のパスキーから同様に拡張子推定で導出可能。gitが無い環境でも使える |

### 3.4 ネットワーク・承認

| 収集項目 | Claude Code | Codex |
|---|---|---|
| セッション／ターン単位のネットワーク許可設定 | 取得手段なし | 直接観測（`turn_context.payload.sandbox_policy.network_access`、真偽値。`0.153.0`でも存在を再確認済み） |
| 承認ポリシー設定 | 直接観測（`permissionMode`） | 直接観測（`turn_context.payload.approval_policy`、`world_state.payload.state.permissions.approved_command_prefixes`。`0.153.0`でも存在を再確認済み） |
| 個別の承認要求／許可／拒否イベント | 未取得（サンプルが`bypassPermissions`のため発生せず） | 未取得（サンプルがsandbox内実行で発生せず） |
| 実際の通信先 | 推定可能（コマンド文字列からのbest-effort抽出） | 推定可能（同上） |

### 3.5 サブエージェント・中断・コンテキスト圧縮

| 収集項目 | Claude Code | Codex |
|---|---|---|
| サブエージェント機構の存在 | 直接観測（`isSidechain`フラグ） | 直接観測（`multi_agent_mode`／`multi_agent_version`） |
| サブエージェントの実データ | 未取得（サンプルに委任なし） | 未取得（同上） |
| ターン中断イベント | 未取得 | 直接観測（`event_msg`/`turn_aborted`。`reason`/`started_at`/`completed_at`/`duration_ms`） |
| コンテキスト圧縮 | 未取得 | 直接観測（`0.151.0-alpha.7.2`ではトップレベル`type:"compacted"`＋`item_completed`の`ContextCompaction`マーカー。`0.153.0`では`event_msg`/`context_compacted`という別名で存在を確認 — **こちらもイベント名がバージョン間で変わっている**） |
| 添付資料 | 直接観測（`attachment`行種別） | 未取得 |

---

## 4. 8.3節の受入ケースへの対応

`-codex.md`7節の対応表を踏襲する（今回の再調査による訂正は3節に反映済みで、このケース対応表自体への影響はない）。詳細は[`provider-capability-matrix-codex.md`](provider-capability-matrix-codex.md)7節を参照。要点のみ再掲する。

| 受入ケース | 収集源と判定 |
|---|---|
| 重複再送・provisional更新 | Claude: 同一`message.id`の反復を直接観測。Codex: response/turn/thread usageスコープを直接観測（版により`token_usage_record`の有無が変わる点は2.2節で追記） |
| 拒否・起動成功・部分失敗・非同期完了 | 両Providerとも承認発生イベントは未取得。tool/permission lifecycleの状態遷移設計は必要（Provider申告） |
| requestedとactual network destination | requestedは一部Provider申告、actualは取得手段なし。OS telemetryが別途必要 |
| pricing change・取得失敗・unknown | 料金表とusageから推定可能。実請求は取得手段なし |
| format cutover | 直接観測 — 本書2節がまさにその実例（同一開発機で`0.151.0-alpha.7.2`→`0.153.0`の間にコマンド実行・ファイル変更・コンテキスト圧縮のイベント名が変わっていた） |

---

## 5. Phase 1・Phase 3への決定事項（統合後）

`-codex.md`8節の決定事項1〜10は本書でも有効。以下は今回の再調査で追加・強化された点のみ挙げる。

1. **Claude Codeの第一相関キーを`prompt_id`に変更する**（2.1節）。`internal/store.FindUnresolvedInstructions`のヒューリスティックは、`prompt_id`が欠測した場合のフォールバックに格下げする。
2. **Codexのコマンド実行・ファイル変更・コンテキスト圧縮関連イベントは、フィールド単位ではなく「イベント名の集合」ごとバージョンで変わり得る**ことを前提に設計する（2.2/2.3節）。共通スキーマへの変換層は、複数バージョンのイベント名を吸収するアダプター（例: `item_completed`/`CommandExecution`と`custom_tool_call`のどちらから来ても同じ`Operation`イベントに正規化する）として実装し、単一バージョンのフィールド名をハードコードしない。
3. **収集記録には対象バージョンを必ず記録する**（`-codex.md`8節item1の`engine_version`はこの目的にも使う）。Phase 0 fixtureは`0.151.0-alpha.7.2`と`0.153.0`の両方を残し、将来のバージョンで再度形式が変わった場合に検知できるようにする（回帰テスト用）。
4. `exit_code`・コマンド種別分類（`parsed_cmd`相当）に依存する監査ロジック（`docs/proposal-command-audit-and-pricing.md`1節の監査用情報）は、**現行のCodexバージョンでは前提にできない**。取得できたときのみ利用する任意情報として設計し、無い場合はテキスト（コマンド文字列そのもの、出力テキスト）からの推定にフォールバックする。

---

## 6. 残る検証項目

`-codex.md`9節の項目に加え、本書の再調査で新たに判明した点を追加する。

- `-codex.md`9節の項目（VS Code GUIからの同一シナリオ実行、deny/failure/subagent/compact/model switch/interruptの発生、親子usageの包含関係、telemetry照合、App Server public eventのみでの充足性確認、CLI/拡張自動更新後の再取得）は未解消のまま引き継ぐ。
- **Codexの`0.153.0`系で`exit_code`相当の情報が本当に一切取得できないのか**は、より多くの実セッション（特に失敗したコマンドを含むセッション）で確認する必要がある。今回確認できた失敗コマンド（`status:"failed"`のパターンは旧版でのみ確認）は新版のサンプルには含まれていなかった。
- **`0.153.0`と`0.153.4`（VS Code拡張同梱）で、今回発見したイベント名（`patch_apply_end`、`context_compacted`等）が同一か**は未確認（`-codex.md`はApp Serverスキーマを`0.153.4`から生成しているが、rollout file自体は`0.153.0`のみで確認した）。
- Claude Code側で`prompt_id`が複数ターン・複数セッションにわたって本当に一意であること（再利用や衝突が無いこと）は、単一ターンの確認のみでは言い切れない。

---

## 7. 匿名化fixture

| Provider | ディレクトリ | 追加・更新 |
|---|---|---|
| Claude Code | [`internal/provider/claude/testdata/phase0/`](../internal/provider/claude/testdata/phase0/README.md) | 既存3件に加え、本書のため`bash-tool-use.jsonl`（`Bash`の`tool_use`/`tool_result`/`toolUseResult`）を追加 |
| Codex | [`internal/provider/codex/testdata/phase0/`](../internal/provider/codex/testdata/phase0/README.md) | 既存4件に加え、本書のため`file-change-0.153.jsonl`（現行版の`patch_apply_end`）と`command-execution-legacy-0.151.jsonl`（旧版の`CommandExecution`、バージョン差の実例）を追加 |

fixtureは実データの内容を再配布するものではない。field presence・型・順序・IDの相関・重複関係のみを再現し、実際のコマンド文字列・ファイルパス・本文は合成値に置換してある。

## 8. 参照した一次資料

`-codex.md`11節（Claude Code Hooks reference、Sessions、Monitoring usage with OpenTelemetry、Manage costs effectively、Agent SDK cost tracking、VS Code integration、Status line、Codex Hooks、App Server、Advanced configuration and OpenTelemetry、Pricing、OpenAI Codex source repository）をそのまま引き継ぐ。本書のために新たに参照した一次資料は無く、2節の再調査はすべて実機（CLI実行・実rollout file比較）による直接観測である。
