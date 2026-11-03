# Phase 2 フラットファイル収集基盤

Phase 2では、hookの受付処理と解析・永続化を分離する。JSONLジャーナルと内容オブジェクトが正本であり、catalogとsummaryは削除して再生成できる投影である。DBは使用しない。

## 書き込み順序

1. `maatplug hook`はstdinを1件のinbox recordへ格納し、同じディレクトリ内の一時ファイルをfsyncしてから`pending`へrenameする。
2. collectorは`pending`から`processing`へのrenameに成功した1プロセスだけがrecordを取得する。
3. 参照される内容オブジェクトをcontent ID、variant、media type、保存方針、内容で検証して原子的に保存する。
4. 共通イベントを検証し、Run単位のJSONLへ追記してfsyncする。チェックポイントはジャーナル確定後に更新する。
5. inbox recordを削除して処理済みにする。
6. 変更されたプロジェクトのcatalogとRun summaryを再生成する。

ジャーナル追記後、inbox削除前に停止した場合は、同じreceipt由来のイベントIDを再処理する。ジャーナルを再走査して同一内容を確認し、2行目を追加せず処理を完了する。同じIDで内容が異なる場合は競合として隔離する。

## 保存レイアウト

```text
<MAATPLUG_HOME>/
  inbox/
    pending/<receipt>.json
    processing/<receipt>.json
    failed/<receipt>.record.json
    failed/<receipt>.error.json
  objects/sha256/<先頭2桁>/<content-id>.json
  pricing/
    sources/<source-id>/<sha256>.json
    versions/<version>.json
    active.json
    fetch-state/<source-id>.json
  billing/
    records/<record-id>.json
    derivations/conversions/<version>.json
    derivations/allocations/<version>.json
  projects/<project-id>/
    streams/<provider>/<session-id>/<run-id>.jsonl
    catalog.json
    summaries/runs/<route-hash>.json
    derivations/attribution-policies/<version>.json
    derivations/attribution-active.json
    derivations/cost/<calculation-id>.json
  state/
    format-cutover.json
    collector-worker.json
    query-baselines/<timestamp>.json
    checkpoints/<stream-hash>.json
    ingest-errors/<receipt>.json
    journal-recovery/<stream-hash>.partial
    provider-cursors/<provider>/<transcript-hash>.json
```

session IDとRun IDはbase64urlでパス要素に変換する。project IDとproviderは安全な文字だけを許可し、相対パスや絶対パスを受け付けない。イベント、inbox、内容オブジェクト、catalogは所有者だけが読めるモードで作成する。

## 障害時の扱い

- 原子的保存の途中で残った一時ファイルは通常のrecordとして読まない。一定時間後にfailedへ保存し、`ingest-errors`へ診断を残す。
- `processing`に残ったrecordは一定時間後に`pending`へ戻す。
- 処理失敗はrecord内に試行回数と次回実行時刻を保存して再試行し、上限到達後にrecordとエラーをfailedへ隔離する。
- JSONL末尾の改行されていない断片は`journal-recovery`に保存してから切り離し、最後の完全なイベントから追記を再開する。
- 同じRunの追記はRunファイルごとのロックで直列化する。異なるRunは別のロックを使用する。
- catalogには入力ジャーナル、最終byte offset、イベント数、最終イベントID、集計版を保存する。`doctor collection`はこれを実ファイルと照合する。

## コマンド

```text
maatplug collect --once [--heartbeat 30s]
maatplug collect --watch [--interval 1s] [--heartbeat 30s]
maatplug rebuild-catalog [--project <id>]
maatplug doctor collection [--json] [--record-baseline]
```

`collect --once`は実行時点で処理可能なrecordを取り込む。再試行待ちのrecordはinboxに残る。`collect --once`と`collect --watch`は起動時と既定30秒間隔でワーカー状態とinbox件数を表示する。`collect --watch`は停止シグナルまで処理を繰り返し、ロック競合や一時的なI/O障害で周期が失敗してもエラーを表示して`--interval`後に再試行する。`--heartbeat 0`で定期表示を無効にできる。大きなtranscriptは500行または4 MiBごとにジャーナルとカーソルを確定し、確定位置を進捗として表示する。処理が中断した場合は、最後に確定したチャンクの次から再開する。`rebuild-catalog`は正本から投影を作り直す。`doctor collection`は未処理数、処理中、隔離、受付エラー、破損ジャーナル、内容参照、catalogの欠落・遅れを表示する。

Phase 3では、collectorがprovider hookをデコードしてraw payloadと意味イベントを保存し、Stopを境界にProvider固有transcriptを差分解析する。transcriptのbyte offset、行番号、世代、正規化済みProvider IDは`state/provider-cursors`へ保存する。詳細は[provider-adapter-phase3.md](provider-adapter-phase3.md)を参照する。

Phase 4では、使用量照合、指示への帰属、入力構成をcatalog内の再生成可能な派生結果として作る。ユーザーの帰属訂正だけを版付きJSONとして保存し、元イベントとProvider使用量は変更しない。詳細は[usage-reconciliation-phase4.md](usage-reconciliation-phase4.md)を参照する。

Phase 5では、料金資料の取得状態、変更不可の料金版、使用量別費用、請求・支払・返金、為替換算、配賦を別のフラットファイルとして保存する。詳細は[pricing-and-billing-phase5.md](pricing-and-billing-phase5.md)を参照する。

Phase 6では、操作要求、承認、結果、環境、指定対象、観測作用、成果物、時間区間をcatalog内の再生成可能な操作プロジェクションとして結合する。危険操作の判定と検証結果も根拠・規則版・欠測を伴って保持する。詳細は[operation-audit-phase6.md](operation-audit-phase6.md)を参照する。

Phase 7では、CLIとWebの読み取りを`internal/query`へ集約し、切替時刻、ワーカー状態、料金鮮度、観測範囲、性能基準値をフラットファイルで管理する。詳細は[query-and-operations-phase7.md](query-and-operations-phase7.md)を参照する。
