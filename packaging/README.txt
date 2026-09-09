maatplug Windows portable package
=================================

This directory can be moved anywhere on another Windows PC. Go and the maatplug
source code are not required on the destination PC. Git is required to measure
changes in Git repositories. cloc is optional and is used for line counts.

1. Add this extracted directory to PATH
---------------------------------------

Temporarily, in PowerShell:

  $env:Path = "$PWD;$env:Path"

For permanent use, add the extracted directory to the user's PATH in Windows
Settings. Confirm that this works:

  maatplug.exe status

2. Enable the Claude Code hooks
-------------------------------

Preview the settings change:

  .\maatplug.exe init --scope user --binary ".\maatplug.exe"

Then register the hooks in the current user's Claude settings:

  .\maatplug.exe init --write --scope user --binary "<FULL-PATH>\maatplug.exe"

Replace <FULL-PATH> with this extracted directory's absolute path. An absolute
path ensures that Claude can find maatplug even when it starts in another
project. Existing Claude settings are preserved.

3. Install the Codex plugin
---------------------------

Keep the .agents and plugins directories in this package. From this extracted
directory, run:

  codex plugin marketplace add "$PWD"
  codex plugin add maatplug@maatplug-local

Start a new Codex session and approve/trust the plugin hooks when prompted.
Because the hook command is `maatplug`, the extracted directory must be on PATH.
For each completed turn, maatplug reads token usage from Codex's local transcript
on a best-effort basis. If the transcript is unavailable or its format is not
recognized, the turn is recorded with tokens unavailable instead.

4. Start the dashboard
----------------------

  .\maatplug-web.exe --addr 127.0.0.1:4173

Then open http://127.0.0.1:4173 in a browser.

Data is stored under the normal maatplug user data directory. Set MAATPLUG_HOME
before launching maatplug or maatplug-web to use a different location.

5. Pricing reference data
--------------------------

pricing.json holds per-model USD-per-million-token rates for Claude and
Codex, scraped from each provider's public pricing page at build time. It is
a plain, editable config file, not required for maatplug/maatplug-web to
run. If a provider's page couldn't be parsed at build time, that provider
keeps whatever rates were already on record from a previous build rather
than going blank.
