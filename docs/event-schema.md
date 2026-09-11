# maatplugイベント契約 version 1

更新日: 2026-09-10

本書は [`proposal-command-audit-and-pricing.md`](proposal-command-audit-and-pricing.md) の Phase 1で定義する、Provider非依存イベントの契約である。Go型は [`internal/model/event.go`](../internal/model/event.go)、機械可読なJSON Schemaは [`event.schema.json`](event.schema.json)、受入fixtureは [`testdata/events/`](../testdata/events/) に置く。

この契約はClaude CodeやCodexのtranscript形式を共通化したものではない。Providerの原記録を失わず、観測できた事実、推定、欠測、訂正、集計上の採否を分けて保存するための境界である。

## 1. 基本方針

- JSONLの各行はEventEnvelopeだけを格納する。
- schemaVersionは整数1で固定する。Provider版や料金表版には流用しない。
- eventTypeがpayloadのGo型とJSON Schema定義を決める。
- 全IDはnamespaceとvalueの組で表す。
- 本文、コマンド、tool output、pathなどの内容はContentRefで参照する。
- 未知値を空文字、0、false、nullだけで表現しない。FieldAvailabilityまたはQuantity.availabilityとreasonを使う。
- Providerの生usageと正規化usageを別にする。
- raw eventを変更せず、訂正と集計上の採否をRevision、Relation、Adoptionで表す。
- Provider時刻、観測時刻、collector受信時刻を同一視しない。

## 2. EventEnvelope

| field | 必須 | 意味 |
|---|---:|---|
| schemaVersion | yes | 共通契約の版。現在は1 |
| eventId | yes | イベント自身のNamespacedID |
| eventType | yes | payload discriminator |
| source | yes | Provider、surface、版、収集経路 |
| time | yes | Provider・観測・受信時刻 |
| sequence | no | source stream上のgenerationとposition |
| scope | yes | この観測が属するproject、run、session、instruction等 |
| availability | no | 論理fieldごとの可用性 |
| relations | no | 親子、包含、共有、再試行、訂正、入力・結果関係 |
| revision | no | supersedesまたはcorrectsするevent |
| adoption | no | 集計候補のcandidate、adopted、rejected、superseded |
| payload | yes | eventTypeに対応するpayload |

source.surfaceはcli、vscode、sdk、collectorなどの観測面を表す。surfaceVersionとengineVersionは別fieldである。VS Code拡張と同梱CLIの版が異なる場合、片方で代用しない。

## 3. IDとscope

NamespacedIDはnamespaceとvalueを必須とする。namespaceにはIDを発行した領域を指定する。例としてclaude/session、codex/turn、openai/response、maatplug/event、fixtureがある。同じ文字列値でもnamespaceが違えば別IDである。

EntityRef.kindはIDが指す対象を表す。version 1のGo型は次を定義する。

- project、run、session、thread
- instruction、turn
- actor、agent
- model_call、model_attempt、response
- usage、input_element
- operation、approval、artifact
- price、spend、stream

scopeには観測が属する既知の範囲を並べる。scopeは所有関係やusage包含を意味しない。親子、共有、包含はRelationで個別に表す。

## 4. 時刻と順序

EventTimeは次のclockを分離する。

| field | 意味 |
|---|---|
| providerAt | Providerの原記録に記載された発生時刻 |
| observedAt | watcher、VCS、OS telemetry等が状態を観測した時刻 |
| receivedAt | maatplugがイベントを受領した時刻。必須 |

hook payloadに時刻がない場合、receivedAtだけを記録する。receivedAtをproviderAtへ複写してはならない。

EventSequenceはstream、generation、position、providerSequenceを持つ。positionは0が正当な値なのでpointerで表す。transcriptのtruncate・rotation、collectorの再開、schema cutoverではgenerationを変える。generationまたはbaselineが不明な累積値から差分を作らない。

## 5. 可用性

| status | 意味 |
|---|---|
| observed | 今回の収集で値を直接得た |
| provider_reported | Providerが仕様または値として申告した |
| inferred | 他の観測から導出した |
| not_collected | 設定または方針により収集しなかった |
| unavailable | 対象の収集源から取得できない |
| unknown | 取得可否または値を確定できない |
| conflict | 複数の候補が矛盾している |

observed以外はreasonを必須とする。FieldAvailability.pathは共通イベントへのJSON Pointerであり、Provider固有field名を入れない。根拠eventがある場合はevidenceへevent IDを記録する。

Quantityはvalueとavailabilityを持つ。observed、provider_reported、inferredではvalueが必須で、それ以外ではvalueを持たない。これにより、実測0、欠測、取得不能、競合を区別する。

## 6. Relation、Revision、Adoption

Relation.kindは次を定義する。

- parent_of、contains、shared_with
- retry_of、caused_by、attributed_to
- supersedes、corrects、adopts、duplicate_of
- derived_from、input_to、result_of

certaintyはobserved、provider_reported、inferred、disputedのいずれかである。observed以外はreasonを必須とする。

includesはusageや費用の包含関係にだけ使う。agent Aがagent Bのparentであっても、Aのusage totalがBを含むとは限らない。Providerが包含を示さない場合はincludesを省略する。

Revisionはeventの版番号とsupersedes/corrects対象を記録する。Adoptionは派生集計が候補を採用したかを表す。rejectedやsupersededにしても元eventは削除しない。集計規則の版はruleVersionへ記録する。

## 7. ContentRef

内容本体はイベントstreamから分離する。ContentRefはcontent objectのID、variant、media type、byte length、SHA-256、availability、reason、derivedFromを持つ。

variantはraw、redacted、summary、hash_only、omitted、deletedである。

- rawとredactedは異なるcontent objectにする。
- summaryや切詰め結果はderivedFromで元contentを参照する。
- 収集しなかった場合はomitted、保存後にretentionで消した場合はdeletedとする。
- omittedとdeletedはreasonを必須とする。
- hashとlengthだけを残す場合はhash_onlyを使用する。

この区別により、「本文が元から存在しない」「収集しなかった」「マスクした」「保存後に削除した」を監査時に判別できる。

## 8. payload一覧

| eventType | Go payload | 主な対象 |
|---|---|---|
| instruction.recorded | InstructionPayload | ユーザー指示、指示content、提出状態 |
| actor.observed | ActorPayload | user、main agent、subagent、tool等の実行主体 |
| model.call | ModelCallPayload | instructionから生じたモデル呼出し、要求・実行モデル |
| model.attempt | ModelAttemptPayload | retryを含む個別attemptとresponse |
| usage.reported | UsagePayload | response、attempt、turn、query、thread等のusage |
| input.observed | InputElementPayload | prompt、履歴、rule、tool定義・結果、file、attachment |
| operation.requested | OperationPayload | shell、編集、network、MCP、browser等の要求 |
| approval.resolved | ApprovalPayload | 承認判断、判断主体、理由 |
| operation.result | OperationResultPayload | 成功、失敗、拒否、部分結果、async結果 |
| state.changed | StateTransitionPayload | 任意entityの状態遷移 |
| artifact.observed | ArtifactPayload | file、git tree、diff、build artifact等 |
| pricing.published | PricingPayload | 有効期間付き料金と適用条件 |
| spend.reported | SpendPayload | 見積り、請求、subscription、credit、refund |
| collection.state | CollectionStatePayload | cursor、generation、停止、欠測、隔離、raw evidence、coverage |

状態値やoperation kindの語彙はProviderの進化を妨げないためstringとして保持する。Phase 1のvalidatorは必須性を検査し、Provider固有値を共通の偽のenumへ押し込まない。集計層で共通状態へ変換する場合は変換規則の版を残す。

## 9. usage

UsagePayloadは次の三層を分離する。

### 9.1 RawUsage

unitとProvider fieldをそのまま保持する。fieldsはJSON値を許し、input_tokens、cache_read_input_tokens、total_tokens等の名前や包含意味を変えない。

### 9.2 NormalizedUsage

| collection | 意味 |
|---|---|
| exclusiveBuckets | 合計可能な排他的bucket |
| subsets | 親bucketの内数。ofで親bucket名を指定 |
| totals | Providerが示した合計または検算用合計 |

たとえばCodexのreasoning_output_tokensがoutput_tokensの内数なら、outputをexclusiveBuckets、reasoning_outputをsubsetsに置く。両方を加算しない。Claudeのcache readとcache creationのように料金区分が排他的なら、それぞれexclusiveBucketsに置く。

正規化不能なraw usageはnormalizedを省略し、FieldAvailabilityで理由を残す。

### 9.3 CounterSemantics

kindはdelta、cumulative、snapshotである。cumulativeはgeneration必須とする。baselineEventIdが確認できる場合だけ累積差分の根拠として使用する。

MeasurementStateはprovisional、final、correctedである。finalでも別情報源と競合する場合があるため、MeasurementStateとAdoptionを別にする。

## 10. 料金と支出

PricingPayloadはProvider、modelPattern、currency、有効期間、usage class別rate、条件、source contentを持つ。amountは浮動小数の丸めを避けるためdecimal stringで保存する。

SpendPayloadもamountをdecimal stringで保存し、kindでprovider_estimate、calculated_estimate、billed、subscription、credit、refund等を区別する。usageとcoverageは独立しており、請求が収集対象外runを含む場合も表現できる。

tokenから計算した費用、Providerのローカル見積り、実際の請求額を一つの値へ上書きしない。

## 11. 検証とfixture

internal/model.ValidateJSONは次を検査する。

- envelopeとpayloadの未知field
- schemaVersionとeventType
- 必須ID、source、receivedAt、scope
- availabilityとreasonの整合
- revision、adoption、relation
- ContentRefの状態
- Quantityのvalueとavailability
- cumulative counterのgeneration
- normalized usageの排他的bucketとsubset参照
- payloadごとの必須参照、金額、duration

internal/model/event_test.goはJSON Schemaのversion、eventType、payload mapping、Go JSON fieldとの一致を確認する。またacceptance.jsonlの全行をGo validatorで検査し、15ケースと全eventTypeがfixtureに存在すること、期待値が参照するevent IDが存在することを確認する。

testdata/events/expectations.jsonはPhase 2以降の保存、集計、API試験から共用する。期待集計をfixtureと別にすることで、入力eventを変更せずに集計規則の実装を比較できる。

## 12. 現行実装との境界

internal/model/model.goに残るSessionStartEvent、InstructionEvent、TurnEndEvent、SessionEndEventは現在のrecorder、store、aggregate、webをコンパイル可能に保つための一時的な投影型である。version 1のEventEnvelopeには含めず、event.schema.jsonにも定義しない。

Phase 2で新しいjournalとcatalogをEventEnvelopeへ接続し、Phase 3でProvider adapterを切り替えた時点で旧投影型と旧session JSONLを削除する。旧形式の読み込み互換、二重書き込み、DB migrationは実装しない。
