@echo off
setlocal
cd /d "%~dp0"
if not exist "%~dp0maatplug-web.exe" (
    echo maatplug-web.exe was not found. Extract the entire ZIP before starting.
    exit /b 1
)
if not exist "%~dp0maatplug.exe" (
    echo maatplug.exe was not found. Extract the entire ZIP before starting.
    exit /b 1
)
set "PATH=%~dp0;%PATH%"
echo Open http://127.0.0.1:4173 in your browser.
echo Press Ctrl+C to stop.
"%~dp0maatplug-web.exe" --addr 127.0.0.1:4173 %*
exit /b %errorlevel%
