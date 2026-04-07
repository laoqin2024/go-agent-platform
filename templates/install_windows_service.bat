@echo off
setlocal enabledelayedexpansion

REM Usage:
REM   install_windows_service.bat SERVER_ADDR TOKEN DEVICE_ID
REM Example:
REM   install_windows_service.bat http://10.0.0.2:8080/ingest mytoken dev-001
REM
REM Note:
REM   Current GoAgent uses "-api-url" as ingest flag. We keep parameter name SERVER_ADDR
REM   but pass it to "-api-url" to avoid unknown-flag startup failures.

if "%~1"=="" goto :usage
if "%~2"=="" goto :usage
if "%~3"=="" goto :usage

set "SERVER_ADDR=%~1"
set "TOKEN=%~2"
set "DEVICE_ID=%~3"
set "ERROR_LOG=%CD%\install_error.txt"

REM Reset previous error log for this run
if exist "%ERROR_LOG%" del /f /q "%ERROR_LOG%" >nul 2>&1

REM Must run as Administrator
net session >nul 2>&1
if errorlevel 1 (
  powershell -NoProfile -Command "Add-Type -AssemblyName PresentationFramework; [System.Windows.MessageBox]::Show('请右键点击并选择“以管理员身份运行”','GoAgent 安装',0,48) | Out-Null" >nul 2>&1
  call :fail 10 "Access is denied: script must run as Administrator."
)

REM Pre-flight network check: extract host from SERVER_ADDR and ping once
set "SERVER_HOST="
for /f "usebackq delims=" %%H in (`powershell -NoProfile -Command "$u='%SERVER_ADDR%'; try { ([uri]$u).Host } catch { '' }"`) do set "SERVER_HOST=%%H"
if "%SERVER_HOST%"=="" (
  call :fail 11 "Invalid SERVER_ADDR: %SERVER_ADDR%"
)
ping -n 1 "%SERVER_HOST%" >nul 2>&1
if errorlevel 1 (
  echo [ERR] 检测到网络不通，请检查 VPN 或内网连接
  call :fail 12 "Network precheck failed: ping %SERVER_HOST% failed."
)

set "BASE_DIR=C:\Program Files\GoAgent"
set "EXE=%BASE_DIR%\go-agent.exe"
set "LOG_DIR=%BASE_DIR%\logs"

if not exist "%EXE%" (
  call :fail 2 "Agent executable not found: %EXE%"
)

if not exist "%LOG_DIR%" (
  mkdir "%LOG_DIR%" >nul 2>&1
)

REM Ensure service exists and configure it via NSSM
REM (requires nssm.exe to be in PATH or current directory)
nssm install GoAgent "%EXE%" >nul 2>&1
if errorlevel 1 (
  REM Service may already exist; continue to set/update config.
)

REM Map SERVER_ADDR -> -api-url
nssm set GoAgent AppParameters "-api-url %SERVER_ADDR% -control-token %TOKEN% -device-id %DEVICE_ID%" >nul 2>&1
if errorlevel 1 call :fail 20 "nssm set AppParameters failed."
nssm set GoAgent AppStdout "%LOG_DIR%\out.log" >nul 2>&1
if errorlevel 1 call :fail 21 "nssm set AppStdout failed."
nssm set GoAgent AppStderr "%LOG_DIR%\err.log" >nul 2>&1
if errorlevel 1 call :fail 22 "nssm set AppStderr failed."
nssm set GoAgent AppExit Default Restart >nul 2>&1
if errorlevel 1 call :fail 23 "nssm set AppExit failed."

REM Start (or ensure running)
net start GoAgent >nul 2>&1
if errorlevel 1 (
  REM If already running, net start returns an error; treat it as OK.
  for /f "tokens=*" %%s in ('sc query GoAgent ^| findstr /I "STATE"') do set "STATE=%%s"
  echo !STATE! | findstr /I "RUNNING" >nul 2>&1
  if errorlevel 1 (
    call :fail 3 "Failed to start service GoAgent."
  )
)

echo [OK] GoAgent service installed/configured and running.
exit /b 0

:usage
echo Usage: %~nx0 SERVER_ADDR TOKEN DEVICE_ID
exit /b 1

:fail
set "ERR_CODE=%~1"
set "ERR_MSG=%~2"
(
  echo [%date% %time%] install failed
  echo code: %ERR_CODE%
  echo message: %ERR_MSG%
  echo server_addr: %SERVER_ADDR%
  echo device_id: %DEVICE_ID%
) > "%ERROR_LOG%"
echo [ERR] %ERR_MSG%
echo [ERR] 详情已写入 "%ERROR_LOG%"
exit /b %ERR_CODE%
