# Phase 4 使用量の照合・帰属・入力構成

Phase 4は、Run別JSONLジャーナルを正本としたまま、プロジェクト単位で使用量を重複排除し、「どの指示に、なぜその使用量が生じたか」をcatalogへ投影する。DBは使用しない。照合結果と入力構成はいつでも再生成でき、ユーザー訂正だけを版付きフラットファイルとして保持する。

## 使用量照合

`internal/reconcile`は`usage-reconcile-v1`として、各`usage.reported`を`counted`、`explanatory`、`excluded`、`conflict`のいずれかへ分類する。各判断には入力event ID、Usage、適用対象、scope、counter、測定状態、規則版、理由を残す。

- 同じProvider Usage IDの再送は、訂正、確定、暫定の順と明示的なadoptionを使って1件を選ぶ。内容が同じでもUsage IDが違う実呼び出しは統合しない。
- 同じRun scopeにdelta明細がある場合、cumulativeまたはsnapshotを重ねて加算しない。cumulative差分は対象、世代、bucket定義が一致し、値が減少していないときだけ計算できる。
- 正規化合計がなく基準値を確定できないcumulativeは採用せず、不明を残す。
- `contains`関係で包含が明示された親合計と子明細は照合する。子が親と一致すれば子明細を採用し、子が親未満なら親を採用して差を`undecomposed`にする。子が親を超える場合は競合として確定合計を作らない。
- エージェントの親子関係だけから使用量の包含は推定しない。

`ProjectCatalog.knownUsageTokens`はプロジェクト全体で選ばれた非重複Usage集合から作る。Run別や指示別の表示値はその算出元にしない。期間別の使用量も同じプロジェクト選択集合をevent日時へ割り当てるため、複数Runにまたがる親合計と子明細を二重加算しない。

## 指示への帰属と配賦

`internal/attribution`の自動規則は、model callとinstructionの対応、および明示された`attributed_to`関係を使う。採用されたUsageごとに次を区別する。

| 区分 | 意味 |
|---|---|
| `direct` | 1件の指示へ明示的に対応できる |
| `shared` | 複数指示が同じUsageを共有し、未配賦である |
| `unattributed` | 指示との対応根拠がない |
| `corrected_allocated` | ユーザー方針により整数比で任意配賦した推定値 |

作業合計`workTotalTokens`は照合結果の`knownSubtotal`をそのまま参照する。`work`、`instructions`、`calls`のsummaryには、既知小計、直接帰属、共有未配賦、未分解、未帰属、配賦済み推定、配賦残額、欠測を持たせる。指示summaryの`knownSubtotal`は直接帰属または配賦された排他的な量だけであり、共有Usageは各関係指示の`sharedUnallocated`へ説明用に表示する。指示別表示を足して作業合計を作らない。配賦比率の整数除算で生じた差と意図的な未配賦分は`correctionRemainder`に残り、配賦済み推定との和は元Usageに一致する。

ユーザー訂正は次のようなJSONで保存する。`version`は変更のたびに新しくし、同じversionの内容は上書きできない。

```json
{
  "version": "reviewed-2026-09-10",
  "basedOn": "automatic-v1",
  "corrections": [
    {
      "usage": {"kind": "usage", "id": {"namespace": "codex/usage", "value": "usage-id"}},
      "allocations": [
        {"instruction": {"kind": "instruction", "id": {"namespace": "codex/instruction", "value": "a"}}, "units": 1, "totalUnits": 2},
        {"instruction": {"kind": "instruction", "id": {"namespace": "codex/instruction", "value": "b"}}, "units": 1, "totalUnits": 2}
      ],
      "reason": "ユーザーが作業内容を確認して等分した"
    }
  ]
}
```

```text
maatplug attribution apply --project <id> --file <policy.json>
maatplug attribution activate --project <id> --version <version>
maatplug attribution activate --project <id> --version automatic-v1
maatplug attribution list --project <id>
```

`apply`は新しい版を保存して有効化し、catalogを再構築する。`activate`は保存済み版または自動規則へ切り替える。catalogの`attributionVersions`には各版の結果と、`basedOn`で指定した旧版（省略時は`automatic-v1`）との差を保持する。参照した版が存在しなければ方針の保存を拒否する。帰属を変更しても`workTotalDelta`は0であり、元のusage eventや照合結果は変更されない。

## 入力構成

`internal/inputcomposition`は呼び出しごとに`input.observed`を集め、`instruction`、`history`、`rule`、`tool_definition`、`tool_result`、`file`、`attachment`などの`kind`をそのまま保持する。同じ内容オブジェクトが複数回入力された場合も、呼び出しごとに別の入力事実として残す。

加工後の`ContentRef.derivedFrom`は元内容IDを指し、`transformation`に切り詰めや要約の方法を記録する。元ツール出力とモデルへ渡した加工後内容は別のcontent objectとして扱う。byte数は`ContentRef.byteLength`、要素別トークン数はavailabilityと根拠を持つ`Quantity`で記録する。

モデル呼び出しが指示を参照していても実際の直列化入力が観測できない場合は、指示内容を`inferred`要素として示し、トークン数を作らない。Providerの選択済みUsageに入力bucketがある場合だけProvider入力合計と比較し、差を次に分類する。

| gap kind | 意味 |
|---|---|
| `matched` | 観測済み要素とProvider入力使用量が一致 |
| `estimation_error` | 要素別トークン数に推定が含まれる |
| `non_displayed_elements` | 測定済み要素だけではProvider入力使用量に届かない |
| `non_displayed_or_missing_elements` | 要素または要素別トークン数が欠測 |
| `missing_provider_usage` | 比較対象となるProvider入力使用量がない |

要素別推定はProvider申告使用量を置き換えない。完全な実入力がProviderから得られない場合も、未観測部分を0として扱わず、差とavailabilityをcatalogに残す。

## 検証範囲

fixtureテストは、同一Usageの暫定値から確定値への切り替え、異なる実呼び出しの保持、累積リセット、親子包含の一致・残差・競合、直接・共有・未帰属・任意配賦、複数回入力された加工済みツール結果、Provider入力との差を検証する。さらに、別Runにある親合計と子明細からプロジェクト合計を一度だけ作り、帰属版の変更前後で元使用量が不変であることを統合テストで確認する。
