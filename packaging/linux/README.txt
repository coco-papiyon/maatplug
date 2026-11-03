maatplug Linux 配布パッケージ
============================
Web、Codex Plugin、Claude Code Pluginを同梱しています。配布先にGoは不要です。
amd64はx86_64用、arm64はaarch64用です。アーキテクチャに合う版を展開してください。
必要なもの: Linux、POSIX sh、chmod、readlink、各プロバイダーCLI。
Gitはリポジトリの変更量計測に必要です。clocは任意です。

導入
----
1. tar -xzf maatplug-<version>-linux-<arch>.tar.gz
2. 展開されたフォルダーへcdします。以後このフォルダーを保持してください。
3. 必要な方を実行します。両方インストールしても構いません。sudoは不要です。
     sh install-codex.sh
     sh install-claude.sh
4. 成功時に表示される作業手順に従います。
5. sh start-web.sh を実行し http://127.0.0.1:4173 をブラウザーで開きます。
   Ctrl+CでWebと子の収集処理を終了します。

インストーラーは ~/.local/bin/maatplug と maatplug-web にシンボリックリンクを
作成し、ユーザー単位でプラグインをインストールします。既存の異なるリンクや
ファイルは上書きせず停止します。再実行では同じリンクをそのまま使います。
シェル設定ファイルは変更しません。必要なら起動環境に次を設定してください:
  export PATH="$HOME/.local/bin:$PATH"
シェルの起動設定に追加し、再ログインしてCodex/Claude Codeを再起動してください。
GUIから起動する場合も、そのアプリがこのPATHを継承する必要があります。
失敗時は非ゼロで終了します。作成済みリンクや登録済みマーケットプレイスは残る
場合があるため、CLIエラーを確認して再実行してください。

導入後の確認
------------
Codex: 新しいセッションで /plugins を開きmaatplugを有効化します。
       hookの承認・信頼確認が表示された場合は内容を確認してください。
Claude Code: 再起動して /plugin と /hooks で有効化・hookを確認します。
対象プロジェクトでプロンプトを送信し、Webで記録を確認してください。

以前にmaatplug init --writeでClaudeのhookを登録していた場合:
二重記録を防ぐため、Claudeを終了して設定をバックアップし、
~/.claude/settings.json および対象プロジェクトの .claude/settings.json から
maatplugを呼ぶ古いhookエントリーのみ削除してください。他の設定は保持します。
インストーラーは古いhookを削除しません。

Web起動オプションの例:
  sh start-web.sh --addr 127.0.0.1:5000
この場合は http://127.0.0.1:5000 を開きます。ブラウザーは自動では開きません。
Windows上で作成したアーカイブでも動くよう、shから起動してください。
起動・インストール時に同梱バイナリへ実行権限を付けます。
実行禁止(noexec)のファイルシステムでは、実行可能な場所へ展開し直してください。

データ保存先は通常のmaatplugユーザーデータフォルダーです。
MAATPLUG_HOME環境変数で変更できます。pricing.jsonは編集可能な料金参考データです。
Codexのトークン使用量は取得できない場合、tokens unavailableになります。
.agents、.claude-plugin、plugins、pricing.json、両バイナリは一緒に保持してください。

削除・移動
----------
使用した方のCLIでプラグインとマーケットプレイスを削除します:
  codex plugin remove maatplug@maatplug-local
  codex plugin marketplace remove maatplug-local
  claude plugin uninstall maatplug@maatplug-claude-local --scope user
  claude plugin marketplace remove maatplug-claude-local
~/.local/binのリンク先を確認し、このパッケージを指すmaatplug/maatplug-webのリンクを
削除します。Webを終了してから展開フォルダーを削除できます。保存済み記録は残ります。
移動する場合は同様に登録とリンクを除去し、新しい場所からインストールしてください。
