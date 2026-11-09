@echo off
rem Frog bot dev server: http://127.0.0.1:5173 (auto reload). /api -> http://127.0.0.1:8080
cd /d "%~dp0web"
if not exist node_modules\.bin\vite.cmd call npm install
start "" /b cmd /c "timeout /t 5 /nobreak >nul & start http://127.0.0.1:5173"
call node_modules\.bin\vite.cmd --host 127.0.0.1 --port 5173 --strictPort
echo.
echo Dev server stopped. See the message above.
pause