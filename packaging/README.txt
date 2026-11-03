maatplug Windows 配布パッケージ
==============================

Webダッシュボード、Codex Plugin、Claude Code Pluginを同梱しています。
配布先にGoやソースコードは不要です。ZIP全体を展開して使用してください。

必要なもの
----------
- Windows（配布ZIPのamd64 / arm64に対応する環境）
- Windows PowerShell 5.1以上
- Codex CLI（Codex Pluginを使う場合。codex pluginコマンド対応版）
- Claude Code CLI（Claude Pluginを使う場合。claude pluginコマンド対応版）
- Git（Gitリポジトリの変更量を計測する場合）
- cloc（任意。ソースコードの行数計測に使用）

まず行うこと
------------
1. ZIPを、継続して使うフォルダーにすべて展開します。例: C:\Tools\maatplug
2. 必要な方のインストールbatを実行します。両方実行しても構いません。
   Codex:       install-codex.bat
   Claude Code: install-claude.bat
3. 成功後に表示される作業手順に従ってください。
4. start-web.batを実行し、http://127.0.0.1:4173 をブラウザーで開きます。
5. 対象プロジェクトでプロンプトを送信し、Webで記録を確認します。

インストールbatは現在のユーザーに対してプラグインをインストールし、
この展開フォルダーをユーザーPATHに追加します。管理者権限は不要です。
既存のPATHは保持します。各CLIはあらかじめPATHから実行できる必要があります。
batは成功・失敗時とも表示を残すためキー入力を待ちます。
自動実行時は --no-pause を指定すると待機しません。
失敗時は終了コード1を返します。途中までのPATH更新やマーケットプレイス登録は
残る場合があります。表示されたCLIエラーを確認して再実行してください。

インストール後の作業: Codex
--------------------------
1. Windowsからサインアウトして再サインインし、新しいPATHをアプリに反映します。
2. Codexで新しいセッションを開始します。
3. /pluginsからmaatplugを開き、有効になっていることを確認します。
   hookの承認・信頼確認が表示された場合は内容を確認して承認してください。
4. start-web.batを起動し、対象プロジェクトでプロンプトを送って記録を確認します。

インストール後の作業: Claude Code
--------------------------------
1. Windowsからサインアウトして再サインインし、新しいPATHをアプリに反映します。
2. Claude Codeを起動し、/pluginでmaatplugが有効なことを確認します。
3. /hooksでmaatplugのhookを確認します。信頼確認が表示されたら内容を確認します。
4. start-web.batを起動し、対象プロジェクトでプロンプトを送って記録を確認します。

以前にmaatplug init --writeでClaudeのhookを登録した場合:
プラグインとの併用で二重記録になるため、Claude Codeを終了して設定をバックアップし、
ユーザーの ~/.claude/settings.json および使用中のプロジェクトの
.claude/settings.json から、maatplugを呼ぶ古いhookエントリーだけを削除してください。
ほかのツールのhookや設定は残してください。インストールbatは古いhookを削除しません。

Webの起動・終了
--------------
start-web.batを実行すると同じコンソールでWebと収集処理を開始します。
ブラウザーは自動では開きません。Ctrl+Cで収集処理も含めて停止します。
追加オプションを渡せます。例:
  start-web.bat --addr 127.0.0.1:5000
この場合は http://127.0.0.1:5000 を開いてください。

保存先は通常のmaatplugユーザーデータフォルダーです。
MAATPLUG_HOME環境変数を設定すると保存先を変更できます。
pricing.jsonはモデル別料金の参考データで、手動編集できます。
Codexのトークン使用量はローカルトランスクリプトから取得するため、
取得できない場合はtokens unavailableとして記録します。

フォルダー構成（移動・削除せず保持してください）
----------------------------------------------
  maatplug.exe / maatplug-web.exe / pricing.json
  start-web.bat
  install-codex.bat / install-claude.bat / install-plugin.ps1
  README.txt
  .agents/plugins/marketplace.json       Codex用マーケットプレイス
  .claude-plugin/marketplace.json        Claude用マーケットプレイス
  plugins/maatplug/                     Codex Plugin
  plugins/maatplug-claude/              Claude Plugin

インストール後は展開フォルダーを移動・削除しないでください。
移動する場合は各CLIでプラグインとマーケットプレイスを削除し、古いユーザーPATHを
Windowsの環境変数設定から除去したうえで、新しい場所のインストールbatを実行します。

アンインストール
----------------
  codex plugin remove maatplug@maatplug-local
  codex plugin marketplace remove maatplug-local
  claude plugin uninstall maatplug@maatplug-claude-local --scope user
  claude plugin marketplace remove maatplug-claude-local
使用している方だけ実行し、Windowsの環境変数設定でユーザーPATHから展開フォルダーを
除去してください。Webを終了すれば配布フォルダーを削除できます。
保存済みの利用記録は配布フォルダーとは別に保持されます。

公式仕様
--------
https://developers.openai.com/plugins/build/plugins
https://code.claude.com/docs/en/plugin-marketplaces
https://code.claude.com/docs/en/plugins-reference
