#!/usr/bin/env bash
# RidgeRiceTalk 一键备份（embedded/baremetal 通用）
# 备份内容：数据库（含 WAL 归并副本）、storage/（上传/音频/密钥）、.env.production、livekit.yaml
# 用法：sudo bash scripts/backup.sh [输出目录]（默认 ./backups）
# 恢复：sudo bash scripts/restore.sh <备份包>
set -eu

here="$(cd "$(dirname "$0")" && pwd)"
SERVER_DIR="$(cd "$here/.." && pwd)"
STORAGE_DIR="${RRT_STORAGE_DIR:-$SERVER_DIR/storage}"
ENV_FILE="${RRT_ENV_FILE:-$SERVER_DIR/.env.production}"
DB_FILE="${RRT_DATABASE_FILE:-$STORAGE_DIR/ridgericetalk.db}"
OUT_DIR="${1:-$SERVER_DIR/backups}"
STAMP="$(date +%Y%m%d-%H%M%S)"
ARCHIVE="$OUT_DIR/ridgericetalk-backup-$STAMP.tar.gz"

mkdir -p "$OUT_DIR"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "[backup] 收集 $STAMP"

# 1. 数据库：sqlite3 .backup 在线归并（含 WAL），避免拷贝热文件不一致
mkdir -p "$WORK/db"
if command -v sqlite3 >/dev/null 2>&1; then
    sqlite3 "$DB_FILE" ".backup '$WORK/db/ridgericetalk.db'"
    echo "[backup] 数据库在线归并完成"
else
    # 无 sqlite3 时退化为直接拷贝（WAL 未 checkpoint 时可能缺最新写入，恢复后建议先起一次服务再压库）
    cp "$DB_FILE" "$WORK/db/ridgericetalk.db"
    [[ -f "$DB_FILE-wal" ]] && cp "$DB_FILE-wal" "$WORK/db/" || true
    [[ -f "$DB_FILE-shm" ]] && cp "$DB_FILE-shm" "$WORK/db/" || true
    echo "[backup] 警告：未安装 sqlite3，使用直接拷贝（建议 apt install sqlite3 后重备）"
fi

# 2. storage：排除 bot-tmp 转码临时物与 livekit.yaml（单独入包）
mkdir -p "$WORK/storage"
if command -v tar >/dev/null 2>&1; then
    tar cf - -C "$STORAGE_DIR" --exclude='./audio/bot-tmp' --exclude='./livekit.yaml' . | tar xf - -C "$WORK/storage"
fi

# 3. 配置
mkdir -p "$WORK/config"
[[ -f "$ENV_FILE" ]] && cp "$ENV_FILE" "$WORK/config/" || true
[[ -f "$STORAGE_DIR/livekit.yaml" ]] && cp "$STORAGE_DIR/livekit.yaml" "$WORK/config/" || true

# 4. 清单与打包
find "$WORK" -type f | sed "s|$WORK/||" > "$WORK/MANIFEST.txt"
tar czf "$ARCHIVE" -C "$WORK" .
chmod 600 "$ARCHIVE"

echo "[backup] 完成: $ARCHIVE ($(du -h "$ARCHIVE" | cut -f1))"
echo "[backup] 恢复方法: sudo bash $here/restore.sh $ARCHIVE"
