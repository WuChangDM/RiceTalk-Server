#!/usr/bin/env bash
# RidgeRiceTalk 开发启动脚本（自动清理 + 自动构建）

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

# 自动检测平台后缀
if [[ "$OSTYPE" == msys* || "$OSTYPE" == cygwin* || "$OSTYPE" == win32* ]]; then
    BIN_SUFFIX=".exe"
else
    BIN_SUFFIX=""
fi

# 1. 先清理旧进程
echo "[dev] Cleaning old processes..."
"$SCRIPT_DIR/kill-server.sh"

# 2. 自动构建
echo "[dev] Building..."
cd "$PROJECT_DIR"
go build -o "build/ridgericetalk-dev${BIN_SUFFIX}" ./cmd/server

# 3. 启动服务
echo "[dev] Starting RidgeRiceTalk server..."

# 默认配置（可在环境变量中覆盖）
export RRT_PORT="${RRT_PORT:-8080}"
export RRT_DATABASE_URL="${RRT_DATABASE_URL:-./storage/ridgericetalk.db}"
export RRT_JWT_SECRET="${RRT_JWT_SECRET:-ridgericetalk-secret-key}"
export RRT_CSRF_TOKEN_SECRET="${RRT_CSRF_TOKEN_SECRET:-ridgericetalk-csrf-secret}"

echo "[dev] Port: $RRT_PORT"
echo "[dev] DB: $RRT_DATABASE_URL"

exec "$PROJECT_DIR/build/ridgericetalk-dev${BIN_SUFFIX}"
