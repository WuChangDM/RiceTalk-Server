#!/usr/bin/env bash
# RidgeRiceTalk 裸机部署卸载脚本
set -euo pipefail

# 颜色
C_RESET='\033[0m'; C_RED='\033[91m'; C_GREEN='\033[92m'
C_YELLOW='\033[93m'; C_CYAN='\033[96m'; C_MAGENTA='\033[95m'
ok()   { echo -e "  ${C_GREEN}[OK]${C_RESET} $1"; }
warn() { echo -e "  ${C_YELLOW}[WARN]${C_RESET} $1"; }
info() { echo -e "  ${C_CYAN}[INFO]${C_RESET} $1"; }
banner() {
    echo -e "\n${C_MAGENTA}========================================${C_RESET}"
    echo -e "${C_MAGENTA}  $1${C_RESET}"
    echo -e "${C_MAGENTA}========================================${C_RESET}"
}

DEPLOY_DIR="/opt/ridgericetalk"

banner "RidgeRiceTalk 卸载"

# 1. 停止并禁用服务
for svc in livekit ridgericetalk; do
    if systemctl is-active --quiet $svc 2>/dev/null; then
        systemctl stop $svc
        ok "已停止 $svc.service"
    fi
    if systemctl is-enabled --quiet $svc 2>/dev/null; then
        systemctl disable $svc
        ok "已禁用 $svc.service"
    fi
done

# 2. 删除 systemd 单元文件
for svc in ridgericetalk livekit; do
    if [ -f "/etc/systemd/system/$svc.service" ]; then
        rm -f /etc/systemd/system/$svc.service
        ok "已删除 /etc/systemd/system/$svc.service"
    fi
done
systemctl daemon-reload

# 3. 询问是否删除数据库
echo ""
read -p "是否删除数据库 ridgericetalk 和用户 ridgericetalk？[y/N] " DELETE_DB
if [[ "$DELETE_DB" =~ ^[Yy]$ ]]; then
    su - postgres -c "dropdb --if-exists ridgericetalk"
    su - postgres -c "psql -c \"DROP USER IF EXISTS ridgericetalk;\""
    ok "已删除数据库和用户"
else
    info "保留数据库（默认）"
fi

# 4. 询问是否删除 storage
echo ""
read -p "是否删除 $DEPLOY_DIR（含 storage/secrets.json）？[y/N] " DELETE_FILES
if [[ "$DELETE_FILES" =~ ^[Yy]$ ]]; then
    rm -rf $DEPLOY_DIR
    ok "已删除 $DEPLOY_DIR"
else
    info "保留 $DEPLOY_DIR（默认）"
    info "如需手动删除：rm -rf $DEPLOY_DIR"
fi

# 5. 询问是否删除系统用户
if id ridgericetalk &>/dev/null; then
    echo ""
    read -p "是否删除系统用户 ridgericetalk？[y/N] " DELETE_USER
    if [[ "$DELETE_USER" =~ ^[Yy]$ ]]; then
        userdel ridgericetalk
        ok "已删除系统用户 ridgericetalk"
    else
        info "保留系统用户（默认）"
    fi
fi

# 6. 询问是否卸载依赖
echo ""
echo "系统依赖（postgresql/ffmpeg/go/node）未卸载，如需卸载请手动执行："
echo "  apt-get remove --purge postgresql ffmpeg golang-go nodejs"
echo "  rm -rf /usr/local/go"

banner "卸载完成"
echo ""
info "RidgeRiceTalk 已从本机移除"
