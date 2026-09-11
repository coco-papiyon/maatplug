# Provider-independent Phase 1 fixtures

acceptance.jsonl は docs/proposal-command-audit-and-pricing.md 8.3節の15ケースを、Provider固有フィールドを使わずに表した schema version 1 のイベント列である。各行は docs/event.schema.json の EventEnvelope で、scope の run ID がケースを識別する。

expectations.json は各runについて、後続の保存・集計・APIテストが共有する期待結果を記録する。Phase 1では集計器そのものは実装せず、期待値の語彙を次のように固定する。

- knownUsageTokens: 採用対象として合計できる既知token数。
- unknownUsage: usageの一部または全部を数値化できないこと。
- selectedEventIds / excludedEventIds: 訂正・重複・包含を考慮した採用候補。
- operationState: 操作の最終的な既知状態。終了欠測は pending。
- availability: 欠測、競合、収集不能を維持すべき論理fieldと状態。
- notes: 将来の集計器が満たすべき、数値以外の条件。

fixture内のID、時刻、token数、金額、hashはすべて合成値である。本文は保存せず、ContentRefだけを使用している。

