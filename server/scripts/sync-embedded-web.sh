#!/usr/bin/env bash
# N34 遗留收口：server 仓内嵌 web/ 副本 ← 独立 web 仓 构建期对齐脚本
#
# 背景：embedded 部署的 admin 前端以部署树 web/（server 仓内嵌副本）为源码重建，
# 该副本曾长期滞后独立 web 仓（真源），所有前端修复对部署产物无效（known_issues N34）。
# 本脚本把「手工 tar 对齐」固化为可重复执行的一步。
#
# 用法：
#   bash scripts/sync-embedded-web.sh            # dry-run：仅列出差异
#   bash scripts/sync-embedded-web.sh --apply    # 实际同步
#   WEB_SRC=/path/to/web bash scripts/sync-embedded-web.sh   # 显式指定独立 web 仓
#
# 排除（永不进入内嵌副本）：.git node_modules dist voice 归档数据 归档物
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"          # .../server
EMBEDDED_WEB="$SERVER_DIR/web"                       # 内嵌副本（同步目标）

# 源：默认取独立 web 仓（开发机布局：RICETALK/{ridgericetalk/{server,web}, web}
# → server 上两级即 RICETALK 根，其下 web/ 为独立真源仓）
WEB_SRC="${WEB_SRC:-$(cd "$SERVER_DIR/../.." 2>/dev/null && pwd)/web}"
APPLY=0
[[ "${1:-}" == "--apply" ]] && APPLY=1

if [[ ! -d "$WEB_SRC/admin" || ! -d "$WEB_SRC/shared" ]]; then
    echo "[sync-web] 独立 web 仓未找到（$WEB_SRC），跳过对齐。"
    echo "[sync-web] 发布树/纯 server 树场景属预期（内嵌副本即源）；开发机请确认目录布局。"
    exit 0
fi

echo "[sync-web] 源: $WEB_SRC"
echo "[sync-web] 目标: $EMBEDDED_WEB"
echo "[sync-web] 模式: $([[ $APPLY -eq 1 ]] && echo APPLY || echo DRY-RUN)"

SYNC_DIRS=(admin shared)
EXCLUDES=(--exclude '.git/' --exclude 'node_modules/' --exclude 'dist/' --exclude 'voice/' --exclude '归档数据/' --exclude '*.log')

changed=0
for d in "${SYNC_DIRS[@]}"; do
    src="$WEB_SRC/$d"
    dst="$EMBEDDED_WEB/$d"
    if [[ ! -d "$src" ]]; then
        echo "[sync-web] ⚠ 源缺 $d，跳过"
        continue
    fi
    diff_out=$(diff -rq --exclude='.git' --exclude='node_modules' --exclude='dist' --exclude='voice' --exclude='归档数据' --exclude='*.log' --exclude='test-results' "$src" "$dst" 2>/dev/null || true)
    if [[ -n "$diff_out" ]]; then
        changed=1
        echo "[sync-web] ── $d 差异 ──"
        echo "$diff_out" | head -20
        [[ $(echo "$diff_out" | wc -l) -gt 20 ]] && echo "  ...（更多差异省略）"
        if [[ $APPLY -eq 1 ]]; then
            rm -rf "$dst"
            mkdir -p "$dst"
            # rsync 不可用时用 tar 管道镜像（含排除；Git Bash/无 rsync 环境兼容）
            tar -C "$src" --exclude='.git' --exclude='node_modules' --exclude='dist' --exclude='voice' --exclude='归档数据' --exclude='test-results' --exclude='*.log' -cf - . | tar -C "$dst" -xf -
            echo "[sync-web] ✅ $d 已同步"
        fi
    else
        echo "[sync-web] ✓ $d 无差异"
    fi
done

if [[ $APPLY -eq 0 ]]; then
    if [[ $changed -eq 1 ]]; then
        echo "[sync-web] 存在差异。执行「bash scripts/sync-embedded-web.sh --apply」同步。"
        exit 1   # 供 CI/部署链感知滞后
    fi
    echo "[sync-web] 内嵌副本与真源一致。"
else
    echo "[sync-web] 对齐完成。建议重跑 admin 构建（deploy-embedded 会以新源重建）。"
fi
