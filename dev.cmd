@echo off
rem 前端开发服务：http://127.0.0.1:5173 ，改代码后页面自动刷新。
rem /api 会转发到已经在运行的后端 http://127.0.0.1:8080 。
cd /d "%~dp0web"
if not exist node_modules\.bin\vite.cmd call npm install
start "" http://127.0.0.1:5173
call npx vite --host 127.0.0.1 --port 5173 --strictPort
pause
