@echo off
setlocal
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0install-plugin.ps1" -Provider claude
set "INSTALL_RESULT=%errorlevel%"
if not "%~1"=="--no-pause" pause
exit /b %INSTALL_RESULT%
