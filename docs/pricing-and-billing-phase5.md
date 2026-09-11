# Phase 5 料金履歴・費用・請求

Phase 5は、使用時点の料金による推定を再現し、現在料金での比較、Provider見積り、実請求、支払、返金を区別する。料金版、取得状態、請求記録、派生計算はフラットファイルへ保存し、DBは使用しない。

## Providerごとの観測範囲

Claudeの一部の実行結果にはProvider計算の`total_cost_usd`やモデル別`costUSD`が存在する。Codexのローカル記録には対応する見積りがない。どちらも実請求、subscription支出、支払、返金の取得経路は確認できていない。このため、Provider見積りは`provider_estimate`、公開料金表による計算は`calculated_estimate`、外部から取り込む請求等はそれぞれ別のkindとして扱う。存在しないCodex見積りを0円として生成しない。

## 料金版と解決

`internal/pricing`は次の責務を分ける。

- `FetchClaude`、`FetchCodex`: 公開ページの取得とProvider固有解析
- `RatesFromModels`: 既存fetcherの結果を共通rateへ変換
- `VersionStore`: 料金版の検証、変更不可の保存、有効版の選択
- `Version.Resolve`: 使用量に適用するrateの条件評価
- `RefreshWorker`: 定期取得と情報源別状態の永続化

料金版は`pricing/versions/<version>.json`へ保存する。同じversionを別内容で上書きできない。各rateはID、Provider、service、contract、model pattern、usage class、通貨、decimal単価、単位量、有効期間、数量閾値、追加属性、優先度、元資料IDを持つ。

```json
{
  "schemaVersion": 1,
  "version": "manual-2026-09-11",
  "createdAt": "2026-09-11T00:00:00Z",
  "reason": "契約料金を手動登録",
  "sources": [
    {"id": "contract-document", "kind": "manual", "retrievedAt": "2026-09-11T00:00:00Z"}
  ],
  "rates": [
    {
      "id": "codex-input-standard",
      "provider": "codex",
      "service": "api",
      "contract": "team-a",
      "modelPattern": "gpt-*",
      "usageClass": "input_uncached",
      "currency": "USD",
      "amount": "2.50",
      "unitScale": 1000000,
      "effectiveFrom": "2026-09-01T00:00:00Z",
      "sourceId": "contract-document"
    }
  ]
}
```

解決結果は`resolved`、`unregistered`、`condition_unknown`、`conflict`のいずれかになる。service、contract、属性が必要なのに観測されていない場合は`condition_unknown`とし、候補rateと理由を残す。同じ優先度・具体度のrateが複数適用される場合は`conflict`とし、都合のよいrateを選ばない。

公開料金の定期更新では、Provider fetcherが返した変換前のモデル料金一覧を`pricing/sources/<source-id>/<sha256>.json`へ先に保存し、料金版のsourceにそのdigest、URL、取得日時を記録する。その取得日時を新rateの開始時刻とする。実際の改定時刻を元資料から確定できないためである。同じ情報源の旧rateは削除せず、新rate開始時刻を終了時刻として版内に残す。古い料金版も保持するため、当時版の再現と現在版の比較を分けられる。

## 費用計算

`internal/cost`はPhase 4で採用された各Usageを、対応するmodel callの実モデル、Provider、使用日時、排他的usage bucketごとに解決する。1つの指示が異なるモデルの複数呼び出しを含んでも、最後のモデルのrateでまとめて計算しない。

各計算recordにはUsageID、Call、Provider、モデル、使用日時、料金評価日時、usage class、数量、RateID、通貨、decimal金額、解決状態、丸め方式、計算版を保存する。計算は`big.Rat`で行い、既定では小数12桁のhalf-upを使用する。全bucketを解決できたUsageだけを通貨別の既知合計へ加える。料金未登録、条件不明、競合、モデル欠測、数量欠測は金額を持たず、0円として合計しない。

catalogには全料金版の費用結果と`basedOn`版からの差、有効料金版の結果を含める。指定版の再計算結果は、料金版とservice・contract・評価日時等から作る`calculationId`を使い、`projects/<project>/derivations/cost/<calculation-id>.json`へ保存する。通常はUsageの発生日時でrateを選ぶ。`--at`を指定した比較計算だけは、Usage日時を保持したまま指定日時の料金条件を評価するため、当時計算と現在条件の比較結果は別ファイルになる。

```text
maatplug pricing import --file <version.json> [--activate]
maatplug pricing activate --version <version>
maatplug pricing list
maatplug pricing calculate --project <id> --version <version> [--service api] [--contract <name>]
maatplug pricing calculate --project <id> --version <current> --at <RFC3339>
maatplug pricing conflicts --project <id> [--version <version>] [--service api]
```

`pricing list`は料金版とともに情報源別の`lastAttempt`、`lastSuccess`、`freshUntil`、`complete`、`lastError`、反映済み版を表示する。collectorの永続ジョブは24時間ごとにClaudeとCodexを別々に試行する。一方が失敗しても既存rateを消さず、失敗状態を保存する。更新を試みた周期では、全プロジェクトのcatalogを料金更新後に再構築する。

## 請求、支払、返金

`internal/billing`は共通JSONとCSVを取り込む。record IDは変更不可であり、再送は同一内容の場合だけ許可する。kindは次のいずれかである。

| kind | 意味 |
|---|---|
| `provider_estimate` | Provider自身の見積り |
| `calculated_estimate` | 料金表から計算した見積り |
| `billed` | 確定請求 |
| `subscription` | 定額契約の料金 |
| `payment` | 支払 |
| `credit` | クレジット |
| `refund` | 返金 |
| `adjustment` | その他の調整 |

JSONは`{"schemaVersion":1,"records":[...]}`形式を使う。CSVの必須列は`id,kind,amount,currency,occurred_at,source_id`、任意列は`external_id,note,usage,coverage`である。`usage`と`coverage`は`;`区切りの`kind|namespace|value`で記述する。coverageは収集対象外の利用を含む請求範囲も表せる。

```text
maatplug billing import --file <records.json|records.csv> [--format json|csv]
maatplug billing list
maatplug billing convert --file <conversion.json>
maatplug billing allocate --file <allocation.json>
```

`billing list`のsummaryはkindと通貨ごとにだけ集計し、見積り、請求、支払、返金を1つの総額へ足さない。これにより請求額とその支払、または請求と見積りを重複加算しない。

為替換算recordは版、元金額・通貨、換算後金額・通貨、decimalレート、レート日時、方式、情報源、丸め桁を保持する。配賦recordは版、配賦元ID、元金額、整数比、方式、配賦額、未配賦残額を保持する。丸め後の各配賦額を元額から引いて残額を求めるため、配賦済み金額と未配賦分は元金額に一致する。結果は`billing/derivations`以下へ変更不可の版として保存する。
