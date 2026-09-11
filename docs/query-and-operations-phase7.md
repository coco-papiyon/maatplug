# Phase 7 共通問い合わせと運用

Phase 7では、CLIとWeb APIの新形式データ読み取りを`internal/query`へ集約した。正本はRun単位のJSONLジャーナルで、catalog、Run summary、期間summaryは削除可能な投影である。DBは導入していない。

## 読み取り規則

- プロジェクト一覧は`projects/<project-id>/catalog.json`を使う。schema、project ID、入力ファイル数、byte offsetを検証し、欠落・破損・古いcatalogはジャーナルから再構築する。
- セッション、Run、指示、モデル呼び出しの詳細は対象ジャーナルから使用量、帰属、費用、操作を再計算する。
- 日別トレンドと内訳は新形式ジャーナルの非重複Usage集合から生成する。金額はdecimal文字列と有理数演算を使い、通貨別既知小計、`costMissing`、`usageMissing`を返す。
- 旧形式の`projects/<id>/sessions/*.jsonl`は読み取らない。CLIとWebはイベントファイルを直接解釈せず、`internal/query`だけを利用する。

共通の結果は観測期間、既知小計、直接帰属、共有未配賦、未分解、未帰属、推定配賦、欠測を別々に返す。推定費用は通貨別の値と料金版・Rate IDを返し、料金や使用量が不足する場合は`costMissing`を返す。実支出は、projectまたは対象entityに明示的に関連付けられた請求recordだけを返す。

## CLI

```text
maatplug status [--project <id>]
maatplug report [--project <id>] [--format table|json]
maatplug operations --project <id> [--kind <kind>] [--status <status>]

maatplug query projects
maatplug query project --project <id> [--provider <provider>] [--since <RFC3339>] [--until <RFC3339>]
maatplug query work --project <id> [--provider <provider>] [--since <RFC3339>] [--until <RFC3339>]
maatplug query periods --project <id>
maatplug query sessions --project <id> [--provider <provider>]
maatplug query session --project <id> --provider <provider> --session <id>
maatplug query run --project <id> --provider <provider> --session <id> --run <id>
maatplug query instruction --project <id> --namespace <namespace> --id <id>
maatplug query call --project <id> --namespace <namespace> --id <id>

maatplug doctor collection [--json] [--record-baseline]
```

`status`と`report`は既知小計、共有未配賦、未帰属、未計算、操作数を表示する。`query`の詳細結果には使用量、入力構成、推定費用、関連付け済み実支出、経過時間を含む操作、成果物、欠測、料金根拠が含まれる。

## Web APIと画面

従来の`/api/projects`系も`internal/query`を使用する。新形式の完全な結果は次のAPIから取得できる。

```text
GET /api/v1/projects
GET /api/v1/projects/{projectId}
GET /api/v1/projects/{projectId}/work
GET /api/v1/projects/{projectId}/periods
GET /api/v1/projects/{projectId}/sessions
GET /api/v1/projects/{projectId}/sessions/{provider}/{sessionId}
GET /api/v1/projects/{projectId}/sessions/{provider}/{sessionId}/runs/{runId}
GET /api/v1/projects/{projectId}/entities/{instruction|model_call}?namespace=<namespace>&entityId=<id>
GET /api/collection-status
```

Webの「収集状態」画面は、データ領域ごとにワーカーの最終成功、inbox滞留、隔離・解析エラー、料金版と鮮度切れ、観測欠測、ファイル数・容量、一覧・詳細・期間問い合わせ時間を表示する。リクエスト時はinbox・ワーカー・料金状態だけを軽量に更新し、ジャーナル観測範囲と性能値は`maatplug doctor`が`state/doctor-report.json`へ保存したsnapshotを読む。Webが停止していてもhookはinboxへ原子的に保存でき、collectorは後から処理できる。

## 切替と性能基準値

collectorが新しい実装で初めて動いた時刻に、`state/format-cutover.json`を一度だけ作る。ファイルには導入時刻、イベントschema版、catalog schema版、保存先を記録する。doctorは導入後の受信イベント、usageイベント、操作イベントをProvider別に数える。Providerが外部の完全な件数counterを公開しない場合があるため、現時点の照合基準はジャーナルへ採用されたProvider観測であり、その制約をdoctorの`cutover.limitations`に明記する。

各collector cycleは`state/collector-worker.json`へ最終試行・成功・エラー・処理時間・処理件数を保存する。`doctor collection`は全ジャーナルの観測範囲と次の性能値を測定し、収集状態画面用の`state/doctor-report.json`を更新する。`--record-baseline`指定時は性能値を`state/query-baselines/<timestamp>.json`にも保存する。

- 全ファイル数と総byte数
- プロジェクト一覧時間
- 最初のプロジェクトの詳細時間
- 期間summary取得時間
- collectorの直近追いつき時間

この基準値で日常的な応答時間、catalog再構築時間、ファイル増加を観測する。ジャーナル分割やsummaryで許容値を満たせないことが実測されるまではDBを追加しない。

## 現時点の観測上の制約

- 実支出は請求recordにproject、usage、call等の関連がある範囲だけを対象へ結び付ける。関連のない契約料金は0円にせず対象外として残る。
- Provider外部counterを取得できない環境では、Provider側総件数との完全な突合はできない。受信・usage・操作の内部整合と重複受付を継続監視する。
- 日別グラフは新形式ジャーナルを読み、未計測トークンと未計算費用を既知小計と分けて表示する。
