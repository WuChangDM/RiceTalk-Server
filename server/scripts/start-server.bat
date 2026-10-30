@echo off
rem ============================================================
rem  RidgeRiceTalk Windows server - one-click deploy entry
rem  Usage: right-click this file -> "Run as administrator"
rem  Extra args are passed through to scripts\deploy-windows.ps1:
rem      start-server.bat -NoFirewall -NoBrowser
rem      start-server.bat -PortApi 18080 -PortAdmin 19090
rem ============================================================
chcp 65001 >nul
setlocal EnableExtensions
cd /d "%~dp0"

set "PORT_API=8080"
set "PORT_ADMIN=9090"
set "PORT_LK_WS=7880"
set "PORT_LK_TCP=7881"
set "PORT_LK_UDP=7882"

rem Public address: first non-loopback IPv4 by default; override by setting PUBLIC_ADDRESS
if not defined PUBLIC_ADDRESS (
    for /f "usebackq delims=" %%i in (`powershell -NoProfile -Command "(Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue ^| Where-Object { $_.IPAddress -notlike '127.*' -and $_.PrefixOrigin -ne 'WellKnown' } ^| Select-Object -First 1).IPAddress"`) do set "LAN_IP=%%i"
    if defined LAN_IP (
        set "PUBLIC_ADDRESS=http://%LAN_IP%:%PORT_API%"
    ) else (
        set "PUBLIC_ADDRESS=http://127.0.0.1:%PORT_API%"
    )
)

net session >nul 2>&1
if errorlevel 1 (
    echo [INFO] Administrator privileges required - requesting elevation...
    powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
    exit /b
)

echo.
echo  RidgeRiceTalk server deployment
echo    Bundle dir     : %~dp0
echo    Public address : %PUBLIC_ADDRESS%
echo    Ports          : API=%PORT_API% Admin=%PORT_ADMIN% LiveKitWS=%PORT_LK_WS% TCP=%PORT_LK_TCP% UDP=%PORT_LK_UDP%
echo.

powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\deploy-windows.ps1" -FromBundle "%~dp0." -PublicAddress "%PUBLIC_ADDRESS%" -PortApi %PORT_API% -PortAdmin %PORT_ADMIN% -PortLkWs %PORT_LK_WS% -PortLkTcp %PORT_LK_TCP% -PortLkUdp %PORT_LK_UDP% %*
set "RC=%ERRORLEVEL%"

echo.
if "%RC%"=="0" (
    echo [OK] Deployment finished. Desktop console shortcut is on your Desktop.
) else (
    echo [FAIL] Deployment failed ^(exit %RC%^). Please send the output above to the maintainer.
)
echo.
pause
exit /b %RC%
