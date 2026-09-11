# レビュー結果: command-audit-and-pricing 実装

`docs/proposal-command-audit-and-pricing.md` の実装差分に対するコードレビュー結果。
基盤層(journal/ingest/store)、プロバイダーアダプタ/remotesync、pricing/cost/billing、
attribution/query/reconcile、web/CLI連携、proposal準拠監査の6領域を並行してレビューし、
重複を除いた16件の検証済み指摘を重大度順にまとめた。

## サマリ

proposalの主要な設計方針のうち、以下が実装で守られていない箇所がある。

- **新形式限定読み取り(8.1)**: ダッシュボードのトレンド/内訳グラフが新形式journalを一切読まず旧JSONLのみを読み続けている。`query.go`には旧形式への互換変換(`fromLegacy`)が実装されており、proposalが「書かなくて良い」としたはずのコードが存在する。
- **float64をレジャーの正本にしない(10.1)**: 旧`internal/aggregate`経由の一部のコストパスがfloat64のまま生き残っている。decimalバリデーションが内部でParseFloat経由になっている箇所もある。
- **欠測を0にしない**: 未登録モデルのコストがエラーなく$0として表示される箇所が複数ある。

特に上位5件(二重計上・データ欠落に直結するもの)は実装のやり直しが必要な水準。

## 指摘一覧(重大度順)

### 1. Codexの `turn.completed` usage が無条件に非累積デルタとして採用される
- **File**: `internal/provider/codex/phase3.go:695` (`addCodFinalUsage`)
- **Category**: correctness
- addCodFinalUsageは常にturn.completed.usageを最終的な非累積デルタとして採用するが、phase3設計ドキュメントは非累積であると確認できた場合のみ採用すべきとしている。`exec-events.jsonl`フィクスチャの`cached_input_tokens`値は累積合計のように見える。
- **失敗シナリオ**: 3ターンのCodexセッションでturn.completed.usageが累積合計1000, 2500, 4000トークンを報告している場合、reconcile.goはこれを合算して7500として扱ってしまう(真の値は4000)。proposal 3.4が明示的に警告している二重計上を直接再現する。turn.completedを複数含む、またはtoken_usage_recordと混在するトランスクリプトを検証するテストは存在しない。

### 2. コンテインメントで子の合計が不明な場合、親子ともCountedのまま残る
- **File**: `internal/reconcile/reconcile.go:175`
- **Category**: correctness
- 明示的な親子コンテインメントで片方の正規化合計が欠けている場合、Conflictを記録し`result.Unknown=true`にするが、等号/未満/超過の分岐と異なりロールを降格させない。そのため親と既知の子は`RoleCounted`のまま両方合算される。
- **失敗シナリオ**: 親usage P=100(Counted)が子A(tokens=nil)と子B(tokens=30, Counted)を含む場合、子Aが不明なため誰も降格されず、KnownSubtotalは130になる(子Bの30は親の100に既に含まれているにもかかわらず)。proposal 3.4の「重複範囲が不明なら単一の確定合計を作らない」という規則に違反する。

### 3. Journalの重複検出がバイト完全一致を要求し、正当な再送イベントを取りこぼす
- **File**: `internal/journal/journal.go:143`
- **Category**: correctness
- Appendは正規化JSONを比較し、同じコンテンツ由来のEventIDでもコレクタのメタデータ(例: `Time.ReceivedAt`)が異なる再送は「already exists with different content」として拒否する(重複排除ではなくハードエラー)。
- **失敗シナリオ**: 同一の観測が異なるReceivedAtタイムスタンプを持つ別々のinboxレコードとして2回配信され、EventIDは同一になる場合、journal.Appendは毎回確定的に失敗する。MaxAttempts後、コレクタはレコードを`inbox/failed/`に移動し、実際の観測を重複排除ではなく完全に失ってしまう。

### 4. ダッシュボードのTrend/Breakdownが旧JSONL形式しか読まない
- **File**: `internal/query/legacy_views.go:13`
- **Category**: correctness
- `Service.Trend()`/`Breakdown()`は無条件に既存の`internal/aggregate`パッケージ(float64演算、旧埋め込みpricing.json)を旧形式の per-session JSONLに対して呼び出しており、新形式journalストリームのチェックが一切ない(直下の`SessionView`はチェックしている点と対照的)。`ProjectSummary`/`SessionSummary`はdecimalベースの`cost.Build`/`pricing.Version`パイプラインに移行済み。
- **失敗シナリオ**: プロジェクトが新コレクタに移行すると、hookは旧`sessions/*.jsonl`への書き込みを完全に停止する(二重書き込みパスは存在しないことを確認済み)。ダッシュボードのコスト/トークン推移グラフとモデル/プロバイダー別内訳(proposalが実現しようとしている主要な可視化)は、エラーなく永久に静止・陳腐化する。一方ProjectSummaryは正しく更新され続けるため、同じプロジェクトの2つのビューが目に見えて食い違う。

### 5. List/Project/Sessionsが旧形式float64アグリゲータへ静かにフォールバックする
- **File**: `internal/query/query.go:167`
- **Category**: correctness
- `ListProjects`/`Project`/`Sessions`は新形式journalストリームがゼロかどうかをチェックし、ない場合は旧`aggregate.Project`/`Sessions`を`store.Store`に対して呼び出し、`fromLegacy()`で新しい構造体形式に変換する。これはproposal 8.1が「書かなくて良い」としたはずの互換変換コードそのもの。
- **失敗シナリオ**: 新コレクタ導入以前のプロジェクト(またはアップグレード直後、最初の`collect --once`実行前)は`maatplug status`/`report`で`Source:"legacy"`として表示され、旧float64価格パスで計算されたコストが、decimalベースの`internal/cost`で計算された新形式プロジェクトと同じ一覧/JSON配列に混在する。見落としやすい`source`フィールドだけが2つの会計方式を区別する唯一の手がかりになっている。

### 6. 新形式サマリでLines/Tokens/Abandonedが常にゼロ
- **File**: `internal/query/query.go:555`
- **Category**: correctness
- `summaryFromCatalog`と`summaryFromDetail`は埋め込まれた`aggregate.ProjectSummary`/`SessionSummary`の`LinesAdded`、`LinesRemoved`、`TokensInput/Output/CacheRead/CacheCreation`、`AbandonedInstructions`、`CostUnpricedInstructions`を一切設定しない(`CostUSD`のみ後から明示的に設定される)。そのため新形式journal/catalog由来の全プロジェクト(このマイグレーションの目標形式)でこれらが偽のゼロになる。
- **失敗シナリオ**: app.jsは新形式プロジェクトに対して実際のデータに関わらず差分サイズを'+0/-0'と表示し、`costMissing`がtrueでも'+unpriced'バッジを表示しない。`maatplug report --format json`も、放棄されたターンが実際に存在するプロジェクトで常に`abandonedInstructions: 0`を報告する。

### 7. Pricing計算IDが入力イベントを含まず、再計算が過去の計算結果を上書きする
- **File**: `cmd/maatplug/cmd_pricing.go:202`
- **Category**: correctness
- `CalculationID`は`CalculationVersion + PricingVersion + Options`のみをハッシュし、プロジェクトの入力イベント/usageスナップショットを含まない。`runPricingCalculate`はそのパスへ不変性/上書きチェックなしの`AtomicWriteFile`で書き込む(同PR内の`pricing.VersionStore.Save`や`billing.DerivationStore.save`は異なる既存ファイルへの上書きを明示的に拒否しているのと対照的)。
- **失敗シナリオ**: `maatplug pricing calculate --project P`を今日実行し、来週(同じpricing version、usageは増えている)再度実行すると同じCalculationIDにハッシュされ、2回目の`AtomicWriteFile`が1回目の保存済み導出JSONを無警告で上書きする。pricingレジャーの存在意義である監査可能なコスト根拠が破壊される。

### 8. 未登録モデル/プロバイダーのコストがグラフで無警告に$0扱いになる
- **File**: `internal/aggregate/breakdown.go:132`
- **Category**: correctness
- `cost, _ := costFor(...)`で「レート未検出」を示すboolを破棄しており、Breakdown(および`trend.go:86-88`経由でTrend)で未登録のモデル/プロバイダーがGoのゼロ値0.0をカテゴリ/日次合計に無警告で加算する。`DailyPoint`には欠測/不完全を示すフィールドもない。
- **失敗シナリオ**: 旧埋め込みpricing.jsonに存在しないモデルへの呼び出しを含むプロジェクトの場合、ダッシュボードのモデル別内訳チャートとトレンドチャートはそのコストを未登録フラグなしで正確に$0として表示する。ユーザーが実際の支出として読み取る数値において、「欠測を0にしない」というproposalのコスト整合性ルールに直接違反する。

### 9. 手動SyncNowとバックグラウンドポーラー間でロックがない
- **File**: `internal/remotesync/remotesync.go:178`
- **Category**: correctness
- `fetchFile`は固定の`localPath+'.download-tmp'`に`os.Create`(切り詰め)後にリネームするが、エリア単位のロックがない。HTTPハンドラの同期的な`SyncNow`とバックグラウンドPollerのticker駆動の`syncAll`が同じエリアに対して同時に`Sync`を呼び出せる。
- **失敗シナリオ**: ユーザーがWeb UIで「今すぐ同期」をクリックしたタイミングでバックグラウンドポーラーの定期サイクルが同じエリアに対して発火すると、両方のgoroutineが同じtmpファイルを`os.Create`し、一方がsshの出力をストリーミング中にもう一方が切り詰める。どちらの`os.Rename`が後に完了するかで結果が無警告に決まり、`needsFetch`が最新と誤判定しうる破損/切り詰められたファイルが`localPath`に残る可能性がある。

### 10. 不完全レコードでKnownTotalsが解決済みバケットを含まない
- **File**: `internal/cost/cost.go:188`
- **Category**: correctness
- usageレコードが解決済み/未解決の排他バケットの混在を持つ場合、`record.Amount`は`nil`のまま、`Status='incomplete'`になるが、成功裏に解決されたバケット金額(既に行単位で計算済み)は`result.KnownTotals`に一切反映されない。
- **失敗シナリオ**: あるモデル呼び出しが`input_uncached`を既知の$2レートに解決し、`cache_write_input`が未登録の場合、既知の$2はKnownTotalsに一切現れない。設計が明示的に要求している既知/未知サブトータルの分離に依存する下流コンシューマは、混在解決レコードごとに既知支出を無警告で過小カウントする。

### 11. finish()が親スライスを走査中に生きたスライスを変異させる
- **File**: `internal/operation/operation.go:508`
- **Category**: correctness
- 外側の`range`は`op.States`のスナップショットを固定するが、内側のフォールバック検索は生きた(変異中の)`op.States`を再スライスする。そのため、ある未解決の`approval_pending`のために追加された合成の'approved'遷移が、同じ操作内の後続の`approval_pending`の解決検索から見えてしまう。
- **失敗シナリオ**: 1つの操作に、実際の解決なしに後続のstarted/requested遷移を持つ`approval_pending`状態が2つある場合、最初の処理で推論された'approved'遷移が合成され、2番目の処理がその合成エントリにマッチして、観測されていない(推論された)イベントを起点とする2つ目の`approval_wait`区間を作り出してしまい、待機時間を二重に帰属させ、2つの承認サイクルを1つの信号に混同する。

### 12. decimalのpricing/spend金額がParseFloat経由でバリデートされる
- **File**: `internal/model/event.go:1088`
- **Category**: correctness
- `PricingPayload.Rates[].Amount`と`SpendPayload.Amount`をバリデートする`validDecimal`は、decimal文字列を`strconv.ParseFloat`でパースしInf/NaNを拒否する。これらのフィールドがfloat64セマンティクスを避けるためにdecimal/stringである、というproposalの明示的なルールに反する。
- **失敗シナリオ**: 構文的に有効だがfloat64の範囲/精度を超える高精度decimal文字列が+Inf/-Infにパースされたり精度を失ったりすると、`math.IsInf`が`event.Validate()`(`journal.Append`が呼び出す)で本来有効なPricingPayload/SpendPayloadを拒否し、正当な財務データが一切追加できなくなる。

### 13. Billing変換のrateがvalidDecimalチェックをスキップする
- **File**: `internal/billing/derive.go:27`
- **Category**: correctness
- `Convert`は`rate`を`big.Rat.SetString`(`'1/2'`のような分数や`'1.5e2'`のような指数表記もパースする)と非負チェックのみで受け入れ、`validDecimal`を経由しない。`SaveConversion`は`Original.Amount`と`Converted.Amount`はバリデートするが`Rate`はしない。
- **失敗シナリオ**: `billing convert --file req.json`に`rate:"1.505e2"`を渡すと計算自体は正しく成功するが、その非正規形式の文字列が不変の`billing/derivations/conversions/<version>.json`レジャーにそのまま永久に書き込まれる。「通貨と十進値を正規化文字列で保存する」ルールに違反し、すべてのdecimalフィールドが正規パターンに一致すると仮定する将来のツールを壊す。

### 14. SaveAppSettingsがsettings.jsonを非アトミックに書き込む
- **File**: `internal/store/app_settings.go:67`
- **Category**: correctness
- `SaveAppSettings`は同パッケージ自身の`store.AtomicWriteFile`ヘルパーではなくプレーンな`os.WriteFile`を使用している。journal、ingest、contentstore、catalogは同PR内で全てアトミックヘルパーを使用している。
- **失敗シナリオ**: 書き込み中のクラッシュ・OOM-kill・電源喪失により`settings.json`が切り詰められた状態で残る。次回の`GetAppSettings`呼び出しは`json.Unmarshal`に失敗しエラーを返す(デフォルトへのフォールバックはしない)ため、手動でファイルを修正または削除するまでapp settingsの全呼び出し元が壊れる。

### 15. addCodexUsageが兄弟関数にあるtotal_tokens整合性チェックを欠く
- **File**: `internal/provider/codex/phase3.go:396`
- **Category**: correctness
- `transcript.go`の`normalizeUsage`は`usage.Total != computedTotal`のレコードを明示的に拒否する(「もっともらしいが不正確な合計を保存するのではなく、内部矛盾のあるレコードを拒否する」)が、実際に本番パス(`cmd_hook.go`→`ingest`→`collector`経由)に配線されている`phase3.go`の`addCodexUsage`には同等のガードがなく、`total_tokens`をそのまま採用する。
- **失敗シナリオ**: CodexビルドまたはApp Serverのバリアントが`input_tokens+output_tokens`と一致しない`total_tokens`を報告する場合(proposalの「推測せず診断する」要求が対象とする未知のトランスクリプト形式)、`addCodexUsage`は診断を発することなく誤った合計を無警告で採用してしまう。まさにこのケースのために意図的に強化された並行の関数とは対照的。

### 16. handleCollectionStatusがリクエストごとに重い非キャッシュの全走査を行う
- **File**: `internal/web/server.go:512`
- **Category**: efficiency
- `handleCollectionStatus`は`query.Doctor()`を呼び出し、これが`Measure()`(データエリアルート全体への`filepath.WalkDir`、および`ListProjects`/`ProjectDetail`/`Periods`のフルパス)と`coverageAndCutover`(全プロジェクトの全journalイベントの再読み込み)を、設定済みデータエリアごとに、`GET /api/collection-status`が呼ばれるたびに同期的かつキャッシュなしで実行する。
- **失敗シナリオ**: 履歴が多いデータエリア(プロジェクト/journalファイルが多数)では、訪問のたびにcollection-statusページの表示に数秒〜数分かかり、キャッシュが一切ない。catalog/summaryデータはリクエストごとの再計算ではなくキャッシュから提供されるべきという期待に反する。

## 修正結果

2026-09-12に全16件を修正した。旧形式との読み取り互換性は要件に含めず、問い合わせ経路を新形式ジャーナルへ一本化した。

| # | 修正内容 | 主な検証 |
|---:|---|---|
| 1 | Codex `turn.completed`をthread累積候補として保持し、同じRunにresponse deltaがあれば行順に依存せず抑止する | 累積2件は最新値だけ、delta混在はdeltaだけを採用 |
| 2 | 親子包含のどれかで合計が不明なら親と採用候補の子を全て`conflict`へ降格する | 100の親、未知の子、30の子で既知小計0 |
| 3 | 同じEventIDの再送では`receivedAt`だけを受付メタデータとして比較対象外にする | 時刻違いはduplicate、観測内容違いはconflict |
| 4 | Trend/Breakdownを新形式ジャーナル、`reconcile`、有効なpricing版、`cost`から生成する | Web APIの新形式journalテスト |
| 5 | List/Project/Sessions/SessionViewから旧形式フォールバックを削除する | 旧形式なしの新形式APIテスト |
| 6 | token内訳、差分計測、未計算件数を新形式サマリへ反映し、収集不能のabandonedは省略する | Web表示でpartial/unobserved/not collectedを区別 |
| 7 | CalculationIDに入力EventID集合を含め、保存済み導出結果をファイルロック下で不変にする | 入力snapshot違いでIDが変わるテスト |
| 8 | グラフに既知の通貨別小計と`costMissing`/`usageMissing`を返し、画面に欠測警告を表示する | 未登録料金を0円と確定しない表示 |
| 9 | データ領域単位の同期mutexと一意なdownload一時ファイルを導入する | 同一領域の並行同期が直列化されるテスト |
| 10 | 一部だけ料金解決できたrecordでも解決済みlineを`KnownTotals`へ加える | $2既知＋未解決bucketのテスト |
| 11 | approval解決推論は観測済み状態のsnapshotだけを検索する | 合成approvedを次のpendingに再利用しないテスト |
| 12 | decimalを正規表現で構文検証し、float64範囲に依存しない | 超高精度decimalと不正な指数・分数のテスト |
| 13 | 為替rate、元金額、配賦元金額に同じcanonical decimal検証を適用する | 指数、分数、先頭ゼロを拒否するテスト |
| 14 | `settings.json`を0600のatomic writeで保存する | storeテストと全体テスト |
| 15 | Codex `total_tokens`とinput＋outputの矛盾を`conflict`として非正規化にする | 矛盾レコードのテスト |
| 16 | Webは軽量な`CachedDoctor`を使い、全走査する明示的doctorがsnapshotを更新する | 壊れたjournalを再走査せずsnapshotを返すテスト |
