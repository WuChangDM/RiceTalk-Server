#!/usr/bin/env bash
# 将本地 ridgericetalk 代码安全同步到远程服务器
# Usage: ./sync-to-server.sh [OPTIONS] user@host[:/path/to/deploy]
#
# 安全排除规则：
#   - 排除运行时目录 .git/、node_modules/、third_party/*、models/*、storage/*、logs/*
#   - 精确排除 ./server/storage/* 和 ./server/logs/*（运行时数据），但保留 server/internal/storage/ Go 包
#   - 支持 --dry-run 预览同步内容

set -euo pipefail

# ============================================================
# 默认值
# ============================================================
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
DEFAULT_REMOTE_PATH="/opt/ridgericetalk"
DRY_RUN=false

# ============================================================
# 帮助信息
# ============================================================
show_help() {
    cat <<'EOF'
Usage: sync-to-server.sh [OPTIONS] <user@host>[:/path/to/deploy]

将本地 RidgeRiceTalk 源码安全同步到远程服务器部署目录。

Options:
  -h, --help     显示此帮助
  -n, --dry-run  模拟运行，输出将要同步的文件但不实际传输

Examples:
  ./sync-to-server.sh root@192.168.31.187
  ./sync-to-server.sh root@192.168.31.187:/opt/ridgericetalk
  ./sync-to-server.sh --dry-run root@192.168.31.187
EOF
}

# ============================================================
# 参数解析
# ============================================================
if [[ $# -lt 1 ]]; then
    show_help
    exit 1
fi

while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help) show_help; exit 0 ;;
        -n|--dry-run) DRY_RUN=true; shift ;;
        *) break ;;
    esac
done

DEST="${1:-}"
if [[ -z "$DEST" ]]; then
    show_help
    exit 1
fi

HOST="${DEST%%:*}"
REMOTE_PATH="${DEST#*:}"
if [[ "$HOST" == "$REMOTE_PATH" ]]; then
    REMOTE_PATH="$DEFAULT_REMOTE_PATH"
fi

# ============================================================
# 前置检查
# ============================================================
if [[ ! -d "$PROJECT_DIR/server" ]]; then
    echo "[FAIL] 本地源码目录不存在: $PROJECT_DIR/server" >&2
    exit 1
fi

echo "[INFO] 本地源码: $PROJECT_DIR"
echo "[INFO] 远程目标: $HOST:$REMOTE_PATH"
if [[ "$DRY_RUN" == "true" ]]; then
    echo "[INFO] 模拟运行（--dry-run），不会实际修改远程文件"
fi

# ============================================================
# 构建 tar 排除规则
# ============================================================
TAR_EXCLUDES=(
    '.git'
    'node_modules'
    './third_party/*'
    './models/*'
    './storage/*'
    './logs/*'
    './server/storage/*'
    './server/logs/*'
    './server/webhost/dist/*'
    './server/webhost/admin/*'
    './server/ridgericetalk'
    './server/ridgericetalk-migrate'
    './server/*.exe'
    './services/netease-api/node_modules/*'
    '.env'
    '.env.*'
)

EXCLUDE_ARGS=()
for item in "${TAR_EXCLUDES[@]}"; do
    EXCLUDE_ARGS+=("--exclude=$item")
done

# ============================================================
# 执行同步
# ============================================================
cd "$PROJECT_DIR"

if [[ "$DRY_RUN" == "true" ]]; then
    # 模拟运行：列出将要传输的文件，并在最后高亮显示 server/internal/storage 是否被保留
    echo ""
    echo "[INFO] 将要同步的文件列表（压缩预览）:"
    tar -czf /dev/null "${EXCLUDE_ARGS[@]}" -v . 2>&1 | grep -E '^./' || true
    echo ""
    if tar -czf /dev/null "${EXCLUDE_ARGS[@]}" -v . 2>&1 | grep -q 'server/internal/storage'; then
        echo "[OK] server/internal/storage/ Go 包将被保留"
    else
        echo "[WARN] server/internal/storage/ Go 包似乎被排除，请检查排除规则"
    fi
else
    tar -czf - "${EXCLUDE_ARGS[@]}" . | ssh "$HOST" "bash -c 'mkdir -p \"$REMOTE_PATH\" && cd \"$REMOTE_PATH\" && sudo tar -xzf - && sudo chown -R ridgericetalk:ridgericetalk .'"
    echo "[OK] 已同步到 $HOST:$REMOTE_PATH"
fi
