@echo off
setlocal

cd /d "%~dp0"

where powershell.exe >nul 2>&1
if errorlevel 1 (
    echo PowerShell was not found.
    exit /b 1
)

echo Refreshing provider pricing data...
go run .\cmd\maatplug-pricing-fetch
if errorlevel 1 (
    echo Failed to refresh provider pricing data.
    exit /b 1
)

powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\package.ps1" %*
if errorlevel 1 (
    echo Build or packaging failed.
    exit /b 1
)

echo Build and packaging completed successfully.
endlocal
