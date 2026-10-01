@echo off
chcp 65001 >nul
:: RidgeRiceTalk Server — Windows One-Click Launcher
:: Double-click this file to start the server
:: The command window will stay open; closing it stops the server.

title RidgeRiceTalk Server

echo ========================================
echo   RidgeRiceTalk Server Starterecho ========================================
echo.

:: Check if PowerShell is available
powershell -Command "Get-Host" >nul 2>&1
if %errorlevel% neq 0 (
    echo [ERROR] PowerShell is required but not found.
    pause
    exit /b 1
)

:: Run the main start script
cd /d "%~dp0"
powershell -ExecutionPolicy Bypass -File "scripts\start-server.ps1"

:: If the script exits (e.g., error or Ctrl+C), pause so user can see the message
if %errorlevel% neq 0 (
    echo.
    echo Server exited with error code %errorlevel%.
    pause
)
