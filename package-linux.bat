@echo off
setlocal
call "%~dp0build.bat" -Platform linux %*
exit /b %errorlevel%
