#!/usr/bin/env bash
# RidgeRiceTalk 局域网 HTTPS 测试脚本
# 生成自签名证书 + Nginx 反代，使浏览器可在 HTTPS 下完成 getUserMedia / WebRTC 测试
# Usage: sudo ./setup-lan-https.sh [OPTIONS]

set -euo pipefail

# ============================================================
# 颜色输出
# ============================================================
C_RESET='\033[0m'
C_RED='\033[91m'
C_GREEN='\033[92m'
C_YELLOW='\033[93m'
C_CYAN='\033[96m'
C_WHITE='\033[97m'
C_GRAY='\033[90m'
C_BLUE='\033[94m'
C_MAGENTA='\033[95m'

ok()     { echo -e "  ${C_GREEN}[OK]${C_RESET} $1"; }
warn()   { echo -e "  ${C_YELLOW}[WARN]${C_RESET} $1"; }
fail()   { echo -e "  ${C_RED}[FAIL]${C_RESET} $1"; }
info()   { echo -e "  ${C_GRAY}[INFO]${C_RESET} $1"; }
stage()  { echo -e "\n${C_BLUE}[${1}]${C_RESET} ${C_CYAN}${2}${C_RESET}"; }
banner() {
    echo -e "\n${C_MAGENTA}========================================${C_RESET}"
    echo -e "${C_MAGENTA}  $1${C_RESET}"
    echo -e "${C_MAGENTA}========================================${C_RESET}"
}

# ============================================================
# 默认值
# ============================================================
DEPLOY_DIR="/opt/ridgericetalk"
SERVER_DIR="$DEPLOY_DIR/server"
ENV_FILE="$SERVER_DIR/.env.production"
LAN_IP=""
DOMAIN=""
HTTPS_PORT=443
CERT_DAYS=365
FORCE=false
CERT_COPY_DEST=""

# ============================================================
# 帮助信息
# ============================================================
show_help() {
    cat <<'EOF'
Usage: setup-lan-https.sh [OPTIONS]

RidgeRiceTalk 局域网 HTTPS 测试脚本

Options:
  --help                  显示此帮助
  --lan-ip <ip>           局域网 IP（默认自动检测第一个非本地 IPv4）
  --domain <fqdn>         额外写入证书 SAN 与 Nginx server_name 的域名（可选，
                          如 voice.example.com；deploy-baremetal.sh --tls selfsigned
                          的 --domain 会透传到这里）
  --https-port <port>     HTTPS 端口（默认 443）
  --deploy-dir <path>     部署目录（默认 /opt/ridgericetalk）
  --copy-cert-to <path>   将生成的证书复制到指定目录，便于分发到客户端
  --force                 强制覆盖已有 Nginx 配置和证书

Examples:
  sudo ./setup-lan-https.sh
  sudo ./setup-lan-https.sh --lan-ip 192.168.31.187 --https-port 443
  sudo ./setup-lan-https.sh --lan-ip 192.168.31.187 --copy-cert-to /tmp/cert
EOF
}

# ============================================================
# 参数解析
# ============================================================
while [[ $# -gt 0 ]]; do
    case "$1" in
        --help|-h) show_help; exit 0 ;;
        --lan-ip) LAN_IP="$2"; shift 2 ;;
        --domain) DOMAIN="$2"; shift 2 ;;
        --https-port) HTTPS_PORT="$2"; shift 2 ;;
        --deploy-dir) DEPLOY_DIR="$2"; shift 2 ;;
        --copy-cert-to) CERT_COPY_DEST="$2"; shift 2 ;;
        --force) FORCE=true; shift ;;
        *)
            fail "未知参数: $1（使用 --help 查看用法）"
            exit 2
            ;;
    esac
done

# ============================================================
# root 检查
# ============================================================
if [[ $EUID -ne 0 ]]; then
    fail "此脚本必须以 root 身份运行"
    exit 1
fi
ok "以 root 身份运行"

# ============================================================
# 读取现有配置
# ============================================================
if [[ ! -f "$ENV_FILE" ]]; then
    fail "未找到配置文件: $ENV_FILE"
    info "请先运行 deploy-baremetal.sh 完成基础部署"
    exit 1
fi

read_env() {
    local key="$1"
    local default="${2:-}"
    local value
    value=$(grep -E "^${key}=" "$ENV_FILE" 2>/dev/null | head -1 | cut -d'=' -f2- | tr -d '[:space:]' || true)
    echo "${value:-$default}"
}

PORT_API=$(read_env "RRT_PORT" "8080")
PORT_ADMIN=$(read_env "RRT_ADMIN_PORT" "9090")
PORT_LK_WS=$(read_env "RRT_LIVEKIT_PORT" "7880")

# 如果 RRT_LIVEKIT_URL 是 ws://localhost:PORT 形式，提取端口
LK_URL=$(read_env "RRT_LIVEKIT_URL" "ws://localhost:$PORT_LK_WS")
if [[ "$LK_URL" =~ ^wss?://localhost:([0-9]+) ]]; then
    PORT_LK_WS="${BASH_REMATCH[1]}"
fi

info "读取现有端口配置: API=$PORT_API Admin=$PORT_ADMIN LiveKit=$PORT_LK_WS"

# ============================================================
# 检测局域网 IP
# ============================================================
if [[ -z "$LAN_IP" ]]; then
    info "自动检测局域网 IP..."
    LAN_IP=$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -v '^$' | grep -v '^127\.' | grep -v '^169\.254\.' | head -1 || true)
    if [[ -z "$LAN_IP" ]]; then
        fail "无法自动检测局域网 IP"
        info "请使用 --lan-ip 手动指定"
        exit 1
    fi
fi
ok "使用局域网 IP: $LAN_IP"

# ============================================================
# 检查端口占用
# ============================================================
if command -v ss &>/dev/null && ss -tlnp 2>/dev/null | grep -q ":$HTTPS_PORT "; then
    if [[ "$FORCE" != "true" ]]; then
        fail "端口 $HTTPS_PORT 已被占用"
        info "请使用 --https-port 指定其他端口，或 --force 强制继续"
        exit 1
    else
        warn "端口 $HTTPS_PORT 已被占用，--force 继续"
    fi
fi
ok "HTTPS 端口 $HTTPS_PORT 可用"

# ============================================================
# 安装 Nginx
# ============================================================
stage "1/4" "安装/检查 Nginx..."
if ! command -v nginx &>/dev/null; then
    info "安装 Nginx..."
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nginx >/dev/null
fi
ok "Nginx: $(nginx -v 2>&1 | head -1)"

# ============================================================
# 生成自签名证书
# ============================================================
stage "2/4" "生成自签名证书..."
CERT_DIR="/etc/nginx/ssl"
CERT_KEY="$CERT_DIR/ridgericetalk.key"
CERT_CRT="$CERT_DIR/ridgericetalk.crt"
mkdir -p "$CERT_DIR"
chmod 755 "$CERT_DIR"

if [[ -f "$CERT_CRT" && "$FORCE" != "true" ]]; then
    warn "证书已存在: $CERT_CRT（使用 --force 覆盖）"
else
    info "生成自签名 SAN 证书（有效期 $CERT_DAYS 天）..."
    # SAN 覆盖：主机 IP + --domain（可选）+ localhost（DES-2026-0912-03 --tls selfsigned 档要求）
    openssl req -x509 -nodes -days "$CERT_DAYS" -newkey rsa:2048 \
        -keyout "$CERT_KEY" \
        -out "$CERT_CRT" \
        -subj "/CN=${DOMAIN:-RidgeRiceTalk LAN}" \
        -addext "subjectAltName=IP:$LAN_IP,DNS:localhost${DOMAIN:+,DNS:$DOMAIN}" 2>/dev/null
    chmod 600 "$CERT_KEY"
    chmod 644 "$CERT_CRT"
    ok "证书生成完成: $CERT_CRT"
fi

# 可选：复制证书到客户端可访问目录
if [[ -n "${CERT_COPY_DEST:-}" ]]; then
    mkdir -p "$CERT_COPY_DEST"
    cp "$CERT_CRT" "$CERT_COPY_DEST/ridgericetalk.crt"
    chown "${SUDO_USER:-root}:" "$CERT_COPY_DEST/ridgericetalk.crt" 2>/dev/null || true
    ok "证书已复制到 $CERT_COPY_DEST/ridgericetalk.crt"
fi

# ============================================================
# 生成 Nginx 配置
# ============================================================
stage "3/4" "生成 Nginx 反向代理配置..."
NGINX_SITE="/etc/nginx/sites-available/ridgericetalk-https"
NGINX_ENABLED="/etc/nginx/sites-enabled/ridgericetalk-https"

# 备份旧配置
if [[ -f "$NGINX_SITE" && "$FORCE" != "true" ]]; then
    warn "Nginx 配置已存在: $NGINX_SITE（使用 --force 覆盖）"
else
    info "生成 Nginx 配置: $NGINX_SITE"
    cat > "$NGINX_SITE" <<EOF
# RidgeRiceTalk 局域网 HTTPS 测试配置
# 由 setup-lan-https.sh 自动生成，请勿手动修改

# WebSocket 连接头映射（H-4/P2-3）：客户端发 Upgrade 时置 upgrade，否则 close，
# 供 /admin 与 /ws 等混合/长连接 location 复用（条件式，优于写死 "upgrade"）
map \$http_upgrade \$connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen ${HTTPS_PORT} ssl;
    server_name ${LAN_IP} localhost${DOMAIN:+ ${DOMAIN}};

    ssl_certificate ${CERT_CRT};
    ssl_certificate_key ${CERT_KEY};
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;
    ssl_prefer_server_ciphers on;

    # 安全头（测试环境可适当放宽）
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Content-Type-Options "nosniff" always;

    # Voice 静态资源与业务 API 统一反代到 API 端口
    # （admin 的业务 API /api/v1/admin/*、/api/admin/* 在 API 端口同样挂载，走此处即可）
    location / {
        proxy_pass http://127.0.0.1:${PORT_API};
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_read_timeout 300s;
    }

    # Admin 后台 API（兼容 Admin 独立端口部署）
    location /api/admin/ {
        proxy_pass http://127.0.0.1:${PORT_ADMIN}/api/admin/;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }

    # Admin 管理页（H-4/P2-3）：静态 + /admin/ws WebSocket 反代到 Admin 独立端口。
    # 生产环境 admin 静态页只挂在 dedicated admin engine（AdminPortShared 在生产被忽略，
    # API 端口不服务 /admin 静态），故必须单独反代；/admin/ws 需要 Upgrade/Connection
    # 头完成 WebSocket 握手（此前缺失 → 管理页可打开但实时推送静默失效）。
    location /admin {
        proxy_pass http://127.0.0.1:${PORT_ADMIN}/admin;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection \$connection_upgrade;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
    }

    # LiveKit WebSocket 代理（HTTPS 页面必须通过 wss 连接）
    # 注意 proxy_pass 带尾斜杠，Nginx 会去掉 /livekit 前缀再转发，
    # 使 /livekit/rtc 正确映射到 LiveKit Server 的 /rtc 路径。
    location /livekit {
        proxy_pass http://127.0.0.1:${PORT_LK_WS}/;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
    }

    # RidgeRiceTalk WebSocket 端点（HTTPS 页面必须通过 wss 连接）
    location /ws {
        proxy_pass http://127.0.0.1:${PORT_API}/ws;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
    }
}
EOF
    ok "Nginx 配置生成完成"
fi

# 启用站点
if [[ ! -L "$NGINX_ENABLED" ]]; then
    ln -sf "$NGINX_SITE" "$NGINX_ENABLED"
    ok "已启用 Nginx 站点: ridgericetalk-https"
else
    ok "Nginx 站点已启用"
fi

# 校验并重启
info "校验 Nginx 配置..."
if nginx -t 2>/dev/null; then
    ok "Nginx 配置校验通过"
else
    fail "Nginx 配置校验失败"
    nginx -t
    exit 1
fi

info "重启 Nginx..."
systemctl restart nginx
ok "Nginx 已重启"

# ============================================================
# 更新 .env.production
# ============================================================
stage "4/4" "更新生产环境配置..."

if [[ ! -f "$ENV_FILE" ]]; then
    fail "未找到 $ENV_FILE"
    exit 1
fi

# 备份
BACKUP_ENV="${ENV_FILE}.bak.$(date +%s)"
cp "$ENV_FILE" "$BACKUP_ENV"
info "已备份原配置到 $BACKUP_ENV"

update_env() {
    local key="$1"
    local value="$2"
    if grep -qE "^${key}=" "$ENV_FILE"; then
        sed -i "s|^${key}=.*|${key}=${value}|" "$ENV_FILE"
    else
        echo "${key}=${value}" >> "$ENV_FILE"
    fi
}

update_env "RRT_PUBLIC_ADDRESS" "https://${LAN_IP}:${HTTPS_PORT}"
update_env "RRT_LIVEKIT_PUBLIC_URL" "wss://${LAN_IP}:${HTTPS_PORT}/livekit"

# N27 根因修复：CORS 白名单改为「合并」而非「整表替换」。
# 旧行为把白名单整体替换成 https-only：TLS 配好后一旦有客户端仍以 HTTP 直连
# API 端口（桌面端 origin-rewrite 的旧地址 / 未走反代的场景），WS 握手全被
# 403 —— N27 事故的根因写点（演练机 .env 的正确白名单只存在于备份）。
# 现保留既有 origin（含 http 族），仅追加缺失的 https 族。
CORS_MERGED="$(grep -E '^RRT_CORS_ORIGINS=' "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- | tr -d '[:space:]' || true)"
for _cors_origin in "https://${LAN_IP}" "https://${LAN_IP}:${HTTPS_PORT}" "https://localhost" "https://localhost:${HTTPS_PORT}"; do
    case ",${CORS_MERGED}," in
        *",${_cors_origin},"*) : ;;  # 已存在，跳过
        *) CORS_MERGED="${CORS_MERGED:+${CORS_MERGED},}${_cors_origin}" ;;
    esac
done
update_env "RRT_CORS_ORIGINS" "$CORS_MERGED"
update_env "RRT_COOKIE_SECURE" "true"

chmod 600 "$ENV_FILE"
ok "已更新 $ENV_FILE"

# ============================================================
# 重启 RidgeRiceTalk 服务
# ============================================================
info "重启 ridgericetalk 与 livekit 服务以应用新配置..."
systemctl restart ridgericetalk || warn "ridgericetalk 重启失败"
systemctl restart livekit || warn "livekit 重启失败"
ok "服务已重启"

# ============================================================
# 完成输出
# ============================================================
banner "局域网 HTTPS 配置完成"

echo -e "  ${C_BLUE}HTTPS 访问地址:${C_RESET}"
echo -e "    Voice:   ${C_GREEN}https://${LAN_IP}:${HTTPS_PORT}/${C_RESET}"
echo -e "    Admin:   ${C_GREEN}https://${LAN_IP}:${HTTPS_PORT}/admin${C_RESET}"
echo ""
echo -e "  ${C_BLUE}LiveKit WSS 地址:${C_RESET}"
echo -e "    ${C_GREEN}wss://${LAN_IP}:${HTTPS_PORT}/livekit${C_RESET}"
echo ""
echo -e "  ${C_BLUE}证书文件:${C_RESET}"
echo -e "    证书:    ${C_GREEN}${CERT_CRT}${C_RESET}"
echo -e "    私钥:    ${C_GREEN}${CERT_KEY}${C_RESET}"
echo ""
echo -e "  ${C_YELLOW}浏览器/客户端信任证书说明:${C_RESET}"
echo -e "    1. 将 ${CERT_CRT} 复制到测试客户端"
echo -e "    2. Windows: 双击 crt 文件 → 安装证书 → 本地计算机 → 受信任的根证书颁发机构"
echo -e "    3. macOS:   sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ${CERT_CRT}"
echo -e "    4. 浏览器首次访问 https://${LAN_IP}:${HTTPS_PORT} 时，选择"继续前往"或导入证书"
echo ""
echo -e "  ${C_YELLOW}注意:${C_RESET}"
echo -e "    本脚本仅用于局域网 WebRTC 测试，生产环境请使用受信任的域名证书（Let's Encrypt 等）。"
echo ""
echo -e "  ${C_GRAY}常用命令:${C_RESET}"
echo -e "    ${C_GRAY}查看后端日志:  journalctl -u ridgericetalk -f${C_RESET}"
echo -e "    ${C_GRAY}查看 Nginx 日志: tail -f /var/log/nginx/error.log${C_RESET}"
echo ""
