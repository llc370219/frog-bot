@echo off
rem 前端开发服务：http://127.0.0.1:5173 ，改代码后页面自动刷新。
rem /api 会转发到已经在运行的后端 http://127.0.0.1:8080 。
cd /d "%~dp0web"
if not exist node_modules\.bin\vite.cmd call npm install
rem 等服务起来再打开浏览器，避免一打开就显示“拒绝连接”
start "" /b cmd /c "timeout /t 5 /nobreak >nul & start http://127.0.0.1:5173"
call node_modules\.bin\vite.cmd --host 127.0.0.1 --port 5173 --strictPort
echo.
echo 开发服务已退出，上面是原因。
pause
