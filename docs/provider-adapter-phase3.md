# Phase 3 Provider収集アダプター

Phase 3では、Claude CodeとCodexのhook・transcriptをPhase 1の共通イベントへ変換する。受付、解析、集計を分離し、正本はPhase 2の内容オブジェクトとRun別JSONLジャーナルに保存する。DBは使用しない。

## 実行境界

`maatplug hook <provider> --event <name>`はstdin payloadを原子的なinbox recordへ保存して終了する。JSONの意味解析、transcriptの読み取り、usage正規化、catalog再構築は`maatplug collect`が行う。hookプロセスではgit diff、cloc、transcript全走査、料金取得を実行しない。

collectorはProvider別デコーダで`session_id`、`cwd`、Claudeの`prompt_id`、Codexの`turn_id`／`root_turn_id`を取得する。raw hook payloadを内容オブジェクトに保存し、同じreceiptから意味イベントを生成する。参照する内容オブジェクトをすべて確定した後にイベントを追記する。

## Transcript差分取り込み

Providerとtranscript pathの組ごとに`state/provider-cursors/<provider>/<hash>.json`を持つ。カーソルには次の情報を保存する。

- transcript pathと世代ID
- 次に読むbyte offsetと行番号
- 正規化済みの応答、usage、tool、artifactのProvider ID

世代IDはpathと先頭の完全な行から生成する。ファイルが縮小した場合、または先頭行が変わった場合はローテーションとして先頭から読む。改行前の末尾は`partial_tail`として記録し、カーソルを進めない。新しい完全な行が到着すると次回に取り込む。カーソルの読み取り、イベント追記、更新は同じファイルロック内で行い、イベントが永続化された後にだけ位置を進める。

各完全行はraw内容オブジェクトと`collection.state`イベントを持つ。`sequence`にstream、generation、行位置を記録し、意味イベントの`source.recordId`から根拠行を参照できる。未知のJSON、未知のrecord type、transcript未提供、部分行、ローテーションは推測値へ変換せず診断イベントとして残す。

同じProvider応答が別の行や後着行に再掲された場合もraw行は残す。モデル呼び出しと採用usageはProvider応答IDごとに一度だけ生成し、処理済みIDをカーソルへ保存する。物理行が重複してもcatalogの採用合計は増えない。

## Claude Code

hookデコーダはライフサイクル、指示、ツール前後、承認、サブエージェント、ファイル変更、compactを処理する。Claude設定には`SessionStart`、`UserPromptSubmit`、`PreToolUse`、`PermissionRequest`、`PermissionDenied`、`PostToolUse`、`PostToolUseFailure`、`SubagentStart`、`SubagentStop`、`FileChanged`、`PreCompact`、`PostCompact`、`Stop`、`SessionEnd`を登録する。

transcriptでは`message.id`をモデル呼び出しとusageの主キーにする。同じmessageがthinking、tool use等の複数行に現れてもusageは一度だけ採用する。`tool_use.id`と`tool_result.tool_use_id`を操作要求・結果として対応させる。`isSidechain`はサブエージェントの観測として残すが、親IDが得られない場合は`unknown`を明記する。usageは通常入力、cache write、cache read、出力を排他的bucketとして保持する。

## Codex

Codex pluginはライフサイクル、指示、ツール前後、承認、サブエージェント、compactのhookをinboxへ送る。

transcriptの`response_id`をモデル呼び出し単位とする。`usage`は応答単位のdeltaとして採用し、`turn_token_usage`と`thread_token_usage`は累積candidateとして別に保持する。累積値から根拠のない差分は生成しない。`reasoning_output_tokens`は出力のsubsetに置き、排他的bucketや合計へ再加算しない。CLI JSONの`turn.completed.usage`とApp Serverの`rawResponse/completed`は、それぞれの観測面で非累積と確認できる場合だけ採用する。

CommandExecutionはcommand、cwd、parsed command、stdout、stderr、終了コード、所要時間を取得できた範囲で操作イベントへ変換する。patchとFileChangeは編集操作、結果、pathごとのartifactへ変換し、call IDがある場合は`result_of`関係を付ける。App Serverのturn状態とtoken snapshotも元のcounter semanticsを維持する。

transcript行自身がprompt／turn IDを持つ場合はそのRunへ保存する。Stop hookのRunと異なる行が混在した場合はtranscript側IDで振り分け、`/scope/run`を`conflict`として残す。IDがない行はhookのRunへ置くため、同一transcriptをIDなしで並行利用した範囲は確定できない。

## 欠測とProvider差

存在しない値を0や空の確定値として補完しない。ClaudeにCodexのreasoning subsetを作らず、CodexにClaudeのcache write値を推定しない。モデル名、操作ID、終了コード、所要時間、親agent、transcript path等が得られない場合は、該当イベントのavailabilityまたはcollection診断に理由を記録する。raw記録は正規化できない場合にも保存する。

## 検証と性能

Phase 0の全fixtureをテストへ接続した。Claudeは重複message、tool use/result、Bashを検証し、Codexは応答usage、turn/thread累積、reasoning subset、旧CommandExecution、file change、CLI JSON、App Serverを検証する。collector統合テストでは、未変更transcriptの再読防止、追記、後着重複、部分行、ローテーション、採用合計の不変性を確認する。

2026-09-10にWindows上で`go test ./cmd/maatplug -run '^$' -bench BenchmarkHookBoundary -benchtime=20x -count=3`を実行した。Phase 3のinbox受付は中央値約3.48 ms/op、旧direct recorderは約86.1 ms/opで、いずれも測定中の失敗は0だった。既存の32並列enqueueテストでも失敗・receipt重複・未確定ファイル露出は0である。この値はローカルファイルシステム上の境界比較であり、collector処理時間をhook待ち時間へ含めていない。
