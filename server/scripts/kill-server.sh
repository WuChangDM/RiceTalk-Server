#!/usr/bin/env bash
# 终止 RidgeRiceTalk 服务进程（跨平台）
#
# 支持：macOS、Linux、Windows Git Bash / MSYS2

set -e

echo "[cleanup] Stopping all RidgeRiceTalk processes..."

OS="$(uname -s)"

if [[ "$OS" == MINGW* || "$OS" == MSYS* || "$OS" == CYGWIN* || "$OS" == Windows_NT ]]; then
    # Windows 环境（Git Bash / MSYS2 / Cygwin）
    powershell -Command "Get-Process | Where-Object { \$_.ProcessName -like 'ridgericetalk*' } | Stop-Process -Force -ErrorAction SilentlyContinue" 2>/dev/null || true
    taskkill /F /IM ridgericetalk.exe 2>/dev/null || true
    taskkill /F /IM ridgericetalk2.exe 2>/dev/null || true
    taskkill /F /IM ridgericetalk3.exe 2>/dev/null || true
    taskkill /F /IM ridgericetalk4.exe 2>/dev/null || true
    taskkill /F /IM ridgericetalk5.exe 2>/dev/null || true
    taskkill /F /IM ridgericetalk6.exe 2>/dev/null || true
    taskkill /F /IM ridgericetalk7.exe 2>/dev/null || true

    sleep 2

    REMAINING=$(tasklist 2>/dev/null | grep -i ridgericetalk | wc -l || echo "0")
    if [ "$REMAINING" -gt 0 ]; then
        echo "[cleanup] WARNING: $REMAINING process(es) still running, trying wmic..."
        wmic process where "name like 'ridgericetalk%'" delete 2>/dev/null || true
        sleep 2
    fi
else
    # macOS / Linux
    if command -v pkill &>/dev/null; then
        pkill -f "ridgericetalk" 2>/dev/null || true
    elif command -v killall &>/dev/null; then
        killall -9 ridgericetalk 2>/dev/null || true
    else
        echo "[cleanup] WARNING: neither pkill nor killall found; please stop processes manually"
        exit 0
    fi

    sleep 2

    REMAINING=$(pgrep -f "ridgericetalk" 2>/dev/null | wc -l || echo "0")
    if [ "$REMAINING" -gt 0 ]; then
        echo "[cleanup] WARNING: $REMAINING process(es) still running, sending SIGKILL..."
        pkill -9 -f "ridgericetalk" 2>/dev/null || true
        sleep 1
    fi
fi

echo "[cleanup] Done. RidgeRiceTalk processes cleaned."
