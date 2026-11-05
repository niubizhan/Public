@echo off
rem ---------------------------------------------------------------
rem  TVBox 4.4 LAN API - keep-alive launcher (Windows)
rem  Runs server.mjs and restarts it if it ever exits.
rem  Started hidden at logon by: "%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\tvbox-api.vbs"
rem ---------------------------------------------------------------
setlocal
cd /d "%~dp0"

rem Resolve node: PATH first, then the bundled WorkBuddy runtime.
set "NODE="
for /f "delims=" %%i in ('where node 2^>nul') do if not defined NODE set "NODE=%%i"
if not defined NODE set "NODE=C:\Users\Administrator\.workbuddy\binaries\node\versions\22.22.2-6\node.exe"
if not exist "%NODE%" (
  echo [%date% %time%] node.exe not found, abort.>> server.log
  exit /b 1
)

:loop
echo [%date% %time%] starting server.mjs >> server.log
"%NODE%" server.mjs >> server.log 2>&1
echo [%date% %time%] server exited, restart in 5s >> server.log
timeout /t 5 /nobreak >nul
goto loop
