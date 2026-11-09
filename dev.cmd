@echo off
setlocal
set "PATH=C:\Program Files\nodejs;%PATH%"

where node.exe >nul 2>&1
if errorlevel 1 (
  echo Node.js was not found. Install Node.js and add it to PATH.
  goto :failed
)
where npm.cmd >nul 2>&1
if errorlevel 1 (
  echo npm was not found. Check your Node.js installation.
  goto :failed
)

where go.exe >nul 2>&1
if errorlevel 1 if exist "%~dp0..\.beta-tools\go\bin\go.exe" set "PATH=%~dp0..\.beta-tools\go\bin;%PATH%"
where go.exe >nul 2>&1
if errorlevel 1 (
  echo Go was not found. Install Go 1.22 or newer and add it to PATH.
  goto :failed
)

cd /d "%~dp0web"
if errorlevel 1 goto :failed
if not exist node_modules\vite\bin\vite.js (
  echo Installing frontend dependencies...
  call npm install
  if errorlevel 1 goto :failed
)
echo Building Frog Bot frontend...
call npm run build
if errorlevel 1 goto :failed

cd /d "%~dp0"
if errorlevel 1 goto :failed
echo Starting Frog Bot. Open the URL shown below if the browser does not open.
go run ./cmd/frogbot
if errorlevel 1 goto :failed
exit /b 0

:failed
echo.
echo Frog Bot could not start. See the error above.
if not defined CI pause
exit /b 1
