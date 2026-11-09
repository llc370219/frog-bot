@echo off
cd /d "%~dp0web"
set PATH=C:\Program Files\nodejs;%PATH%
if not exist node_modules\vite\bin\vite.js call npm install
echo Starting Frog Bot frontend dev server on http://127.0.0.1:5173 ...
start "" /b cmd /c "timeout /t 3 /nobreak >nul & start http://127.0.0.1:5173"
node node_modules\vite\bin\vite.js --host 127.0.0.1 --port 5173
echo.
echo Dev server exited.
pause