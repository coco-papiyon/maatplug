# Provider能力マトリクス（Codex調査、Phase 0）

更新日: 2026-09-10

本書は [`proposal-command-audit-and-pricing.md`](proposal-command-audit-and-pricing.md) 10.2節の Phase 0 を実施した結果である。調査の実施主体は Codex、調査対象は Claude Code と Codex の両方である。目的は、指示ごとのトークン使用量と費用を説明し、ツール・承認・危険な操作を監査するために、Provider から何をどの粒度で収集できるかを確定することにある。

結論は次のとおりである。

1. 指示との相関には Claude Code の `prompt_id`、Codex の `turn_id` を利用できる。現行設計にある「Claude Code hook には prompt ID がない」という前提は、2.1.263 では成立しない。
2. どちらも単一情報源だけでは足りない。hook は指示・操作・承認・終了、telemetry は API 呼び出し・使用量・再試行、transcript は内容と復旧用の原記録として役割を分ける。
3. hook payload には Provider の発生時刻がない。collector の受信時刻を Provider 時刻として扱わず、transcript、OpenTelemetry、App Server の時刻と別フィールドで保存する必要がある。
4. Claude Code transcript は同一 `message.id` の複数行に同じ usage が繰り返される。Codex は response、turn、thread の usage を別スコープで持つ。元記録を残し、ID・スコープ・確定状態を使って採用値を選ばなければ二重計上になる。
5. Provider 内だけで、実際のネットワーク接続先、シェルが行った全ファイル変更、実請求・返金を完全には取得できない。OS telemetry、差分取得、管理者向け請求データを別の収集源として扱う。
6. CLI は Windows で直接観測できた。VS Code 拡張はインストール物と公式の共有設定／App Server 経路まで確認したが、GUI から同じシナリオを再生した直接観測は未実施である。

## 1. 判定方法

各セルは次のいずれかで判定する。

| 判定 | 意味 |
|---|---|
| **直接観測** | 今回の Windows 環境で、実際の CLI 出力、hook payload、transcript、またはインストール物から値と型を確認した |
| **Provider申告** | 公式資料、公式生成スキーマ、`--help` が仕様として示しているが、該当イベントを今回の実行では発生させていない |
| **推定可能** | 専用値はないが、複数の観測値、差分、順序から推定できる。推定値であることを保持する必要がある |
| **未取得** | 取得経路は示されているが、今回の対象版・シナリオでは値を直接採取していない |
| **取得手段なし** | 調査した Provider の出力だけでは取得できない。別の OS、VCS、請求系の収集源が必要 |

`Provider申告` と `直接観測` は値の真実性を保証する等級ではない。たとえば token 数は Provider が計算した値を直接観測したものであり、独自 tokenizer による再計算結果ではない。

## 2. 調査環境と方法

| 対象 | 観測値 | 方法 |
|---|---|---|
| OS | Microsoft Windows `10.0.26200`、`Windows NT 10.0.26200.0` | OS 情報を直接取得 |
| Claude Code CLI | `2.1.263 (Claude Code)` | PATH 上の `claude.exe --version` |
| Claude Code VS Code 拡張 | `anthropic.claude-code-2.1.266-win32-x64`、同梱 CLI `2.1.266` | 拡張ディレクトリ、`package.json`、同梱実行ファイル |
| Codex CLI | `codex-cli 0.153.0` | PATH 上の `codex.exe --version`。PATH は旧 OpenAI 拡張同梱バイナリを指していた |
| Codex VS Code 拡張 | `openai.chatgpt-26.903.61454-win32-x64`、同梱 CLI `0.153.4` | 拡張ディレクトリ、`package.json`、同梱実行ファイル |

CLI の hook を一時設定で有効にし、秘密を含まない最小指示を非対話実行した。Claude Code は `claude -p --output-format json`、Codex は `codex exec --json --ephemeral` を使用した。既存のローカル transcript は内容を出力せず、行種別、キー、ID の重複数、usage の構造だけを集計した。Codex App Server は拡張同梱 0.153.4 の `generate-json-schema` で型を生成した。

PATH 上の CLI と最新拡張の同梱 CLI には版差があった。収集記録には `surface`（CLI、VS Code、SDK 等）、`surface_version`、実際に記録を生成した `engine_version` を別々に残す必要がある。

## 3. 情報源の役割と優先順位

| 情報源 | Claude Code | Codex | 採用方針 |
|---|---|---|---|
| lifecycle/tool hook | 指示、prompt ID、ツール前後、承認、subagent、compact、終了 | 指示、turn ID、ツール前後、承認、subagent、compact、終了 | 低遅延の原記録。重複を許して inbox に保存する |
| OpenTelemetry | API request、usage、見積費用、エラー、retry trace、tool decision/result | API attempt、response 完了 usage、tool decision/result、turn token metric | API 呼び出しと usage の主要収集源。content は既定で記録しない |
| 非対話 JSON 出力 | query 全体 usage、モデル別 usage、見積費用、終了状態 | turn 完了 usage | wrapper が起動した処理では確定境界として利用する |
| transcript | メッセージ、tool block、response ID、usage、時刻 | session/turn/response ID、usage スコープ、item、時刻 | 非同期・非安定形式。復旧と内容相関の補助にし、未知行を保持する |
| App Server | 該当なし | thread/turn/item、承認、状態、時刻、usage | maatplug 自身が App Server client のとき有力。他 client の stream を横取りできない |
| 料金表・請求データ | 料金履歴、組織請求 | API 料金履歴、ChatGPT plan/credit、請求 | token からの見積りと実支出を別レコードにする |

既存 CLI／拡張を sidecar として観測する構成では、hook とローカル telemetry collector を主経路とし、transcript の差分取込みで不足を補うのが現実的である。Codex App Server は maatplug が会話 client を実装する場合に限って主要経路にできる。

## 4. 能力マトリクス

### 4.1 セッション、指示、応答、使用量

| 収集項目 | Claude Code | Codex | 意味・制約 |
|---|---|---|---|
| セッション ID | **直接観測** `session_id` | **直接観測** hook `session_id`、JSON の `thread_id`、transcript session meta | `provider + ID namespace + ID` で保存する |
| 指示 ID | **直接観測** hook `prompt_id` | **直接観測** hook `turn_id` | Claude は 2.1.196 以降の prompt ID。Codex JSON の `turn.started` 自体には ID がなかった |
| 指示本文 | **直接観測** `UserPromptSubmit.prompt` | **直接観測** `UserPromptSubmit.prompt` | 保存しない設定でも、長さ、hash、content reference の状態は残せる |
| 応答 ID | **直接観測** transcript `message.id`、fallback `requestId` | **直接観測** transcript `response_id`。**Provider申告** App Server `responseId` | Claude の transcript 行 `uuid` は response ID ではない |
| 指示と応答の相関 | **直接観測** `prompt_id` と OTel `prompt.id`。transcriptにも `promptId` | **直接観測** transcript/App Server の `turn_id`。OTel 単独の turn 相関は **未取得** | 情報源ごとの ID namespace を保持する |
| モデル | **直接観測** response `message.model`、result `modelUsage` | **直接観測** hook、turn context。**Provider申告** model reroute 通知 | 要求モデルと実行モデル、切替前後を分ける |
| API response 単位 usage | **直接観測** transcript assistant usage。OTel は **Provider申告** | **直接観測** transcript `usage`。App Server internal event は **Provider申告** | Claude は同じ response usage が複数行に反復。Codex internal event は安定契約にしない |
| 指示／turn 単位 usage | **直接観測** `claude -p` result は query 単位の累積 | **直接観測** `turn.completed.usage`、transcript `turn_token_usage` | Claude の query と対話 UI の prompt 境界が同一とは限らない |
| session/thread 累積 usage | **推定可能** response 集合から算出。status line の context usage は累積費用ではない | **直接観測** transcript `thread_token_usage`、App Server `tokenUsage.total` | 累積値は generation と前値がそろう場合だけ差分化する |
| cache token | **直接観測** creation/read を独立区分で取得 | **直接観測** cached/cache-write。cached は input の内数 | raw の Provider 区分を保持し、排他的な課金区分へ正規化する |
| reasoning/thinking token | result/model usage で **直接観測**。response ごとの扱いは版依存 | **直接観測** `reasoning_output_tokens`、output の内数 | output と重複加算しない |
| usage の Provider 時刻 | transcript は **直接観測**、OTel は **Provider申告** | transcript は **直接観測**、App Server/OTel は **Provider申告** | hook の collector 受信時刻とは区別する |
| API latency／turn duration | result duration は **直接観測**、OTel は **Provider申告** | transcript duration は **直接観測**、App Server は **Provider申告** | queue、API、turn、tool を別 duration にする |
| 終了状態 | **直接観測** Stop/SessionEnd、result `stop_reason` 等 | **直接観測** Stop/SessionEnd。App Server turn status は **Provider申告** | `completed`、`interrupted`、`failed` と hook 欠測を分ける |
| Provider の token 費用見積り | **直接観測** result `total_cost_usd` と model `costUSD`。OTel は **Provider申告** | **取得手段なし**（今回のローカル結果）。料金表から **推定可能** | Claude の値も請求明細ではない |
| 実請求・subscription 消費・refund | **取得手段なし**（ローカル記録のみでは不可） | **取得手段なし**（ローカル記録のみでは不可） | 管理者向け billing/credit データを別途 import する |

### 4.2 ツール、承認、監査

| 収集項目 | Claude Code | Codex | 意味・制約 |
|---|---|---|---|
| tool request ID | **Provider申告** Pre/PostToolUse `tool_use_id`、transcript では **直接観測** | **Provider申告** Pre/PostToolUse `tool_use_id`、App Server item ID | PermissionRequest には両者とも tool use ID がない |
| tool 名・入力 | transcript で **直接観測**、hook は **Provider申告** | hook/App Server は **Provider申告**、transcript で **直接観測** | shell command、cwd、MCP server/tool、file path 等を raw と正規化値に分ける |
| tool 開始前／結果 | **Provider申告** PreToolUse、PostToolUse、PostToolUseFailure | **Provider申告** PreToolUse、PostToolUse | Codex の shell 非ゼロ終了も PostToolUse。success 判定をイベント名だけに依存させない |
| tool duration | PostToolUse `duration_ms` は **Provider申告** | OTel/App Server は **Provider申告** | 欠測時は前後時刻から **推定可能** |
| stdout/stderr/result | transcript で **直接観測**、hook は **Provider申告** | transcript/App Server は **直接観測／Provider申告** | 原出力、切詰め後、モデルへ再投入された内容を同一視しない |
| 承認要求 | **Provider申告** PermissionRequest | **Provider申告** PermissionRequest/App Server request | 承認 ID がない版では tool input、prompt/turn、順序による推定相関になる |
| 承認判断・理由 | OTel `tool_decision` は **Provider申告** | OTel/App Server resolution は **Provider申告** | accept、reject、source、policy amendment を保持する |
| 操作の最終状態 | success/failure hook は **Provider申告** | App Server item status は **Provider申告** | request、approval、started、completed、failed、declined を別イベントにする |
| shell の終了コード | hook `tool_response` 内は版・tool 依存で **未取得** | App Server command item は **Provider申告**、今回 hook では **未取得** | raw result を保持し、フィールドがある場合だけ正規化する |
| 実ファイル変更 | FileChanged hook は **Provider申告**。VCS 差分から **推定可能** | App Server fileChange は **Provider申告**。VCS 差分から **推定可能** | shell や外部プロセスの全副作用を Provider item だけでは列挙できない |
| 要求された network 宛先 | sandbox permission signal は **Provider申告** | approval `networkApprovalContext.host/protocol` は **Provider申告** | Claude は sandbox 内 network request で PermissionRequest が発火しない場合がある |
| 実際の network 接続先 | **取得手段なし** | **取得手段なし** | OS/process 単位の network telemetry が必要。WebSearch 等の hosted tool は hook 対象外の場合がある |
| 危険性分類 | command/input から **推定可能** | command/action/path から **推定可能** | Provider の承認有無は危険性そのものではない。規則版と根拠を保存する |

### 4.3 subagent、retry、compact、内容

| 収集項目 | Claude Code | Codex | 意味・制約 |
|---|---|---|---|
| subagent ID・種別・親 | **Provider申告** SubagentStart/Stop、OTel agent metadata | **Provider申告** SubagentStart/Stop、App Server collab/subagent item | 親子関係と usage 包含関係は別に保持する |
| subagent usage | OTel `query_source=subagent` 等は **Provider申告** | thread/turn usage は **Provider申告** | 親 total が子を含むかはシナリオ別の直接検証が未実施 |
| API retry 回数 | 通常 OTel error の total attempt は **Provider申告** | OTel `api_request` attempt は **Provider申告** | Claude の各 attempt 詳細は beta trace の `gen_ai.request.attempt` が必要 |
| retry ごとの usage・cost | trace/raw request を有効化した場合 **Provider申告** | response/attempt telemetry の組合せで **推定可能** | 失敗 attempt が無課金とは仮定しない。請求と照合する |
| compact 開始・終了 | **Provider申告** PreCompact/PostCompact | **Provider申告** PreCompact/PostCompact、App Server contextCompaction | trigger、turn/prompt、前後 context usage を保持する |
| compact 後の usage reset | status line では次の API まで null になると **Provider申告** | 累積系列の変化から **推定可能** | baseline 欠測時にゼロ起点の差分を作らない |
| model switch/reroute | Pre/PostModelSwitch は **Provider申告** | App Server model reroute は **Provider申告** | requested/from/to/reason を分離する |
| 完全な upstream input | content logging を明示有効化した trace でも **未取得**範囲が残り得る | **取得手段なし**に近い。user input/item は取得できても隠れた system/tool schema 全体は保証されない | input token の全量を可視 content の合計に強制一致させない |
| tool output の切詰め・変換 | transcript/content block の比較で **推定可能** | transcript/item の比較で **推定可能** | 元出力、保存版、モデル投入版、hash、長さ、変換関係を別々に保存する |
| instruction/rule/config snapshot | InstructionsLoaded/ConfigChange は **Provider申告** | session/turn context と config は **Provider申告** | 実際に upstream input に入った全文と同義ではない |

## 5. 実測した ID、重複、確定境界

### 5.1 Claude Code

最小実行では `SessionStart → UserPromptSubmit → Stop → SessionEnd` を観測した。`UserPromptSubmit`、`Stop`、`SessionEnd` に同じ `prompt_id` が入り、セッション内の指示境界として使用できる。`Stop` には `last_assistant_message`、`effort`、background task 情報があった。hook payload 自体には発生時刻がなかった。

別の実 transcript では、assistant 行 1,259 行に対して一意な `message.id` は 738、重複する ID は 480 あった。同一 response の thinking、text、tool_use などが別行として現れ、各行に同じ usage が反復する。したがって次の規則が必要である。

- response の同一性は `message.id`、欠測時は `requestId` で判断する。
- transcript 行の `uuid` は raw record ID として全件残す。
- usage は response ID ごとに候補をまとめ、同値の反復を一度だけ採用する。
- 同じ ID で値が変わった場合は上書きせず、provisional/supersedes 関係を記録する。
- `claude -p` result の `usage` と `total_cost_usd` は query 全体の集計候補であり、個別 response usage と加算しない。

公式資料によれば transcript は非同期に書かれ、内部形式は版間で変わり得る。Stop の到着、transcript の末尾、usage の到着は別の確定条件として扱う。

### 5.2 Codex

最小実行の `codex exec --json` は `thread.started → turn.started → item.completed → turn.completed` を出力した。`thread.started` には thread ID があったが、`turn.started` と `turn.completed` には turn ID がなかった。hook の `UserPromptSubmit` と `Stop` には同じ `turn_id` があったため、wrapper 出力だけより hook を併用した方が相関が強い。

transcript の `token_usage_record` では次を区別できた。

- `response_id`: 一つの upstream response。
- `turn_id` / `root_turn_id`: 指示と派生処理の境界。
- `usage`: 当該 response の値。
- `turn_token_usage`: turn 累積。
- `thread_token_usage`: thread 累積。
- `state`、開始・完了時刻、duration。

App Server 0.153.4 の生成スキーマにも `thread/tokenUsage/updated` の `last` と `total`、turn ID がある。`rawResponse/completed` は response ID、thread ID、turn ID と exact usage を持つが、説明が internal-only である。Phase 1 の安定契約はこれを必須にせず、取得できたときだけ高粒度の raw evidence とする。

Codex の確定境界は App Server の `turn/completed` または wrapper の `turn.completed` である。途中の token update は暫定値として保存し、最終値が来ない場合も消さない。`cached_input_tokens` は input の内数、`reasoning_output_tokens` は output の内数として保持し、料金計算で重複加算しない。

## 6. CLI と VS Code 拡張

| 確認事項 | Claude Code | Codex |
|---|---|---|
| CLI hook の実発火 | **直接観測** 2.1.263、Windows | **直接観測** 0.153.0、Windows |
| 拡張と CLI の設定共有 | **Provider申告** `~/.claude/settings.json` の hooks/permissions/MCP を共有 | hooks は runtime 機能として **Provider申告**。拡張は App Server client と公式例に記載 |
| 拡張の transcript 利用 | private CLI 同梱を **直接観測**、同一 payload/形式は **未取得** | private CLI/App Server 同梱を **直接観測**、同一 payload/形式は **未取得** |
| Windows path／payload | CLI で backslash の絶対 path を **直接観測** | CLI で backslash の絶対 path を **直接観測** |
| 同版性 | CLI 2.1.263、拡張 2.1.266 で不一致 | CLI 0.153.0、拡張同梱 0.153.4 で不一致 |

拡張 GUI からテスト用 session を起動して hook と transcript を採取する工程は未実施である。このため「設定を共有する」「拡張は App Server を使う」は Provider 申告、「CLI と完全に同じ payload／transcript」は未取得とする。Phase 3 の完成判定には、両拡張で一度ずつ user prompt、shell、file edit、deny、subagent、compact を発生させる fixture 追加が必要である。

## 7. 8.3節の受入ケースへの対応

| 受入ケース | 観測源と判定 | Phase 1 以降で必要な表現 |
|---|---|---|
| 複数モデル・複数 attempt | Claude OTel/model switch、Codex OTel/model reroute。**Provider申告** | call/attempt ID、要求／実行モデル、状態、usage、cost を別イベントにする |
| 重複再送・provisional 更新 | Claude の同一 `message.id` 反復を **直接観測**。Codex の response/turn/thread 系列を **直接観測** | raw record ID、provider ID、scope、revision、supersedes、採用状態 |
| 累積 reset・baseline 欠測 | Codex の累積系列は **直接観測**、reset 自体は **未取得**。Claude compact 後は **Provider申告** | counter generation、前値、reset reason、known/unknown delta |
| 親 total と子明細 | 両 Provider の subagent ID/usage は **Provider申告**、包含関係は **未取得** | `contains` と `parent_of` を分け、包含未確認なら足さない |
| 途中の追加指示・同一 session 並行 | prompt/turn ID は **直接観測**。Codex App Server steer は **Provider申告** | instruction group、actor、concurrency、shared/unresolved attribution |
| 重複・切詰められた tool output | transcript 比較で **推定可能** | content object、元／保存／投入の variant、hash、length、transformation |
| deny・start・success・partial failure・async | tool/permission/subagent lifecycle は **Provider申告** | operation と attempt の状態遷移。結果欠測と失敗を区別する |
| requested と actual network destination | requested は一部 **Provider申告**、actual は **取得手段なし** | requested/approved/observed destination と source を分離。OS telemetry を任意追加 |
| end 欠測・usage 遅延 | transcript の非同期性は **Provider申告** | provisional、observed_at、received_at、watermark、後続 correction |
| ingest stop・log rotation | Provider 能力ではなく collector の責務 | source identity、offset、generation、raw hash、原子的 checkpoint |
| pricing change・取得失敗・unknown | 料金表と usage から **推定可能** | effective range、source、currency、price version、unknown reason |
| billing の範囲外・subscription・refund | ローカル Provider 記録では **取得手段なし** | estimated cost と billed spend/credit/refund、対象 scope、照合差額 |
| concurrent edit・cloc net・time | VCS/FS と lifecycle から **推定可能** | snapshot、actor、baseline、wall/API/tool time、overlap を分ける |
| raw content 省略・mask・retention delete | content 設定は **Provider申告** | content reference の omitted/redacted/deleted、policy、hash/length |
| format cutover | engine/version は **直接観測** | schema version、source version、stream generation、cutover marker、旧 parser 診断 |

全ケースに収集源を割り当てられるが、`取得手段なし`を含むケースは「値がない」として完了させてはならない。可用性と観測範囲そのものをイベントに保存する。

## 8. Phase 1・Phase 3への決定事項

1. 共通 envelope には `provider`、`surface`、`surface_version`、`engine_version`、`source_kind`、`source_record_id`、`observed_at`、`received_at`、`availability` を持たせる。
2. ID は `session`、`instruction/prompt/turn`、`response`、`operation/tool_use`、`approval`、`agent`、`attempt` を別 namespace にする。値がない関係を時刻だけで確定しない。
3. usage は raw 区分、scope（response/turn/query/thread）、counter kind（delta/cumulative）、generation、state（provisional/final/corrected）、包含関係を保持する。
4. hook は payload を短時間で flat-file inbox に書くだけにする。transcript の走査、diff、危険性判定、料金計算は後続処理へ移す。
5. telemetry と transcript は cursor と stream generation を持つ差分取込みにする。末尾不完全行と未知形式を failed evidence として隔離する。
6. tool/approval は要求、policy 判断、ユーザー判断、開始、途中出力、完了、失敗、拒否を別イベントにする。危険性は version 付き派生判定とする。
7. content は本文と metadata を分離し、本文を収集しない場合も hash、長さ、mask/omit/delete reason、retention policy を残す。
8. 費用見積りは token usage と履歴料金表から再生成可能にする。Provider のローカル見積り、API 請求、subscription/credit/refund は別系列として照合する。
9. Claude Code は `prompt_id` を第一相関キーに改める。Codex は `turn_id` と `root_turn_id` の適用範囲を raw のまま保持する。
10. App Server internal-only field と transcript 固有 field は optional evidence とし、共通契約の必須 field にしない。

この方針は DB を前提としない。Phase 0 fixture はすべて JSON/JSONL と README であり、Phase 1 以降も append-only JSONL、content object、再生成可能な catalog で開始できる。

## 9. 残る検証項目

Phase 0 の主要な構造調査は完了した。次の項目は版・surface・異常系を直接観測する追加 fixture として残す。

- Claude Code／Codex の VS Code GUI から同一シナリオを実行し、hook と transcript の一致範囲を確定する。
- 両 Provider で deny、tool failure、subagent、compact、model switch、interrupt、同一 session への steer を発生させる。
- 親 usage が子 agent usage を含むか、Provider・版・実行モード別に数値で確認する。
- telemetry exporter を有効にし、hook/transcript と同一実行を ID と時刻で照合する。
- Codex App Server の public event だけで必要粒度を満たせるか確認し、internal-only event 消失時の fallback を試す。
- CLI と拡張の自動更新後に fixture を再取得し、format cutover を検知できることを確認する。

これらは「存在すると仮定した共通 field」にはせず、現時点では `未取得`または`推定可能`として保持する。

## 10. 匿名化fixture

- Claude Code: [`internal/provider/claude/testdata/phase0/README.md`](../internal/provider/claude/testdata/phase0/README.md)
- Codex: [`internal/provider/codex/testdata/phase0/README.md`](../internal/provider/codex/testdata/phase0/README.md)

fixture は実データの内容を再配布するものではない。field presence、型、順序、ID の相関、usage scope、重複関係を再現する。実測値を保持したファイルと合成値を用いたファイルは各 README に明記した。

## 11. 参照した一次資料

Claude Code:

- [Hooks reference](https://code.claude.com/docs/en/hooks)
- [Sessions](https://code.claude.com/docs/en/sessions)
- [Monitoring usage with OpenTelemetry](https://code.claude.com/docs/en/monitoring-usage)
- [Manage costs effectively](https://code.claude.com/docs/en/costs)
- [Agent SDK cost tracking](https://code.claude.com/docs/en/agent-sdk/cost-tracking)
- [VS Code integration](https://code.claude.com/docs/en/ide-integrations)
- [Status line](https://code.claude.com/docs/en/statusline)

Codex:

- [Hooks](https://learn.chatgpt.com/docs/hooks)
- [App Server](https://learn.chatgpt.com/docs/app-server)
- [Advanced configuration and OpenTelemetry](https://learn.chatgpt.com/docs/config-file/config-advanced)
- [Pricing](https://learn.chatgpt.com/docs/pricing)
- [OpenAI Codex source repository](https://github.com/openai/codex)

公式資料に加え、各 CLI の `--help`、実 hook、実 transcript、非対話 JSON 出力、ローカルに生成した App Server JSON Schema を照合した。仕様書と実測が異なる場合は、対象版を伴う実測を `直接観測`として記載し、将来版の保証には使っていない。
