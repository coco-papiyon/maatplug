@echo off
setlocal

cd /d "%~dp0"

echo Installing maatplug...
go install ./cmd/maatplug
if errorlevel 1 (
    echo Failed to install maatplug.
    exit /b 1
)

echo Fetching provider pricing data...
go run .\cmd\maatplug-pricing-fetch
if errorlevel 1 (
    echo Failed to fetch provider pricing data.
    exit /b 1
)

rem go install places maatplug.exe under GOBIN (or GOPATH\bin if GOBIN is
rem unset), disconnected from this repo directory. Copy pricing.json next to
rem it so a maatplug binary invoked from anywhere can find it alongside
rem itself, the same way the packaged zip ships pricing.json next to
rem maatplug.exe (scripts\package.ps1).
for /f "delims=" %%G in ('go env GOBIN') do set "MAATPLUG_GOBIN=%%G"
if "%MAATPLUG_GOBIN%"=="" (
    for /f "delims=" %%G in ('go env GOPATH') do set "MAATPLUG_GOBIN=%%G\bin"
)
copy /Y "internal\pricing\pricing.json" "%MAATPLUG_GOBIN%\pricing.json" >nul
if errorlevel 1 (
    echo Failed to copy pricing.json to %MAATPLUG_GOBIN%.
    exit /b 1
)

echo Starting maatplug-web...
go run .\cmd\maatplug-web
if errorlevel 1 (
    echo Failed to start maatplug-web.
    exit /b 1
)

endlocal
