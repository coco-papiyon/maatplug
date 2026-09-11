# Codex Phase 0 fixtures

Phase 0（2026-09-10）で Codex CLI `0.153.0` を Windows 上から実行し、VS Code 拡張同梱 CLI `0.153.4` の生成スキーマも確認して作成した匿名化 fixture である。

| ファイル | 由来 | 観測範囲 |
|---|---|---|
| `hook-lifecycle.jsonl` | 実際の `codex exec` 実行 | `SessionStart`、`UserPromptSubmit`、`Stop`、`SessionEnd` のフィールドと順序 |
| `exec-events.jsonl` | 同じ `codex exec --json` 実行 | 公開 JSONL の thread／turn／item と turn 完了 usage |
| `transcript-usage.jsonl` | 実 transcript の構造を縮小再現 | response、turn、thread の usage スコープと相関 ID |
| `app-server-schema-events.jsonl` | `codex app-server generate-json-schema` の 0.153.4 スキーマから作成 | App Server のターン状態、累積 usage、1 upstream response の usage |
| `file-change-0.153.jsonl` | 実 rollout file（CLI `0.153.0`、originator `codex_vscode`）の構造を再現 | `event_msg`/`patch_apply_end` — ファイル変更のネイティブ記録（`changes`: パス→`{type, unified_diff, move_path}`）。gitに依存しない |
| `command-execution-legacy-0.151.jsonl` | 別の実 rollout file（CLI `0.151.0-alpha.7.2`、originator `codex_vscode`）の構造を再現 | `event_msg`/`item_completed`（`item.type=="CommandExecution"`）— **0.153.0系には存在しない、旧バージョン限定の構造**。`exit_code`・`duration`・`parsed_cmd`（コマンド種別分類）を含む |

匿名化では thread・turn・response・item・session の ID、絶対パス、本文を合成値へ置換した。`exec-events.jsonl` の token 数は最小プローブの値を保持した。`transcript-usage.jsonl` の token 数と時刻は合成値で、フィールドの存在、型、包含関係だけが実測に基づく。`file-change-0.153.jsonl`・`command-execution-legacy-0.151.jsonl` はいずれも実際にこの開発機に残っていた rollout file から構造のみを抽出した（本文・パス・コマンド文字列は合成値に置換）。

`app-server-schema-events.jsonl` は実行イベントの採取結果ではない。0.153.4 が生成した型から組み立てた例である。特に `rawResponse/completed` はスキーマ上で internal-only と明記されているため、安定した契約として扱わない。hook は受信 payload 自体に時刻を持たない。transcript も安定 API ではない。

## 版依存の注意（Phase 0 再調査で判明）

同じ開発機に残っていた実rollout fileを比較した結果、**コマンド実行・ファイル変更を表すレコードの構造がCLIバージョン間で実際に変わっていた**ことを確認した（`docs/provider-capability-matrix.md`の該当節を参照）。

- **CLI `0.151.0-alpha.7.2`**（`command-execution-legacy-0.151.jsonl`）: `event_msg`/`item_completed`の`item.type`が`"CommandExecution"`／`"FileChange"`で、`exit_code`・`duration`・`parsed_cmd`（コマンド種別分類）を直接持つ、高レベルの構造化レコード。
- **CLI `0.153.0`**（`exec-events.jsonl`・`file-change-0.153.jsonl`）: 同じ`item_completed`という仕組み自体が確認できず、ファイル変更は`event_msg`/`patch_apply_end`（`changes`のキーは`content`ではなく`unified_diff`）に、コマンド実行は低レベルの`response_item`/`custom_tool_call`（`name:"exec"`）のみになっている。**`exit_code`・`duration`（構造化フィールドとして）・`parsed_cmd`に相当する情報は0.153.0のサンプルでは確認できなかった**（`custom_tool_call_output`の出力先頭に`"Wall time X.X seconds"`という非構造化テキストとしてのみ所要時間が残る）。

この事実は「Codexのtranscript形式は安定APIではない」という既存コードのコメント（`internal/provider/codex/transcript.go:46-48`）が、単なる予防的な注意書きではなく、実際にこの開発機の2つのバージョン間で起きていたことを裏付ける。`exit_code`・コマンド種別分類に依存する設計（監査での「破壊的操作」判定など）は、利用可能なら使うが前提にはできない。

