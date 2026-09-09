[CmdletBinding()]
param(
    [ValidateSet('amd64', 'arm64')]
    [string]$Architecture = 'amd64'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

$repoRoot = Split-Path -Parent $PSScriptRoot
$distRoot = Join-Path $repoRoot 'dist'
$pluginManifest = Join-Path $repoRoot 'plugins\maatplug\.codex-plugin\plugin.json'
$pricingConfig = Join-Path $repoRoot 'internal\pricing\pricing.json'

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw 'Go was not found on PATH. Install Go 1.22 or later before running build.bat.'
}

if (-not (Test-Path -LiteralPath $pricingConfig)) {
    throw "internal\pricing\pricing.json not found. Run 'go run .\cmd\maatplug-pricing-fetch' first (build.bat does this automatically)."
}

$plugin = Get-Content -Raw -LiteralPath $pluginManifest | ConvertFrom-Json
$packageName = 'maatplug-{0}-windows-{1}' -f $plugin.version, $Architecture
$stageRoot = Join-Path $distRoot $packageName
$zipPath = Join-Path $distRoot ($packageName + '.zip')
$hashPath = $zipPath + '.sha256'

New-Item -ItemType Directory -Force -Path $distRoot | Out-Null
if (Test-Path -LiteralPath $stageRoot) {
    Remove-Item -LiteralPath $stageRoot -Recurse -Force
}
if (Test-Path -LiteralPath $zipPath) {
    Remove-Item -LiteralPath $zipPath -Force
}
if (Test-Path -LiteralPath $hashPath) {
    Remove-Item -LiteralPath $hashPath -Force
}
New-Item -ItemType Directory -Force -Path $stageRoot | Out-Null

$previousGoos = $env:GOOS
$previousGoarch = $env:GOARCH
$previousCgo = $env:CGO_ENABLED
$previousGocache = $env:GOCACHE
try {
    $env:GOOS = 'windows'
    $env:GOARCH = $Architecture
    $env:CGO_ENABLED = '0'
    $env:GOCACHE = Join-Path $distRoot '.gocache'

    Push-Location $repoRoot
    try {
        Write-Host "Building maatplug.exe for windows/$Architecture..."
        & go build -trimpath -ldflags '-s -w' -o (Join-Path $stageRoot 'maatplug.exe') .\cmd\maatplug
        if ($LASTEXITCODE -ne 0) { throw 'Failed to build maatplug.exe.' }

        Write-Host "Building maatplug-web.exe for windows/$Architecture..."
        & go build -trimpath -ldflags '-s -w' -o (Join-Path $stageRoot 'maatplug-web.exe') .\cmd\maatplug-web
        if ($LASTEXITCODE -ne 0) { throw 'Failed to build maatplug-web.exe.' }
    }
    finally {
        Pop-Location
    }
}
finally {
    $env:GOOS = $previousGoos
    $env:GOARCH = $previousGoarch
    $env:CGO_ENABLED = $previousCgo
    $env:GOCACHE = $previousGocache
}

# Keep this layout intact: Codex resolves the local plugin path from the
# marketplace root supplied to `codex plugin marketplace add`.
New-Item -ItemType Directory -Force -Path (Join-Path $stageRoot '.agents\plugins') | Out-Null
Copy-Item -LiteralPath (Join-Path $repoRoot '.agents\plugins\marketplace.json') -Destination (Join-Path $stageRoot '.agents\plugins\marketplace.json')
Copy-Item -LiteralPath (Join-Path $repoRoot 'plugins') -Destination $stageRoot -Recurse
Copy-Item -LiteralPath (Join-Path $repoRoot 'packaging\README.txt') -Destination (Join-Path $stageRoot 'README.txt')
Copy-Item -LiteralPath $pricingConfig -Destination (Join-Path $stageRoot 'pricing.json')

Compress-Archive -LiteralPath $stageRoot -DestinationPath $zipPath -CompressionLevel Optimal

# Fail the build if a future packaging change accidentally drops integration
# files that are needed after moving the ZIP to another machine.
Add-Type -AssemblyName System.IO.Compression.FileSystem
$archive = [System.IO.Compression.ZipFile]::OpenRead($zipPath)
try {
    $entries = @($archive.Entries | ForEach-Object { $_.FullName.Replace('\', '/') })
    $required = @(
        "$packageName/maatplug.exe",
        "$packageName/maatplug-web.exe",
        "$packageName/.agents/plugins/marketplace.json",
        "$packageName/plugins/maatplug/.codex-plugin/plugin.json",
        "$packageName/plugins/maatplug/hooks/hooks.json",
        "$packageName/README.txt",
        "$packageName/pricing.json"
    )
    foreach ($entry in $required) {
        if ($entries -notcontains $entry) {
            throw "Package validation failed: missing $entry"
        }
    }
}
finally {
    $archive.Dispose()
}

$sha256 = [System.Security.Cryptography.SHA256]::Create()
$zipStream = [System.IO.File]::OpenRead($zipPath)
try {
    $hashBytes = $sha256.ComputeHash($zipStream)
    $hash = ([System.BitConverter]::ToString($hashBytes)).Replace('-', '').ToLowerInvariant()
}
finally {
    $zipStream.Dispose()
    $sha256.Dispose()
}
Set-Content -LiteralPath $hashPath -Value "$hash  $($packageName).zip" -Encoding ASCII

Write-Host ''
Write-Host "Package: $zipPath"
Write-Host "SHA-256: $hash"
