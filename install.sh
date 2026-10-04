#!/usr/bin/env bash
# RidgeRiceTalk 一键引导安装脚本（Linux/macOS）
# 探测基础工具 → 克隆仓库 → 转交 server/scripts/deploy-embedded.sh 完成部署
# ---------------------------------------------------------------
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/WuChangDM/RiceTalk-Server/main/install.sh | bash
#   bash install.sh                          # 自动探测公网 IP
#   sudo bash install.sh --public-address http://YOUR_IP:8080

set -euo pipefail

REPO_URL="${RRT_REPO_URL:-https://github.com/WuChangDM/RiceTalk-Server.git}"
CLONE_DIR="ridgericetalk-src"

# 1. 基础工具探测（缺则给发行版安装提示，直接中止）
for tool in git tar; do
    if ! command -v "$tool" &>/dev/null; then
        echo "[FAIL] 缺少 $tool，请先安装：Debian/Ubuntu 'sudo apt install $tool'；RHEL/CentOS 'sudo yum install $tool'" >&2
        exit 1
    fi
done

# 2. 非 root 提醒（默认部署目录 /opt/ridgericetalk 需要 root 写权限）
if [[ "$(id -u)" != "0" ]]; then
    echo "[WARN] 当前非 root：建议 sudo 运行以写入 /opt/ridgericetalk，或用 --deploy-dir 指定可写目录"
fi

# 3. 防嵌套：当前目录已是本仓库（存在 server/scripts/deploy-embedded.sh）则直接转交
if [[ -f "server/scripts/deploy-embedded.sh" ]]; then
    echo "[INFO] 当前目录已是 RidgeRiceTalk 仓库，跳过克隆，直接执行部署脚本"
    exec bash server/scripts/deploy-embedded.sh "$@"
fi

# 4. 克隆（浅克隆只取最新提交）→ 进入部署脚本目录 → 参数全部透传
rm -rf "$CLONE_DIR"
git clone --depth 1 "$REPO_URL" "$CLONE_DIR"
cd "$CLONE_DIR/server/scripts"
exec bash deploy-embedded.sh "$@"
