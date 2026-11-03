[CmdletBinding()]
param(
    [ValidateSet('windows', 'linux')]
    [string]$Platform = 'windows',
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
if ($plugin.version -notmatch '^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$') {
    throw 'Invalid plugin version for a package directory name.'
}
$packageName = 'maatplug-{0}-{1}-{2}' -f $plugin.version, $Platform, $Architecture
$stageRoot = Join-Path $distRoot $packageName
$zipPath = Join-Path $distRoot ($packageName + '.zip')
if ($Platform -eq 'linux') { $zipPath = Join-Path $distRoot ($packageName + '.tar.gz') }
$hashPath = $zipPath + '.sha256'
$suffix = '.exe'
if ($Platform -eq 'linux') {
    $suffix = ''
    if (-not (Get-Command tar -ErrorAction SilentlyContinue)) { throw 'tar is required to package Linux builds.' }
}

# Validate the absolute output paths before replacing previous build artifacts.
$distPrefix = [System.IO.Path]::GetFullPath($distRoot).TrimEnd('\') + '\'
foreach ($target in @($stageRoot, $zipPath, $hashPath)) {
    if (-not [System.IO.Path]::GetFullPath($target).StartsWith($distPrefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Output path escapes dist: $target"
    }
}

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
    $env:GOOS = $Platform
    $env:GOARCH = $Architecture
    $env:CGO_ENABLED = '0'
    $env:GOCACHE = Join-Path $distRoot '.gocache'

    Push-Location $repoRoot
    try {
        Write-Host "Building maatplug for $Platform/$Architecture..."
        & go build -trimpath -ldflags '-s -w' -o (Join-Path $stageRoot "maatplug$suffix") .\cmd\maatplug
        if ($LASTEXITCODE -ne 0) { throw 'Failed to build maatplug.exe.' }

        Write-Host "Building maatplug-web for $Platform/$Architecture..."
        & go build -trimpath -ldflags '-s -w' -o (Join-Path $stageRoot "maatplug-web$suffix") .\cmd\maatplug-web
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
New-Item -ItemType Directory -Force -Path (Join-Path $stageRoot '.claude-plugin') | Out-Null
Copy-Item -LiteralPath (Join-Path $repoRoot 'packaging\claude-marketplace.json') -Destination (Join-Path $stageRoot '.claude-plugin\marketplace.json')
# Both provider plugins are released together under the same package version.
$claudeManifestPath = Join-Path $stageRoot 'plugins\maatplug-claude\.claude-plugin\plugin.json'
$claudePlugin = Get-Content -Raw -LiteralPath $claudeManifestPath | ConvertFrom-Json
$claudePlugin.version = $plugin.version
$claudePlugin | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $claudeManifestPath -Encoding UTF8
if ($Platform -eq 'windows') {
    Copy-Item -LiteralPath (Join-Path $repoRoot 'packaging\README.txt') -Destination (Join-Path $stageRoot 'README.txt')
    foreach ($file in @('start-web.bat', 'install-codex.bat', 'install-claude.bat', 'install-plugin.ps1')) {
        Copy-Item -LiteralPath (Join-Path $repoRoot "packaging\$file") -Destination (Join-Path $stageRoot $file)
    }
}
else {
    # Windows checkouts can contain CRLF. Linux shell scripts must use LF, without BOM.
    foreach ($file in @('start-web.sh', 'install-codex.sh', 'install-claude.sh', 'install-plugin.sh', 'README.txt')) {
        $content = [System.IO.File]::ReadAllText((Join-Path $repoRoot "packaging\linux\$file")).Replace("`r`n", "`n")
        [System.IO.File]::WriteAllText((Join-Path $stageRoot $file), $content, (New-Object System.Text.UTF8Encoding($false)))
    }
}
Copy-Item -LiteralPath $pricingConfig -Destination (Join-Path $stageRoot 'pricing.json')

if ($Platform -eq 'windows') {
    Compress-Archive -LiteralPath $stageRoot -DestinationPath $zipPath -CompressionLevel Optimal
}
else {
    & tar -czf $zipPath -C $distRoot $packageName
    if ($LASTEXITCODE -ne 0) { throw 'Failed to create Linux archive.' }
}

# Fail the build if a future packaging change accidentally drops integration
# files that are needed after moving the ZIP to another machine.
$archive = $null
try {
    if ($Platform -eq 'windows') {
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        $archive = [System.IO.Compression.ZipFile]::OpenRead($zipPath)
        $entries = @($archive.Entries | ForEach-Object { $_.FullName.Replace('\', '/') })
    }
    else {
        $entries = @(& tar -tzf $zipPath)
        if ($LASTEXITCODE -ne 0) { throw 'Failed to inspect Linux archive.' }
    }
    $required = @(
        "$packageName/maatplug.exe",
        "$packageName/maatplug-web.exe",
        "$packageName/.agents/plugins/marketplace.json",
        "$packageName/plugins/maatplug/.codex-plugin/plugin.json",
        "$packageName/plugins/maatplug/hooks/hooks.json",
        "$packageName/README.txt",
        "$packageName/start-web.bat",
        "$packageName/install-codex.bat",
        "$packageName/install-claude.bat",
        "$packageName/install-plugin.ps1",
        "$packageName/.claude-plugin/marketplace.json",
        "$packageName/plugins/maatplug-claude/.claude-plugin/plugin.json",
        "$packageName/plugins/maatplug-claude/hooks/hooks.json",
        "$packageName/pricing.json"
    )
    if ($Platform -eq 'linux') {
        $required = @($required | Where-Object { $_ -notmatch '\.(exe|bat|ps1)$' })
        $required += @('maatplug', 'maatplug-web', 'start-web.sh', 'install-codex.sh', 'install-claude.sh', 'install-plugin.sh') | ForEach-Object { "$packageName/$_" }
    }
    foreach ($entry in $required) {
        if ($entries -notcontains $entry) {
            throw "Package validation failed: missing $entry"
        }
    }
}
finally {
    if ($null -ne $archive) { $archive.Dispose() }
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
Set-Content -LiteralPath $hashPath -Value "$hash  $([System.IO.Path]::GetFileName($zipPath))" -Encoding ASCII

Write-Host ''
Write-Host "Package: $zipPath"
Write-Host "SHA-256: $hash"
