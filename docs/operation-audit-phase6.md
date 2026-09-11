# Phase 6 操作・環境・作用・成果物

Phase 6は、Phase 3で保存したProviderイベントと内容オブジェクトから、操作単位の監査プロジェクションを作る。正本は引き続きRun別JSONLと内容オブジェクトであり、操作プロジェクションは`catalog.json`を削除して再生成できる。DBは使用しない。

## 操作ライフサイクル

`internal/operation`は`operation-projection-v1`として、同じoperation IDを参照する次のイベントを結合する。

- `operation.requested`: 操作要求、実行主体、入力、要求時刻
- `approval.resolved`: 承認待ち、許可、拒否と判断イベント
- `operation.result`: 完了、失敗、拒否、部分状態、非同期完了、終了コード、出力
- `state.changed`: Providerが別の状態遷移を通知した場合の補足
- `artifact.observed`: `result_of`関係が明示された実作用と成果物

要求、承認待ち、許可・拒否、開始、完了を時刻順の`states`として保持する。Providerが開始通知を持たず終了時刻と所要時間だけを持つ場合、開始は`inferred`として逆算する。Claudeのように要求行と結果行の時刻しかない場合も、その差を推定区間として扱う。結果が無い操作、operation IDを持たない承認、要求と対応しない結果や成果物は、完了扱いにせず`missing`または`orphan*`へ残す。

非同期結果は`async_completed`等として同期完了と区別する。`tool`、`approval_wait`、`operation_wall`の区間を保存し、全体の`elapsed`は区間の和ではなく重なりを統合した壁時計時間を返す。このため並列操作を二重加算しない。モデル呼び出しに開始・終了の両境界が無いProvider記録から、モデル実行時間を推測して作ることはしない。

## 操作種別と実行環境

Provider名は保持したまま、監査表示用に`shell`、`edit`、`browser`、`connector`、`external_api`等へ共通化する。未知のtool名は元のkindを残す。

操作時点の環境には、作業ディレクトリ、OS、shell、実行主体、permission mode、permission profile、sandbox、network accessを持たせる。各値は`availability`、理由、根拠event IDを伴う。Codexの`turn_context`、`CommandExecution.cwd`とargv、Claude transcriptの`cwd`と`permissionMode`、hook payloadの利用可能なfieldを操作入力の内容オブジェクトへ保存する。片方のProviderに無い値を、もう片方の既定値で埋めない。

## 指定対象と実作用

`requestedTargets`はtool input、Codexの`parsed_cmd`、コマンド引数から得た指定対象である。Providerの構造化fieldは`provider_reported`、コマンド文字列からのURL抽出は`inferred`とする。URLは監査一覧へ本文やqueryを複写せず、protocolとhostを保持する。

`observedEffects`は、Providerのartifact通知がoperationへ明示的に関連付く場合だけ作る。指定されたfile pathやURLから「実際に変更・通信した」とは推定しない。ネットワーク要求を検出しても通信telemetryが無ければ`/observedEffects/networkEndpoints`を`unknown`にする。Claudeの`FileChanged`のように操作IDを持たない通知は`orphanArtifacts`に残し、近い時刻の操作へ自動帰属しない。

Codexの`patch_apply_end`と`FileChange`は編集Operationとpath別artifactへ変換し、Providerのcall IDで`result_of`関係を作る。Claudeの`Edit`、`Write`、`Bash`は要求と結果を操作へ変換するが、Providerが実作用を通知しない範囲は欠測になる。

## diff・成果物・検証

artifactにunified diffがある場合だけ、`unified_diff_line_prefix_v1`で追加・削除行数を測り、locatorの拡張子から言語別純増減を出す。測定結果には方式と対象範囲を付ける。完全なfile内容をdiffと解釈しない。git treeの前後境界が観測できない操作や、並行編集を含むRunについて、Run全体の差を個別指示の正確な変更とは表示しない。

`go test`、`go vet`、`npm test`、`pytest`等の既知の検証コマンドは、要求とoperation resultを結び付けた`validations`として表示する。コマンド本文と出力本文はcatalogへ埋め込まず、既存のContentRefを参照する。

## 危険操作の補助監査

`operation-risk-v1`は要求されたコマンドを対象に、再帰的強制削除、`git reset --hard`、`git clean -f`、破壊的DB文をhigh、権限昇格や広いpermission変更等をmediumとして分類する。規則に一致しなかった解析可能なコマンドはlow、入力を読めない操作はunknownである。これは要求内容の静的判定であり、実作用の観測結果ではない。評価には規則版、理由、availability、根拠event IDを残す。

## 指示との関係と利用方法

同じProvider・Run scopeの`instruction.recorded`または`model.call`から、関連指示を`inferred`として付ける。operation event自体が指示IDを持たない場合に、時刻の近さだけで別Runの指示へ結び付けることはしない。

操作プロジェクションは`ProjectCatalog.derivations.operations`に保存される。CLIでは次を使う。

```text
maatplug operations --project <id>
maatplug operations --project <id> --kind shell
maatplug operations --project <id> --status failed
```

CLIは先にcatalogを正本から再構築し、操作、環境、状態、指定対象、観測作用、成果物、検証、risk、欠測をJSONで返す。

## Provider別の観測限界

- CodexはCommandExecutionのcwd、終了コード、所要時間、parsed command、patch/file change、turn contextの権限・sandboxを取得できる版がある。ただしtranscript形式は安定契約ではないため、未知recordは従来どおりcollection診断へ残る。
- Claudeはtool use/result、transcript時刻、セッションcwd、permission modeを取得できる。終了コード、操作単位cwd、構造化parsed command、実通信先は得られない場合がある。
- どちらも、現行fixtureでは実通信先を直接観測するOS telemetryを持たない。要求URLと実通信先を同一視しない。
- git外環境または境界snapshotが無い場合、artifact通知以上の変更範囲を作らない。これにより並行作業を特定指示の変更として誤表示しない。

