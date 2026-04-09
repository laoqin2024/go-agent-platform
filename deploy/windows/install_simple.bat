@echo off
setlocal enabledelayedexpansion

REM 简化版安装脚本：不依赖 PowerShell，只做最核心的服务安装
REM 使用方法：
REM   install_simple.bat "http://192.168.8.168:8080/ingest" "" "win-usb-001"

if "%~1"=="" goto :usage
if "%~2"=="" goto :usage
if "%~3"=="" goto :usage

set "SERVER_ADDR=%~1"
set "TOKEN=%~2"
set "DEVICE_ID=%~3"

REM 权限检查
net session >nul 2>&1
if errorlevel 1 (
  echo [ERR] 请以“管理员身份运行”命令提示符或批处理.
  exit /b 10
)

REM 简单解析 host（用于 ping）
set "SERVER_HOST=%SERVER_ADDR%"
if /I "%SERVER_HOST:~0,7%"=="http://"  set "SERVER_HOST=%SERVER_HOST:~7%"
if /I "%SERVER_HOST:~0,8%"=="https://" set "SERVER_HOST=%SERVER_HOST:~8%"
for /f "tokens=1 delims=/" %%H in ("%SERVER_HOST%") do set "SERVER_HOST=%%H"

if "%SERVER_HOST%"=="" (
  echo [ERR] Invalid SERVER_ADDR: %SERVER_ADDR%
  exit /b 11
)

ping -n 1 "%SERVER_HOST%" >nul 2>&1
if errorlevel 1 (
  echo [ERR] 网络不通，请检查 VPN/内网连接（ping %SERVER_HOST% 失败）
  exit /b 12
)

set "BASE_DIR=C:\Program Files\GoAgent"
set "EXE=%BASE_DIR%\go-agent.exe"
set "LOG_DIR=%BASE_DIR%\logs"

if not exist "%EXE%" (
  echo [ERR] 找不到 Agent 可执行文件：%EXE%
  exit /b 2
)
if not exist "%LOG_DIR%" mkdir "%LOG_DIR%" >nul 2>&1

nssm install GoAgent "%EXE%" >nul 2>&1
nssm set GoAgent AppParameters "-api-url %SERVER_ADDR% -control-token %TOKEN% -device-id %DEVICE_ID%" >nul 2>&1
nssm set GoAgent AppStdout "%LOG_DIR%\out.log" >nul 2>&1
nssm set GoAgent AppStderr "%LOG_DIR%\err.log" >nul 2>&1
nssm set GoAgent AppExit Default Restart >nul 2>&1

net start GoAgent >nul 2>&1
if errorlevel 1 (
  echo [ERR] 无法启动服务 GoAgent，请用 services.msc 检查详情.
  exit /b 3
)

echo [OK] Service GoAgent started successfully.
exit /b 0

:usage
echo Usage: %~nx0 SERVER_ADDR TOKEN DEVICE_ID
exit /b 1