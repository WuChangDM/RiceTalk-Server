#!/usr/bin/env bash
# RidgeRiceTalk 备份恢复
# 用法：sudo bash scripts/restore.sh <备份包.tar.gz> [--force]
# 行为：停服提示 → 恢复 DB/storage/配置 → 提示 migrate up 与启动。
#       默认拒绝覆盖非空 storage（--force 跳过确认）。
set -eu

ARCHIVE="${1:-}"
FORCE="${2:-}"
here="$(cd "$(dirname "$0")" && pwd)"
SERVER_DIR="$(cd "$here/.." && pwd)"
STORAGE_DIR="${RRT_STORAGE_DIR:-$SERVER_DIR/storage}"
ENV_FILE="${RRT_ENV_FILE:-$SERVER_DIR/.env.production}"

if [[ -z "$ARCHIVE" || ! -f "$ARCHIVE" ]]; then
    echo "用法: $0 <备份包.tar.gz> [--force]"
    exit 1
fi

if [[ "$FORCE" != "--force" ]]; then
    echo "[restore] 将覆盖 $STORAGE_DIR 与 $ENV_FILE。确认请加 --force 重跑。"
    exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
tar xzf "$ARCHIVE" -C "$WORK"

echo "[restore] 建议先停止服务（systemctl stop ridgericetalk），恢复后重启并执行 migrate up。"

# 1. 数据库
if [[ -f "$WORK/db/ridgericetalk.db" ]]; then
    mkdir -p "$STORAGE_DIR"
    rm -f "$STORAGE_DIR/ridgericetalk.db" "$STORAGE_DIR/ridgericetalk.db-wal" "$STORAGE_DIR/ridgericetalk.db-shm"
    cp "$WORK/db/ridgericetalk.db" "$STORAGE_DIR/ridgericetalk.db"
    echo "[restore] 数据库已恢复"
fi

# 2. storage（合并恢复：备份有的文件覆盖，备份没有的本地文件保留）
if [[ -d "$WORK/storage" ]]; then
    (cd "$WORK/storage" && tar cf - .) | (cd "$STORAGE_DIR" && tar xf -)
    echo "[restore] storage 已合并恢复"
fi

# 3. 配置
[[ -f "$WORK/config/.env.production" ]] && cp "$WORK/config/.env.production" "$ENV_FILE" && echo "[restore] .env.production 已恢复"
[[ -f "$WORK/config/livekit.yaml" ]] && cp "$WORK/config/livekit.yaml" "$STORAGE_DIR/livekit.yaml" && echo "[restore] livekit.yaml 已恢复"

echo "[restore] 完成。后续步骤："
echo "  1) 启动服务（systemctl start ridgericetalk 或 nohup 方式）"
echo "  2) 若跨版本恢复：sudo bash $SERVER_DIR/scripts/.. 或执行 ridgericetalk-migrate up"
echo "  3) 验证：curl http://127.0.0.1:<端口>/api/health"
