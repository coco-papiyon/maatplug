# 一覧専用の集計ファイル

収集処理が一覧専用のJSONを作成する。通常の一覧取得では、JSONLの列挙、各runファイルの存在確認、大きなcatalogの解析や再集計を行わない。

| ファイル | 内容 |
|---|---|
| データ領域直下の `project-list.json` | 全プロジェクトの一覧行、Provider別の一覧行、日別トークン・費用。通常のProject一覧とグラフで使用 |
| `projects/<id>/session-list.json` | セッションの一覧行。イベント・使用量の根拠・操作本文を含まない。runの識別子と基本情報のみ保持 |
| `projects/<id>/list-summary.json` | 領域全体の一覧を組み立てるための、プロジェクト単位の中間ファイル |

収集サイクルでは、変更されたプロジェクトのcatalogと一覧行を更新する。初回や一覧ファイルの欠落・破損・バージョン不一致時は、受信イベントがなくても収集側で再作成する。領域全体のindexは小さな中間ファイルから組み立て、内容が同じなら書き換えない。各ファイルはatomic renameで公開する。

一覧ファイルは最後に完了した収集時点の結果となる。欠落・破損時のHTTP取得には従来の互換経路を残しているため、収集開始前の既存データも表示できる。通常起動ではWebがcollectorを起動するため、初回収集が完了すると専用ファイルへ切り替わる。`--no-collect`で使う場合は別途collectorを実行する。

全期間のプロジェクト一覧とセッション一覧はProviderを指定しても専用ファイルを使う。通常グラフも日別の集計済みデータを使う。開始・終了日時を指定した一覧は、途中のイベントだけを含めた正確な集計を保つため、従来のイベント集計経路を使う。

集計行の型・生成処理は `internal/catalog/list_views.go`、保存は `internal/catalog/list_cache.go` にある。詳細APIは引き続き元の記録を参照する。

検証:

```powershell
go test ./internal/catalog ./internal/collector ./internal/query ./internal/web
go test ./internal/query -run '^$' -bench BenchmarkProjectListSnapshot -benchtime=500ms
```
