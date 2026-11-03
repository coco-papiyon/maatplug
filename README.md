# maatplug

Claude Code / Codex の利用状況（指示、ソース変更量、取得可能な場合のトークン使用量）を JSONL に記録し、CLI と Web ダッシュボードで確認するツールです。

現時点で実装済みなのは Claude Code / Codex の hook 連携、CLI、`maatplug-web` です。Copilot の exec-wrap、VS Code 拡張、`maatplug serve` サブコマンドはまだ実装されていません。

## 必要なもの

- Go 1.22 以上（`go.mod` は Go 1.22.5 を指定）
- Claude Code（hook連携を使う場合）
- Codex（Codexプラグインを使う場合）
- Git（Gitリポジトリの変更量を計測する場合）
- `cloc`（任意。未インストールでも記録処理は継続します）

## ビルド

リポジトリのルートで実行します。

```sh
go build ./...
```

実行ファイルを `bin` に出力する場合:

```sh
mkdir -p bin
go build -o bin/maatplug ./cmd/maatplug
go build -o bin/maatplug-web ./cmd/maatplug-web
```

Windows PowerShell では出力名に `.exe` を付けます。

```powershell
New-Item -ItemType Directory -Force bin | Out-Null
go build -o bin/maatplug.exe ./cmd/maatplug
go build -o bin/maatplug-web.exe ./cmd/maatplug-web
```

Go のインストール先（通常は `GOBIN` または `GOPATH/bin`）へ直接インストールすることもできます。

```sh
go install ./cmd/maatplug ./cmd/maatplug-web
```

## Claude Code プラグイン（hook）のインストール

maatplug は現在、独立したプラグインパッケージではなく、Claude Code の `settings.json` に hook を登録して連携します。

まず登録内容を確認します（この時点ではファイルを変更しません）。

```sh
maatplug init --scope user
```

確認後、ユーザー設定へ登録します。

```sh
maatplug init --write --scope user
```

プロジェクト単位で登録する場合は、対象プロジェクトのルートで次を実行します。

```sh
maatplug init --write --scope project
```

`--scope user` は `~/.claude/settings.json`、`--scope project` はプロジェクト内の `.claude/settings.json` を更新します。既存の設定は保持され、同じコマンドを再実行しても hook は重複登録されません。

`maatplug` が PATH にない場合は、hook に登録する実行ファイルを指定します。

```sh
maatplug init --write --scope user --binary /path/to/maatplug
```

Windows の例:

```powershell
maatplug.exe init --write --scope user --binary 'C:\Program Files\maatplug\maatplug.exe'
```

登録後に Claude Code を起動すると、`SessionStart`、`UserPromptSubmit`、`Stop`、`SessionEnd` hook が記録を作成します。保存先を変更する場合は `MAATPLUG_HOME` を設定してください。

```sh
MAATPLUG_HOME=/path/to/maatplug-data maatplug status
```

## Codexプラグインのインストール

先に `maatplug` をビルドまたは `go install` し、実行ファイルを PATH から起動できる状態にしてください。次に、このリポジトリをローカルマーケットプレイスとして登録し、プラグインを追加します。マーケットプレイスのマニフェストは `.agents/plugins/marketplace.json` にありますが、`marketplace add` にはそのファイルを含むリポジトリルートを指定します。

```sh
codex plugin marketplace add /path/to/maatplug
codex plugin add maatplug@maatplug-local
```

Windows PowerShell の例:

```powershell
codex plugin marketplace add C:\data\dev\github\maatplug
codex plugin add maatplug@maatplug-local
```

### プラグインの承認

インストール後、Codexを起動します（起動中の場合は新しいセッションを開始します）。セッション内で `/plugins` を実行し、`maatplug` を選択してください。

1. `maatplug` の詳細を開く。
2. `hooks/hooks.json` に定義されたhooksの内容を確認する。
3. 内容を信頼できることを確認して、プラグインを承認・有効化する。プラグイン一覧では `Space` キーで有効／無効を切り替えられます。
4. Codexを再起動、または新しいセッションを開始して、hooksを有効にする。

承認または有効化を後回しにした場合も、後から `/plugins` を開いて同じ操作を行えます。`maatplug` のhookは `SessionStart`、`UserPromptSubmit`、`Stop`、`SessionEnd` で実行されます。hookの内容を確認できない場合や信頼できない場合は、有効化しないでください。

Codexのhook payloadにはトークン使用量フィールドがないため、maatplugは `transcript_path` が指すローカルtranscriptを読み、`turn_id` に対応するトークン数をベストエフォートで取得します。Codexのtranscript形式は安定したhook APIではないため、ファイルを読めない場合、対応する使用量がない場合、または未知の形式の場合は `tokensUnavailable:true`（Webでは `tokens unavailable`）として保存します。指示、変更量、トークン数は、`UserPromptSubmit` と `Stop` に共通する `turn_id` で対応付けます。

## CLIで確認

```sh
maatplug status
maatplug report
maatplug report --format json
```

## Webダッシュボードの起動

Windowsでは、リポジトリ直下の`start.bat`を実行すると`go install`後にWebダッシュボードを起動する。

```powershell
.\start.bat
```

Webは <http://127.0.0.1:4173> で待ち受ける。`start.bat`は新しいウィンドウを開かず、そのまま同じコンソールで`maatplug-web`をフォアグラウンド実行する。`maatplug-web`は起動時に`maatplug collect --watch`を自分の子プロセスとして起動し、その出力を`[collect] `プレフィックス付きで自分のログへ流し込む（1ウィンドウで両方の状態が見える）。子プロセスが異常終了した場合は自動的に再起動を試みる。Ctrl+Cで（またはウィンドウを閉じると）子の収集処理も含めて停止する。収集を別ホスト等で自前管理していて`maatplug-web`に起動させたくない場合は`--no-collect`を付ける。

ビルドせずに起動する場合:

```sh
go run ./cmd/maatplug-web
```

ビルド済みバイナリを使う場合:

```sh
./bin/maatplug-web --addr 127.0.0.1:4173
```

Windows PowerShell:

```powershell
.\bin\maatplug-web.exe --addr 127.0.0.1:4173
```

ブラウザで <http://127.0.0.1:4173> を開いてください。データディレクトリを明示する場合は `--data-dir` を指定します。

```sh
go run ./cmd/maatplug-web --data-dir /path/to/maatplug-data
```

既定では localhost のみで待ち受け、認証はありません。外部ネットワークへ公開する場合は、適切な認証・アクセス制御を別途設定してください。

## テスト

```sh
go test ./...
go vet ./...
```
