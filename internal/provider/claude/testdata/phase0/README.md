# Claude Code Phase 0 fixtures

Phase 0（2026-09-10）で Claude Code `2.1.263` を Windows 上から実行して得た記録を、秘密情報を除去して保存した fixture である。

| ファイル | 由来 | 観測範囲 |
|---|---|---|
| `hook-lifecycle.jsonl` | 実際の `claude -p` 実行 | `SessionStart`、`UserPromptSubmit`、`Stop`、`SessionEnd` のフィールドと順序 |
| `print-result.json` | 同じ `claude -p --output-format json` 実行 | query 全体の usage、モデル別 usage、ローカル費用見積り、終了状態 |
| `transcript-duplicate-response.jsonl` | 実 transcript の構造を縮小再現 | 同じ `message.id` の複数行、usage の重複、`Read` tool use/result の相関 |
| `bash-tool-use.jsonl` | 実 transcript（`Bash` tool_use を含むセッション）の構造を再現 | `Bash`の`tool_use.input`（`command`/`description`）、対応する`tool_result`、Claude Code独自の便宜フィールド`toolUseResult`（`stdout`/`stderr`/`interrupted`/`isImage`/`noOutputExpected`）。**終了コードに相当するフィールドは存在しない**（`docs/provider-capability-matrix.md`で確認） |

匿名化ではセッション・プロンプト・応答・要求・ツールの ID、絶対パス、本文、コマンドを合成値へ置換した。`print-result.json` の token 数と費用は最小プローブで観測した値を保持し、duration などの非本質的な scalar は代表値へ置換した。`transcript-duplicate-response.jsonl`・`bash-tool-use.jsonl` の件数は小さな合成値であり、フィールドの存在、型、重複関係だけが実測に基づく。

hook は受信 payload 自体に時刻を持たない。fixture の行順は観測順であり、Provider 時刻として利用してはならない。実 transcript は非同期に追記され、形式は安定 API ではない。fixture はパーサー互換性の根拠ではなく、未知フィールドを保持する回帰試験用とする。
