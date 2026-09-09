---
name: provider-check
description: Claude CodeやCodex CLIのフック仕様・トランスクリプト形式・設定ファイルなど、プロバイダー固有の挙動に依存する実装(hook処理、transcript解析、settings.json操作、instructionId相関など)に着手する前に必ず使う。実装方針の検討や、実装の難易度・不確実性が高くユーザーへの確認が必要な場面の判断に使う。
metadata:
  target-agents: claude-code, codex-cli
---

# プロバイダー実装前チェック

maatplugはClaude Code / Codex CLI(将来的にGitHub Copilot CLIも)という複数のAIコーディングエージェントを横断してhookやtranscriptから使用量を記録するツール([docs/design.md](../../../docs/design.md))。各Providerの具体的な挙動(起動フラグ、hook payloadの形、transcriptのJSON形式、設定ファイルの場所)は、design.mdの記述だけを鵜呑みにせず実装時に個別検証する方針になっている(design.md 1節: 「各Provider CLIの具体的な起動フラグやJSON出力仕様は実装時に各CLI自身のドキュメント／`--help`で個別に検証する」)。このスキルはその検証〜方針決定〜(必要なら)ユーザー確認、という手順を徹底するためのもの。

## 手順

### 1. 調査する

- 対象Provider(Claude Code / Codex CLI、両方に関わる変更なら両方)の既存実装を読む:
  - `internal/provider/claude/`(hookペイロード解析、transcript集計、settings.jsonマージ)
  - `internal/provider/codex/`(hookペイロード解析、turn_id相関)
  - 共通の記録経路: `internal/recorder/`、`internal/store/`
- ドキュメントの記述だけで挙動を断定せず、必要に応じて対象CLI自身の公式ドキュメント・`--help`・実際のhook payload/transcriptサンプルを確認する(WebSearch/WebFetch、または実際にCLIを動かしての確認)。
- `docs/design.md` 12節(リスク・要検討事項)に該当する既知の未検証事項がないか確認する。特に以下は明示的に「要検証」とされている:
  - Claude CodeのVSCode拡張がターミナル版と同一のhook設定を共有するか(6.4節・12節1番)
  - Codexのtranscript形式は非公式で変わりうる(6.2節・12節2番)
  - Windows環境でのhookスクリプト実行の差異(12節7番)

### 2. 実装方針を検討する

- Claude CodeとCodex CLIで挙動の仕組みが異なっていても、`internal/model`が定義するイベントスキーマ(`session_start`/`instruction`/`turn_end`/`session_end`)は共通に保ち、Provider固有の差異は各`internal/provider/<provider>/`パッケージ側に閉じ込める設計になっているかを確認する。
- 例えばinstructionIdの相関方式はClaude(未解決instructionの推測+曖昧検出でabandoned化、6.1節)とCodex(hookが渡す`turn_id`をそのまま使用、6.2節)で全く異なる実装だが、結果として書き込まれるJSONLイベントの形は共通、という既存パターンに倣う。
- 一方のProviderにしか実装しない/できない機能の場合は、その非対称性(例: Copilotのexec-wrap方式はmaatplug経由の実行しか捕捉できない、design.md 12節3番)を明示できる設計にする。

### 3. 難しい・不確実な場合はユーザーに確認する

次のいずれかに該当する場合は、推測で実装を進めずユーザーに確認する:

- 対象CLIの挙動(hookのpayload形式、transcript形式、設定ファイルの場所など)が、実装時点で調べた範囲(ドキュメント/`--help`/実データ)だけでは確証を持てない
- Claude CodeとCodex CLIで挙動が大きく異なり、どちらの仕様に合わせるか・両対応するか・非対応にするかの判断がユーザーの意図に依存する
- `docs/design.md` 12節に記載の未検証リスクに直接抵触する、またはそれに準じる新しい不確実性が見つかった

確認する際は、調査で分かったこと・分からなかったこと・考えられる実装方針の選択肢とそれぞれのトレードオフを添えて質問する(「実装できませんでした」で終わらせない)。
