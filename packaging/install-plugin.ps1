[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('codex', 'claude')]
    [string]$Provider
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

try {
    $packageRoot = $PSScriptRoot
    if (-not (Test-Path -LiteralPath (Join-Path $packageRoot 'maatplug.exe'))) {
        throw 'maatplug.exe is missing. Extract the entire ZIP first.'
    }
    $cli = Get-Command $Provider -CommandType Application -ErrorAction Stop | Select-Object -First 1
    if ($Provider -eq 'codex') {
        $manifest = '.agents\plugins\marketplace.json'
        $selector = 'maatplug@maatplug-local'
    }
    else {
        $manifest = '.claude-plugin\marketplace.json'
        $selector = 'maatplug@maatplug-claude-local'
    }
    if (-not (Test-Path -LiteralPath (Join-Path $packageRoot $manifest))) {
        throw "Missing marketplace manifest: $manifest"
    }

    # Keep existing PATH entries intact; do not use setx (it can truncate PATH).
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $paths = @($userPath -split ';' | Where-Object { $_ })
    $alreadyPresent = @($paths | Where-Object {
        [Environment]::ExpandEnvironmentVariables($_).Trim('"').TrimEnd('\') -ieq $packageRoot.TrimEnd('\')
    }).Count -gt 0
    if (-not $alreadyPresent) {
        $newPath = $packageRoot
        if ($userPath) { $newPath = $userPath.TrimEnd(';') + ';' + $packageRoot }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        Write-Host "Added to user PATH: $packageRoot"
    }
    $env:Path = $packageRoot + ';' + $env:Path

    & $cli.Source plugin marketplace add $packageRoot
    if ($LASTEXITCODE -ne 0) { throw 'Marketplace registration failed. See the CLI error above.' }
    if ($Provider -eq 'codex') {
        & $cli.Source plugin add $selector
    }
    else {
        & $cli.Source plugin install $selector --scope user
    }
    if ($LASTEXITCODE -ne 0) { throw 'Plugin installation failed. See the CLI error above.' }

    Write-Host ''
    Write-Host "Installed: $selector"
    Write-Host 'Next steps:'
    Write-Host '1. Sign out of Windows and sign in again so all apps receive the new PATH.'
    if ($Provider -eq 'codex') {
        Write-Host '2. Open a new Codex session. Run /plugins and verify maatplug is enabled.'
        Write-Host '   Review and approve/trust its hooks if prompted.'
    }
    else {
        Write-Host '2. Restart Claude Code. Run /plugin and verify maatplug is enabled.'
        Write-Host '   Run /hooks to check its hooks. Review any trust prompts.'
        Write-Host '   If you previously used maatplug init, remove its old hook entries first'
        Write-Host '   to avoid recording the same activity twice (see README.txt).'
    }
    Write-Host '3. Run start-web.bat and open http://127.0.0.1:4173.'
    Write-Host '4. Send a prompt in a project and check the dashboard for recorded activity.'
    Write-Host 'Keep this extracted directory in place. See README.txt before moving it.'
}
catch {
    Write-Host "Installation failed: $($_.Exception.Message)" -ForegroundColor Red
    Write-Host 'Check that the provider CLI is installed and available on PATH, then retry.'
    Write-Host 'If PATH was already updated, that change remains in place.'
    exit 1
}
