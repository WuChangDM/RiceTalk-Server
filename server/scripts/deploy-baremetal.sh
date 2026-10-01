#!/usr/bin/env bash
# RidgeRiceTalk 一键裸机部署脚本（Debian 系 / RHEL 系）
# 在干净 Debian 11+/Ubuntu 20.04+（及 Debian 系衍生）、Rocky 9+/AlmaLinux/
# CentOS Stream 9/Fedora 等 RHEL 系服务器上执行，完成全栈部署（不依赖 Docker）
# ---------------------------------------------------------------
# 前提：项目源码已部署到 /opt/ridgericetalk（通过 git clone 或 rsync）
# Usage: sudo ./deploy-baremetal.sh [OPTIONS]

set -euo pipefail

# ============================================================
# 颜色输出（参考 start-server.sh 风格）
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

stage()  { echo -e "\n${C_BLUE}[$1/9]${C_RESET} ${C_CYAN}$2${C_RESET}"; }
ok()     { echo -e "  ${C_GREEN}[OK]${C_RESET} $1"; }
warn()   { echo -e "  ${C_YELLOW}[WARN]${C_RESET} $1"; }
fail()   { echo -e "  ${C_RED}[FAIL]${C_RESET} $1"; }
info()   { echo -e "  ${C_GRAY}[INFO]${C_RESET} $1"; }
banner() {
    echo -e "\n${C_MAGENTA}========================================${C_RESET}"
    echo -e "${C_MAGENTA}  $1${C_RESET}"
    echo -e "${C_MAGENTA}========================================${C_RESET}"
}

# ============================================================
# 常量
# ============================================================
DEPLOY_DIR="/opt/ridgericetalk"
SERVER_DIR="$DEPLOY_DIR/server"
LIVEKIT_DIR="$DEPLOY_DIR/livekit"
STORAGE_DIR="$SERVER_DIR/storage"
ENV_FILE="$SERVER_DIR/.env.production"
LIVEKIT_YAML="$STORAGE_DIR/livekit.yaml"
SERVICE_USER="ridgericetalk"
RIDGERICETALK_HOME="/var/lib/ridgericetalk"
# Go 工具链版本：既是「低于此版本才安装」的安装目标版本，也是「可用」的最低要求
# （判定口径为 installed >= GO_VERSION，D14：此前是精确相等，装了更高版本也会被
# 判为不匹配而重新安装，甚至 rm -rf /usr/local/go）。
GO_VERSION="1.25.10"
# ISSUE-092/086: 升级 LiveKit 到 v1.13.4，消除与 livekit-client v2.19 的
# 协议协商不兼容（v1.8.x 无 trackPublishedResponse/offer-id ack，导致
# 发布轨道 negotiate() 30s 超时 → 周期性 NegotiationError 重连循环，
# 纯语音挂起也每 30s 抖动一次）。v1.13.4 已经 ISSUE-076 实测验证。
LIVEKIT_VERSION="v1.13.4"
NETEASE_PORT=3300
# DES-2026-0731-02: EasyTier 虚拟局域网配置
# EasyTier 监听 UDP 端口（雨云服务器白名单 5005-5009，使用 5007）
# EasyTier Web API 监听 127.0.0.1:11210（仅供服务端健康检查）
EASYTIER_PORT="${PORT_EASYTIER:-5007}"
EASYTIER_WEB_PORT=11210
EASYTIER_VERSION="v2.6.0"
EASYTIER_SECRET_FILE="/etc/easytier/secret"
EASYTIER_BIN="/usr/local/bin/easytier-core"
EASYTIER_DATA_DIR="/var/lib/easytier"

# 确保从任何阶段恢复时都能找到通过本脚本安装的 Go。
# 必须「前置」/usr/local/go/bin：若系统另装有旧版 Go（如发行版包管理器装的
# /usr/bin/go），把新路径追加到 PATH 末尾会让 command -v go 仍解析到旧版，
# 于是本脚本编译时用的是错的 Go（D13）。
export PATH="/usr/local/go/bin:$PATH"

# 默认使用国内 Go 模块代理，避免中国大陆/受限网络下载失败
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

# 脚本名称（用于提示信息；curl|bash 模式下 $0 为 "bash"，需用此变量给出准确提示）
if [[ -n "${BASH_SOURCE[0]:-}" && "${BASH_SOURCE[0]}" != "bash" ]]; then
    SCRIPT_NAME="${BASH_SOURCE[0]}"
else
    SCRIPT_NAME="deploy-baremetal.sh"
fi

# 默认官方仓库 URL（交互式向导中源码不存在时使用）
DEFAULT_REPO_URL="https://github.com/WuChangDM/RidgeRiceTalk-Sever.git"
DEFAULT_REPO_BRANCH="main"

# ============================================================
# 参数默认值
# ============================================================
STAGE_START=1
FORCE=false
PORT_API=8080
PORT_ADMIN=9090
PORT_LK_WS=7880
PORT_LK_TCP=7881
PORT_LK_UDP=7882
PUBLIC_IP=""
REPO_URL=""
REPO_BRANCH="main"
WITH_MONITORING=false
SKIP_MIGRATE=false
SKIP_TTS_MODEL=false
# 默认安装每日自动备份 timer（ridgericetalk-backup.timer）；--no-backup-timer 可关闭
BACKUP_TIMER=true
DRY_RUN=false
# N1: 无人值守确认开关（--yes / -y），**默认关闭**（不得默认自动确认）。
# 关闭时：能提问就提问；无控制终端（CI/Ansible/cron/非交互 SSH）则「可安全取默认值的提问」
# 走默认值、「必须人工确认的提问」明确报错退出，绝不隐式继续。
# 开启时：所有提问一律走默认值，不读终端、不报错（等价于对所有提问回车）。
ASSUME_YES=false
# H17: 源码树与「预期构建来源」不一致（陈旧 / 落后远端）时，是否仍允许编译。
# **默认 false = 拒绝**：陈旧源码树会静默把服务端退回旧版本（实测丢失过 H14 安全修复）。
# 仅当人工核对过这棵树就是本次要发布的源码时才显式指定。
ALLOW_STALE_SOURCE=false

# H-4 ②（DES-2026-0912-04 P2-2）：是否无条件重建 Admin 管理页前端。
# **默认 false = 按 admin 源码指纹决定**：指纹与上次构建（server/.rrt-admin-src-hash）
# 一致则跳过重建（打印明确提示），不一致 / 无记录则自动重建。--rebuild-admin 置 true
# 可跳过比对强制重建（排查前端问题时用）。
REBUILD_ADMIN=false

# N28（DES-20261002-01 §3.4）：TURN 参数化开关（--enable-turn）。
# **默认 false = 不开**：此时生成的 livekit.yaml 与 .env.production 和改动前
# 逐字节一致（硬验收）。开启时：
#   - livekit.yaml 增加 turn 块（udp_port 3478，TURN over UDP）；
#   - tls_port 5349 仅在证书文件已存在时写入 —— LiveKit 校验 tls_port 必须伴随
#     domain/cert_file/key_file，缺证书写 tls_port 会让配置加载直接失败；
#   - .env.production 追加端口放行说明（3478/udp、5349/tcp 需云安全组/防火墙放行）。
ENABLE_TURN=false
TURN_UDP_PORT=3478
TURN_TLS_PORT=5349
# TURN TLS 证书路径（与 setup-lan-https.sh 生成的自签证书一致；acme 档为同一
# Nginx 接管路径）
TURN_CERT_FILE="/etc/nginx/ssl/ridgericetalk.crt"
TURN_KEY_FILE="/etc/nginx/ssl/ridgericetalk.key"

# F3: 本次运行 Stage 5 是否真的生成了新数据库口令并写入 /tmp/.rrt_pg_pwd。
# 仅该标志为 true 时，收尾阶段才删除该临时文件；否则保留（供 --stage db 后
# --stage config 续跑使用）。
PG_PWD_WRITTEN=false

# DES-2026-0912-03 P4（§3.1-5）/ D3/D4：HTTPS 三档（--tls none|selfsigned|acme）。
# D4 决策：默认 none，与既有行为完全一致（仅桌面客户端使用），但在部署收尾明确提示
# 「网页端语音（麦克风采集）需要 HTTPS」，按需选档，不默认改变任何现场行为。
TLS_MODE="none"
# TLS 使用的域名：acme 档必填（Let's Encrypt 颁证对象，缺失在参数解析后立即报错）；
# selfsigned 档可选（写入自签证书 SAN 与 Nginx server_name）；none 档忽略。
TLS_DOMAIN=""
# 部署收尾访问地址的协议前缀：TLS 档配置成功后置为 https（none 档保持 http，
# 收尾 banner 输出与改造前逐字一致）。
RRT_URL_SCHEME="http"

# ============================================================
# 帮助信息
# ============================================================
show_help() {
    cat <<'EOF'
Usage: deploy-baremetal.sh [OPTIONS]

RidgeRiceTalk 一键裸机部署脚本（Debian/Ubuntu）

Options:
  --help                  显示此帮助
  --stage <name>          从指定阶段恢复执行（deps/livekit/build/db/config/systemd/vpn/netease/start）
  --dry-run               只打印将要执行的动作，不做任何修改（安全检查/预览用）
  --force                 强制覆盖配置文件
  --yes, -y               无人值守：所有提问一律采用默认值（**默认关闭**）。
                          非交互环境（CI / Ansible / cron / 非交互 SSH）下，若既未指定该
                          开关、又未提供 CLI 参数，部署会在「确认开始部署」处**明确报错退出**
                          （绝不会隐式按默认值继续），以免无人值守场景误改生产。
  --allow-stale-source    允许在「源码树陈旧 / 与预期构建来源不一致」时仍然编译（**默认拒绝**）。
                          默认行为：Stage 0 发现源码树落后远端、或源码树不是 git 仓库却存在
                          构建批次记录（.rrt-commit，说明现行二进制由别处的归档构建而来）时，
                          打印告警并**拒绝继续**，避免用旧源码静默覆盖运行中的二进制（H17）。
                          仅在你已人工核对「这棵树就是本次要发布的源码」时才指定本开关。
  --mirror-preset <mode>  下载镜像预设（默认 auto，G-5）：
                          auto   保持既有回退顺序（Go/Node 国内优先、GitHub/npm 直连优先）；
                          cn     国内镜像优先（GitHub 加速前缀前置、npm 直接用 npmmirror）；
                          global 官方源优先（海外服务器）。
                          仅影响本脚本的下载类请求；**不修改**系统 apt/dnf 源配置。
  --http-proxy <url>     经代理出网（受限网络），贯穿 curl/npm/git/apt 等下载与探测请求。
  --https-proxy <url>    同上（HTTPS 流量）。示例: --https-proxy http://127.0.0.1:7890
  --rebuild-admin         无条件重建 Admin 管理页前端（**默认按源码指纹决定**）。
                          默认行为：Stage 4 对 webhost/dist/admin 已有产物的情况，比对
                          web/admin 源码指纹与上次构建记录（server/.rrt-admin-src-hash）——
                          一致则跳过并提示，不一致 / 无记录则自动重建（H-4/P2-2）。
                          指定本开关可跳过比对，强制 npm 重建管理页。
  --port-api <port>       API 端口（默认 8080）
  --port-admin <port>     Admin 端口（默认 9090）
  --port-lk-ws <port>     LiveKit WebSocket 端口（默认 7880）
  --port-lk-tcp <port>    LiveKit TCP 端口（默认 7881）
  --port-lk-udp <port>    LiveKit UDP 端口（默认 7882）
  --enable-turn           启用 LiveKit TURN 中继（默认关，N28）：
                          livekit.yaml 写入 turn 块（udp 3478）；证书就绪时另写
                          tls_port 5349。需防火墙/安全组放行 3478/udp（及 5349/tcp）。
  --public-ip <ip>        公网 IP（默认自动检测）
  --repo-url <url>        自动 git clone 源码到 /opt/ridgericetalk（若不存在）
  --repo-branch <name>    克隆时使用的分支（默认 main）
  --with-monitoring       安装 Netdata 系统监控（可选，默认不启用）
  --skip-migrate          跳过数据库迁移（高级用户）
  --skip-tts-model        跳过 TTS 模型下载/复制（网络受限时使用）
  --no-backup-timer       不安装每日自动备份 timer（默认会安装 ridgericetalk-backup.timer，
                          每日 04:17 触发 backup-db.sh + backup-storage.sh，仅启用 timer 不启动服务）
  --tls <mode>            HTTPS 部署档位（默认 none，DES-2026-0912-03 D3/D4）：
                          none       不配置 HTTPS（与既有行为一致，仅桌面客户端可用；
                                     网页端语音需 HTTPS，收尾时会给出提示）
                          selfsigned 自签名证书 + Nginx 反代（局域网/内网用；浏览器需手动
                                     信任 /etc/nginx/ssl/ridgericetalk.crt）
                          acme       Let's Encrypt 正式证书（需 --domain，且 80/443 公网可达）
  --domain <fqdn>         TLS 域名：acme 档必填；selfsigned 档可选（写入证书 SAN 与 Nginx
                          server_name）；主机 IP 与 localhost 始终包含在自签证书 SAN 中

Examples:
  # 标准部署（交互式）
  sudo ./deploy-baremetal.sh

  # 干净服务器一键部署（自动克隆源码）
  sudo ./deploy-baremetal.sh --repo-url https://github.com/<org>/ridgericetalk.git

  # 局域网 HTTPS（网页端语音可用，浏览器需信任自签证书）
  sudo ./deploy-baremetal.sh --tls selfsigned

  # 公网域名 + Let's Encrypt 正式证书（80/443 需公网可达）
  sudo ./deploy-baremetal.sh --tls acme --domain voice.example.com

  # 雨云服务器 NAT 部署（5 端口；IP 与端口均为示例，请替换为你的真实值）
  sudo ./deploy-baremetal.sh \
    --port-api 50100 --port-admin 50103 \
    --port-lk-ws 50101 --port-lk-tcp 50102 --port-lk-udp 50104 \
    --public-ip 203.0.113.10

  # 从配置阶段恢复
  sudo ./deploy-baremetal.sh --stage config
EOF
}

# ============================================================
# 参数解析
# ============================================================
while [[ $# -gt 0 ]]; do
    case "$1" in
        --help|-h) show_help; exit 0 ;;
        --stage)
            case "${2:-}" in
                deps)    STAGE_START=1 ;;
                livekit) STAGE_START=3 ;;
                build)   STAGE_START=4 ;;
                db)      STAGE_START=5 ;;
                config)  STAGE_START=6 ;;
                systemd) STAGE_START=7 ;;
                vpn)     STAGE_START=7 ;;  # DES-2026-0731-02: vpn 从 Stage 7 开始，包含 EasyTier 部署（Stage 7.7）
                netease) STAGE_START=8 ;;
                start)   STAGE_START=9 ;;
                *)
                    fail "未知阶段: ${2:-}（可选值: deps/livekit/build/db/config/systemd/vpn/netease/start）"
                    exit 2
                    ;;
            esac
            shift 2 ;;
        --force)         FORCE=true; shift ;;
        --yes|-y)        ASSUME_YES=true; shift ;;
        --allow-stale-source) ALLOW_STALE_SOURCE=true; shift ;;
        --mirror-preset)
            case "${2:-}" in
                cn|global|auto) RRT_MIRROR_PRESET="$2" ;;
                *)
                    fail "--mirror-preset 仅支持 cn|global|auto（当前: ${2:-}）"
                    exit 2
                    ;;
            esac
            shift 2 ;;
        --rebuild-admin) REBUILD_ADMIN=true; shift ;;
        --dry-run)       DRY_RUN=true; shift ;;
        --port-api)      PORT_API="$2"; shift 2 ;;
        --port-admin)    PORT_ADMIN="$2"; shift 2 ;;
        --port-lk-ws)    PORT_LK_WS="$2"; shift 2 ;;
        --port-lk-tcp)   PORT_LK_TCP="$2"; shift 2 ;;
        --port-lk-udp)   PORT_LK_UDP="$2"; shift 2 ;;
        --enable-turn)   ENABLE_TURN=true; shift ;;
        --public-ip)     PUBLIC_IP="$2"; shift 2 ;;
        --http-proxy)    HTTP_PROXY_ARG="$2"; shift 2 ;;
        --https-proxy)   HTTPS_PROXY_ARG="$2"; shift 2 ;;
        --repo-url)      REPO_URL="$2"; shift 2 ;;
        --repo-branch)   REPO_BRANCH="$2"; shift 2 ;;
        --with-monitoring) WITH_MONITORING=true; shift ;;
        --skip-migrate) SKIP_MIGRATE=true; shift ;;
        --skip-tts-model) SKIP_TTS_MODEL=true; shift ;;
        --no-backup-timer) BACKUP_TIMER=false; shift ;;
        --tls)
            case "${2:-}" in
                none|selfsigned|acme) TLS_MODE="$2" ;;
                *)
                    fail "--tls 仅支持 none|selfsigned|acme（当前: ${2:-}）"
                    exit 2
                    ;;
            esac
            shift 2 ;;
        --domain)        TLS_DOMAIN="$2"; shift 2 ;;
        *)
            fail "未知参数: $1（使用 --help 查看用法）"
            exit 2
            ;;
    esac
done

# --tls 参数合法性收口：acme 档必须提供 --domain（Let's Encrypt 颁发证书的对象）；
# selfsigned 档 --domain 可选（用于证书 SAN 与反代 server_name）；none 档忽略 --domain。
if [[ "$TLS_MODE" == "acme" && -z "$TLS_DOMAIN" ]]; then
    fail "--tls acme 需要同时指定 --domain <fqdn>（用于 Let's Encrypt 签发证书）"
    info "示例: sudo $SCRIPT_NAME --tls acme --domain voice.example.com"
    exit 2
fi

# G-5：代理参数 → 标准环境变量。curl/npm/git/apt 与 lib 内所有探测请求
# 自动遵循小写 http_proxy/https_proxy；同时导出大写变体以覆盖部分工具。
if [[ -n "${HTTP_PROXY_ARG:-}" ]]; then
    export http_proxy="$HTTP_PROXY_ARG" HTTP_PROXY="$HTTP_PROXY_ARG"
    info "已设置代理: http_proxy=$HTTP_PROXY_ARG"
fi
if [[ -n "${HTTPS_PROXY_ARG:-}" ]]; then
    export https_proxy="$HTTPS_PROXY_ARG" HTTPS_PROXY="$HTTPS_PROXY_ARG"
    info "已设置代理: https_proxy=$HTTPS_PROXY_ARG"
fi

# ============================================================
# 脚本所在目录 + 能力层（G-2，DES-2026-0912-03 §3.1）
# ============================================================
# 放在参数解析之后：`--help` 与「未知参数」两条路径不需要能力层，
# 保持与改造前一样能在没有源码目录的情况下工作。
#
# 必须用 BASH_SOURCE 定位脚本自身，**不能依赖调用者的 cwd**：
#   - 常见调用方式是在任意目录下执行 `sudo /opt/ridgericetalk/server/scripts/deploy-baremetal.sh`；
#   - `--stage xxx` 续跑也可能从别处发起。
# 注：curl|bash 管道模式下 BASH_SOURCE 为空、此处会退化为 cwd，找不到 lib/ 时
#     直接给出明确报错（而不是等到 Stage 2 才半途失败）。
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 能力层：os（发行版/包管理器/init 探测）、pkg（逻辑依赖名 → 各发行版包名映射
# + 安装抽象）、net（公网 IP / 镜像预设）。主流程从此只依赖抽象，不再直接写
# apt-get / dpkg（dnf/apk 分支见 G-3/G-4）。
RRT_LIB_DIR="$SCRIPT_DIR/lib"
if [[ ! -f "$RRT_LIB_DIR/os.sh" && -f "$SERVER_DIR/scripts/lib/os.sh" ]]; then
    # 兜底：脚本与源码不同源时，用部署目录下的能力层
    RRT_LIB_DIR="$SERVER_DIR/scripts/lib"
fi
for RRT_LIB_NAME in os pkg net mirrors wscheck; do
    if [[ ! -f "$RRT_LIB_DIR/${RRT_LIB_NAME}.sh" ]]; then
        fail "能力层库文件缺失: $RRT_LIB_DIR/${RRT_LIB_NAME}.sh"
        info "本脚本需要与 lib/ 目录一起运行（deploy-baremetal.sh 与 lib/ 必须同时存在）"
        info "请先获取完整源码，再执行: sudo $SERVER_DIR/scripts/deploy-baremetal.sh"
        exit 1
    fi
    # shellcheck source=/dev/null
    source "$RRT_LIB_DIR/${RRT_LIB_NAME}.sh"
done
unset RRT_LIB_NAME

# 提前计算目标架构，供 Stage 2 的 Go/Node 下载与 Stage 3 LiveKit 等场景使用。
# D2 收口：架构探测统一用 uname -m，**不得在发行版闸门之前依赖 dpkg** ——
# 旧实现在 RHEL/Alpine 等无 dpkg 的系统上会在闸门之前直接 "command not found"
# 退出，闸门的友好提示永远打不出来。uname -m 在所有 Linux 上行为一致，映射
# 结果（x86_64→amd64 / aarch64→arm64）与 dpkg --print-architecture 完全一致，
# 下载产物命名不受影响。能力层 lib/os.sh: rrt_arch 仍保留完整探测实现供其使用。
case "$(uname -m 2>/dev/null || echo unknown)" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *)             ARCH="$(uname -m 2>/dev/null || echo unknown)" ;;
esac

# ============================================================
# 辅助函数
# ============================================================

# 判断指定阶段是否应该执行（>= STAGE_START）
run_stage() { [[ $1 -ge $STAGE_START ]]; }

# ------------------------------------------------------------
# 发行版版本下限闸门（D2 放宽的配套，DES-2026-0912-03 §3.1-2）
# ------------------------------------------------------------
# 只对明确给出下限的发行版硬校验（fail 退出）：
#   debian ≥ 11、ubuntu ≥ 20.04、rocky/centos 主版本 ≥ 9
#   （CentOS Stream 9 的 os-release 为 ID=centos, VERSION_ID="9"）
# 其余放行发行版（Deepin/UOS/麒麟/Mint/Pop/Kali/Raspbian/AlmaLinux/Fedora/RHEL/OL）
# 只 warn 不阻断 —— 它们的 VERSION_ID 格式各异（或缺失），硬编码下限容易误杀。
# 依赖 lib/os.sh 的 rrt_os_id/rrt_os_version/rrt_version_ge（闸门在能力层 source 后执行）。
rrt_gate_version_guard() {
    local id ver
    id="$(rrt_os_id)"
    ver="$(rrt_os_version)"
    case "$id" in
        debian)
            if ! rrt_version_ge "$ver" "11"; then
                fail "Debian 版本过低: ${ver:-未知}（要求 ≥ 11 bullseye）"
                info "老版本缺少本脚本依赖链的安全更新，未验证可用性；请升级系统后重试"
                return 1
            fi
            ;;
        ubuntu)
            if ! rrt_version_ge "$ver" "20.04"; then
                fail "Ubuntu 版本过低: ${ver:-未知}（要求 ≥ 20.04 focal）"
                info "老版本缺少 Node 20 所需的 glibc 与 NodeSource 源支持，未验证可用性；请升级系统后重试"
                return 1
            fi
            ;;
        rocky|centos)
            if ! rrt_version_ge "${ver%%.*}" "9"; then
                fail "版本过低: ${PRETTY_NAME:-$id} ${ver:-未知}（要求主版本 ≥ 9，即 Rocky 9 / CentOS Stream 9 及以上）"
                info "CentOS 7/8 与 Rocky 8 缺少本脚本依赖的包（如 pkgconf-pkg-config），请升级后重试"
                return 1
            fi
            ;;
        *)
            warn "未对 ${PRETTY_NAME:-$id} 设置最低版本校验（VERSION_ID=${ver:-缺失}）：请自行确认系统版本较新"
            ;;
    esac
    return 0
}

# 是否存在「可用的控制终端」（即能否向用户提问）。
# 三种调用场景必须区分开：
#   1) 终端直接运行            → stdin 就是 TTY
#   2) curl ... | sudo bash    → stdin 是管道，但进程仍挂着控制终端，/dev/tty 可打开
#   3) 非交互 SSH（ssh host 'bash deploy-baremetal.sh'）/ CI / Ansible / cron
#      → **没有控制终端**：/dev/tty 的设备节点依然存在（`-c` 为真），
#        但 open 会失败 ENXIO（No such device or address）。
# 因此**必须实际尝试打开 /dev/tty**：`-c /dev/tty` 在场景 3 也成立，不足以判断 ——
# 这正是 N1 的根因：旧代码用 `-t 0` 判断、用 `-c /dev/tty` 触发向导，
# 于是把「非交互」误判成「有 tty 可用」，向导一进 read_prompt 就崩。
# 实现：子 shell 内做一次重定向（不执行任何命令，零副作用）；失败时
# bash 只会向该子 shell 的 stderr 报错，已被丢弃，非 0 退出码即「无可用终端」。
rrt_can_prompt() {
    [[ -t 0 ]] && return 0
    [[ -c /dev/tty ]] || return 1
    ( : </dev/tty ) 2>/dev/null
}

# 交互式读取（兼容 curl|bash 管道模式）
# curl ... | sudo bash 时 stdin 是管道，read -p 会立即返回 EOF，
# 必须从 /dev/tty 读取才能与用户交互。
#
# N1 修复（2026-09-14 宁波生产机实测）：
#   旧实现在 `[[ -t 0 ]]` 为假时**硬读 /dev/tty**，而无控制终端时这次打开必然失败
#   （`deploy-baremetal.sh: line 217: /dev/tty: No such device or address`），
#   在 set -e 下立即中断向导第 1 步 → CI / Ansible / 远程非交互 SSH / curl|bash
#   全部无法部署（讽刺的是该分支本意**正是**支持管道模式）。
#   现在：无法提问时**不读任何设备**，打印提示 +「采用默认值」说明，并把变量赋成
#   **空串**（等价于交互时直接回车），由调用点既有的 `${VAR:-默认值}` /
#   `[[ -n "$VAR" ]]` 逻辑接管 —— 逐调用点判断过，向导里这些项都有安全默认值
#   （IP / 端口 / 是否启用音乐机器人 / 是否克隆），且都有 CLI 参数可覆盖。
#   ⚠️ 必须**赋空串**而不是 unset：脚本是 `set -u`，调用点直接引用 "$VAR" 会
#      因未定义变量报错（交互时回车得到的就是空串，语义完全一致）。
#   必须由人拍板的提问（「确认开始部署？」）请用 read_prompt_required，见下。
# 用法: read_prompt "提示文本" VAR_NAME
read_prompt() {
    local prompt="$1"
    local var_name="$2"

    # --yes（默认关闭）：无人值守模式，等同「对所有提问直接回车」，不读终端也不报错
    if [[ "$ASSUME_YES" == "true" ]]; then
        printf -v "$var_name" '%s' ''
        info "非交互(--yes): $prompt → 采用默认值"
        return 0
    fi

    if rrt_can_prompt; then
        if [[ -t 0 ]]; then
            read -p "$prompt" "$var_name"
        else
            read -p "$prompt" "$var_name" </dev/tty
        fi
        return 0
    fi

    # 无可用控制终端：不读 /dev/tty（读了必崩），留空走默认值
    printf -v "$var_name" '%s' ''
    warn "非交互环境（无可用控制终端）: $prompt → 采用默认值"
    if [[ "${RRT_NONINTERACTIVE_HINT_SHOWN:-false}" != "true" ]]; then
        RRT_NONINTERACTIVE_HINT_SHOWN=true
        info "如需指定上述取值，请改用 CLI 参数：--public-ip / --port-api / --port-admin / --port-lk-ws|tcp|udp / --repo-url"
        info "否则请对全部提问采用默认值：加 --yes（默认关闭，需显式指定）"
    fi
    return 0
}

# 必须由人确认的提问：无可用控制终端时**明确报错**，绝不隐式采用默认值。
# 用在「确认开始部署？」这类一旦默认继续、就可能在无人值守场景误改生产的节点。
# 用法: read_prompt_required "提示文本" VAR_NAME
#   返回 0 = 已确认（交互回车 / --yes）；返回 1 = 无法确认，调用方须 `|| exit 1`。
read_prompt_required() {
    local prompt="$1"
    local var_name="$2"

    if [[ "$ASSUME_YES" == "true" ]]; then
        printf -v "$var_name" '%s' 'Y'
        info "非交互(--yes): $prompt → 默认 Y"
        return 0
    fi

    if rrt_can_prompt; then
        read_prompt "$prompt" "$var_name"
        return 0
    fi

    printf -v "$var_name" '%s' ''
    fail "非交互环境无法确认: $prompt"
    info "当前会话没有可用的控制终端（CI / Ansible / cron / 非交互 SSH 均如此）"
    info "二选一后再重试："
    info "  1) 确认按默认值继续 → 加 --yes（等价于对该项回车；默认关闭，须显式指定）"
    info "  2) 由人确认 → 分配控制终端：ssh -tt <host>，或直接在服务器终端上执行"
    info "也可用 CLI 参数代替向导：--public-ip / --port-* / --repo-url"
    return 1
}

# 判断当前是否为 curl|bash 管道模式（stdin 不是 TTY）
# 注意：stdin 是管道 **不代表** 无法交互（curl|bash 仍有控制终端可用 /dev/tty），
# 故「能否提问」必须用 rrt_can_prompt 判断，不能用本函数。
is_pipe_mode() { [[ ! -t 0 ]]; }

# ------------------------------------------------------------
# DRY-RUN 支持（严格只读）
# ------------------------------------------------------------
# DRY_RUN=true 时不创建/修改/删除任何文件，也不改动任何服务状态。
# 用法: if ! dry_run "动作A" "动作B"; then <原始修改步骤>; fi
#   DRY_RUN=true  → 逐条打印「[DRY-RUN] 将执行: <动作>」并返回 0（调用方跳过该步骤）
#   DRY_RUN=false → 不打印、返回 1（调用方照常执行该步骤）
# 说明：为保证「零副作用」，对每个产生修改的阶段做整体短路，并在短路前把该阶段
# 将要执行的具体动作逐条打印出来（宁可少打印，也不漏判）。
dry_run() {
    [[ "$DRY_RUN" == "true" ]] || return 1
    local action
    for action in "$@"; do
        echo -e "  ${C_YELLOW}[DRY-RUN]${C_RESET} 将执行: $action"
    done
    return 0
}

# ------------------------------------------------------------
# 读取 $ENV_FILE 中某个 KEY 的值（统一解析口径，Stage 5/6 共用）
# ------------------------------------------------------------
# 行为（与 server/scripts/backup-db.sh 的 env_get 对齐）：
#   - 匹配 ^[[:space:]]*KEY=
#   - 取 = 之后的全部内容
#   - 去掉尾部 \r（CRLF 配置）
#   - 去掉两端成对的英文双引号
#   - 找不到该键 / 文件不存在 → 返回非 0（调用方须用 `|| true` 兜底，
#     否则 set -euo pipefail 下取不到值会静默中止脚本）
# 注意：本函数只读，不修改任何文件。
env_get() {
    local key="$1" line
    [[ -f "$ENV_FILE" ]] || return 1
    # `|| true`：grep 未命中时使整条管道返回 0，避免 set -o pipefail 中止
    line="$(grep -E "^[[:space:]]*${key}=" "$ENV_FILE" 2>/dev/null | head -1 || true)"
    [[ -n "$line" ]] || return 1
    line="${line#*=}"
    line="${line%$'\r'}"
    # 去掉两端可能存在的成对英文双引号
    line="${line%\"}"
    line="${line#\"}"
    printf '%s' "$line"
}

# 从 $ENV_FILE 解析数据库口令（统一口径，Stage 5/6 共用）。
# 依次尝试 POSTGRES_PASSWORD → RRT_DB_PASSWORD → RRT_DATABASE_URL。
# 找到非空口令则 printf 输出；三处都取不到则返回非 0（调用方自行兜底）。
env_db_password() {
    local pwd="" url userinfo
    pwd="$(env_get POSTGRES_PASSWORD || true)"
    [[ -n "$pwd" ]] || pwd="$(env_get RRT_DB_PASSWORD || true)"
    if [[ -z "$pwd" ]]; then
        # postgres(ql)://user:pass@host:port/db —— 取 :// 之后到「最后一个 @」
        # 之间的 user:pass，再取 pass 段（口令可能含 ':' 或 '@'）
        url="$(env_get RRT_DATABASE_URL || true)"
        if [[ "$url" =~ ^postgres(ql)?://(.*)@ ]]; then
            userinfo="${BASH_REMATCH[2]}"
            if [[ "$userinfo" == *:* ]]; then
                pwd="${userinfo#*:}"
            fi
        fi
    fi
    [[ -n "$pwd" ]] || return 1
    printf '%s' "$pwd"
}

# 判断 LiveKit 是否由 ridgericetalk 主进程内嵌托管。
# 与 Go 侧真实语义一致：仅当 .env.production 中 RRT_LIVEKIT_AUTOSTART=false
# 且 RRT_EMBEDDED_DEPS 未被显式设为 false 时，主进程才托管 LiveKit。
# （若用户显式 RRT_EMBEDDED_DEPS=false，主进程不会拉 LiveKit，仍需独立单元。）
# 返回 0 表示由主进程托管（应跳过独立 livekit.service，避免抢端口）；
# 返回 1 表示需要独立 livekit.service（新装默认如此）。
livekit_managed_by_main() {
    [[ -f "$ENV_FILE" ]] || return 1
    local autostart embedded
    autostart="$(env_get RRT_LIVEKIT_AUTOSTART | tr -d '[:space:]' || true)"
    embedded="$(env_get RRT_EMBEDDED_DEPS | tr -d '[:space:]' || true)"
    [[ "$autostart" == "false" ]] || return 1
    # 显式 RRT_EMBEDDED_DEPS=false → 主进程不托管 LiveKit，仍走独立单元
    [[ "$embedded" != "false" ]]
}

# 主进程托管 LiveKit 时，停用并禁用现场既有的独立 livekit.service（修复既存端口冲突）。
# 非托管场景不做任何事；动作受 --dry-run 守卫（dry-run 只打印计划，不改服务状态）。
disable_managed_livekit_unit() {
    livekit_managed_by_main || return 0
    [[ -f /etc/systemd/system/livekit.service ]] || return 0
    if dry_run "systemctl disable --now livekit（LiveKit 由主进程托管，停用/禁用既有独立单元，修复端口冲突）"; then
        return 0
    fi
    systemctl disable --now livekit 2>/dev/null || true
    info "已停用并禁用既有的独立 livekit.service（LiveKit 由主进程托管，避免与主进程抢端口）"
}

# ------------------------------------------------------------
# LiveKit 二进制原子安装（N2 修复，2026-09-14 宁波生产机实测）
# ------------------------------------------------------------
# 为什么不能再用 `cp` 直接覆盖：
#   LiveKit 由主进程/livekitmgr 的健康看门狗托管，**已部署机器重跑部署时它正在运行**。
#   `cp` 会以 O_WRONLY|O_TRUNC 打开目标这个「正在被执行的」文件，内核拒绝
#   → `cp: cannot create regular file '...': Text file busy`（ETXTBSY）→ Stage 3 中断。
#   实测还确认：kill 掉该进程也没用 —— 看门狗 2 秒内就以新 PID 把它拉起来
#   （1844561 → 1951696），故**无法靠「先停进程」绕过**。
#   ⚠️ 影响面：全新部署不受影响（LiveKit 尚未运行）；**已部署机器重跑必然中断**
#   —— 而「部署出问题了再跑一次一键脚本」正是新手最自然的动作。
#
# 手法（照抄本项目已验证的 rollback-binary.sh:503-548，不另创方法）：
#   1) 复制到**同目录**临时文件（活文件不被写入 → 不触发 ETXTBSY；
#      必须同目录，跨文件系统的 mv 会退化成 copy+unlink，既非原子也会重新踩 ETXTBSY）
#   2) 校验临时文件：ELF magic（拒绝非本机格式）+ 与源文件 md5 一致（复制完整性）
#   3) 校验临时文件可执行：--version 有输出（与既有「验证」同口径）
#   4) `mv -f` 原子替换：只改目录项、不写已打开的文件 → **不触发 ETXTBSY**；
#      运行中的进程继续用它已映射的旧 inode，重启服务后才切到新二进制
#   5) 再校验**活文件**：md5 一致 + 还原属主/权限
#      （rollback-binary.sh 的核心教训：不验活文件就会在没换成功时报成功）
# 任一步失败：清理临时文件、活文件保持原样、返回 1（绝不半途替换）。
# 用法: install_livekit_binary_atomic <已就绪的源文件路径>
#   目标固定 $LIVEKIT_DIR/livekit-server；测试可用 RRT_LK_BIN_TARGET 覆盖。
install_livekit_binary_atomic() {
    local src="$1"
    local target="${RRT_LK_BIN_TARGET:-$LIVEKIT_DIR/livekit-server}"
    local tmp="${target}.new.$$"
    local src_md5="" tmp_md5="" live_md5="" owner="" mode="755"

    if [[ ! -f "$src" ]]; then
        fail "LiveKit 源文件不存在: $src（原二进制未改动）"
        return 1
    fi

    # 记录活文件现有属主，替换后原样还原（重跑时保持与原来一致；新装时无活文件，用 root）
    if [[ -e "$target" ]]; then
        owner="$(stat -c '%U:%G' "$target" 2>/dev/null || true)"
    fi

    # --- 步骤 1/5：复制到同目录临时文件（不触碰活文件） ---
    if ! cp "$src" "$tmp"; then
        fail "复制到临时文件失败: $tmp（原二进制未改动）"
        rm -f -- "$tmp" 2>/dev/null || true
        return 1
    fi
    chmod "$mode" "$tmp" 2>/dev/null || true

    # --- 步骤 2/5：校验临时文件（ELF magic + md5 与源一致） ---
    if ! head -c 4 "$tmp" 2>/dev/null | grep -q 'ELF'; then
        fail "临时副本不是本机可执行格式（疑似非 Linux 二进制）: $tmp（原二进制未改动）"
        rm -f -- "$tmp" 2>/dev/null || true
        return 1
    fi
    src_md5="$(md5sum "$src" 2>/dev/null | awk '{print $1}' || true)"
    tmp_md5="$(md5sum "$tmp" 2>/dev/null | awk '{print $1}' || true)"
    if [[ -z "$src_md5" || "$tmp_md5" != "$src_md5" ]]; then
        fail "临时副本 md5 与源不一致：期望 ${src_md5:-?}，实际 ${tmp_md5:-?}（原二进制未改动）"
        rm -f -- "$tmp" 2>/dev/null || true
        return 1
    fi

    # --- 步骤 3/5：临时副本自检（可执行且能输出版本） ---
    if ! "$tmp" --version >/dev/null 2>&1; then
        fail "临时副本自检失败（--version 无输出）: $tmp（原二进制未改动）"
        rm -f -- "$tmp" 2>/dev/null || true
        return 1
    fi

    # --- 步骤 4/5：原子替换（mv 只改目录项，不写已打开的文件 → 不触发 ETXTBSY） ---
    if ! mv -f "$tmp" "$target"; then
        fail "原子替换失败: $tmp -> $target（原二进制未改动）"
        rm -f -- "$tmp" 2>/dev/null || true
        return 1
    fi

    # --- 步骤 5/5：还原属主/权限，并再校验活文件 ---
    if [[ -n "$owner" ]]; then
        chown "$owner" "$target" 2>/dev/null || true
    fi
    chmod "$mode" "$target" 2>/dev/null || true
    live_md5="$(md5sum "$target" 2>/dev/null | awk '{print $1}' || true)"
    if [[ "$live_md5" != "$src_md5" ]]; then
        fail "替换后活文件 md5 与源不一致：期望 $src_md5，实际 ${live_md5:-?}"
        warn "替换结果不可信，请人工核对: $target"
        return 1
    fi
    return 0
}

# 确保迁移工具是「本机可执行」的。
#
# 背景（2026-09-12 生产实测）：服务器上曾存在一个从开发机 scp 上去的 **Windows PE**
# 版 ridgericetalk-migrate，Linux 上执行报 `cannot execute binary file: Exec format error`，
# 而当时的报错文案却提示「请检查 PostgreSQL 连接配置」，误导排查方向；且在生产 AutoMigrate
# 关闭的前提下，迁移无法应用会成为发布阻断。此处先做平台校验，必要时就地重编译。
ensure_migrate_binary() {
    local bin="$SERVER_DIR/ridgericetalk-migrate"
    # ELF magic（0x7f 'E' 'L' 'F'）——Linux 上必须匹配
    if [[ -f "$bin" ]] && head -c 4 "$bin" 2>/dev/null | grep -q 'ELF'; then
        return 0
    fi
    if [[ -f "$bin" ]]; then
        warn "迁移工具不是本机可执行格式（疑似其它平台的二进制，如 Windows PE）: $bin"
    else
        warn "迁移工具不存在: $bin"
    fi
    if dry_run "重新编译迁移工具 go build -o ridgericetalk-migrate ./cmd/migrate（覆盖非本机格式的二进制）"; then
        return 0
    fi
    info "尝试在服务器上重新编译迁移工具..."
    if (cd "$SERVER_DIR" && go build -o ridgericetalk-migrate ./cmd/migrate/); then
        if [[ -x "$bin" ]] && head -c 4 "$bin" 2>/dev/null | grep -q 'ELF'; then
            ok "迁移工具已重新编译为可执行格式"
            return 0
        fi
    fi
    fail "迁移工具不可用，且无法在服务器上编译（$bin）"
    info "请先执行: sudo $SCRIPT_NAME --stage build  以补齐 Go 工具链与二进制，再重试"
    return 1
}

# >>> H17-GUARD-BEGIN >>>（以下三个函数可被 scripts/tests/deploy-safety.test.sh 整段抽取做桩化测试，标记勿删）
# ============================================================
# H17 ①：源码树新鲜度闸门
# ============================================================
# 背景（2026-09-14 宁波生产机实测，H17）：
#   Stage 0 此前对「源码目录已存在」一律打印「源码已存在，跳过克隆」并继续，而生产机的
#   /opt/ridgericetalk 源码树停留在 2026-08-07（且**不是 git 仓库**）—— 于是 Stage 4
#   会拿这棵两个月前的树重新编译，**静默覆盖正在运行的正确二进制**，把服务端退回旧版本
#   （实测会丢失 H14 的 ValidateControlEvent 安全修复、H7 的建表修复、云文件预览/重命名/
#   移动、分享链接等近期改动）。
#   「源码已存在」≠「与预期构建来源一致」—— 本闸门补上后半个判断。
#
# 判定（source_tree_verdict，只判定、零副作用）：
#   missing            源码树不存在（走既有的克隆路径，本闸门不干预）
#   git-current        git 仓库且未落后远端                            → 放行
#   git-behind         git 仓库但落后远端                              → **拒绝**
#   git-unverifiable   git 仓库但取不到远端引用 / fetch 失败           → 无法判断
#   nongit-recorded    非 git 仓库且存在 .rrt-commit                   → **拒绝**
#                      （有确证：现行二进制由该 commit 的源码在**别处**构建后部署，
#                        本树不可能是它的源码）
#   nongit-unrecorded  非 git 仓库且无任何 provenance 记录             → 无法判断
#   unknown-*          环境不具备判定条件（如未安装 git）              → 无法判断
#
# 裁决动作（source_tree_guard）：
#   - 拒绝分支 → 打印**具体**原因 + 两条可行动路径，返回 1（调用方须 `|| exit 1`），
#     除非显式指定 --allow-stale-source；
#   - 无法判断 → 打印明确告警后放行（不阻断「手工 rsync 源码」这条脚本文档化的路径）；
#   - --dry-run 下只预告「真实执行时会拒绝」，不改退出码（dry-run 本就不会编译）。
# 为什么不在本脚本里自己 `git archive` 重建源码（对齐 deploy_server_binary.sh）：
#   该做法要求**本机有 git 仓库与可用远端**，而生产机的源码树不是仓库、仓库又是私有的
#   （无法在服务器上 clone）。故此处选择「拒绝 + 指向已验证的归档构建工具」，
#   而不是在服务器上再造一条构建路径。
#
# 判定用环境变量（供测试注入，生产勿改）：
#   RRT_SOURCE_BRANCH     用于比对的远端分支名（默认 $REPO_BRANCH）
#   RRT_SOURCE_SKIP_FETCH true 时不执行 git fetch（离线环境/测试用）
# ------------------------------------------------------------

# 现行二进制的构建批次记录（$1 = 部署目录；打印内容，无记录则无输出）
source_tree_recorded_commit() {
    local f="${1:-$DEPLOY_DIR}/server/.rrt-commit"
    [[ -f "$f" ]] || return 0
    tr -d '[:space:]' < "$f" 2>/dev/null | head -c 40 || true
}

# 源码树是否为 git 工作区
source_tree_is_git() {
    local dir="${1:-$DEPLOY_DIR}"
    command -v git >/dev/null 2>&1 || return 1
    [[ -n "$(git -C "$dir" rev-parse --is-inside-work-tree 2>/dev/null || true)" ]]
}

# 远端分支的 rev（优先 refs/remotes/origin/<branch>，其次上游 @{u}）；取不到返回 1
source_tree_remote_rev() {
    local dir="$1" branch="$2" rev=""
    rev="$(git -C "$dir" rev-parse --verify --quiet "refs/remotes/origin/${branch}" 2>/dev/null || true)"
    if [[ -z "$rev" ]]; then
        rev="$(git -C "$dir" rev-parse --verify --quiet '@{u}' 2>/dev/null || true)"
    fi
    [[ -n "$rev" ]] || return 1
    printf '%s' "$rev"
}

# 落后远端的提交数（取不到远端引用 / 无法计数则无输出）
source_tree_behind_count() {
    local dir="$1" branch="${2:-${RRT_SOURCE_BRANCH:-$REPO_BRANCH}}" remote_rev=""
    remote_rev="$(source_tree_remote_rev "$dir" "$branch" || true)"
    [[ -n "$remote_rev" ]] || return 0
    git -C "$dir" rev-list --count "HEAD..${remote_rev}" 2>/dev/null || true
}

# 源码树中最新的 *.go 修改日期（YYYY-MM-DD）—— 供日志直观看出树有多旧
source_tree_newest_go_date() {
    local dir="${1:-$DEPLOY_DIR}"
    find "$dir/server" -name '*.go' -printf '%TY-%Tm-%Td\n' 2>/dev/null | LC_ALL=C sort | tail -1 || true
}

# 源码树裁决（只判定，不打印任何东西）：打印上述裁决词之一
source_tree_verdict() {
    local dir="${1:-$DEPLOY_DIR}" branch="${RRT_SOURCE_BRANCH:-$REPO_BRANCH}"
    local remote_rev="" behind=""

    [[ -f "$dir/server/cmd/server/main.go" ]] || { printf 'missing\n'; return 0; }

    # git 不可用 → 无法做任何 VCS 判定（不能把「没装 git」误判成「不是 git 仓库」）
    if ! command -v git >/dev/null 2>&1; then
        printf 'unknown-git-missing\n'; return 0
    fi

    if source_tree_is_git "$dir"; then
        remote_rev="$(source_tree_remote_rev "$dir" "$branch" || true)"
        [[ -n "$remote_rev" ]] || { printf 'git-unverifiable\n'; return 0; }
        behind="$(git -C "$dir" rev-list --count "HEAD..${remote_rev}" 2>/dev/null || true)"
        [[ -n "$behind" ]] || { printf 'git-unverifiable\n'; return 0; }
        if [[ "$behind" -gt 0 ]]; then
            printf 'git-behind\n'
        else
            printf 'git-current\n'
        fi
        return 0
    fi

    # 非 git 仓库：唯一可用的“预期构建来源”证据是 .rrt-commit（现行二进制的构建批次）
    if [[ -n "$(source_tree_recorded_commit "$dir")" ]]; then
        printf 'nongit-recorded\n'
    else
        printf 'nongit-unrecorded\n'
    fi
}

# 刷新远端引用（只更新 refs，不动工作区；--dry-run / 离线时不执行）
source_tree_refresh() {
    local dir="${1:-$DEPLOY_DIR}"
    source_tree_is_git "$dir" || return 0
    [[ "${RRT_SOURCE_SKIP_FETCH:-false}" == "true" ]] && return 0
    if dry_run "git -C $dir fetch（只更新远端引用，不改工作区）"; then
        return 0
    fi
    # 注意 fetch 只更新远端引用，不触碰工作区文件 → 不会破坏现场
    if command -v timeout >/dev/null 2>&1; then
        timeout 60 git -C "$dir" fetch --quiet 2>/dev/null && return 0
    else
        git -C "$dir" fetch --quiet 2>/dev/null && return 0
    fi
    warn "git fetch 失败（网络不可达 / 无远端 / 私有仓库）：无法据此判断源码树是否落后"
    return 0
}

# 源码树闸门：打印裁决结论，必要时**拒绝继续**（返回 1）。
# 用法:  source_tree_guard "$DEPLOY_DIR" || exit 1
source_tree_guard() {
    local dir="${1:-$DEPLOY_DIR}" verdict="" recorded="" behind="" head_short="" newest=""

    source_tree_refresh "$dir"
    verdict="$(source_tree_verdict "$dir")"

    # ---- 放行分支 ----
    case "$verdict" in
        missing)
            return 0 ;;
        git-current)
            head_short="$(git -C "$dir" rev-parse --short HEAD 2>/dev/null || true)"
            ok "源码树为 git 仓库且未落后远端（HEAD=${head_short:-unknown}）"
            if [[ -n "$(git -C "$dir" status --porcelain 2>/dev/null || true)" ]]; then
                warn "源码树工作区有未提交改动：本次编译产物无法对应任何 commit（建议核对）"
            fi
            return 0 ;;
        git-unverifiable)
            head_short="$(git -C "$dir" rev-parse --short HEAD 2>/dev/null || true)"
            newest="$(source_tree_newest_go_date "$dir")"
            warn "无法确认源码树是否为最新：git 仓库但取不到 origin/${RRT_SOURCE_BRANCH:-$REPO_BRANCH} 引用 / fetch 失败"
            info "本树: $dir（HEAD=${head_short:-unknown}，最新 .go 改动 ${newest:-未知}）"
            info "陈旧源码树会用旧源码覆盖运行中的二进制（H17）；请人工核对 HEAD 后再继续"
            info "若本机取不到远端，请改用归档构建: bash dev-scripts/deploy/deploy_server_binary.sh <commit>"
            return 0 ;;
        unknown-*)
            warn "源码树裁决不可用（$verdict）：无法校验源码树是否为预期构建来源"
            info "请人工核对: $dir"
            return 0 ;;
        nongit-unrecorded)
            newest="$(source_tree_newest_go_date "$dir")"
            warn "源码树不是 git 仓库，且没有任何构建批次记录 —— 无法校验它是否与预期构建来源一致"
            info "本树: $dir（最新 .go 改动 ${newest:-未知}）"
            info "本脚本将就这棵树编译（手工 rsync 源码的首次部署属于此情形，故不阻断）"
            info "若这棵树可能陈旧，请改用归档构建: bash dev-scripts/deploy/deploy_server_binary.sh <commit>"
            return 0 ;;
    esac

    # ---- 拒绝分支（陈旧 / 与预期构建来源不一致）----
    if [[ "$verdict" == "git-behind" ]]; then
        behind="$(source_tree_behind_count "$dir" || true)"
        head_short="$(git -C "$dir" rev-parse --short HEAD 2>/dev/null || true)"
        fail "源码树已落后远端 ${behind:-?} 个提交：继续编译会用旧源码覆盖运行中的二进制（H17），拒绝并退出"
        info "本树: $dir（HEAD=${head_short:-unknown}）"
        info "两条可行动路径（任选其一）："
        info "  1) 先更新源码树再重跑: sudo git -C $dir pull --ff-only"
        info "  2) 用归档构建升级: bash dev-scripts/deploy/deploy_server_binary.sh <commit>"
        info "     （在开发机执行；git archive 打包指定 commit 上传到全新目录编译，绕开源码树）"
    elif [[ "$verdict" == "nongit-recorded" ]]; then
        recorded="$(source_tree_recorded_commit "$dir")"
        newest="$(source_tree_newest_go_date "$dir")"
        fail "源码树不是 git 仓库，但存在构建批次记录 .rrt-commit=${recorded:-?}：现行二进制由该 commit 的源码在**别处**构建后部署，本树不是它的源码（H17），拒绝并退出"
        info "本树: $dir（最新 .go 改动 ${newest:-未知}）"
        info "继续在这棵树上编译，会把服务端静默退回旧版本（实测丢失 H14 安全修复、H7 建表修复等）"
        info "两条可行动路径（任选其一）："
        info "  1) 用归档构建升级（推荐）: bash dev-scripts/deploy/deploy_server_binary.sh ${recorded:-<commit>}"
        info "     （在开发机执行；本机与开发机均无需可用源码树，最稳）"
        info "  2) 若确实要让本脚本在这台机器上编译：删除陈旧源码树后重跑以重新克隆"
        info "     sudo rm -rf $dir && sudo $SCRIPT_NAME --repo-url <url>"
    else
        warn "源码树裁决未知（$verdict）—— 无法判断是否为预期构建来源"
        return 0
    fi

    info "已人工核对「这棵树就是本次要发布的源码」时，可加 --allow-stale-source 显式放行"
    if [[ "$ALLOW_STALE_SOURCE" == "true" ]]; then
        warn "已用 --allow-stale-source 显式放行：本次将就用这棵源码树编译（风险自负）"
        return 0
    fi
    if [[ "$DRY_RUN" == "true" ]]; then
        info "（--dry-run：本次不中止；真实执行时会在 Stage 0 拒绝并退出）"
        return 0
    fi
    return 1
}

# ============================================================
# H17 ②：编译前备份现行服务端二进制（保留可回滚性）
# ============================================================
# 背景（2026-09-14 实测）：Stage 4 直接 `go build -o ridgericetalk` 覆盖**正在运行**的
# 二进制，且不留任何备份 —— 一旦编译所用源码树不对（见 ①），现场没有可回滚的二进制，
# 只能重新构建才能恢复（实测正是如此）。
# 命名 / 权限 / md5 旁文件口径与 deploy_server_binary.sh 和 rollback-binary.sh
# **完全一致**（不复用同一函数是因为那两处各有一份，跨文件抽公共库不在本次范围）：
#   /var/backups/ridgericetalk/bin/ridgericetalk-bin-<UTC yyyymmdd-HHMMSS>-<md5前8位>（root:root 600）
#   + 同名 .md5 旁文件（内容为备份自身 md5，root:root 600）
# 这样 `rollback-binary.sh --list` 能看到本脚本写下的备份，形成「部署 → 回滚」闭环。
# 返回 0 = 已备份，或本来就没有现行二进制（全新部署）；返回 1 = 备份失败（调用方须中止编译）。
# 备份目录可用 RRT_BIN_BACKUP_DIR 覆盖（与 rollback-binary.sh 同名口径）。
backup_current_binary() {
    local bin="${1:-$SERVER_DIR/ridgericetalk}"
    local backup_dir="${RRT_BIN_BACKUP_DIR:-/var/backups/ridgericetalk/bin}"
    local md5="" ts="" dest="" got=""

    if [[ ! -f "$bin" ]]; then
        info "无现行二进制可备份（全新部署）: $bin"
        return 0
    fi

    md5="$(md5sum "$bin" 2>/dev/null | awk '{print $1}' || true)"
    if [[ -z "$md5" ]]; then
        fail "无法计算现行二进制 md5，备份无法建立: $bin"
        return 1
    fi

    ts="$(date -u +%Y%m%d-%H%M%S)"
    dest="$backup_dir/ridgericetalk-bin-${ts}-${md5:0:8}"

    if ! mkdir -p "$backup_dir" 2>/dev/null; then
        fail "备份目录无法创建: $backup_dir"
        return 1
    fi

    # 读取运行中的二进制是安全的（只有**写**正在执行的文件才会 ETXTBSY）
    if ! cp "$bin" "$dest" 2>/dev/null; then
        fail "备份现行二进制失败（cp 失败）: $bin -> $dest"
        rm -f -- "$dest" "$dest.md5" 2>/dev/null || true
        return 1
    fi
    if ! chown root:root "$dest" 2>/dev/null; then
        warn "chown root:root 失败（备份已生成但属主非 root，存在被服务用户篡改的风险）: $dest"
    fi
    chmod 600 "$dest" 2>/dev/null || true

    # 必须校验备份与源一致（rollback-binary.sh 的核心教训：不校验就会在没备成时报成功）
    got="$(md5sum "$dest" 2>/dev/null | awk '{print $1}' || true)"
    if [[ -z "$got" || "$got" != "$md5" ]]; then
        fail "备份 md5 校验不一致：期望 $md5，实际 ${got:-?}（已删除该不可信备份）"
        rm -f -- "$dest" "$dest.md5" 2>/dev/null || true
        return 1
    fi

    if ! printf '%s\n' "$got" > "$dest.md5" 2>/dev/null; then
        warn "写入 md5 旁文件失败: $dest.md5（回滚时仍会校验实际 md5）"
    fi
    chown root:root "$dest.md5" 2>/dev/null || true
    chmod 600 "$dest.md5" 2>/dev/null || true

    ok "编译前已备份现行二进制: $dest（md5=$md5，root:root 600，含 .md5 旁文件）"
    info "如需回滚本次构建: sudo $SERVER_DIR/scripts/rollback-binary.sh --list"
    return 0
}

# ============================================================
# H17 ③：编译前校验版本注入不会静默失效
# ============================================================
# 背景（2026-09-14，与 H17 同源）：Stage 4 用
#   -ldflags "-X ridgericetalk/core/version.Server=<版本>+<短commit> -X …Commit=… -X …BuildTime=…"
# 注入构建元信息，但 `-X` **只对字符串变量有效**；若源码树的 version.go 里是
# `const Server`（M7 之前的形态，2026-09-12 前），`-X` 既不报错也不生效 ——
# 编译出的二进制版本号退回纯 0.2.2、health 也没有 commit/buildTime，**构建批次不可辨识**。
# 更关键的是：该形态本身就是「源码树早于 M7」的确证，即陈旧树的旁证。
# 处置：**告警但继续**（版本号只是元信息，缺了不影响功能，不值得为它中断部署），
# 但必须在日志里让人看见，绝不静默。
# 读取 version.go 中某个符号的声明形态：var | const | missing
version_symbol_kind() {
    local file="$1" sym="${2:-Server}" line=""
    [[ -f "$file" ]] || { printf 'missing\n'; return 0; }
    # 只匹配「顶层声明」两种写法：`var Server = "…"` 与 `const Server = "…"`
    line="$(grep -E "^[[:space:]]*(var|const)[[:space:]]+${sym}[[:space:]]*=" "$file" 2>/dev/null | head -1 || true)"
    [[ -n "$line" ]] || { printf 'missing\n'; return 0; }
    line="$(printf '%s' "$line" | sed -E 's/^[[:space:]]*(var|const).*/\1/')"
    printf '%s\n' "$line"
}

# 校验版本注入可行性；总是返回 0（告警但不中断），并把结论写入全局
# RRT_VERSION_INJECTION_OK（供编译后复述：避免「以为注入了、实际没注入」）。
verify_version_injection() {
    local file="${1:-$SERVER_DIR/core/version/version.go}"
    local k_server="" k_commit="" k_buildtime=""

    RRT_VERSION_INJECTION_OK=false

    if [[ ! -f "$file" ]]; then
        warn "未找到 $file，无法校验版本注入：本次构建的版本号将回退为默认值（不可辨识）"
        return 0
    fi

    k_server="$(version_symbol_kind "$file" Server)"
    k_commit="$(version_symbol_kind "$file" Commit)"
    k_buildtime="$(version_symbol_kind "$file" BuildTime)"

    if [[ "$k_server" == "var" && "$k_commit" == "var" && "$k_buildtime" == "var" ]]; then
        RRT_VERSION_INJECTION_OK=true
        info "版本注入校验通过（$file: var Server/Commit/BuildTime）"
        return 0
    fi

    if [[ "$k_server" == "const" ]]; then
        warn "core/version/version.go 是 **const Server** —— -ldflags -X 对常量静默无效，本次构建的版本号将不可辨识"
        info "原因: Go 的 -X 只能覆盖字符串**变量**；对 const 既不报错也不生效（编译照常成功）"
        info "后果: /api/health 只有纯 0.2.2，没有 commit/buildTime，部署批次无法辨识"
        info "旁证: 该形态是 M7（2026-09-12）之前的版本 → **这棵源码树早于 2026-09-12**，高度疑似陈旧树（H17）"
        info "建议: 改用归档构建 sudo bash dev-scripts/deploy/deploy_server_binary.sh <commit>（版本可辨识）"
    elif [[ "$k_server" == "missing" ]]; then
        warn "在 $file 中未找到 var/const Server 声明：版本号将回退为默认 0.2.2（不可辨识）"
    fi
    if [[ "$k_commit" == "missing" || "$k_buildtime" == "missing" ]]; then
        warn "未找到 Commit/BuildTime 变量声明 → /api/health 将不含 commit/buildTime 字段（部署批次不可辨识）"
    fi
    # 收口一句话，确保日志里**一定**有一条「本次构建的版本可辨识性」结论，不被上面细节淹没
    if [[ "$k_server" != "var" ]]; then
        warn "本次构建的版本号将不可辨识（来源: $file）；功能不受影响，但这说明源码树与预期构建来源可能不一致"
    else
        warn "本次构建的版本号仍可注入，但 /api/health 缺少 commit/buildTime（部署批次不可辨识）"
    fi
    return 0
}
# <<< H17-GUARD-END <<<

# ============================================================
# H-4 ②（DES-2026-0912-04 P2-2）：Admin 前端源码指纹
# ============================================================
# 背景：Stage 4 此前对「webhost/dist/{voice,admin}/index.html 都存在」一律整体跳过
#   前端构建 —— 升级服务端源码后 web/admin 已更新，管理页产物却停留在上一次部署
#   的版本（审计 P2-2：静默不一致，「升级了服务端但管理页还是旧版」）。
# 现按 admin 源码指纹决定是否重建：
#   指纹与上次构建一致 → 跳过（打印明确提示与 --rebuild-admin 出口）；
#   指纹变化 / 无指纹记录 / 显式 --rebuild-admin → 重建，成功后把新指纹写入
#   $SERVER_DIR/.rrt-admin-src-hash（供下次比对）。
# 指纹记录放在 $SERVER_DIR（与 .rrt-commit 同级、同风格），**不放** webhost/dist/admin/
# 里面 —— 那是 adminEngine 的静态服务目录（routes.go /admin/*filepath 直接映射），
# 多一个文件就多一个可被 HTTP 拉到的端点。

# admin 源码树指纹：对源码文件集合（路径 + 内容）做稳定哈希。
# - 排除 node_modules/dist/.git：依赖与构建产物不参与指纹，产物目录自身变化不会
#   造成「永远不相等」；
# - 路径列表与逐文件内容都参与：增删文件、只改内容、只改名均可检出；
# - 源码目录不存在 / 找不到任何文件 → 返回空串（调用方据此走「无法比对」分支）。
admin_src_fingerprint() {
    local src="$1"
    [[ -d "$src" ]] || return 0
    find "$src" -type f \
        -not -path '*/node_modules/*' \
        -not -path '*/dist/*' \
        -not -path '*/.git/*' \
        -print0 2>/dev/null \
        | LC_ALL=C sort -z \
        | xargs -0 -r md5sum 2>/dev/null \
        | md5sum \
        | awk '{print $1}'
}

# 读取上次构建时记录的 admin 源码指纹（无记录 → 返回空串）
admin_recorded_hash() {
    local f="${1:-$SERVER_DIR}/.rrt-admin-src-hash"
    [[ -f "$f" ]] || return 0
    tr -d '[:space:]' < "$f" 2>/dev/null | head -c 64 || true
}

# 自动配置防火墙（放行 API/Admin/LiveKit/EasyTier 端口）
configure_firewall() {
    # 除 5 个主端口外，必须一并放行 EasyTier 的 UDP 监听端口（默认 5007）；
    # 此前只在 Stage 7.7 打印一句提示，导致自建防火墙的主机上 VPN 组网不可达（D10）。
    local ports=("$PORT_API/tcp" "$PORT_ADMIN/tcp" "$PORT_LK_WS/tcp" "$PORT_LK_TCP/tcp" "$PORT_LK_UDP/udp" "$EASYTIER_PORT/udp")
    # N28：--enable-turn 时追加 TURN 端口（默认关 → 端口清单与改动前一致）
    if [[ "$ENABLE_TURN" == "true" ]]; then
        ports+=("${TURN_UDP_PORT}/udp" "${TURN_TLS_PORT}/tcp")
    fi

    # ufw
    if command -v ufw &>/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
        info "检测到 ufw，自动放行端口..."
        for p in "${ports[@]}"; do
            ufw allow "$p" comment "RidgeRiceTalk" >/dev/null 2>&1 || true
        done
        ok "ufw 规则已添加"
        return
    fi

    # firewalld
    if command -v firewall-cmd &>/dev/null && systemctl is-active firewalld &>/dev/null; then
        info "检测到 firewalld，自动放行端口..."
        for p in "${ports[@]}"; do
            firewall-cmd --permanent --add-port="$p" >/dev/null 2>&1 || true
        done
        firewall-cmd --reload >/dev/null 2>&1 || true
        ok "firewalld 规则已添加"
        return
    fi

    # iptables（仅当没有 ufw/firewalld 时尝试）
    if command -v iptables &>/dev/null; then
        info "检测到 iptables，自动放行端口..."
        for spec in "${ports[@]}"; do
            local port proto
            port=$(echo "$spec" | cut -d/ -f1)
            proto=$(echo "$spec" | cut -d/ -f2)
            iptables -C INPUT -p "$proto" --dport "$port" -j ACCEPT 2>/dev/null || \
                iptables -A INPUT -p "$proto" --dport "$port" -j ACCEPT
        done
        ok "iptables 规则已添加（当前会话有效，重启后建议使用 iptables-persistent 持久化）"
        warn "iptables 规则未持久化，建议安装 iptables-persistent 或改用 ufw/firewalld"
        return
    fi

    warn "未检测到受支持的防火墙工具（ufw/firewalld/iptables），请手动放行端口: ${ports[*]}"
}

# 检查端口是否被占用
check_port_used() {
    local port="$1"
    local name="$2"
    local port_check=""
    if command -v ss &>/dev/null; then
        port_check="ss -tlnp"
    elif command -v netstat &>/dev/null; then
        port_check="netstat -tlnp"
    fi
    if [[ -n "$port_check" ]]; then
        if $port_check 2>/dev/null | grep -q ":$port "; then
            warn "端口 $port ($name) 已被占用"
        else
            ok "端口 $port ($name) 可用"
        fi
    fi
}

# ------------------------------------------------------------
# HTTPS 配置（--tls selfsigned|acme，DES-2026-0912-03 §3.1-5 / D3/D4）
# ------------------------------------------------------------
# 两档共用约定：
#   - 仅在 Stage 9 服务已启动、$ENV_FILE 已生成后执行（TLS 反代依赖既有端口配置）；
#   - 成功后把全局 RRT_URL_SCHEME 置为 https，部署收尾的访问地址随之切换为 https；
#   - --dry-run 下只打印计划、不执行任何动作（零副作用，对齐既有 dry_run 用法）。
# selfsigned 档：收纳 setup-lan-https.sh 的既有能力（自签 SAN 证书 + Nginx 反代 +
#   .env 改写 + 服务重启）——该脚本此前从未被主流程引用（D3/D24）。nginx/openssl
#   依赖在此先装齐：setup-lan-https.sh 内部只写了 apt-get 安装分支，缺依赖时在
#   非 Debian 系上会走到那里失败，故主流程必须保证调用前依赖就位。
# acme 档：certbot nginx 插件优先（自动把正式证书接入反代），无 nginx 时
#   --standalone（80 端口须空闲，证书需手工接入前端）。

# 自签名档：SAN 覆盖 主机 IP + --domain（可选） + localhost
tls_setup_selfsigned() {
    # 前置依赖：nginx（反代）与 openssl（签证书）。缺哪个装哪个，走能力层抽象。
    if ! command -v nginx &>/dev/null; then
        if dry_run "安装 nginx（HTTPS 反代依赖）"; then :; else
            info "安装 nginx..."
            rrt_pkg_install nginx >/dev/null
        fi
    fi
    if ! command -v openssl &>/dev/null; then
        if dry_run "安装 openssl（自签证书依赖）"; then :; else
            info "安装 openssl..."
            rrt_pkg_install openssl >/dev/null
        fi
    fi

    local lan_ip=""
    lan_ip="$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -v '^$' | grep -v '^127\.' | grep -v '^169\.254\.' | head -1 || true)"
    local lan_args=()
    [[ -n "$lan_ip" ]] && lan_args=(--lan-ip "$lan_ip")
    local domain_args=()
    [[ -n "$TLS_DOMAIN" ]] && domain_args=(--domain "$TLS_DOMAIN")

    if dry_run \
        "生成自签名 SAN 证书（SAN: IP=${lan_ip:-自动检测}${TLS_DOMAIN:+, DNS=$TLS_DOMAIN}, DNS=localhost）" \
        "调用 setup-lan-https.sh 生成并启用 Nginx HTTPS 反代（443 → API/Admin/LiveKit WS/业务 WS）" \
        "改写 $ENV_FILE：RRT_PUBLIC_ADDRESS/RRT_LIVEKIT_PUBLIC_URL/RRT_CORS_ORIGINS/RRT_COOKIE_SECURE 切换为 https/wss" \
        "重启 nginx 与 ridgericetalk/livekit 服务"; then
        return 0
    fi

    info "调用 $SCRIPT_DIR/setup-lan-https.sh 生成自签名证书与 Nginx 反代..."
    # --force：重跑部署时用新的 IP/域名参数覆盖旧证书与反代配置（幂等更新）
    bash "$SCRIPT_DIR/setup-lan-https.sh" --force ${lan_args[@]+"${lan_args[@]}"} ${domain_args[@]+"${domain_args[@]}"}
    RRT_URL_SCHEME="https"
    ok "自签名 HTTPS 配置完成（浏览器首次访问需信任证书；客户端可导入 /etc/nginx/ssl/ridgericetalk.crt）"
}

# ACME 档：Let's Encrypt 正式证书（--domain 必填已在参数解析后校验）
tls_setup_acme() {
    if ! command -v certbot &>/dev/null; then
        if dry_run "安装 certbot 与 nginx 插件（ACME 证书签发依赖）"; then :; else
            info "安装 certbot..."
            rrt_pkg_install certbot >/dev/null
        fi
    fi

    warn "ACME HTTP-01 验证要求 80/443 端口公网可达（云服务器请在安全组放行 TCP 80/443）"

    if command -v nginx &>/dev/null; then
        # nginx 插件路径：先由 selfsigned 档建立基础 443 反代（server_name 含域名），
        # certbot --nginx 再为该 vhost 签发正式证书并替换自签证书（HTTP-01 challenge
        # 由插件临时改写 nginx 配置完成，续期由 certbot.timer 自动管理）。
        info "检测到 nginx：先建立基础 HTTPS 反代，再由 certbot 换发正式证书..."
        tls_setup_selfsigned
        if dry_run "certbot --nginx -d $TLS_DOMAIN --non-interactive（签发 Let's Encrypt 证书并自动接入 Nginx）"; then
            return 0
        fi
        info "向 Let's Encrypt 申请证书: $TLS_DOMAIN ..."
        if certbot --nginx -d "$TLS_DOMAIN" --non-interactive --agree-tos -m "admin@${TLS_DOMAIN}" --redirect; then
            ok "Let's Encrypt 证书已签发并接入 Nginx（自动续期由 certbot.timer 管理）"
            RRT_URL_SCHEME="https"
        else
            warn "certbot 签发失败（基础自签名 HTTPS 反代仍可用，浏览器需手动信任证书）"
            info "常见原因: 80/443 未放行、域名未解析到本机、DNS 记录未生效"
            info "可稍后手动重试: sudo certbot --nginx -d $TLS_DOMAIN"
        fi
    else
        # standalone：无 nginx 时用 certbot 临时内置 Web 服务器在 80 端口完成验证（须空闲）
        if dry_run "certbot certonly --standalone -d $TLS_DOMAIN（80 端口须空闲；签发后需手动接入反向代理）"; then
            return 0
        fi
        info "未检测到 nginx：使用 certbot standalone 模式签发证书（80 端口须空闲）..."
        if certbot certonly --standalone -d "$TLS_DOMAIN" --non-interactive --agree-tos -m "admin@${TLS_DOMAIN}"; then
            ok "证书已签发: /etc/letsencrypt/live/$TLS_DOMAIN/"
            warn "standalone 模式未自动配置反向代理，需将证书接入 Nginx/Caddy 等前端后网页端语音才可用"
            info "  证书: /etc/letsencrypt/live/$TLS_DOMAIN/fullchain.pem"
            info "  私钥: /etc/letsencrypt/live/$TLS_DOMAIN/privkey.pem"
            info "  也可先安装 nginx 再重跑本脚本 --tls acme --domain $TLS_DOMAIN（走 nginx 插件自动接入）"
        else
            fail "certbot standalone 签发失败"
            info "常见原因: 80 端口被占用、80/443 未放行、域名未解析到本机"
            exit 1
        fi
    fi
}

# ------------------------------------------------------------
# 临时数据库口令 /tmp/.rrt_pg_pwd 的兜底清理（D20）
# ------------------------------------------------------------
# 该文件含**明文** DB 口令：Stage 5 生成新口令时写入，供 Stage 5.5/6 读取。
# 此前只有脚本最末尾的「收尾」会按 PG_PWD_WRITTEN 删除 —— 一旦中途被 Ctrl-C
# 或 set -e 中止，明文文件就永久残留在 /tmp；更糟的是后续 `--stage config`
# 会把它当作权威口令来源读取（陈旧口令隐患）。
# 这里补 EXIT/INT/TERM 兜底，但保留一条关键约束：
#   只有本次生成的口令已经落到 .env.production（0600，非 /tmp 明文）后，
#   才删除 /tmp 下的明文副本。
# 原因：若尚未落盘（例如 Stage 5 刚写完就被中断），该明文文件是这条新口令的
# 唯一来源，删掉会让「--stage db → --stage config」续跑彻底失败（库口令已改、
# 配置却没有）—— 这正是 known_issues H11 反复强调不能破坏的语义。故此处按
# 「标志 + 已落盘」双条件清理。
cleanup_pg_pwd_tmp() {
    [[ "$PG_PWD_WRITTEN" == "true" ]] || return 0
    [[ -f /tmp/.rrt_pg_pwd ]] || return 0
    if [[ -f "$ENV_FILE" ]] && grep -qE '^POSTGRES_PASSWORD=.+' "$ENV_FILE" 2>/dev/null; then
        rm -f /tmp/.rrt_pg_pwd 2>/dev/null || true
    fi
    return 0
}
# 注：Stage 7 内部会另行设置一个 EXIT trap（清理 mktemp 渲染产物），它会覆盖
# 这里注册的 EXIT 处理器；但 INT/TERM 兜底仍然有效，Stage 7 的 trap 也已链式
# 调用 cleanup_pg_pwd_tmp（见该处），脚本末尾「收尾」亦按标志删除。
trap cleanup_pg_pwd_tmp EXIT
# 显式 exit：设置 INT/TERM trap 会覆盖「默认终止」行为，若不 exit，Ctrl-C 后
# 脚本会继续往下跑。130 = 128 + SIGINT。
trap 'cleanup_pg_pwd_tmp; exit 130' INT TERM

# ============================================================
# 前置检查（始终执行）
# ============================================================
banner "RidgeRiceTalk 裸机部署"

# 1. root 检查
if [[ $EUID -ne 0 ]]; then
    fail "此脚本必须以 root 身份运行"
    info "请使用: sudo $SCRIPT_NAME"
    exit 1
fi
ok "以 root 身份运行"

# 2. 操作系统检查
if [[ ! -f /etc/os-release ]]; then
    fail "无法检测操作系统（/etc/os-release 不存在）"
    exit 1
fi
# shellcheck source=/dev/null
source /etc/os-release
# 探测改走能力层（lib/os.sh），闸门取值与改造前一致（ID → OS_ID）。
OS_ID="$(rrt_os_id)"
# 发行版闸门（D2 放宽，DES-2026-0912-03 §3.1-2 / §3.2）：
#   - Debian 系：debian/ubuntu 及其衍生（Deepin/UOS/麒麟/Mint/Pop/Kali/Raspbian）
#     —— 均有 apt，能力层 apt 后端直接可用；
#   - RHEL 系：Rocky/AlmaLinux/CentOS/Fedora/RHEL/OL —— 走 dnf 抽象
#     （lib/pkg.sh 包名映射表，无 dnf 的老版本回退 yum）。
# Alpine 及其它未知发行版仍拒绝（D2 决策 ①：本次只覆盖 Debian 系衍生 + RHEL 系）。
case "$OS_ID" in
    debian|ubuntu|deepin|uos|kylin|linuxmint|pop|kali|raspbian)
        ok "操作系统（Debian 系）: ${PRETTY_NAME:-$OS_ID}"
        rrt_gate_version_guard || exit 1
        ;;
    rocky|almalinux|centos|fedora|rhel|ol)
        ok "操作系统（RHEL 系）: ${PRETTY_NAME:-$OS_ID}"
        rrt_gate_version_guard || exit 1
        ;;
    *)
        fail "不支持的发行版: ${PRETTY_NAME:-$OS_ID} (ID=$OS_ID${ID_LIKE:+, ID_LIKE=$ID_LIKE})"
        info "当前支持: Debian 11+/Ubuntu 20.04+ 及 Debian 系衍生（Deepin/UOS/麒麟/Mint/Pop/Kali/Raspbian）"
        info "          RHEL 系（Rocky 9+/AlmaLinux/CentOS Stream 9+/Fedora/RHEL/OL）"
        info "Alpine 等其它发行版的支持见 DES-2026-0912-03 后续批次"
        exit 1
        ;;
esac

# 3. 磁盘空间检查（≥5GB）
AVAILABLE_KB=$(df -k / | awk 'NR==2 {print $4}')
REQUIRED_KB=$((5 * 1024 * 1024))
if [[ $AVAILABLE_KB -lt $REQUIRED_KB ]]; then
    fail "磁盘空间不足: 需要 ≥5GB，当前可用 $((AVAILABLE_KB / 1024))MB"
    exit 1
fi
ok "磁盘空间充足: 可用 $((AVAILABLE_KB / 1024))MB"

info "部署目录: $DEPLOY_DIR"
if [[ $STAGE_START -gt 1 ]]; then
    info "从阶段 $STAGE_START/9 恢复执行"
fi

# ------------------------------------------------------------
# 清理上一次运行残留的临时数据库口令文件（陈旧口令隐患）
# ------------------------------------------------------------
# 仅当 .env.production 已存在且含非空 POSTGRES_PASSWORD 时清理：
# 此时配置文件中的口令才是权威口令，任何残留的 /tmp/.rrt_pg_pwd 都是陈旧值；
# 且 Stage 5 会复用配置口令、不再写该文件，删除它不影响本次运行。
# （新装场景 .env.production 不存在 → 保留 /tmp，供 --stage db 后的 --stage config 续跑）
if [[ -f "$ENV_FILE" ]] && grep -qE "^POSTGRES_PASSWORD=.+" "$ENV_FILE" 2>/dev/null; then
    if [[ -f /tmp/.rrt_pg_pwd ]] && ! dry_run "删除上一次运行残留的陈旧临时口令 /tmp/.rrt_pg_pwd"; then
        rm -f /tmp/.rrt_pg_pwd
        info "已清理上一次运行残留的 /tmp/.rrt_pg_pwd（陈旧口令隐患）"
    fi
fi

# ============================================================
# 交互式向导（无 --public-ip 时触发，新手友好）
# 触发条件用 -c /dev/tty 而非 -t 0，以支持 curl|bash 管道模式
# （管道模式下 stdin 不是 TTY，但 /dev/tty 字符设备仍可用）。
#
# ⚠️ 这里**刻意不用 rrt_can_prompt**（即「无控制终端时也照常进入向导」）：
#   非交互环境（CI/Ansible/非交互 SSH）下，向导的各提问会打印「采用默认值」说明，
#   随后在 [4/4]「确认开始部署？」处由 read_prompt_required **明确报错退出** ——
#   这正是期望行为：无 tty 时既不崩在第一步，也不静默按默认值继续改生产。
#   若把本条件收紧成 rrt_can_prompt，非交互运行会**整段跳过向导**（连确认都不再问），
#   就退化成「无人值守静默采用默认端口/默认 IP」，与 N1 的修复意图相反。
#   要在无人值守下真的继续，请显式加 --yes。
# ============================================================
if [[ "$DRY_RUN" == "true" && -z "$PUBLIC_IP" && $STAGE_START -le 6 ]]; then
    info "[DRY-RUN] 将执行: 交互式部署向导（公网 IP / 端口 / 音乐机器人确认）—— --dry-run 下跳过，不读取任何输入"
fi
if [[ -z "$PUBLIC_IP" && $STAGE_START -le 6 && -c /dev/tty && "$DRY_RUN" != "true" ]]; then
    echo ""
    echo -e "${C_CYAN}========================================${C_RESET}"
    echo -e "${C_CYAN}  RidgeRiceTalk 部署向导${C_RESET}"
    echo -e "${C_CYAN}========================================${C_RESET}"
    echo ""
    echo -e "${C_WHITE}本向导将引导您完成部署配置。直接回车使用 [默认值]。${C_RESET}"
    echo ""

    # 0. 源码获取（如果 /opt/ridgericetalk 不存在）
    if [[ ! -f "$SERVER_DIR/cmd/server/main.go" && -z "$REPO_URL" ]]; then
        echo -e "${C_YELLOW}[0/4] 源码获取${C_RESET}"
        echo -e "  ${C_GRAY}部署目录 $DEPLOY_DIR 不存在${C_RESET}"
        echo -e "  ${C_GRAY}将自动从 GitHub 克隆官方仓库${C_RESET}"
        echo -e "  ${C_GRAY}仓库: $DEFAULT_REPO_URL${C_RESET}"
        read_prompt "  确认克隆？[Y/n]: " CLONE_CONFIRM
        if [[ "${CLONE_CONFIRM:-Y}" =~ ^[Nn] ]]; then
            echo -e "  ${C_YELLOW}已跳过自动克隆${C_RESET}"
            read_prompt "  请输入自定义仓库 URL（或回车退出）: " CUSTOM_REPO
            if [[ -n "$CUSTOM_REPO" ]]; then
                REPO_URL="$CUSTOM_REPO"
            else
                fail "未提供源码，无法继续部署"
                info "请使用 --repo-url <url> 指定源码仓库"
                exit 1
            fi
        else
            REPO_URL="$DEFAULT_REPO_URL"
            REPO_BRANCH="$DEFAULT_REPO_BRANCH"
            echo -e "  ${C_GREEN}将克隆: $REPO_URL (分支: $REPO_BRANCH)${C_RESET}"
        fi
        echo ""
    fi

    # 1. 公网 IP
    echo -e "${C_YELLOW}[1/4] 公网 IP 地址${C_RESET}"
    # G-2：探测收敛到能力层 lib/net.sh（境外服务 → 云元数据兜底），三处重复合一
    DETECTED_IP="$(rrt_public_ip || true)"
    if [[ -n "$DETECTED_IP" ]]; then
        echo -e "  检测到公网 IP: ${C_GREEN}$DETECTED_IP${C_RESET}"
        read_prompt "  请确认或输入新 IP [$DETECTED_IP]: " USER_IP
        PUBLIC_IP="${USER_IP:-$DETECTED_IP}"
    else
        echo -e "  ${C_RED}无法自动检测公网 IP${C_RESET}"
        echo -e "  ${C_GRAY}常见原因与对策（G-5）：${C_RESET}"
        echo -e "  ${C_GRAY}  · NAT/内网无公网出口 —— 直接指定: --public-ip <IP>${C_RESET}"
        echo -e "  ${C_GRAY}  · 受限网络出不去境外探测服务 —— 配代理: --http-proxy/--https-proxy${C_RESET}"
        echo -e "  ${C_GRAY}  · IPv6-only 主机 —— 本脚本按 IPv4 语义广播地址，请走反向代理方案${C_RESET}"
        read_prompt "  请输入服务器公网 IP: " PUBLIC_IP
        if [[ -z "$PUBLIC_IP" ]]; then
            fail "公网 IP 不能为空"
            info "或使用参数: sudo $SCRIPT_NAME --public-ip <IP>"
            exit 1
        fi
    fi
    echo ""

    # 2. 端口配置（默认端口；端口受限的 VPS 请显式指定）
    echo -e "${C_YELLOW}[2/4] 端口配置${C_RESET}"
    echo -e "  ${C_GRAY}以下为本脚本的默认端口（API/Admin/LiveKit 各自独立，并非 5 端口方案）。${C_RESET}"
    echo -e "  ${C_GRAY}端口受限的 VPS 需自行指定端口，并在云控制台放行相应 TCP/UDP。${C_RESET}"
    echo -e "  ${C_GRAY}如需按宁波那套（TCP 5000-5004 + UDP 5005-5009），请显式传 --port-api/--port-admin/--port-lk-*。${C_RESET}"
    read_prompt "  API 端口 [$PORT_API]: " USER_PORT
    [[ -n "$USER_PORT" ]] && PORT_API="$USER_PORT"
    read_prompt "  Admin 端口 [$PORT_ADMIN]: " USER_PORT
    [[ -n "$USER_PORT" ]] && PORT_ADMIN="$USER_PORT"
    read_prompt "  LiveKit WebSocket 端口 [$PORT_LK_WS]: " USER_PORT
    [[ -n "$USER_PORT" ]] && PORT_LK_WS="$USER_PORT"
    read_prompt "  LiveKit TCP 端口 [$PORT_LK_TCP]: " USER_PORT
    [[ -n "$USER_PORT" ]] && PORT_LK_TCP="$USER_PORT"
    read_prompt "  LiveKit UDP 端口 [$PORT_LK_UDP]: " USER_PORT
    [[ -n "$USER_PORT" ]] && PORT_LK_UDP="$USER_PORT"
    echo ""

    # 3. 是否启用音乐机器人
    echo -e "${C_YELLOW}[3/4] 音乐机器人${C_RESET}"
    read_prompt "  启用网易云音乐机器人？[Y/n]: " USER_CHOICE
    ENABLE_NETEASE=true
    [[ "$USER_CHOICE" =~ ^[Nn] ]] && ENABLE_NETEASE=false
    if [[ "$ENABLE_NETEASE" == "true" ]]; then
        echo -e "  ${C_GREEN}已启用${C_RESET}（将安装 NeteaseCloudMusicApi）"
    else
        echo -e "  ${C_YELLOW}已禁用${C_RESET}"
    fi
    echo ""

    # 4. 确认
    echo -e "${C_YELLOW}[4/4] 配置确认${C_RESET}"
    echo -e "  公网 IP:        ${C_GREEN}$PUBLIC_IP${C_RESET}"
    echo -e "  API 端口:       ${C_GREEN}$PORT_API${C_RESET}"
    echo -e "  Admin 端口:     ${C_GREEN}$PORT_ADMIN${C_RESET}"
    echo -e "  LiveKit WS:     ${C_GREEN}$PORT_LK_WS${C_RESET}"
    echo -e "  LiveKit TCP:    ${C_GREEN}$PORT_LK_TCP${C_RESET}"
    echo -e "  LiveKit UDP:    ${C_GREEN}$PORT_LK_UDP${C_RESET}"
    echo -e "  音乐机器人:     ${C_GREEN}$([ "$ENABLE_NETEASE" == "true" ] && echo "启用" || echo "禁用")${C_RESET}"
    echo ""
    # 唯一「必须由人拍板」的提问：无 tty 且未加 --yes 时明确报错退出（N1）
    read_prompt_required "确认开始部署？[Y/n]: " CONFIRM || exit 1
    if [[ "$CONFIRM" =~ ^[Nn] ]]; then
        echo "部署已取消"
        exit 0
    fi
    echo ""
fi

# ============================================================
# Stage 0/9: 克隆源码（可选）
# ============================================================
echo -e "\n${C_BLUE}[0/9]${C_RESET} ${C_CYAN}克隆源码...${C_RESET}"

if [[ -f "$SERVER_DIR/cmd/server/main.go" ]]; then
    ok "源码已存在，跳过克隆"
    # H17 ①：但「已存在」≠「与预期构建来源一致」。生产机的源码树曾停留在 2026-08-07，
    # 而本脚本的 Stage 4 会在这棵树上 go build 并覆盖运行中的二进制 → 静默降级。
    # 故此处必须过闸门：陈旧/来源不一致 → 明确拒绝（除非 --allow-stale-source）。
    source_tree_guard "$DEPLOY_DIR" || exit 1
elif [[ -z "$REPO_URL" ]]; then
    fail "部署目录不存在: $DEPLOY_DIR"
    info "请使用 --repo-url <url> 自动克隆，或手动 rsync 源码到 $DEPLOY_DIR"
    exit 1
elif dry_run "git clone $REPO_URL (分支 $REPO_BRANCH) 到 $DEPLOY_DIR（必要时先 apt 安装 git）"; then
    :  # dry-run：跳过克隆
else
    info "克隆源码: $REPO_URL (分支: $REPO_BRANCH)"
    if ! command -v git &>/dev/null; then
        info "git 未安装，先安装..."
        # G-2：走能力层包安装抽象（apt 后端展开结果与改造前逐字一致）
        rrt_pkg_update
        rrt_pkg_install git >/dev/null
    fi

    # 护栏（D19）：镜像轮换中克隆失败时会执行 `rm -rf "$DEPLOY_DIR"`。若 $DEPLOY_DIR
    # 原先就存在（例如源码不完整、或目录里有其它数据），整目录删除会误删现场。
    # 规则：
    #   - 目录不存在        → 克隆产物归本脚本所有，后续清理是安全的；
    #   - 目录存在且为空    → git clone 可直接使用（无需删除）；
    #   - 目录非空且无 --force → 拒绝自动删除，明确报错并给出指引；
    #   - 目录非空且 --force   → 显式允许删除（用户已明示）。
    if [[ -d "$DEPLOY_DIR" ]]; then
        if [[ -z "$(ls -A "$DEPLOY_DIR" 2>/dev/null)" ]]; then
            info "目标目录已存在但为空，克隆将直接使用: $DEPLOY_DIR"
        elif [[ "$FORCE" == "true" ]]; then
            warn "--force：删除已存在的非空目录 $DEPLOY_DIR 后重新克隆"
            rm -rf "$DEPLOY_DIR"
        else
            fail "目标目录已存在且非空，但不是完整源码（缺少 cmd/server/main.go）: $DEPLOY_DIR"
            info "为避免误删现场数据，脚本不会自动删除该目录。请二选一："
            info "  1) 手动清理后重试: sudo rm -rf $DEPLOY_DIR && sudo $SCRIPT_NAME --repo-url <url>"
            info "  2) 显式允许本脚本删除: sudo $SCRIPT_NAME --force --repo-url <url>"
            exit 1
        fi
    fi

    # GitHub 国内加速镜像列表（中国大陆服务器访问 GitHub 直连很慢）
    # 依次尝试，任一成功即可。镜像 URL 通过在 GitHub URL 前加前缀实现代理。
    # 注：经上面的护栏后，此处出现的 $DEPLOY_DIR 必为「本脚本本次克隆的产物」，
    # 因此下方失败清理 `rm -rf "$DEPLOY_DIR"` 不再有误删风险。
    CLONE_SUCCESS=false
    # G-5：镜像列表收敛到 lib/mirrors.sh（按 --mirror-preset 排序；auto=历史顺序）
    mapfile -t GITHUB_MIRRORS < <(rrt_mirror_urls github_clone)

    for MIRROR_PREFIX in "${GITHUB_MIRRORS[@]}"; do
        CLONE_URL="${MIRROR_PREFIX}${REPO_URL}"
        if [[ -n "$MIRROR_PREFIX" ]]; then
            info "尝试镜像加速: $MIRROR_PREFIX"
        else
            info "尝试直连 GitHub..."
        fi
        # --depth 1 浅克隆加速；超时 120 秒避免长时间卡住
        if timeout 120 git clone --depth 1 --branch "$REPO_BRANCH" "$CLONE_URL" "$DEPLOY_DIR" 2>&1; then
            ok "源码克隆完成: $DEPLOY_DIR"
            # 新克隆必然与远端同源，无需过闸门；但把批次打出来，便于日志辨识（H17 的「不静默」原则）
            info "克隆批次: HEAD=$(git -C "$DEPLOY_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)（分支 $REPO_BRANCH）"
            CLONE_SUCCESS=true
            break
        else
            warn "克隆失败（${MIRROR_PREFIX:-直连}），尝试下一个..."
            # 仅清理本脚本本次克隆的残留（护栏已保证不会误删现场目录）
            rm -rf "$DEPLOY_DIR" 2>/dev/null
        fi
    done

    if [[ "$CLONE_SUCCESS" != "true" ]]; then
        fail "所有克隆方式均失败"
        info "请检查网络或手动克隆源码到 $DEPLOY_DIR"
        info "也可使用国内镜像手动克隆: git clone https://ghproxy.com/$REPO_URL"
        exit 1
    fi
fi

# 修正 SCRIPT_DIR：curl|bash 模式下脚本通过 stdin 执行，无文件路径，
# SCRIPT_DIR 为当前工作目录。源码就位后，应指向 $SERVER_DIR/scripts，
# 以便 Stage 7 能找到 systemd 模板文件（*.service.tmpl）。
if [[ -d "$SERVER_DIR/scripts" ]]; then
    SCRIPT_DIR="$SERVER_DIR/scripts"
    info "脚本目录: $SCRIPT_DIR"
fi

# ============================================================
# Stage 1/9: 检测依赖
# ============================================================
if run_stage 1; then
    stage 1 "检测依赖..."

    # Go
    if command -v go &>/dev/null; then
        ok "Go: $(go version)"
    else
        warn "Go 未安装（将在 Stage 2 安装）"
    fi

    # PostgreSQL
    if command -v psql &>/dev/null; then
        ok "PostgreSQL: $(psql --version 2>/dev/null | head -1)"
    else
        warn "PostgreSQL 未安装（将在 Stage 2 安装）"
    fi

    # FFmpeg
    if command -v ffmpeg &>/dev/null; then
        ok "FFmpeg: $(ffmpeg -version 2>/dev/null | head -1)"
    else
        warn "FFmpeg 未安装（将在 Stage 2 安装）"
    fi

    # 基础工具（均为「命令」，可用 command -v 探测）
    for tool in curl jq openssl rsync wget; do
        if command -v "$tool" &>/dev/null; then
            ok "$tool: 已安装"
        else
            warn "$tool 未安装（将在 Stage 2 安装）"
        fi
    done

    # ca-certificates 是「包名」而不是命令，command -v 永远为假 —— 旧版把它混进上面的
    # 命令循环，于是永远误报「未安装」（D22）。这里改用包数据库探测（dpkg 缺失的
    # 发行版则跳过该检查，不误报）。
    if command -v dpkg >/dev/null 2>&1; then
        if dpkg -s ca-certificates >/dev/null 2>&1; then
            ok "ca-certificates: 已安装（dpkg -s 探测）"
        else
            warn "ca-certificates 未安装（将在 Stage 2 安装）"
        fi
    fi

    # check_port_used 依赖 ss 或 netstat，二者都缺失时该检查会静默跳过；
    # 这里补检测与友好提示（本次只提示，不改安装逻辑）。
    if command -v ss &>/dev/null; then
        ok "ss: 已安装（端口占用检测）"
    elif command -v netstat &>/dev/null; then
        ok "netstat: 已安装（端口占用检测）"
    else
        warn "ss/netstat 均未安装，将跳过端口占用检测（如需启用请安装 iproute2 或 net-tools）"
    fi

    # 本脚本自 Stage 5 起多处使用 sudo（如 sudo -u postgres psql）；缺失会到那时才失败。
    # 这里补检测与友好提示（本次只提示，不改安装逻辑）。
    if command -v sudo &>/dev/null; then
        ok "sudo: 已安装"
    else
        warn "sudo 未安装（Stage 5 起会用到 sudo -u postgres，请先安装 sudo）"
    fi

    # 端口占用检测
    check_port_used "$PORT_API" "API"
    check_port_used "$PORT_ADMIN" "Admin"
    check_port_used "$PORT_LK_WS" "LiveKit WS"
    check_port_used "$PORT_LK_TCP" "LiveKit TCP"
    check_port_used "$PORT_LK_UDP" "LiveKit UDP"

    ok "依赖检测完成"
fi

# ============================================================
# Stage 2/9: 安装系统依赖
# ============================================================
if run_stage 2; then
    stage 2 "安装系统依赖..."

    if ! dry_run \
        "更新包索引并安装 postgresql postgresql-contrib ffmpeg jq curl openssl rsync ca-certificates wget（包管理器按发行版解析：apt/dnf）" \
        "（RHEL 系）预装 EPEL 仓库（opusfile-devel 等依赖）" \
        "安装 Go $GO_VERSION 到 /usr/local/go，并写入 /etc/profile.d/go.sh" \
        "安装编译工具链（build-essential / gcc gcc-c++ make，CGO 编译依赖）" \
        "安装 pkg-config libopus-dev libopusfile-dev（RHEL 系解析为 pkgconf-pkg-config / opus-devel / opusfile-devel）" \
        "安装 Node.js 20.x 与 npm（NodeSource / npmmirror 镜像，必要时下载官方二进制）"; then

    # RHEL 系：预装 EPEL 仓库（opusfile-devel 等 EPEL 专属包依赖；EPEL 缺失时
    # 这些包 dnf 找不到）。Debian 系零动作。失败不阻断（后续个别包装不上会另行告警）。
    if rrt_os_is_rhel_family && ! rpm -q epel-release >/dev/null 2>&1; then
        info "RHEL 系系统：安装 EPEL 仓库（opusfile 等依赖）..."
        rrt_pkg_install epel-release >/dev/null 2>&1 || warn "EPEL 安装失败，若后续出现依赖找不到（如 opusfile-devel）请手动启用 EPEL"
    fi

    # 系统包（G-2：改走能力层抽象 lib/pkg.sh 的 rrt_pkg_install）
    # 只传「逻辑依赖名」，具体包名由映射表按发行版解析；apt 后端展开出的命令
    # 与改造前逐字一致（DEBIAN_FRONTEND=noninteractive apt-get install -y -qq …）。
    info "更新包索引..."
    rrt_pkg_update

    info "安装系统包: postgresql ffmpeg jq curl openssl rsync ca-certificates wget"
    rrt_pkg_install postgresql postgresql-contrib \
        ffmpeg \
        jq curl openssl rsync ca-certificates wget \
        > /dev/null

    ok "系统包安装完成"
    command -v psql &>/dev/null && ok "PostgreSQL: $(psql --version 2>/dev/null | head -1)"
    command -v ffmpeg &>/dev/null && ok "FFmpeg: $(ffmpeg -version 2>/dev/null | head -1)"

    # Go 安装
    # 判定口径：已装版本 >= GO_VERSION 即视为可用（D14）。此前要求「精确相等」，
    # 装了更高版本（如 1.26）也会被判为不匹配 → 重新下载安装，且旧的
    # /usr/local/go 会被无条件 rm -rf。
    NEED_GO_INSTALL=true
    if command -v go &>/dev/null; then
        GO_INSTALLED_VERSION=$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')
        info "已安装 Go: $GO_INSTALLED_VERSION（要求 ≥ $GO_VERSION）"
        # 用 sort -V 做语义版本比较：取两者中较小者，若较小者就是 GO_VERSION，
        # 说明 installed >= GO_VERSION → 满足要求，跳过安装。
        if [[ -n "$GO_INSTALLED_VERSION" ]] && \
           [[ "$(printf '%s\n%s\n' "$GO_VERSION" "$GO_INSTALLED_VERSION" | sort -V | head -1)" == "$GO_VERSION" ]]; then
            ok "Go 版本满足要求（$GO_INSTALLED_VERSION ≥ $GO_VERSION），跳过安装"
            NEED_GO_INSTALL=false
        else
            info "Go 版本不满足要求（$GO_INSTALLED_VERSION < $GO_VERSION），将安装 $GO_VERSION"
        fi
    fi

    if [[ "$NEED_GO_INSTALL" == "true" ]]; then
        # ARCH 已由脚本顶部安全探测（dpkg → uname -m 兜底），此处不再重复 dpkg 调用
        GO_TARBALL="go${GO_VERSION}.linux-${ARCH}.tar.gz"
        # G-5：镜像列表收敛到 lib/mirrors.sh（auto/cn=国内优先，global=官方优先）
        mapfile -t GO_MIRRORS < <(rrt_mirror_urls go_tarball "$GO_TARBALL")
        GO_TMP=$(mktemp -d)
        GO_DOWNLOADED=false

        for GO_URL in "${GO_MIRRORS[@]}"; do
            info "尝试下载 Go: $GO_URL"
            if curl -fSL --connect-timeout 15 --progress-bar -o "$GO_TMP/$GO_TARBALL" "$GO_URL"; then
                GO_DOWNLOADED=true
                break
            fi
            warn "下载失败，尝试下一个镜像..."
        done

        if [[ "$GO_DOWNLOADED" == "true" ]]; then
            # 解压目标 /usr/local/go 由 tar 覆盖写入。此前无条件 `rm -rf /usr/local/go`，
            # 若该目录并非本脚本所建（例如发行版/手工安装的 Go），会被直接删除（D14）。
            # 现在：仅当它确实是 Go 工具链目录（存在 VERSION 或 bin/go）时才「移开备份」
            # 而非删除；不是 Go 目录则保持不动并告警。
            if [[ -d /usr/local/go ]]; then
                if [[ -f /usr/local/go/VERSION || -x /usr/local/go/bin/go ]]; then
                    GO_DIR_BAK="/usr/local/go.bak.$(date +%s)"
                    mv /usr/local/go "$GO_DIR_BAK"
                    info "已将既有 /usr/local/go 移开备份: $GO_DIR_BAK（提取新版本前避免混入旧文件）"
                else
                    warn "/usr/local/go 存在但不是 Go 工具链目录，保留不动（新版本将解压合并到该目录）"
                fi
            fi
            tar -C /usr/local -xzf "$GO_TMP/$GO_TARBALL"
            rm -rf "$GO_TMP"
            # 写入 profile.d 以持久化 PATH 与 Go 模块代理（国内镜像优先）
            # PATH 必须前置 /usr/local/go/bin，避免系统旧版 Go 遮蔽本脚本安装的版本
            cat > /etc/profile.d/go.sh <<'GOEOF'
# Go language PATH (installed by deploy-baremetal.sh)
export PATH=/usr/local/go/bin:$PATH
export GOPATH=${GOPATH:-$HOME/go}
export PATH=$PATH:$GOPATH/bin
# 默认使用国内代理，避免中国大陆/受限网络下载失败；如已有 GOPROXY 则保留
export GOPROXY=${GOPROXY:-https://goproxy.cn,direct}
GOEOF
            chmod 644 /etc/profile.d/go.sh
            export PATH="/usr/local/go/bin:$PATH"
            export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
            ok "Go $GO_VERSION 安装完成: $(go version)"
        else
            rm -rf "$GO_TMP"
            fail "Go 下载失败，请手动安装 Go $GO_VERSION"
            exit 1
        fi
    fi

    # build-essential（CGO 编译需要 gcc/g++）
    info "安装 build-essential（CGO 编译需要）..."
    rrt_pkg_install build-essential >/dev/null 2>&1 || true
    if command -v gcc &>/dev/null; then
        ok "gcc: $(gcc --version | head -1)"
    else
        warn "gcc 不可用，TTS（sherpa-onnx）将不可用"
    fi

    # pkg-config + libopus-dev + libopusfile-dev：github.com/hraban/opus（CGO）需要 opus/opusfile 头文件与 pc 文件
    # 缺失会导致 `go build` 报错: exec: "pkg-config": executable file not found in $PATH
    # 或: Package 'opusfile', required by 'virtual:world', not found
    info "安装 pkg-config / libopus-dev / libopusfile-dev（hraban/opus CGO 依赖）..."
    rrt_pkg_install pkg-config libopus-dev libopusfile-dev >/dev/null 2>&1 || true
    if command -v pkg-config &>/dev/null && pkg-config --exists opusfile 2>/dev/null; then
        ok "pkg-config: opus=$(pkg-config --modversion opus 2>/dev/null || echo '?') opusfile=$(pkg-config --modversion opusfile 2>/dev/null || echo '?')"
    else
        warn "pkg-config 或 libopus/libopusfile 未就绪，opus 音频编码将不可用"
    fi

    # Node.js 安装（必需依赖：netease npm install 必须可用）
    if ! command -v node &>/dev/null; then
        info "安装 Node.js 20.x（必需依赖，用于 netease npm install）..."
        # 多镜像回退，避免 NodeSource 单点失败导致整体部署失败
        NODE_INSTALLED=false
        # G-5：镜像列表收敛到 lib/mirrors.sh
        mapfile -t NODE_MIRRORS < <(rrt_mirror_urls node_setup)
        for NODE_SETUP_URL in "${NODE_MIRRORS[@]}"; do
            info "尝试 NodeSource 脚本: $NODE_SETUP_URL"
            if curl -fsSL --connect-timeout 15 "$NODE_SETUP_URL" | bash - 2>/dev/null; then
                if DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nodejs >/dev/null 2>&1; then
                    if command -v node &>/dev/null; then
                        NODE_INSTALLED=true
                        break
                    fi
                fi
            fi
            warn "此镜像失败，尝试下一个..."
        done

        # 兜底：直接下载官方二进制（不依赖 NodeSource）
        if [[ "$NODE_INSTALLED" != "true" ]]; then
            info "NodeSource 脚本失败，尝试直接下载 Node.js 二进制..."
            NODE_VERSION="v20.18.1"
            NODE_TARBALL="node-${NODE_VERSION}-linux-${ARCH}.tar.xz"
            mapfile -t NODE_MIRRORS_BIN < <(rrt_mirror_urls node_bin "$NODE_VERSION" "$NODE_TARBALL")
            NODE_TMP=$(mktemp -d)
            for NODE_URL in "${NODE_MIRRORS_BIN[@]}"; do
                info "尝试下载: $NODE_URL"
                if curl -fSL --connect-timeout 15 --progress-bar -o "$NODE_TMP/$NODE_TARBALL" "$NODE_URL"; then
                    tar -xJf "$NODE_TMP/$NODE_TARBALL" -C /usr/local --strip-components=1
                    rm -rf "$NODE_TMP"
                    if command -v node &>/dev/null; then
                        NODE_INSTALLED=true
                        break
                    fi
                fi
                warn "此镜像失败，尝试下一个..."
            done
        fi

        if [[ "$NODE_INSTALLED" == "true" ]]; then
            ok "Node.js: $(node --version)"
            ok "npm: $(npm --version 2>/dev/null)"
        else
            fail "Node.js 安装失败（所有镜像均不可用）"
            info "Node.js 是必需依赖（netease npm install 需要）"
            info "请手动安装 Node.js 20+ 后重试: sudo $SCRIPT_NAME --stage deps"
            exit 1
        fi
    else
        ok "Node.js 已安装: $(node --version)"
        # 检查版本（要求 18+）
        NODE_MAJOR=$(node -e "console.log(process.versions.node.split('.')[0])" 2>/dev/null || echo "0")
        if [[ "$NODE_MAJOR" -lt 18 ]]; then
            warn "Node.js 版本过低（$(node --version)，需要 18+），将升级..."
            # 删除旧版（如果是 apt 装的）
            apt-get remove -y nodejs 2>/dev/null || true
            # 重新走上面的安装逻辑（递归一次）
            if curl -fsSL https://deb.nodesource.com/setup_20.x | bash - 2>/dev/null; then
                DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nodejs >/dev/null 2>&1
                ok "Node.js 升级完成: $(node --version)"
            fi
        fi
    fi

    ok "系统依赖安装完成"
    fi
fi

# ============================================================
# Stage 3/9: 准备 LiveKit Server
# ============================================================
if run_stage 3; then
    stage 3 "准备 LiveKit Server..."

    if ! dry_run \
        "复制/下载 LiveKit Server $LIVEKIT_VERSION 到同目录临时文件，校验后原子替换 $LIVEKIT_DIR/livekit-server 并 chmod +x"; then

    case "$ARCH" in
        amd64) LK_ARCH="amd64" ;;
        arm64) LK_ARCH="arm64" ;;
        *)
            fail "不支持的架构: $ARCH（仅支持 amd64/arm64）"
            exit 1
            ;;
    esac

    mkdir -p "$LIVEKIT_DIR"
    LK_BIN_TARGET="$LIVEKIT_DIR/livekit-server"

    LOCAL_LK_BIN="$DEPLOY_DIR/livekit/bin/linux-${LK_ARCH}/livekit-server"
    SKIP_DOWNLOAD=false

    # 优先使用仓库内置的离线二进制
    if [[ -f "$LOCAL_LK_BIN" ]]; then
        info "使用仓库内置 LiveKit Server: $LOCAL_LK_BIN"
        # N2：不再 `cp` 直接覆盖运行中的二进制（会命中 ETXTBSY 中断部署），改为原子替换
        if ! install_livekit_binary_atomic "$LOCAL_LK_BIN"; then
            fail "LiveKit Server 安装失败（原子替换未通过校验，原二进制未改动）"
            info "排查: ls -l $LK_BIN_TARGET; $LK_BIN_TARGET --version"
            info "如需跳过本阶段继续: sudo $SCRIPT_NAME --stage build"
            exit 1
        fi
        INSTALLED_LK_VERSION=$("$LK_BIN_TARGET" --version 2>/dev/null | head -1 || true)
        # 规范化版本号（脚本变量带 v 前缀，二进制输出可能不带）
        LK_VERSION_NORMALIZED=$(echo "$LIVEKIT_VERSION" | sed 's/^v//')
        INSTALLED_LK_VERSION_NORMALIZED=$(echo "$INSTALLED_LK_VERSION" | sed -n 's/.*\([0-9]\+\.[0-9]\+\.[0-9]\+\).*/\1/p')
        if [[ "$INSTALLED_LK_VERSION_NORMALIZED" == "$LK_VERSION_NORMALIZED" ]]; then
            ok "LiveKit Server 内置二进制版本匹配 ($LIVEKIT_VERSION)"
            SKIP_DOWNLOAD=true
        else
            warn "内置二进制版本不匹配（$INSTALLED_LK_VERSION，期望 $LIVEKIT_VERSION），将尝试网络下载"
        fi
    else
        info "未找到内置二进制: $LOCAL_LK_BIN"
    fi

    # 降级：网络下载（保留原镜像逻辑，仅在本地不存在/版本不匹配时执行）
    if [[ "$SKIP_DOWNLOAD" == "false" ]]; then
        LK_BASE_URL="https://github.com/livekit/livekit/releases/download/${LIVEKIT_VERSION}/livekit-server-linux-${LK_ARCH}"
        LK_MIRRORS=(
            "https://ghproxy.net/${LK_BASE_URL}"
            "https://mirror.ghproxy.com/${LK_BASE_URL}"
            "${LK_BASE_URL}"
        )

        LK_DOWNLOADED=false
        for LK_URL in "${LK_MIRRORS[@]}"; do
            info "尝试下载 LiveKit Server: $LK_URL"
            # N2：先下到同目录临时文件，校验后再由 install_livekit_binary_atomic 原子替换，
            # 避免 curl 直接写入运行中的二进制（同样命中 ETXTBSY）
            LK_DL_TMP="${LK_BIN_TARGET}.download.$$"
            if curl -fSL --connect-timeout 15 --progress-bar -o "$LK_DL_TMP" "$LK_URL"; then
                if install_livekit_binary_atomic "$LK_DL_TMP"; then
                    LK_DOWNLOADED=true
                    rm -f -- "$LK_DL_TMP" 2>/dev/null || true
                    break
                fi
                warn "下载内容校验/替换失败，尝试下一个镜像..."
            fi
            rm -f -- "$LK_DL_TMP" 2>/dev/null || true
            warn "下载失败，尝试下一个镜像..."
        done

        if [[ "$LK_DOWNLOADED" == "true" ]]; then
            ok "LiveKit Server 下载完成"
        else
            fail "LiveKit Server 下载失败（所有镜像均不可用）"
            info "请手动下载并上传到 $LK_BIN_TARGET"
            info "下载地址: $LK_BASE_URL"
            info "或从 LiveKit releases 手动下载: https://github.com/livekit/livekit/releases/tag/${LIVEKIT_VERSION}"
            exit 1
        fi
    fi

    # 验证（活文件，与 install_livekit_binary_atomic 的步骤 5 双保险）
    if "$LK_BIN_TARGET" --version &>/dev/null; then
        ok "LiveKit Server 验证: $("$LK_BIN_TARGET" --version 2>/dev/null | head -1)"
    else
        fail "LiveKit Server 验证失败（--version 无输出）"
        exit 1
    fi
    fi
fi

# ============================================================
# Stage 4/9: 编译后端与前端
# ============================================================
if run_stage 4; then
    stage 4 "编译后端与前端..."

    if ! dry_run \
        "go build 编译后端与迁移工具 ridgericetalk/ridgericetalk-migrate（写入 $SERVER_DIR）" \
        "编译前把现行二进制备份到 ${RRT_BIN_BACKUP_DIR:-/var/backups/ridgericetalk/bin}/ridgericetalk-bin-<UTC>-<md5前8位>（+ .md5 旁文件，root:root 600，H17）" \
        "编译前校验版本注入可行性（core/version/version.go 的 var/const，H17）" \
        "安装 sherpa-onnx 共享库到 /usr/local/lib/ridgericetalk 并 ldconfig" \
        "创建 $SERVER_DIR/webhost/dist/{voice,admin} 并复制/构建前端产物（Admin 按 web/admin 源码指纹决定是否重建：指纹一致跳过并提示，指纹变化/无记录/--rebuild-admin 则重建，H-4/P2-2）"; then

    # 检查源码目录
    if [[ ! -d "$SERVER_DIR" ]]; then
        fail "后端源码目录不存在: $SERVER_DIR"
        info "请先将项目源码部署到 $DEPLOY_DIR"
        exit 1
    fi

    # 确认 Go 可用
    if ! command -v go &>/dev/null; then
        fail "Go 未安装，无法编译后端"
        info "请从 deps 阶段重新开始: --stage deps"
        exit 1
    fi

    # 后端编译（CGO_ENABLED=1，TTS sherpa-onnx 需要）
    info "编译后端（CGO_ENABLED=1）: cd $SERVER_DIR && go build -o ridgericetalk cmd/server/main.go"
    cd "$SERVER_DIR"
    export CGO_ENABLED=1

    # 构建元信息注入（供 /api/health 暴露 version/commit/buildTime，便于辨识部署批次）
    # 与 dev-scripts/deploy/deploy_server_binary.sh 保持一致：版本取 <源码语义版本>+<短commit>。
    # commit 来源优先级：git（源码是仓库时）→ $SERVER_DIR/.rrt-commit（部署工具写入的批次记录）
    # → unknown。生产机的 /opt 源码树通常不是 git 仓库，靠 .rrt-commit 才能保住批次可辨识。
    RRT_COMMIT="$(git -C "$SERVER_DIR" rev-parse --short HEAD 2>/dev/null || true)"
    if [[ -z "$RRT_COMMIT" && -f "$SERVER_DIR/.rrt-commit" ]]; then
        RRT_COMMIT="$(tr -d '[:space:]' < "$SERVER_DIR/.rrt-commit" | head -c 40)"
        [[ -n "$RRT_COMMIT" ]] && info "从 $SERVER_DIR/.rrt-commit 读取构建批次: $RRT_COMMIT"
    fi
    RRT_COMMIT="${RRT_COMMIT:-unknown}"
    RRT_BASE_VERSION="$(sed -n 's/^var Server = "\([^"]*\)".*/\1/p' "$SERVER_DIR/core/version/version.go" 2>/dev/null | head -1)"
    RRT_BASE_VERSION="${RRT_BASE_VERSION:-0.2.2}"
    RRT_BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    RRT_LDFLAGS="-X ridgericetalk/core/version.Server=${RRT_BASE_VERSION}+${RRT_COMMIT} -X ridgericetalk/core/version.Commit=${RRT_COMMIT} -X ridgericetalk/core/version.BuildTime=${RRT_BUILD_TIME}"

    # H17 ③：编译前确认上面的 -X 不会静默失效。若 version.go 里是 `const Server`，
    # -X 既不报错也不生效 → 编译出的版本号退回纯 0.2.2、health 无 commit/buildTime。
    # 处置为「告警但继续」（版本号只是元信息，不值得中断部署），但必须让人在日志里看见。
    verify_version_injection "$SERVER_DIR/core/version/version.go"

    # H17 ②：下面的 go build -o 会覆盖**正在运行**的二进制（且不留备份）。必须先把现行
    # 二进制备份到标准回滚位置 —— 命名/权限/md5 旁文件口径与 rollback-binary.sh、
    # deploy_server_binary.sh 完全一致，使 rollback-binary.sh --list 能看到它。
    # 备份失败则中止编译：没有可回滚点就不该动运行中的二进制（2026-09-14 实测的现场教训）。
    if ! backup_current_binary "$SERVER_DIR/ridgericetalk"; then
        fail "现行二进制备份失败：为保留可回滚性，已中止本次编译（运行中的二进制未被覆盖）"
        info "请检查备份目录可写性与剩余空间: ${RRT_BIN_BACKUP_DIR:-/var/backups/ridgericetalk/bin}"
        info "确认权限/空间后重跑本阶段: sudo $SCRIPT_NAME --stage build"
        exit 1
    fi

    if [[ -f cmd/server/main.go ]]; then
        if go build -ldflags "$RRT_LDFLAGS" -o ridgericetalk ./cmd/server/; then
            ok "后端编译完成: $SERVER_DIR/ridgericetalk"
        else
            fail "后端编译失败（CGO_ENABLED=1）"
            info "如不需要 TTS 功能，可临时禁用 CGO: CGO_ENABLED=0 go build -o ridgericetalk ./cmd/server/"
            exit 1
        fi
    elif [[ -f main.go ]]; then
        if go build -ldflags "$RRT_LDFLAGS" -o ridgericetalk .; then
            ok "后端编译完成: $SERVER_DIR/ridgericetalk"
        else
            fail "后端编译失败"
            exit 1
        fi
    else
        fail "找不到入口文件 (cmd/server/main.go 或 main.go)"
        exit 1
    fi

    # ------------------------------------------------------------
    # N16：编译 TTS worker（与主二进制同环境同口径：CGO_ENABLED=1 + 同 ldflags）。
    # sherpa-onnx / cgo 全部隔离到 tts-worker 子进程（N16：模型损坏时 sherpa 抛
    # C++ 异常致 cgo SIGABRT，绝不允许发生在主进程）。主进程按「主二进制同目录
    # 存在 tts-worker」自动发现；缺文件仅 TTS 降级，不影响服务其余功能。
    # 落盘走「先写 .new 再 mv」：mv(rename) 可替换运行中的可执行文件，而直接
    # go build -o 覆盖正在运行的 tts-worker 会 ETXTBSY（主服务重启前的旧 worker
    # 可能仍在运行）。
    # ------------------------------------------------------------
    if [[ -d cmd/tts-worker ]]; then
        info "编译 TTS worker（CGO_ENABLED=$CGO_ENABLED）: go build -o tts-worker ./cmd/tts-worker/"
        if go build -ldflags "$RRT_LDFLAGS" -o tts-worker.new ./cmd/tts-worker/ && mv -f tts-worker.new tts-worker; then
            ok "TTS worker 编译完成: $SERVER_DIR/tts-worker"
        else
            rm -f tts-worker.new 2>/dev/null || true
            warn "TTS worker 编译失败（主服务不受影响，TTS 将不可用）"
            info "排查: cd $SERVER_DIR && go build -o tts-worker ./cmd/tts-worker/"
        fi
    else
        warn "未找到 cmd/tts-worker 源码目录，跳过 TTS worker 编译（TTS 将不可用）"
    fi

    # 编译后复述一句「本次构建的版本可辨识性」结论（H17 ③），避免日志里只有细节告警
    if [[ "${RRT_VERSION_INJECTION_OK:-false}" == "true" ]]; then
        info "本次构建注入版本: ${RRT_BASE_VERSION}+${RRT_COMMIT}（commit=$RRT_COMMIT，buildTime=$RRT_BUILD_TIME）"
    else
        warn "本次构建**未注入**版本号：/api/health 无法辨识本次部署批次（原因见上方告警）"
    fi

    # 编译版本化迁移工具
    info "编译迁移工具: cd $SERVER_DIR && go build -o ridgericetalk-migrate ./cmd/migrate/"
    if go build -o ridgericetalk-migrate ./cmd/migrate/; then
        ok "迁移工具编译完成: $SERVER_DIR/ridgericetalk-migrate"
    else
        fail "迁移工具编译失败"
        exit 1
    fi

    # 安装 sherpa-onnx 运行时共享库（TTS 功能依赖）
    info "安装 sherpa-onnx 运行时共享库..."
    SHERPA_MOD_DIR="$(go env GOPATH)/pkg/mod/github.com/k2-fsa/sherpa-onnx-go-linux@v1.13.3/lib"
    case "$ARCH" in
        amd64) SHERPA_LIB_ARCH="x86_64-unknown-linux-gnu" ;;
        arm64) SHERPA_LIB_ARCH="aarch64-unknown-linux-gnu" ;;
        *) SHERPA_LIB_ARCH="" ;;
    esac
    if [[ -n "$SHERPA_LIB_ARCH" && -d "$SHERPA_MOD_DIR/$SHERPA_LIB_ARCH" ]]; then
        mkdir -p /usr/local/lib/ridgericetalk
        cp "$SHERPA_MOD_DIR/$SHERPA_LIB_ARCH"/*.so /usr/local/lib/ridgericetalk/
        chmod 755 /usr/local/lib/ridgericetalk/*.so
        # 确保动态链接器能找到库
        if ! grep -q "^/usr/local/lib/ridgericetalk$" /etc/ld.so.conf.d/*.conf 2>/dev/null; then
            echo "/usr/local/lib/ridgericetalk" > /etc/ld.so.conf.d/ridgericetalk-sherpa.conf
        fi
        ldconfig
        ok "sherpa-onnx 共享库已安装到 /usr/local/lib/ridgericetalk"
    else
        warn "未找到 sherpa-onnx Linux 共享库（架构: $SHERPA_LIB_ARCH），TTS 功能可能不可用"
        info "如需要 TTS，请手动安装 libsherpa-onnx-c-api.so"
    fi

    # 创建 webhost/dist 目录
    mkdir -p "$SERVER_DIR/webhost/dist/voice" "$SERVER_DIR/webhost/dist/admin"

    # 前端编译
    # 优先级：仓库预置 dist > npm 在线构建 > 占位 HTML
    # 这样即使服务器无法访问 npm 镜像，前端仍可用（仓库内置 dist）
    FRONTEND_OK=false

    # ------------------------------------------------------------
    # H-4 ②（DES-2026-0912-04 P2-2）：升级部署的管理页刷新
    # ------------------------------------------------------------
    # 此前逻辑是「webhost/dist/{voice,admin}/index.html 都存在 → 整体跳过前端构建」：
    # 升级服务端源码后 web/admin 已更新，管理页产物却停留在上一次部署的版本
    # （审计 P2-2：静默不一致）。现按 admin 源码指纹（见 admin_src_fingerprint）
    # 决定是否重建：指纹一致 → 跳过并打印明确提示；指纹变化 / 无记录 / 产物缺失 /
    # 显式 --rebuild-admin → 重建。
    ADMIN_SRC_DIR="$DEPLOY_DIR/web/admin"
    ADMIN_SRC_FINGERPRINT=""
    if [[ -d "$ADMIN_SRC_DIR" ]]; then
        ADMIN_SRC_FINGERPRINT="$(admin_src_fingerprint "$ADMIN_SRC_DIR")"
    fi
    ADMIN_RECORDED_HASH="$(admin_recorded_hash "$SERVER_DIR")"

    # admin 是否需要（重新）构建 —— 与 voice 产物状态解耦，逐一判定：
    ADMIN_REBUILD_NEEDED=false
    if [[ ! -f "$SERVER_DIR/webhost/dist/admin/index.html" ]]; then
        ADMIN_REBUILD_NEEDED=true   # 产物缺失：必须构建
    elif [[ "$REBUILD_ADMIN" == "true" ]]; then
        warn "--rebuild-admin 已指定：无条件重建 Admin 前端（跳过指纹比对）"
        ADMIN_REBUILD_NEEDED=true
    elif [[ ! -d "$ADMIN_SRC_DIR" ]]; then
        info "Admin 产物已存在且 Admin 源码不存在（手工产物部署场景），无法比对指纹，跳过重建"
    elif [[ -z "$ADMIN_SRC_FINGERPRINT" ]]; then
        warn "Admin 源码存在但指纹计算失败：为保守起见将重建管理页（H-4/P2-2）"
        ADMIN_REBUILD_NEEDED=true
    elif [[ -z "$ADMIN_RECORDED_HASH" ]]; then
        warn "Admin 前端产物存在但无源码指纹记录（$SERVER_DIR/.rrt-admin-src-hash）：无法证明其为最新版，将重建（H-4/P2-2）"
        ADMIN_REBUILD_NEEDED=true
    elif [[ "$ADMIN_SRC_FINGERPRINT" != "$ADMIN_RECORDED_HASH" ]]; then
        warn "Admin 前端源码已变化（当前 ${ADMIN_SRC_FINGERPRINT:0:8}… ≠ 上次构建 ${ADMIN_RECORDED_HASH:0:8}…）：将重建管理页（H-4/P2-2）"
        ADMIN_REBUILD_NEEDED=true
    else
        ok "Admin 前端源码与上次构建一致（指纹 ${ADMIN_SRC_FINGERPRINT:0:8}…），跳过重建"
        info "若要强制重建管理页: sudo $SCRIPT_NAME --stage build --rebuild-admin"
    fi

    # 1. 前端产物齐全且 admin 源码无变化 → 整体跳过 npm 构建
    if [[ -f "$SERVER_DIR/webhost/dist/voice/index.html" && -f "$SERVER_DIR/webhost/dist/admin/index.html" && "$ADMIN_REBUILD_NEEDED" != "true" ]]; then
        ok "检测到已有前端构建产物（Admin 源码指纹一致），跳过 npm 构建"
        FRONTEND_OK=true
    fi

    # 2. 需要构建/重建时（产物缺失、admin 源码变化、--rebuild-admin），尝试 npm 在线构建
    if [[ "$FRONTEND_OK" != "true" ]] && command -v node &>/dev/null && command -v npm &>/dev/null; then
        # Voice 前端
        if [[ -d "$DEPLOY_DIR/web/voice" && -f "$DEPLOY_DIR/web/voice/package.json" ]]; then
            info "编译 Voice 前端..."
            if (cd "$DEPLOY_DIR/web/voice" && npm ci --quiet 2>/dev/null && npm run build); then
                if [[ -d "$DEPLOY_DIR/web/voice/dist" ]]; then
                    cp -r "$DEPLOY_DIR/web/voice/dist/"* "$SERVER_DIR/webhost/dist/voice/"
                    ok "Voice 前端已复制到 webhost/dist/voice/"
                    FRONTEND_OK=true
                else
                    warn "Voice 前端构建后未找到 dist 目录"
                fi
            else
                warn "Voice 前端构建失败"
            fi
        else
            warn "Voice 前端源码不存在: $DEPLOY_DIR/web/voice"
        fi

        # Admin 前端（H-4 ②：按上面的指纹判定结果重建；成功后记录新指纹）
        if [[ -d "$ADMIN_SRC_DIR" && -f "$ADMIN_SRC_DIR/package.json" ]]; then
            if [[ "$ADMIN_REBUILD_NEEDED" == "true" ]]; then
                info "编译 Admin 前端..."
                if (cd "$ADMIN_SRC_DIR" && npm ci --quiet 2>/dev/null && npm run build); then
                    if [[ -d "$ADMIN_SRC_DIR/dist" ]]; then
                        cp -r "$ADMIN_SRC_DIR/dist/"* "$SERVER_DIR/webhost/dist/admin/"
                        ok "Admin 前端已复制到 webhost/dist/admin/"
                        # 记录本次构建对应的源码指纹，供下次升级部署比对（H-4/P2-2）。
                        # 记录失败不回滚构建产物，只损失「下次免重建」的判定依据。
                        if [[ -n "$ADMIN_SRC_FINGERPRINT" ]] && printf '%s\n' "$ADMIN_SRC_FINGERPRINT" > "$SERVER_DIR/.rrt-admin-src-hash" 2>/dev/null; then
                            chown root:root "$SERVER_DIR/.rrt-admin-src-hash" 2>/dev/null || true
                            chmod 644 "$SERVER_DIR/.rrt-admin-src-hash" 2>/dev/null || true
                        else
                            warn "写入 admin 源码指纹记录失败（$SERVER_DIR/.rrt-admin-src-hash）：下次升级会多重建一次，不影响本次产物正确性"
                        fi
                        FRONTEND_OK=true
                    else
                        warn "Admin 前端构建后未找到 dist 目录"
                    fi
                else
                    warn "Admin 前端构建失败"
                fi
            else
                ok "Admin 前端产物已存在且源码未变化，跳过重建"
                FRONTEND_OK=true
            fi
        else
            warn "Admin 前端源码不存在: $ADMIN_SRC_DIR"
        fi
    fi

    # 3. 前端仍不可用时，输出明确错误（不再静默写占位 HTML）
    if [[ "$FRONTEND_OK" != "true" ]]; then
        # 检查是否已有预置产物（可能是 voice 或 admin 单侧缺失）
        if [[ -f "$SERVER_DIR/webhost/dist/voice/index.html" || -f "$SERVER_DIR/webhost/dist/admin/index.html" ]]; then
            warn "前端构建产物部分缺失，但已有预置产物可用"
            FRONTEND_OK=true
        else
            fail "前端构建产物不可用"
            info "原因：仓库未预置 dist 且 Node.js/npm 构建失败"
            info "解决方案："
            info "  1. 在本地执行 npm run build 后将 dist 上传到服务器"
            info "  2. 或在服务器上手动安装 Node.js 20+ 后重试 --stage build"
            info "  3. 或使用预置 dist 的仓库版本"
            # 不再写占位 HTML（这会让新手误以为部署成功）
            exit 1
        fi
    fi

    ok "编译完成"
    fi
fi

# ============================================================
# Stage 5/9: 初始化 PostgreSQL
# ============================================================
if run_stage 5; then
    stage 5 "初始化 PostgreSQL..."

    if ! dry_run \
        "启动并 enable PostgreSQL（systemctl enable --now postgresql）" \
        "创建数据库用户 ridgericetalk：$ENV_FILE 存在则依次从 POSTGRES_PASSWORD/RRT_DB_PASSWORD/RRT_DATABASE_URL 复用口令并校验库口令一致（校验失败则中止），三处均无则报错中止；$ENV_FILE 不存在（真新装）才生成新口令并 ALTER USER" \
        "创建数据库 ridgericetalk 并授予权限" \
        "（仅新装/无配置场景）将本次新生成的口令写入 /tmp/.rrt_pg_pwd 供后续阶段使用"; then

    # 确保 PostgreSQL 已启动
    systemctl enable --now postgresql
    ok "PostgreSQL 服务已启动"

    # 口令策略（防止重跑破坏现场）：
    #   - .env.production 已存在 → 依次尝试 POSTGRES_PASSWORD、RRT_DB_PASSWORD、
    #     RRT_DATABASE_URL；任取到非空口令即复用，不生成新口令、不执行 ALTER USER
    #     （否则库口令变了而配置未同步 → 全站不可用），并做一次非破坏性连接校验。
    #   - .env.production 已存在但三处都取不到非空口令 → 明确报错并中止（绝不静默按
    #     新装生成新口令：那会导致库口令与配置不一致，正是本加固要消灭的失效模式）。
    #   - 无配置文件（真新装）→ 生成新口令并 ALTER USER（保持原行为）。
    PG_PWD=""
    PG_PWD_REUSED=false
    ENV_FILE_EXISTS=false
    if [[ -f "$ENV_FILE" ]]; then
        ENV_FILE_EXISTS=true
        PG_PWD="$(env_db_password || true)"
    fi

    if [[ -n "$PG_PWD" ]]; then
        PG_PWD_REUSED=true
        info "复用已有数据库口令（读取自 $ENV_FILE），不修改数据库用户密码"
        if [[ -f /tmp/.rrt_pg_pwd ]]; then
            # 复用场景下 .env.production 才是权威口令，清除可能残留的陈旧临时口令
            rm -f /tmp/.rrt_pg_pwd
            info "已清除残留的陈旧临时口令 /tmp/.rrt_pg_pwd"
        fi
        # F2: 复用分支同样必须验证「库口令 == 配置口令」，否则会一路跑到迁移
        #     才以误导性错误失败。此验证为非破坏性（仅 SELECT 1）。
        info "验证数据库口令与 $ENV_FILE 一致（非破坏性）..."
        if ! PGPASSWORD="$PG_PWD" psql -h 127.0.0.1 -U ridgericetalk -d postgres -c "SELECT 1" >/dev/null 2>&1; then
            fail "库口令与 $ENV_FILE 不一致（无法用配置中的口令连接数据库）"
            info "可将库口令同步为配置文件中的口令（口令值不回显）:"
            info "  sudo -u postgres psql -c \"ALTER USER ridgericetalk WITH PASSWORD '<配置文件中的口令>'\""
            info "或使用 --force 重新生成配置后重新部署"
            exit 1
        fi
        ok "数据库口令验证通过（与 $ENV_FILE 一致）"
    else
        if [[ "$ENV_FILE_EXISTS" == "true" ]]; then
            fail "$ENV_FILE 已存在，但未找到非空数据库口令"
            info "请检查配置中的 POSTGRES_PASSWORD / RRT_DB_PASSWORD / RRT_DATABASE_URL（三者均未提供有效口令）"
            info "修复配置后重试，或使用 --force 重新生成配置（会覆盖现有配置）后重新部署"
            exit 1
        fi
        PG_PWD=$(openssl rand -hex 16)
        info "已生成数据库密码"
    fi

    # 创建用户（幂等，使用 DO 块避免 ERROR 日志吓到新手）
    info "创建数据库用户 ridgericetalk..."
    sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL || { fail "创建数据库用户失败"; exit 1; }
DO \$\$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ridgericetalk') THEN
        CREATE USER ridgericetalk WITH PASSWORD '$PG_PWD';
    END IF;
END
\$\$;
SQL

    if [[ "$PG_PWD_REUSED" == "true" ]]; then
        # 复用已有口令：绝不执行 ALTER USER，否则库口令被改而 .env.production 未同步 → 全站不可用
        info "跳过 ALTER USER（复用 $ENV_FILE 中的已有口令）"
    else
        # 强制更新密码以确保一致（使用 sudo -u postgres 而非 su -，更可靠）
        info "设置数据库用户密码..."
        if ! sudo -u postgres psql -v ON_ERROR_STOP=1 -c "ALTER USER ridgericetalk WITH PASSWORD '$PG_PWD';" >/dev/null 2>&1; then
            fail "ALTER USER 设置密码失败"
            info "这通常意味着 PostgreSQL 的 pg_hba.conf 配置问题"
            info "请检查: sudo -u postgres psql -c 'SELECT 1'"
            exit 1
        fi

        # 验证密码可用（关键：避免静默失败导致后续迁移失败）
        info "验证数据库密码连接..."
        if ! PGPASSWORD="$PG_PWD" psql -h localhost -U ridgericetalk -d postgres -c "SELECT 1" >/dev/null 2>&1; then
            fail "数据库密码验证失败（密码无法连接）"
            info "pg_hba.conf 可能使用了 scram-sha-256，但 ALTER USER 未生效"
            info "请检查: sudo cat /etc/postgresql/*/main/pg_hba.conf"
            exit 1
        fi
        ok "数据库密码验证通过"
    fi
    ok "数据库用户 ridgericetalk 就绪"

    # 创建数据库（幂等，使用 DO 块）
    info "创建数据库 ridgericetalk..."
    sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL || { fail "创建数据库失败"; exit 1; }
SELECT 'CREATE DATABASE ridgericetalk OWNER ridgericetalk'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'ridgericetalk')\gexec
SQL
    ok "数据库 ridgericetalk 就绪"

    # 授予全部权限
    sudo -u postgres psql -v ON_ERROR_STOP=1 -c "GRANT ALL PRIVILEGES ON DATABASE ridgericetalk TO ridgericetalk;" >/dev/null 2>&1 || true

    # 仅在本次真的生成了新口令时才写入临时文件（供 Stage 6 使用）；
    # 复用已有口令时不写，避免陈旧口令残留被后续部分重跑误捡。
    if [[ "$PG_PWD_REUSED" != "true" ]]; then
        echo "$PG_PWD" > /tmp/.rrt_pg_pwd
        chmod 600 /tmp/.rrt_pg_pwd
        PG_PWD_WRITTEN=true
        ok "数据库密码已写入 /tmp/.rrt_pg_pwd（本次运行结束时删除）"
    else
        info "复用已有口令，未写入 /tmp/.rrt_pg_pwd"
    fi

    ok "PostgreSQL 初始化完成"
    fi
fi

# ============================================================
# Stage 5.5/9: 应用数据库迁移
# ============================================================
if run_stage 5; then
    stage "5.5" "应用数据库迁移..."

    if ! dry_run \
        "执行数据库迁移 ./ridgericetalk-migrate up 并将 $STORAGE_DIR 所有权修正为 $SERVICE_USER"; then

    cd "$SERVER_DIR"
    if [[ "$SKIP_MIGRATE" == "true" ]]; then
        warn "跳过数据库迁移（--skip-migrate）"
        info "生产环境不建议跳过迁移"
    else
        if [[ -f "$ENV_FILE" ]]; then
            while IFS='=' read -r key value; do
                [[ -z "$key" || "$key" =~ ^[[:space:]]*# ]] && continue
                key=$(echo "$key" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
                value=$(echo "$value" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
                [[ -n "$key" ]] && export "$key=$value"
            done < "$ENV_FILE"
            info "已从 $ENV_FILE 加载数据库配置"
        fi

        # 如果 Stage 5 生成了新密码，优先使用它，避免 .env.production 还是旧密码
        # 导致迁移失败（Stage 6 尚未执行，未更新配置文件）。
        if [[ -f /tmp/.rrt_pg_pwd ]]; then
            PG_PWD_MIGRATE=$(tr -d '[:space:]' < /tmp/.rrt_pg_pwd)
            if [[ -n "$PG_PWD_MIGRATE" ]]; then
                export RRT_DATABASE_URL="postgres://ridgericetalk:${PG_PWD_MIGRATE}@localhost:5432/ridgericetalk?sslmode=require"
                export RRT_DB_PASSWORD="$PG_PWD_MIGRATE"
                info "使用 Stage 5 生成的数据库密码执行迁移"
            fi
        fi

        if ensure_migrate_binary; then
            if ./ridgericetalk-migrate up; then
                ok "数据库迁移完成"
                # 关键修复：迁移以 root 运行，可能生成 secrets.json（root 所有），
                # 但 systemd 服务以 ridgericetalk 用户运行，读不到会启动失败。
                # 这里 chown 整个 storage 目录，确保服务用户可读写。
                if [[ -d "$STORAGE_DIR" ]]; then
                    chown -R "$SERVICE_USER:$SERVICE_USER" "$STORAGE_DIR" 2>/dev/null || true
                    # secrets.json 权限严格 0600
                    [[ -f "$STORAGE_DIR/secrets.json" ]] && chmod 600 "$STORAGE_DIR/secrets.json"
                    ok "storage 目录所有权已修正为 $SERVICE_USER"
                fi
            else
                fail "数据库迁移失败"
                info "请检查 PostgreSQL 连接配置和迁移文件: $DEPLOY_DIR/migrations/"
                info "可手动重试: cd $SERVER_DIR && ./ridgericetalk-migrate up"
                info "或使用 --skip-migrate 跳过迁移（不推荐）"
                exit 1
            fi
        else
            fail "迁移工具不存在: $SERVER_DIR/ridgericetalk-migrate"
            info "请从 build 阶段重新执行: sudo $SCRIPT_NAME --stage build"
            exit 1
        fi
    fi
    fi
fi

# ============================================================
# Stage 6/9: 生成配置文件
# ============================================================
if run_stage 6; then
    stage 6 "生成配置文件..."

    if ! dry_run \
        "创建 $STORAGE_DIR" \
        "生成/更新 $ENV_FILE（已存在且未加 --force 时仅保留，不覆盖）" \
        "生成 $LIVEKIT_YAML（已存在且未加 --force 时保留原文件；密钥/端口与权威值不一致时重新渲染并保留 .bak 备份）" \
        "仅在 --force 时重建 $STORAGE_DIR/server-state.json（否则保留）" \
        "将 LiveKit 密钥写回 $ENV_FILE（与 livekit.yaml 保持同一对值）"; then

    # 确保 storage 目录存在（源码包可能未包含此目录，避免写入 livekit.yaml/secrets.json 失败）
    mkdir -p "$STORAGE_DIR"

    # 获取数据库密码（统一解析口径，与 Stage 5 共用 env_db_password）
    PG_PWD=""
    if [[ -f /tmp/.rrt_pg_pwd ]]; then
        PG_PWD="$(tr -d '[:space:]' < /tmp/.rrt_pg_pwd || true)"
        if [[ -n "$PG_PWD" ]]; then
            info "从 /tmp/.rrt_pg_pwd 读取数据库密码"
        fi
    fi
    if [[ -z "$PG_PWD" && -f "$ENV_FILE" ]]; then
        # 从已有配置文件中提取密码（恢复场景）
        PG_PWD="$(env_db_password || true)"
        if [[ -n "$PG_PWD" ]]; then
            info "从 $ENV_FILE 读取数据库密码"
        fi
    fi

    if [[ -z "$PG_PWD" ]]; then
        fail "无法获取数据库密码"
        info "请从 db 阶段重新开始: sudo $SCRIPT_NAME --stage db"
        exit 1
    fi

    # 检测公网 IP（能力层 lib/net.sh：境外服务 → 云元数据兜底）
    if [[ -z "$PUBLIC_IP" ]]; then
        info "自动检测公网 IP..."
        DETECTED_IP="$(rrt_public_ip || true)"
        if [[ -n "$DETECTED_IP" ]]; then
            info "检测到公网 IP: $DETECTED_IP"
            read_prompt "请确认公网 IP [$DETECTED_IP]（回车确认，或输入新 IP）: " USER_IP
            PUBLIC_IP="${USER_IP:-$DETECTED_IP}"
        else
            warn "无法自动检测公网 IP"
            read_prompt "请输入公网 IP: " PUBLIC_IP
            if [[ -z "$PUBLIC_IP" ]]; then
                fail "公网 IP 不能为空"
                info "请使用 --public-ip 参数指定"
                exit 1
            fi
        fi
    fi
    ok "公网 IP: $PUBLIC_IP"

    info "端口配置: API=$PORT_API Admin=$PORT_ADMIN LK_WS=$PORT_LK_WS LK_TCP=$PORT_LK_TCP LK_UDP=$PORT_LK_UDP"

    # 生成 .env.production
    if [[ -f "$ENV_FILE" && "$FORCE" != "true" ]]; then
        warn "配置文件已存在: $ENV_FILE（使用 --force 覆盖）"
    else
        if [[ -f "$ENV_FILE" && "$FORCE" == "true" ]]; then
            BACKUP_ENV="${ENV_FILE}.bak.$(date +%s)"
            cp "$ENV_FILE" "$BACKUP_ENV"
            info "已备份旧配置到 $BACKUP_ENV"
        fi
        info "生成 $ENV_FILE..."
        mkdir -p "$SERVER_DIR"
        # RRT_PUBLIC_ADDRESS：未显式设置时使用本机主 IP 作为默认值，
        # 让新机器一键部署后 Web/Admin 端就自动被 CORS 白名单放行。
        RRT_PUBLIC_ADDRESS="${RRT_PUBLIC_ADDRESS:-http://${PUBLIC_IP}:$PORT_API}"
        # N27：CORS 白名单协议必须由实际对外地址推导（useHttps + 实际端口），
        # 禁止硬编码协议 —— fix3 批次曾把白名单写成 https-only，HTTP 部署下
        # 桌面端 origin-rewrite 的 http://<ip>:<port> 全被 403（N27 实测）。
        # 显式传入 https:// 形态的 RRT_PUBLIC_ADDRESS（或本次带 --tls）时，
        # 白名单同步用 https；默认（http 地址、无 TLS）推导结果与旧硬编码一致。
        CORS_SCHEME="http"
        case "$RRT_PUBLIC_ADDRESS" in
            https://*|wss://*) CORS_SCHEME="https" ;;
        esac
        if [[ "$TLS_MODE" != "none" ]]; then
            CORS_SCHEME="https"
        fi
        # N27 说明（置于 heredoc 外：默认路径下生成的 .env 必须与改动前逐字节一致）
        cat > "$ENV_FILE" <<EOF
# RidgeRiceTalk 生产环境配置（由 deploy-baremetal.sh 生成）
# 生成时间: $(date '+%Y-%m-%d %H:%M:%S')

# ============================================================
# 环境配置
# ============================================================
RRT_ENV=production
RRT_LOG_LEVEL=info
RRT_DEPLOY_MODE=baremetal
RRT_PUBLIC_ADDRESS=$RRT_PUBLIC_ADDRESS

# 端口
RRT_PORT=$PORT_API
RRT_ADMIN_PORT=$PORT_ADMIN

# CORS 白名单：自动放行 Voice、Admin 和 localhost 双端口，确保 Admin 页面可直接访问
RRT_CORS_ORIGINS=${CORS_SCHEME}://${PUBLIC_IP}:$PORT_API,${CORS_SCHEME}://${PUBLIC_IP}:$PORT_ADMIN,${CORS_SCHEME}://localhost:$PORT_API,${CORS_SCHEME}://localhost:$PORT_ADMIN

# LiveKit 公网地址（前端连接使用）
RRT_LIVEKIT_PUBLIC_URL=ws://${PUBLIC_IP}:$PORT_LK_WS

# ============================================================
# 数据库（PostgreSQL）
# ============================================================
RRT_DATABASE_URL=postgres://ridgericetalk:$PG_PWD@localhost:5432/ridgericetalk?sslmode=require
RRT_DB_DRIVER=postgres
RRT_DB_PASSWORD=$PG_PWD
POSTGRES_PASSWORD=$PG_PWD

# ============================================================
# 密钥（留空将自动生成并持久化到 data/secrets.json）
# ============================================================
RRT_JWT_SECRET=
RRT_CSRF_TOKEN_SECRET=
RRT_ENCRYPTION_KEY=

# ============================================================
# LiveKit
# ============================================================
RRT_LIVEKIT_API_KEY=
RRT_LIVEKIT_API_SECRET=
RRT_LIVEKIT_URL=ws://localhost:$PORT_LK_WS
RRT_LIVEKIT_PUBLIC_URL=ws://${PUBLIC_IP}:$PORT_LK_WS
RRT_LIVEKIT_AUTOSTART=false
# 端口配置（供 livekitmgr.generateConfig 读取，避免 livekit.yaml 使用硬编码 7880/7881/7882）
RRT_LIVEKIT_PORT=$PORT_LK_WS
RRT_LIVEKIT_TCP_PORT=$PORT_LK_TCP
RRT_LIVEKIT_UDP_PORT=$PORT_LK_UDP
RRT_LIVEKIT_USE_EXTERNAL_IP=true

# ============================================================
# NeteaseCloudMusicApi（音乐机器人依赖）
# ============================================================
RRT_NETEASE_API_ENDPOINT=http://127.0.0.1:${NETEASE_PORT:-3300}

# ============================================================
# EasyTier 虚拟局域网（DES-2026-0731-02）
# 服务端作为公网入口节点，客户端通过相同的 network_name + network_secret 加入网络
# 密钥由 Stage 7.7 生成并持久化到 /etc/easytier/secret
# ============================================================
RRT_EASYTIER_URL=http://127.0.0.1:${EASYTIER_WEB_PORT:-11210}
RRT_EASYTIER_PORT=${EASYTIER_PORT:-5007}
# RRT_EASYTIER_SECRET 由 Stage 7.7 完成后追加（见下方）

# ============================================================
# 存储
# ============================================================
RRT_STORAGE_TYPE=local
RRT_LOCAL_DATA_PATH=$STORAGE_DIR

# ============================================================
# 用户与注册
# ============================================================
RRT_ALLOW_REGISTER=true
RRT_MAX_USERS=1000

# ============================================================
# Owner 初始化（首次启动自动创建）
# ============================================================
# Path B: Bootstrap Token 流程 — 不预配置 Owner 凭据，让后端生成一次性 bootstrap token
# Owner 通过管理后台向导完成初始化（验证 token → 注册 → 配网络）
# 如需回退到 Path A（env 预配置），取消下面三行注释即可
#RRT_OWNER_USERNAME=admin
#RRT_OWNER_PASSWORD=Admin@2026!
#RRT_OWNER_EMAIL=admin@localhost
EOF
        chmod 600 "$ENV_FILE"
        # N28：--enable-turn 时追加 TURN 端口放行说明与开关标记。默认（不开）
        # 不追加任何内容 —— .env.production 与改动前逐字节一致（硬验收）。
        if [[ "$ENABLE_TURN" == "true" ]]; then
            cat >> "$ENV_FILE" <<EOF

# ============================================================
# TURN 中继（N28：--enable-turn 开启，livekit.yaml 已含 turn 块）
# 需在云安全组/防火墙放行：
#   ${TURN_UDP_PORT}/udp  TURN UDP 中继（已有证书时 livekit.yaml 另有 tls_port=${TURN_TLS_PORT}）
#   ${TURN_TLS_PORT}/tcp  TURN TLS（仅 livekit.yaml 写入 tls_port 后需要）
# ============================================================
RRT_LIVEKIT_TURN_ENABLED=true
EOF
        fi
        ok "已生成 $ENV_FILE（权限 0600）"
    fi

    # server-state.json 清理：仅在 --force 时执行，避免重跑脚本误删已初始化状态
    # （未加 --force 时保留，后端继续沿用已有初始化状态）
    if [[ -f "$STORAGE_DIR/server-state.json" && "$FORCE" == "true" ]]; then
        OLD_STATE="${STORAGE_DIR}/server-state.json.bak.$(date +%s)"
        cp "$STORAGE_DIR/server-state.json" "$OLD_STATE"
        rm -f "$STORAGE_DIR/server-state.json"
        info "已备份并清理旧 server-state.json（--force，避免后端误判已初始化）"
        info "备份: $OLD_STATE"
    elif [[ -f "$STORAGE_DIR/server-state.json" ]]; then
        info "已保留 server-state.json（--force 才会重建）"
    fi

    # ------------------------------------------------------------------
    # LiveKit 密钥：单一权威来源（D18）
    # ------------------------------------------------------------------
    # 旧行为：重跑时 livekit.yaml 用「secrets.json / 临时生成」的密钥渲染，而
    # .env.production 仅在键为空时才回写 → 两者可能持**不同**密钥，表现为难定位
    # 的周期性 LiveKit 鉴权失败。
    # 权威顺序（先到先用）：
    #   1) .env.production 中已存在的非空 RRT_LIVEKIT_API_KEY + SECRET（现场生效值）
    #   2) storage/secrets.json 的 livekit_api_key / livekit_api_secret
    #   3) 都没有 → 生成一对新密钥
    # 随后保证「写进 livekit.yaml 的」与「写进 .env.production 的」是**同一对值**。
    LK_API_KEY="$(env_get RRT_LIVEKIT_API_KEY || true)"
    LK_API_SECRET="$(env_get RRT_LIVEKIT_API_SECRET || true)"
    LK_KEY_SOURCE=".env.production"
    if [[ -z "$LK_API_KEY" || -z "$LK_API_SECRET" ]]; then
        LK_API_KEY=""
        LK_API_SECRET=""
        if [[ -f "$STORAGE_DIR/secrets.json" ]]; then
            LK_API_KEY="$(jq -r '.livekit_api_key // ""' "$STORAGE_DIR/secrets.json" 2>/dev/null || true)"
            LK_API_SECRET="$(jq -r '.livekit_api_secret // ""' "$STORAGE_DIR/secrets.json" 2>/dev/null || true)"
            LK_KEY_SOURCE="secrets.json"
        fi
    fi
    if [[ -z "$LK_API_KEY" || -z "$LK_API_SECRET" ]]; then
        LK_API_KEY="ridgericetalk_$(openssl rand -hex 4)"
        LK_API_SECRET="$(openssl rand -hex 32)"
        LK_KEY_SOURCE="新生成"
        warn "未在 .env.production / secrets.json 找到可用的 LiveKit 密钥，已新生成一对"
    fi
    info "LiveKit 密钥来源: $LK_KEY_SOURCE"

    # 是否重新渲染 livekit.yaml（D17）：
    #   - 文件不存在 / --force / 仍是公开 devkey:devsecret 模板   → 重新渲染；
    #   - 已存在且未加 --force → 默认**保留**现场文件（不再无条件覆盖，避免冲掉
    #     手工调优，与 .env.production 的「未加 --force 不覆盖」口径一致）；
    #     例外：其中的 keys 与权威密钥不一致、或端口与本次 --port-lk-* 不一致时，
    #     仍重新渲染以对齐（否则会静默忽略用户显式传入的端口、并留下鉴权隐患）。
    LK_YAML_REGEN=false
    LK_REGEN_REASON=""
    LK_KEEP_NOTES=""
    if [[ ! -f "$LIVEKIT_YAML" ]]; then
        LK_YAML_REGEN=true; LK_REGEN_REASON="livekit.yaml 不存在"
    elif [[ "$FORCE" == "true" ]]; then
        LK_YAML_REGEN=true; LK_REGEN_REASON="指定了 --force"
    elif grep -q "devkey: devsecret" "$LIVEKIT_YAML" 2>/dev/null; then
        LK_YAML_REGEN=true; LK_REGEN_REASON="检测到公开 devkey:devsecret 模板"
        warn "检测到 livekit.yaml 使用公开 devkey:devsecret，强制重新渲染"
    else
        # 尽力解析既有 livekit.yaml（本脚本生成的固定格式）
        YAML_KEY_LINE="$(awk '/^keys:[[:space:]]*$/{getline; print; exit}' "$LIVEKIT_YAML" 2>/dev/null || true)"
        YAML_LK_KEY="$(printf '%s' "${YAML_KEY_LINE%%:*}" | tr -d '[:space:]')"
        YAML_LK_SECRET="$(printf '%s' "${YAML_KEY_LINE#*:}" | tr -d '[:space:]')"
        YAML_LK_PORT="$(awk '/^port:[[:space:]]*/{print $2; exit}' "$LIVEKIT_YAML" 2>/dev/null || true)"
        YAML_LK_TCP="$(awk '/^[[:space:]]+tcp_port:[[:space:]]*/{print $2; exit}' "$LIVEKIT_YAML" 2>/dev/null || true)"
        YAML_LK_UDP="$(awk '/^[[:space:]]+udp_port:[[:space:]]*/{print $2; exit}' "$LIVEKIT_YAML" 2>/dev/null || true)"
        if [[ -z "$YAML_LK_KEY" || -z "$YAML_LK_SECRET" ]]; then
            # 非本脚本生成的格式，无法校验 → 保留原文件，仅提示
            LK_KEEP_NOTES="（无法解析其 keys 段，未校验密钥/端口一致性）"
            warn "无法解析既有 livekit.yaml 的 keys 段，保留原文件未做校验；如出现 LiveKit 鉴权失败请用 --force 重新生成"
        elif [[ "$YAML_LK_KEY" != "$LK_API_KEY" || "$YAML_LK_SECRET" != "$LK_API_SECRET" ]]; then
            LK_YAML_REGEN=true; LK_REGEN_REASON="既有 keys 与权威密钥不一致"
        elif [[ "$YAML_LK_PORT" != "$PORT_LK_WS" || "$YAML_LK_TCP" != "$PORT_LK_TCP" || "$YAML_LK_UDP" != "$PORT_LK_UDP" ]]; then
            LK_YAML_REGEN=true; LK_REGEN_REASON="既有端口与本次 --port-lk-* 参数不一致"
        elif [[ "$ENABLE_TURN" == "true" ]] && ! grep -q '^turn:' "$LIVEKIT_YAML" 2>/dev/null; then
            # N28：显式 --enable-turn 而既有文件未启用 TURN → 重渲染以对齐。
            # 仅在开关为 true 时参与判定：默认路径的重渲染决策与改动前完全一致。
            LK_YAML_REGEN=true; LK_REGEN_REASON="本次指定 --enable-turn 而既有 livekit.yaml 未启用 TURN"
        else
            LK_KEEP_NOTES="（密钥与端口均一致）"
        fi
    fi

    # N28：--enable-turn 时构造 turn 块（默认关 → 空串，heredoc 输出与改动前逐字节一致）。
    # tls_port 仅在证书文件就绪时写入：LiveKit 校验 tls_port 必须伴随
    # domain/cert_file/key_file，缺证书写 tls_port 会让配置加载直接失败。
    TURN_BLOCK=""
    if [[ "$ENABLE_TURN" == "true" ]]; then
        TURN_BLOCK=$'\n\nturn:\n  enabled: true\n  udp_port: '"${TURN_UDP_PORT}"
        if [[ -f "$TURN_CERT_FILE" && -f "$TURN_KEY_FILE" ]]; then
            TURN_BLOCK+=$'\n  tls_port: '"${TURN_TLS_PORT}"
            TURN_BLOCK+=$'\n  domain: '"${TLS_DOMAIN:-$PUBLIC_IP}"
            TURN_BLOCK+=$'\n  cert_file: '"${TURN_CERT_FILE}"
            TURN_BLOCK+=$'\n  key_file: '"${TURN_KEY_FILE}"
            info "TURN TLS 已启用（tls_port=${TURN_TLS_PORT}，证书: ${TURN_CERT_FILE}）"
        else
            info "未检测到 TURN TLS 证书（${TURN_CERT_FILE}）：turn 块仅含 udp_port=${TURN_UDP_PORT}"
            info "  如需 TURN TLS：先 --tls selfsigned/--tls acme 生成证书，再重跑 --enable-turn --force"
        fi
    fi

    if [[ "$LK_YAML_REGEN" == "true" ]]; then
        if [[ -f "$LIVEKIT_YAML" ]]; then
            OLD_LK="${LIVEKIT_YAML}.bak.$(date +%s)"
            cp "$LIVEKIT_YAML" "$OLD_LK"
            info "已备份旧 livekit.yaml: $OLD_LK"
        fi
        # 立即生成 livekit.yaml（使用 Stage 6 的端口参数 + 上面认定的权威密钥）
        # 不再依赖后端首次启动时渲染，避免 LiveKit 用错端口
        info "生成 livekit.yaml（使用 --port-lk-* 参数；原因: $LK_REGEN_REASON）..."
        cat > "$LIVEKIT_YAML" <<EOF
# Auto-generated by deploy-baremetal.sh — do not edit manually
# 生成时间: $(date '+%Y-%m-%d %H:%M:%S')
# 公网 IP: $PUBLIC_IP
# 端口: WS=$PORT_LK_WS TCP=$PORT_LK_TCP UDP=$PORT_LK_UDP

port: $PORT_LK_WS
bind_addresses:
  - "0.0.0.0"

keys:
  $LK_API_KEY: $LK_API_SECRET

logging:
  level: info
  json: false

rtc:
  tcp_port: $PORT_LK_TCP
  udp_port: $PORT_LK_UDP
  use_external_ip: true
  use_ice_lite: true
  node_ip: $PUBLIC_IP${TURN_BLOCK}
EOF
        chmod 644 "$LIVEKIT_YAML"
        chown "$SERVICE_USER:$SERVICE_USER" "$LIVEKIT_YAML" 2>/dev/null || true
        ok "livekit.yaml 已生成（端口: WS=$PORT_LK_WS TCP=$PORT_LK_TCP UDP=$PORT_LK_UDP）"
        info "LiveKit API Key: $LK_API_KEY"
    else
        info "已保留既有 livekit.yaml（未加 --force 不重新生成）$LK_KEEP_NOTES"
    fi

    # 同步 LiveKit 密钥到 .env.production：确保 livekit.yaml 与 .env 持**同一对值**（D18）。
    # 权威值优先取自 .env 自身，故通常此处「已一致」；仅在 .env 为空（首次生成、或
    # 权威值来自 secrets.json / 新生成）时才回写。
    if [[ -f "$ENV_FILE" ]]; then
        ENV_LK_KEY_CUR="$(env_get RRT_LIVEKIT_API_KEY || true)"
        ENV_LK_SECRET_CUR="$(env_get RRT_LIVEKIT_API_SECRET || true)"
        if [[ "$ENV_LK_KEY_CUR" == "$LK_API_KEY" && "$ENV_LK_SECRET_CUR" == "$LK_API_SECRET" ]]; then
            info "LiveKit 密钥已与 $ENV_FILE 一致，无需更新"
        else
            if grep -q "^RRT_LIVEKIT_API_KEY=" "$ENV_FILE"; then
                sed -i "s|^RRT_LIVEKIT_API_KEY=.*|RRT_LIVEKIT_API_KEY=$LK_API_KEY|" "$ENV_FILE"
            else
                printf 'RRT_LIVEKIT_API_KEY=%s\n' "$LK_API_KEY" >> "$ENV_FILE"
            fi
            if grep -q "^RRT_LIVEKIT_API_SECRET=" "$ENV_FILE"; then
                sed -i "s|^RRT_LIVEKIT_API_SECRET=.*|RRT_LIVEKIT_API_SECRET=$LK_API_SECRET|" "$ENV_FILE"
            else
                printf 'RRT_LIVEKIT_API_SECRET=%s\n' "$LK_API_SECRET" >> "$ENV_FILE"
            fi
            ok "已将 LiveKit 密钥写入 $ENV_FILE（与 livekit.yaml 同一对值）"
        fi
    else
        warn "$ENV_FILE 不存在，跳过 LiveKit 密钥回写"
    fi

    ok "配置文件生成完成"
    fi
fi

# ============================================================
# Stage 7/9: 安装 systemd 服务
# ============================================================
if run_stage 7; then
    stage 7 "安装 systemd 服务..."

    # 每日自动备份 timer 的计划文本（默认启用；--no-backup-timer 关闭）。
    # 置于 dry_run 调用之前，使 --dry-run 时也能把将要安装的单元逐条打印出来。
    BK_DRY_ACTIONS=()
    if [[ "$BACKUP_TIMER" == "true" ]]; then
        BK_DRY_ACTIONS=(
            "渲染并安装 /etc/systemd/system/ridgericetalk-backup.service（Type=oneshot，依次执行 backup-db.sh 与 backup-storage.sh）"
            "渲染并安装 /etc/systemd/system/ridgericetalk-backup.timer（每日 04:17 + RandomizedDelaySec=30min，Persistent=true）"
            "systemctl enable --now ridgericetalk-backup.timer（仅启用/启动 timer，不启动 service）"
        )
    fi

    if ! dry_run \
        "创建系统用户 $SERVICE_USER 并准备 HOME=$RIDGERICETALK_HOME 与 npm cache 目录" \
        "渲染并安装 /etc/systemd/system/ridgericetalk.service" \
        "渲染并安装 /etc/systemd/system/livekit.service（若 RRT_LIVEKIT_AUTOSTART=false 且未显式 RRT_EMBEDDED_DEPS=false 则跳过）" \
        "若 LiveKit 由主进程托管且已存在独立 livekit.service：systemctl disable --now livekit（修复既存端口冲突）" \
        "systemctl daemon-reload && systemctl enable ridgericetalk（LiveKit 由主进程托管时不同时 enable livekit，避免与内嵌实例抢端口）" \
        ${BK_DRY_ACTIONS[@]+"${BK_DRY_ACTIONS[@]}"}; then

    # systemd 服务用户的 HOME 目录，供内嵌 NeteaseCloudMusicApi 的 npm 使用
    RIDGERICETALK_HOME="/var/lib/ridgericetalk"

    # 创建系统用户（幂等），并将 HOME 指向 /var/lib/ridgericetalk，
    # 避免 systemd ProtectHome=true + npm 默认使用 /home/<user> 导致失败。
    if ! id "$SERVICE_USER" &>/dev/null; then
        useradd --system --no-create-home --home-dir "$RIDGERICETALK_HOME" --shell /usr/sbin/nologin "$SERVICE_USER"
        ok "创建系统用户: $SERVICE_USER (HOME=$RIDGERICETALK_HOME)"
    else
        usermod --home "$RIDGERICETALK_HOME" "$SERVICE_USER" 2>/dev/null || true
        ok "系统用户已存在: $SERVICE_USER (HOME=$RIDGERICETALK_HOME)"
    fi

    # 创建目录并设置权限
    mkdir -p "$STORAGE_DIR" "$SERVER_DIR/logs" "$SERVER_DIR/webhost"
    chown -R "$SERVICE_USER:$SERVICE_USER" "$STORAGE_DIR" "$SERVER_DIR/logs" "$SERVER_DIR/webhost"
    ok "目录已创建并设置权限"

    # 创建/修复服务用户 HOME 目录与 npm cache 子目录
    # NPM_CONFIG_CACHE 指向 $DEPLOY_DIR/services/netease-api/.npm（ReadWritePaths 内），
    # 因为 ProtectHome=true 会使 /var/lib/ridgericetalk 只读，npm 无法写入。
    mkdir -p "$DEPLOY_DIR/services/netease-api/.npm/_cacache" "$DEPLOY_DIR/services/netease-api/.npm/_logs"
    chown -R "ridgericetalk:ridgericetalk" "$DEPLOY_DIR/services/netease-api"
    mkdir -p "$RIDGERICETALK_HOME"
    chown -R "ridgericetalk:ridgericetalk" "$RIDGERICETALK_HOME"
    chmod 750 "$RIDGERICETALK_HOME"
    # 以 ridgericetalk 用户初始化 npm cache 目录结构，避免服务启动时
    # 内嵌 npm install 因缺少 _cacache/_logs 等子目录失败。
    if command -v npm &>/dev/null; then
        # npm cache 必须设置在 ReadWritePaths 内，否则 ProtectHome=true 下会报 EROFS
        su -s /bin/bash ridgericetalk -c "npm config set cache $DEPLOY_DIR/services/netease-api/.npm --global" >/dev/null 2>&1 || true
        su -s /bin/bash ridgericetalk -c "npm cache verify" >/dev/null 2>&1 || true
    fi
    ok "服务用户 HOME 目录已准备: $RIDGERICETALK_HOME"

    # 渲染 ridgericetalk.service
    RRT_TMPL="$SCRIPT_DIR/ridgericetalk.service.tmpl"
    if [[ ! -f "$RRT_TMPL" ]]; then
        fail "后端 systemd 模板不存在: $RRT_TMPL"
        exit 1
    fi

    RRT_EXEC_START="$SERVER_DIR/ridgericetalk"
    RRT_RENDERED=$(mktemp)
    # 该 EXIT trap 会覆盖脚本顶部注册的 cleanup_pg_pwd_tmp，故此处链式调用它，
    # 保证 Stage 7 之后的中断/失败同样能兜底清理 /tmp 明文口令（D20）。
    trap 'rm -f "${RRT_RENDERED:-}" "${LK_RENDERED:-}" "${NETEASE_RENDERED:-}" "${BK_SVC_RENDERED:-}" "${BK_TIMER_RENDERED:-}"; cleanup_pg_pwd_tmp' EXIT

    sed \
        -e "s|{{EXEC_START}}|$RRT_EXEC_START|g" \
        -e "s|{{WORKING_DIR}}|$DEPLOY_DIR|g" \
        -e "s|{{DEPLOY_DIR}}|$DEPLOY_DIR|g" \
        -e "s|{{USER}}|$SERVICE_USER|g" \
        -e "s|{{ENV_FILE}}|$ENV_FILE|g" \
        "$RRT_TMPL" > "$RRT_RENDERED"
    ok "渲染 ridgericetalk.service"

    # LiveKit 由主进程托管时（.env.production 中 RRT_LIVEKIT_AUTOSTART=false
    # 且未显式 RRT_EMBEDDED_DEPS=false），不创建独立 livekit.service，否则会与
    # 主进程内嵌的 LiveKit 抢端口（现场两者并存会冲突）。
    LK_SERVICE_SKIP=false
    LK_RENDERED=""
    if livekit_managed_by_main; then
        LK_SERVICE_SKIP=true
        info "检测到 RRT_LIVEKIT_AUTOSTART=false 且未显式禁用 RRT_EMBEDDED_DEPS：LiveKit 由主进程托管，跳过独立 livekit.service"
        # 主进程托管时，停用并禁用现场既有的独立单元（修复既存端口冲突；受 --dry-run 守卫）
        disable_managed_livekit_unit
    fi

    # 渲染 livekit.service
    if [[ "$LK_SERVICE_SKIP" == "true" ]]; then
        info "跳过 livekit.service 渲染（LiveKit 由主进程托管）"
    else
        LK_TMPL="$SCRIPT_DIR/livekit.service.tmpl"
        if [[ ! -f "$LK_TMPL" ]]; then
            fail "LiveKit systemd 模板不存在: $LK_TMPL"
            exit 1
        fi

        LK_RENDERED=$(mktemp)
        LK_EXEC_START="$LIVEKIT_DIR/livekit-server"

        sed \
            -e "s|{{EXEC_START}}|$LK_EXEC_START|g" \
            -e "s|{{CONFIG_PATH}}|$LIVEKIT_YAML|g" \
            -e "s|{{USER}}|$SERVICE_USER|g" \
            -e "s|{{WORKING_DIR}}|$LIVEKIT_DIR|g" \
            "$LK_TMPL" > "$LK_RENDERED"
        ok "渲染 livekit.service"
    fi

    # 渲染每日自动备份单元（service + timer，默认启用；--no-backup-timer 关闭）
    BK_SVC_RENDERED=""
    BK_TIMER_RENDERED=""
    if [[ "$BACKUP_TIMER" == "true" ]]; then
        BK_SVC_TMPL="$SCRIPT_DIR/ridgericetalk-backup.service.tmpl"
        BK_TIMER_TMPL="$SCRIPT_DIR/ridgericetalk-backup.timer.tmpl"
        if [[ ! -f "$BK_SVC_TMPL" ]]; then
            fail "备份 systemd 服务模板不存在: $BK_SVC_TMPL"
            exit 1
        fi
        if [[ ! -f "$BK_TIMER_TMPL" ]]; then
            fail "备份 systemd timer 模板不存在: $BK_TIMER_TMPL"
            exit 1
        fi

        BK_SVC_RENDERED=$(mktemp)
        sed -e "s|{{SERVER_DIR}}|$SERVER_DIR|g" "$BK_SVC_TMPL" > "$BK_SVC_RENDERED"
        BK_TIMER_RENDERED=$(mktemp)
        # timer 模板无占位符，仍统一过一遍 sed 保持口径一致（无 {{}} 时为恒等）
        sed -e "s|{{SERVER_DIR}}|$SERVER_DIR|g" "$BK_TIMER_TMPL" > "$BK_TIMER_RENDERED"

        # 渲染结果中不得残留未替换的 {{...}}，否则装上去的单元是坏的
        if grep -q '{{' "$BK_SVC_RENDERED" "$BK_TIMER_RENDERED"; then
            fail "备份单元模板存在未替换的占位符（{{...}}）"
            exit 1
        fi
        ok "渲染 ridgericetalk-backup.service / ridgericetalk-backup.timer"
    fi

    # systemd-analyze 校验（非阻塞；某些版本对临时文件路径支持不佳）
    if command -v systemd-analyze &>/dev/null; then
        info "校验 systemd 单元文件..."
        SYSTEMD_UNITS=("$RRT_RENDERED")
        [[ -n "$LK_RENDERED" ]] && SYSTEMD_UNITS+=("$LK_RENDERED")
        [[ -n "$BK_SVC_RENDERED" ]] && SYSTEMD_UNITS+=("$BK_SVC_RENDERED")
        [[ -n "$BK_TIMER_RENDERED" ]] && SYSTEMD_UNITS+=("$BK_TIMER_RENDERED")
        if systemd-analyze verify "${SYSTEMD_UNITS[@]}" 2>/dev/null; then
            ok "systemd 单元文件校验通过"
        else
            warn "systemd-analyze verify 校验未通过（继续安装）"
            info "如服务启动异常，请手动检查: journalctl -u ridgericetalk -f"
        fi
    else
        warn "systemd-analyze 不可用，跳过校验"
    fi

    # 安装单元文件
    cp "$RRT_RENDERED" /etc/systemd/system/ridgericetalk.service
    chmod 644 /etc/systemd/system/ridgericetalk.service
    ok "安装 /etc/systemd/system/ridgericetalk.service"

    if [[ "$LK_SERVICE_SKIP" == "true" ]]; then
        info "跳过安装 livekit.service（LiveKit 由主进程托管）"
    else
        cp "$LK_RENDERED" /etc/systemd/system/livekit.service
        chmod 644 /etc/systemd/system/livekit.service
        ok "安装 /etc/systemd/system/livekit.service"
    fi

    # 安装每日自动备份单元（service + timer）
    BK_TIMER_INSTALLED=false
    if [[ "$BACKUP_TIMER" == "true" ]]; then
        cp "$BK_SVC_RENDERED" /etc/systemd/system/ridgericetalk-backup.service
        chmod 644 /etc/systemd/system/ridgericetalk-backup.service
        cp "$BK_TIMER_RENDERED" /etc/systemd/system/ridgericetalk-backup.timer
        chmod 644 /etc/systemd/system/ridgericetalk-backup.timer
        ok "安装 /etc/systemd/system/ridgericetalk-backup.service 与 ridgericetalk-backup.timer"
        BK_TIMER_INSTALLED=true
    else
        info "已指定 --no-backup-timer，跳过每日自动备份单元安装"
    fi

    # daemon-reload + enable
    systemctl daemon-reload
    if [[ "$LK_SERVICE_SKIP" == "true" ]]; then
        systemctl enable ridgericetalk 2>/dev/null
        ok "已启用 ridgericetalk.service（livekit 由主进程托管，未启用独立服务）"
    else
        systemctl enable ridgericetalk livekit 2>/dev/null
        ok "已启用 ridgericetalk.service 和 livekit.service"
    fi

    # 每日自动备份：只 enable/start timer，绝不 start service
    # （避免部署当下就立刻跑一次备份；timer 到点后由 systemd 自动拉起 oneshot 服务）
    if [[ "$BK_TIMER_INSTALLED" == "true" ]]; then
        systemctl enable --now ridgericetalk-backup.timer 2>/dev/null
        ok "已启用并启动 ridgericetalk-backup.timer（每日 04:17 触发备份；未启动 service）"
        info "查看: systemctl list-timers ridgericetalk-backup.timer | journalctl -u ridgericetalk-backup"
    fi

    ok "systemd 服务安装完成"
    fi
fi

# ============================================================
# Stage 7.5/9: 可选安装 Netdata 系统监控
# ============================================================
if [[ "$WITH_MONITORING" == "true" ]] && run_stage 7; then
    echo -e "\n${C_BLUE}[7.5/9]${C_RESET} ${C_CYAN}安装 Netdata 系统监控...${C_RESET}"

    if ! dry_run \
        "下载并执行 Netdata kickstart 安装脚本（--non-interactive --stable-channel --disable-telemetry）"; then

    # 通过官方 kickstart 脚本安装（支持裸机，不依赖 Docker）
    info "下载 Netdata kickstart 脚本..."
    if curl -fsSL --connect-timeout 15 https://my-netdata.io/kickstart.sh -o /tmp/netdata-kickstart.sh; then
        info "执行 Netdata 安装（非交互、稳定通道、禁用遥测）..."
        if sh /tmp/netdata-kickstart.sh --non-interactive --stable-channel --disable-telemetry; then
            ok "Netdata 安装完成"

            # 健康检查（轮询 30 秒）
            info "等待 Netdata 启动..."
            ND_WAITED=0
            ND_MAX_WAIT=30
            while [[ $ND_WAITED -lt $ND_MAX_WAIT ]]; do
                if curl -sf "http://127.0.0.1:19999/api/v1/info" >/dev/null 2>&1; then
                    ok "Netdata 健康检查通过 (/${ND_MAX_WAIT}s)"
                    break
                fi
                sleep 1
                ND_WAITED=$((ND_WAITED + 1))
            done
            if [[ $ND_WAITED -ge $ND_MAX_WAIT ]]; then
                warn "Netdata 在 ${ND_MAX_WAIT}s 内未通过健康检查"
                info "请检查日志: journalctl -u netdata -f"
            fi
        else
            warn "Netdata kickstart 脚本执行失败，监控未安装（不阻断主流程）"
            info "可手动安装: curl -fsSL https://my-netdata.io/kickstart.sh | sh -s -- --non-interactive --stable-channel --disable-telemetry"
        fi
        rm -f /tmp/netdata-kickstart.sh
    else
        warn "Netdata kickstart 脚本下载失败，监控未安装（不阻断主流程）"
        info "中国大陆服务器可能无法访问 my-netdata.io，可手动安装: https://learn.netdata.cloud/docs/installing/nightly-packages"
    fi
    fi
fi

# ============================================================
# Stage 7.6/9: 准备 TTS 模型文件（Sherpa-ONNX）
# ============================================================
if run_stage 7; then
    stage "7.6" "准备 TTS 模型文件..."

    if ! dry_run \
        "准备 TTS 模型 $SERVER_DIR/models/tts/vits-melo-tts-zh_en（复制仓库预置模型或下载解压）"; then

    TTS_MODEL_DIR="$SERVER_DIR/models/tts"
    TTS_MODEL_NAME="vits-melo-tts-zh_en"
    TTS_MODEL_PATH="$TTS_MODEL_DIR/$TTS_MODEL_NAME"
    TTS_MODEL_SRC="$DEPLOY_DIR/models/tts/$TTS_MODEL_NAME"

    # ------------------------------------------------------------
    # N16：模型文件内容级校验（最小体积 1MB + Git LFS 指针特征拒绝）。
    # 缺陷背景：bundle 克隆（不含 LFS 对象）落地的 model.onnx 是 134B 指针
    # 文本，旧流程无校验照搬 → sherpa-onnx 解析失败 → cgo SIGABRT → 服务
    # 整体崩溃循环。校验与 Go 侧 internal/ttsworker.ValidateTTSModel 同口径
    #（阈值 1MB；LFS 指针首行特征 "version https://git-lfs"）。
    # ------------------------------------------------------------
    tts_model_valid() {
        local f="$1" size=""
        [[ -f "$f" ]] || return 1
        size="$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f" 2>/dev/null || echo 0)"
        (( size >= 1048576 )) || return 1
        if head -c 23 "$f" 2>/dev/null | grep -aq "version https://git-lfs"; then
            return 1
        fi
        return 0
    }
    # N16：隔离无效模型文件（改名保留供排查；运行目录回到「模型缺失→TTS
    # 禁用」安全态，服务不受影响）。幂等：可重复执行。
    tts_model_quarantine() {
        local d="$1"
        if [[ -f "$d/model.onnx" ]]; then
            mv -f "$d/model.onnx" "$d/model.onnx.invalid-$(date -u +%Y%m%d%H%M%S)" 2>/dev/null \
                || rm -f "$d/model.onnx"
            warn "已隔离无效模型文件（保留 model.onnx.invalid-* 副本供排查）: $d"
        fi
    }

    mkdir -p "$TTS_MODEL_DIR"

    if [[ "$SKIP_TTS_MODEL" == "true" ]]; then
        warn "跳过 TTS 模型准备（--skip-tts-model）"
        info "TTS 功能将在模型补充后可用；可使用 server/scripts/download-tts-model.sh 手动下载"
    elif [[ -f "$TTS_MODEL_PATH/model.onnx" ]] && tts_model_valid "$TTS_MODEL_PATH"; then
        ok "TTS 模型文件已存在且校验通过，跳过准备: $TTS_MODEL_PATH"
    elif [[ -f "$TTS_MODEL_SRC/model.onnx" ]]; then
        # 运行目录已有坏文件（上次部署遗留）：先隔离，保证幂等修复
        if [[ -f "$TTS_MODEL_PATH/model.onnx" ]]; then
            warn "运行目录已有模型但校验不通过（疑似 LFS 指针/损坏），先隔离旧文件"
            tts_model_quarantine "$TTS_MODEL_PATH"
        fi
        info "从仓库复制预置 TTS 模型到运行目录..."
        if command -v rsync &>/dev/null; then
            rsync -a "$TTS_MODEL_SRC/" "$TTS_MODEL_PATH/"
        else
            cp -r "$TTS_MODEL_SRC" "$TTS_MODEL_PATH"
        fi
        chown -R "$SERVICE_USER:$SERVICE_USER" "$TTS_MODEL_PATH" 2>/dev/null || true
        if tts_model_valid "$TTS_MODEL_PATH"; then
            ok "TTS 模型准备完成（校验通过）: $TTS_MODEL_PATH"
        else
            warn "TTS 模型无效（疑似 LFS 指针/损坏），已跳过安装，TTS 将禁用"
            tts_model_quarantine "$TTS_MODEL_PATH"
            info "修复方式: 在部署源执行 git lfs pull，或手动下载 server/scripts/download-tts-model.sh"
        fi
    else
        warn "未找到预置 TTS 模型文件: $TTS_MODEL_SRC"
        info "将尝试从网络下载 TTS 模型..."
        TTS_MODEL_URL="https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-melo-tts-zh_en.tar.bz2"
        TTS_MODEL_TMP=$(mktemp -d)
        if curl -fSL --connect-timeout 15 --progress-bar -o "$TTS_MODEL_TMP/model.tar.bz2" "$TTS_MODEL_URL"; then
            tar -xjf "$TTS_MODEL_TMP/model.tar.bz2" -C "$TTS_MODEL_DIR"
            rm -rf "$TTS_MODEL_TMP"
            chown -R "$SERVICE_USER:$SERVICE_USER" "$TTS_MODEL_PATH" 2>/dev/null || true
            if tts_model_valid "$TTS_MODEL_PATH"; then
                ok "TTS 模型下载完成（校验通过）: $TTS_MODEL_PATH"
            else
                warn "TTS 模型无效（疑似 LFS 指针/损坏），已跳过安装，TTS 将禁用"
                tts_model_quarantine "$TTS_MODEL_PATH"
                info "修复方式: 在部署源执行 git lfs pull，或手动下载 server/scripts/download-tts-model.sh"
            fi
        else
            warn "TTS 模型下载失败，服务启动后可尝试通过管理后台重新初始化"
            info "手动下载: curl -L -o model.tar.bz2 $TTS_MODEL_URL"
            info "手动解压: tar -xjf model.tar.bz2 -C $TTS_MODEL_DIR"
            info "或使用: server/scripts/download-tts-model.sh"
        fi
    fi
    fi
fi

# ============================================================
# Stage 7.7/9: 准备 EasyTier 虚拟局域网服务（DES-2026-0731-02）
# ============================================================
# EasyTier 是去中心化组网工具，服务端作为公网入口节点，客户端通过相同的
# network_name + network_secret 加入虚拟局域网。服务端不参与数据平面路由，
# 仅作为公网入口转发 peer 节点间的初始握手。
# 雨云服务器白名单：5005-5009，使用 UDP 5007 作为 EasyTier 监听端口。
if run_stage 7; then
    stage "7.7" "准备 EasyTier 虚拟局域网服务..."

    if ! dry_run \
        "下载并安装 EasyTier $EASYTIER_VERSION 到 $EASYTIER_BIN" \
        "生成/复用网络密钥 $EASYTIER_SECRET_FILE 并将 RRT_EASYTIER_SECRET 追加到 $ENV_FILE" \
        "渲染并安装 /etc/systemd/system/easytier.service，enable 并 restart"; then

    EASYTIER_TMPL="$SCRIPT_DIR/easytier.service.tmpl"

    # 检查模板文件
    if [[ ! -f "$EASYTIER_TMPL" ]]; then
        fail "未找到 EasyTier systemd 模板: $EASYTIER_TMPL"
        info "请确认 server/scripts/ 目录完整性"
        exit 1
    fi
    ok "使用 EasyTier systemd 模板: $EASYTIER_TMPL"

    # 检查 EasyTier 二进制是否已安装
    if [[ ! -x "$EASYTIER_BIN" ]]; then
        info "下载并安装 EasyTier $EASYTIER_VERSION..."

        # 根据架构选择下载链接
        # EasyTier release 文件名格式: easytier-linux-<arch>-<version>.zip
        case "$ARCH" in
            amd64)  EASYTIER_ARCH="x86_64" ;;
            arm64)  EASYTIER_ARCH="aarch64" ;;
            *)
                fail "不支持的架构: $ARCH（EasyTier 仅支持 x86_64 和 aarch64）"
                info "EasyTier 部署失败，虚拟局域网功能将不可用"
                info "可手动下载并安装到 $EASYTIER_BIN 后重试: sudo $SCRIPT_NAME --stage vpn"
                # 非致命，继续后续 stage
                EASYTIER_INSTALL_FAILED=true
                ;;
        esac

        if [[ "${EASYTIER_INSTALL_FAILED:-}" != "true" ]]; then
            EASYTIER_URL="https://github.com/EasyTier/EasyTier/releases/download/${EASYTIER_VERSION}/easytier-linux-${EASYTIER_ARCH}-${EASYTIER_VERSION}.zip"
            EASYTIER_TMP_DIR=$(mktemp -d)
            info "下载 EasyTier: $EASYTIER_URL"

            # 尝试直接下载，失败则尝试镜像
            if curl -fSL --connect-timeout 15 --progress-bar -o "$EASYTIER_TMP_DIR/easytier.zip" "$EASYTIER_URL"; then
                ok "EasyTier 下载完成"
            else
                warn "GitHub 直连失败，尝试镜像下载..."
                for MIRROR in "https://ghfast.top/" "https://gh-proxy.com/" "https://ghproxy.net/"; do
                    info "尝试镜像: $MIRROR"
                    if curl -fSL --connect-timeout 15 --progress-bar -o "$EASYTIER_TMP_DIR/easytier.zip" "${MIRROR}${EASYTIER_URL}"; then
                        ok "镜像下载成功: $MIRROR"
                        break
                    fi
                done
            fi

            if [[ -f "$EASYTIER_TMP_DIR/easytier.zip" ]]; then
                # 检查 unzip 是否可用
                if ! command -v unzip &>/dev/null; then
                    info "安装 unzip..."
                    rrt_pkg_install unzip >/dev/null 2>&1 || true
                fi

                if unzip -o "$EASYTIER_TMP_DIR/easytier.zip" -d "$EASYTIER_TMP_DIR" >/dev/null 2>&1; then
                    # 查找 easytier-core 二进制
                    EASYTIER_EXTRACTED=$(find "$EASYTIER_TMP_DIR" -name "easytier-core" -type f | head -1)
                    if [[ -n "$EASYTIER_EXTRACTED" ]]; then
                        install -m 0755 "$EASYTIER_EXTRACTED" "$EASYTIER_BIN"
                        ok "EasyTier 二进制安装完成: $EASYTIER_BIN"
                    else
                        warn "解压后未找到 easytier-core 二进制"
                        EASYTIER_INSTALL_FAILED=true
                    fi
                else
                    warn "EasyTier 解压失败"
                    EASYTIER_INSTALL_FAILED=true
                fi
            else
                warn "EasyTier 下载失败（所有镜像均不可达）"
                EASYTIER_INSTALL_FAILED=true
            fi

            rm -rf "$EASYTIER_TMP_DIR"
        fi
    else
        ok "EasyTier 二进制已安装: $EASYTIER_BIN"
    fi

    # 即使下载失败，也继续配置（secret 和 systemd 模板），让用户后续手动安装二进制
    if [[ "${EASYTIER_INSTALL_FAILED:-}" != "true" ]] || [[ ! -x "$EASYTIER_BIN" ]]; then
        # 生成网络密钥（持久化到 /etc/easytier/secret）
        mkdir -p "$(dirname "$EASYTIER_SECRET_FILE")"
        if [[ -f "$EASYTIER_SECRET_FILE" ]]; then
            EASYTIER_SECRET=$(cat "$EASYTIER_SECRET_FILE" | tr -d '[:space:]')
            ok "复用已存在的网络密钥: $EASYTIER_SECRET_FILE"
        else
            # 生成 32 字节十六进制密钥
            EASYTIER_SECRET=$(openssl rand -hex 32)
            echo -n "$EASYTIER_SECRET" > "$EASYTIER_SECRET_FILE"
            chmod 600 "$EASYTIER_SECRET_FILE"
            chown root:root "$EASYTIER_SECRET_FILE"
            ok "已生成 EasyTier 网络密钥: $EASYTIER_SECRET_FILE"
            info "密钥已持久化，客户端连接时由服务端通过 API 下发（不存储在客户端）"
        fi

        # 创建数据目录
        mkdir -p "$EASYTIER_DATA_DIR"
        chown "$SERVICE_USER:$SERVICE_USER" "$EASYTIER_DATA_DIR"

        # 渲染 systemd 模板
        EASYTIER_RENDERED="/tmp/easytier.service.$(date +%s)"
        sed -e "s|{{SERVICE_USER}}|$SERVICE_USER|g" \
            -e "s|{{EASYTIER_BIN}}|$EASYTIER_BIN|g" \
            -e "s|{{EASYTIER_SECRET}}|$EASYTIER_SECRET|g" \
            -e "s|{{EASYTIER_PORT}}|$EASYTIER_PORT|g" \
            "$EASYTIER_TMPL" > "$EASYTIER_RENDERED"

        # 安装 systemd 服务
        cp "$EASYTIER_RENDERED" /etc/systemd/system/easytier.service
        chmod 644 /etc/systemd/system/easytier.service
        rm -f "$EASYTIER_RENDERED"
        ok "安装 /etc/systemd/system/easytier.service"

        # 启用并启动 EasyTier 服务
        systemctl daemon-reload
        systemctl enable easytier.service >/dev/null 2>&1
        systemctl restart easytier.service

        # 等待服务启动并健康检查
        info "等待 EasyTier 服务启动..."
        EASYTIER_HEALTH_OK=false
        for i in $(seq 1 10); do
            sleep 1
            if curl -sf --max-time 2 "http://127.0.0.1:${EASYTIER_WEB_PORT}/api/v1/health" >/dev/null 2>&1; then
                EASYTIER_HEALTH_OK=true
                break
            fi
        done

        if [[ "$EASYTIER_HEALTH_OK" == "true" ]]; then
            ok "EasyTier 服务健康检查通过（Web API: 127.0.0.1:${EASYTIER_WEB_PORT}）"
            ok "EasyTier 监听 UDP 端口: $EASYTIER_PORT"
        else
            warn "EasyTier 服务健康检查失败"
            info "查看日志: journalctl -u easytier -n 50 --no-pager"
            info "常见原因：二进制架构不匹配、端口被占用、systemd 沙箱限制"
        fi

        # 提示防火墙配置
        info "防火墙提示：确保 UDP $EASYTIER_PORT 端口对外开放"
        info "雨云服务器：UDP 5007 在白名单 5005-5009 范围内，无需额外配置"

        # 将 EasyTier 网络密钥追加到 .env.production（供后端 handler 读取）
        if [[ -f "$ENV_FILE" ]]; then
            # 移除旧的 RRT_EASYTIER_SECRET 行（如有）
            sed -i '/^RRT_EASYTIER_SECRET=/d' "$ENV_FILE"
            # 追加新密钥
            echo "RRT_EASYTIER_SECRET=$EASYTIER_SECRET" >> "$ENV_FILE"
            ok "已将 EasyTier 网络密钥写入 $ENV_FILE"
        else
            warn "$ENV_FILE 不存在，跳过 EasyTier 密钥写入"
            info "请确认 Stage 6 已生成配置文件"
        fi
    else
        warn "EasyTier 二进制安装失败，跳过 systemd 服务配置"
        info "请手动安装 easytier-core 到 $EASYTIER_BIN 后重试: sudo $SCRIPT_NAME --stage vpn"
    fi
    fi
fi

# ============================================================
# Stage 8/9: 准备 NeteaseCloudMusicApi（音乐机器人依赖）
# ============================================================
if run_stage 8; then
    stage 8 "准备 NeteaseCloudMusicApi..."

    if ! dry_run \
        "在 $DEPLOY_DIR/services/netease-api 执行 npm install --omit=dev（必要时切换 npmmirror 镜像）" \
        "修正 services/netease-api 目录所有权为 $SERVICE_USER"; then

    NETEASE_DIR="$DEPLOY_DIR/services/netease-api"

    if [[ ! -f "$NETEASE_DIR/package.json" ]]; then
        fail "未找到仓库内置 NeteaseCloudMusicApi 源码: $NETEASE_DIR"
        info "请确认项目源码已完整部署到 $DEPLOY_DIR，services/netease-api/ 应包含 package.json"
        exit 1
    fi
    ok "使用仓库内置 NeteaseCloudMusicApi 源码: $NETEASE_DIR"

    # npm install（失败非致命，后端启动时会重试）
    # 以服务用户身份运行，并显式设置 HOME，避免 root 安装后子进程因
    # ProtectHome=true 与文件所有权问题无法访问 node_modules。
    # 使用 sudo -u 而非 su，避免系统用户无密码导致 su 认证失败。
    NETEASE_INSTALL_LOG="$SERVER_DIR/logs/netease-npm-install.log"
    mkdir -p "$(dirname "$NETEASE_INSTALL_LOG")"
    if command -v npm &>/dev/null; then
        info "安装 NeteaseCloudMusicApi npm 依赖（生产依赖）..."
        # 中国大陆服务器使用 npmmirror 镜像
        # G-5：cn 档直接使用 npmmirror（不探测）；auto 档保持「探测不可达才切换」
        NPM_REGISTRY_ARG="$(rrt_mirror_npm_registry)"
        if [[ -n "$NPM_REGISTRY_ARG" ]]; then
            info "镜像预设 cn：npm registry 直接使用 npmmirror"
            (cd "$NETEASE_DIR" && sudo -u "$SERVICE_USER" bash -c "HOME='$RIDGERICETALK_HOME' npm config set registry $NPM_REGISTRY_ARG" >>"$NETEASE_INSTALL_LOG" 2>&1)
        elif curl -sf --max-time 3 https://registry.npmjs.org/ >/dev/null 2>&1; then
            info "使用默认 npm registry"
        else
            info "检测到无法访问 npmjs.org，切换到 npmmirror 镜像"
            (cd "$NETEASE_DIR" && sudo -u "$SERVICE_USER" bash -c "HOME='$RIDGERICETALK_HOME' npm config set registry https://registry.npmmirror.com" >>"$NETEASE_INSTALL_LOG" 2>&1)
        fi
        if (cd "$NETEASE_DIR" && sudo -u "$SERVICE_USER" bash -c "HOME='$RIDGERICETALK_HOME' npm install --omit=dev --quiet" >>"$NETEASE_INSTALL_LOG" 2>&1); then
            ok "NeteaseCloudMusicApi npm 依赖安装完成"
        else
            warn "NeteaseCloudMusicApi npm install 失败，后端启动时会自动重试"
            info "详细日志: $NETEASE_INSTALL_LOG"
            info "请检查 Node.js 版本（需 18+）和 npm registry 可用性"
        fi
    else
        warn "npm 不可用，跳过 NeteaseCloudMusicApi 依赖安装"
        info "后端启动时将尝试安装依赖，请确保 Node.js/npm 已可用"
    fi

    # 确保服务用户对源码目录可读写（npm install 已经以服务用户运行，此处做兜底）
    chown -R "$SERVICE_USER:$SERVICE_USER" "$NETEASE_DIR" 2>/dev/null || true
    # 确保 node_modules 中可执行脚本（如 node）对服务用户可读
    chmod -R u+rwX "$NETEASE_DIR" 2>/dev/null || true

    ok "NeteaseCloudMusicApi 准备完成"
    fi
fi

# ============================================================
# Stage 9/9: 启动服务与健康检查
# ============================================================
if run_stage 9; then
    stage 9 "启动服务与健康检查..."

    # --tls 档位的 dry-run 计划行（none 档无新增动作，计划与既有完全一致）
    TLS_DRY_ACTIONS=()
    if [[ "$TLS_MODE" == "selfsigned" ]]; then
        TLS_DRY_ACTIONS=("配置 HTTPS（--tls selfsigned）：自签名 SAN 证书 + Nginx 反代，$ENV_FILE 切换 https/wss 并重启服务")
    elif [[ "$TLS_MODE" == "acme" ]]; then
        TLS_DRY_ACTIONS=("配置 HTTPS（--tls acme）：certbot 为 $TLS_DOMAIN 签发 Let's Encrypt 证书并接入 Nginx 反代（80/443 需公网可达）")
    fi

    # N28：--enable-turn 的 dry-run 计划行（默认关 → 数组为空，计划与既有一致）
    FW_DRY_EXTRA=""
    TURN_DRY_ACTIONS=()
    if [[ "$ENABLE_TURN" == "true" ]]; then
        FW_DRY_EXTRA=" ${TURN_UDP_PORT}/udp ${TURN_TLS_PORT}/tcp"
        TURN_DRY_ACTIONS=(
            "livekit.yaml 写入 turn 块（udp_port=${TURN_UDP_PORT}；证书就绪时含 tls_port=${TURN_TLS_PORT}）"
            "防火墙/安全组需放行 TURN 端口：${TURN_UDP_PORT}/udp ${TURN_TLS_PORT}/tcp"
        )
    fi

    if ! dry_run \
        "执行数据库迁移 ./ridgericetalk-migrate up 并修正 storage/logs 所有权" \
        "自动放行防火墙端口（ufw/firewalld/iptables：$PORT_API/tcp $PORT_ADMIN/tcp $PORT_LK_WS/tcp $PORT_LK_TCP/tcp $PORT_LK_UDP/udp$FW_DRY_EXTRA $EASYTIER_PORT/udp）" \
        "（RHEL 系 SELinux enforcing 时）semanage 配置部署二进制 bin_t 标签、LiveKit 端口 http_port_t、服务用户 HOME 标签" \
        "systemctl restart ridgericetalk 并等待健康检查" \
        "若 LiveKit 由主进程托管且已存在独立 livekit.service：systemctl disable --now livekit" \
        "systemctl restart livekit（若 LiveKit 由主进程托管则跳过）" \
        "部署收尾 WS 握手自检（N27：以实际 origin 探测 /ws，预期 101）" \
        "（仅当本次运行生成了新口令时）清理临时口令文件 /tmp/.rrt_pg_pwd" \
        ${TLS_DRY_ACTIONS[@]+"${TLS_DRY_ACTIONS[@]}"} \
        ${TURN_DRY_ACTIONS[@]+"${TURN_DRY_ACTIONS[@]}"}; then

    # 应用数据库版本化迁移（生产 PostgreSQL 必须执行）
    info "应用数据库迁移..."
    cd "$SERVER_DIR"
    # 迁移工具默认加载 .env，需显式 source .env.production 中的变量
    if [[ -f "$ENV_FILE" ]]; then
        # 安全地导出环境变量（忽略注释和空行）
        while IFS='=' read -r key value; do
            [[ -z "$key" || "$key" =~ ^[[:space:]]*# ]] && continue
            # 去除首尾空白
            key=$(echo "$key" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
            value=$(echo "$value" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
            [[ -n "$key" ]] && export "$key=$value"
        done < "$ENV_FILE"
        info "已从 $ENV_FILE 加载环境变量"
    fi
    if ensure_migrate_binary; then
        if ./ridgericetalk-migrate up; then
            ok "数据库迁移完成"
            # 迁移工具可能生成 secrets.json 等文件，确保服务用户可读写
            chown -R "$SERVICE_USER:$SERVICE_USER" "$STORAGE_DIR" "$SERVER_DIR/logs" 2>/dev/null || true
            # secrets.json 权限严格 0600
            [[ -f "$STORAGE_DIR/secrets.json" ]] && chmod 600 "$STORAGE_DIR/secrets.json"
            ok "storage 目录所有权已修正为 $SERVICE_USER"
        else
            fail "数据库迁移失败"
            info "请检查 PostgreSQL 连接配置和迁移文件: $DEPLOY_DIR/migrations/"
            info "可手动重试: cd $SERVER_DIR && ./ridgericetalk-migrate up"
            exit 1
        fi
    else
        fail "迁移工具不存在: $SERVER_DIR/ridgericetalk-migrate"
        info "请从 build 阶段重新执行: sudo $SCRIPT_NAME --stage build"
        exit 1
    fi

    # 自动开放防火墙
    configure_firewall

    # DES-2026-0912-03 D6：RHEL 系 SELinux enforcing 适配（部署二进制 bin_t、
    # LiveKit 媒体端口 http_port_t、服务用户 HOME 标签）。仅 enforcing 时动手，
    # Debian 系 / Permissive / Disabled 零动作；失败不阻断，仅告警。
    if ! rrt_selinux_adapt "$SERVER_DIR" "$LIVEKIT_DIR" "$RIDGERICETALK_HOME" "$PORT_LK_TCP" "$PORT_LK_UDP"; then
        warn "SELinux 适配未全部完成：若服务启动失败，请检查 AVC 拒绝（ausearch -m avc -ts recent）"
    fi

    # 启动 NeteaseCloudMusicApi（已不再需要独立 systemd 服务，由后端内嵌管理）
    # 保留此处的健康检查提示，便于排查
    if [[ -f "$DEPLOY_DIR/services/netease-api/app.js" ]]; then
        info "NeteaseCloudMusicApi 由 ridgericetalk 服务内嵌管理，无需独立启动"
    fi

    # 启动后端
    info "启动 ridgericetalk 服务..."
    systemctl restart ridgericetalk
    ok "ridgericetalk 服务已启动"

    # 等待后端渲染 livekit.yaml（轮询 30 秒）
    info "等待后端渲染 livekit.yaml..."
    LK_WAITED=0
    LK_MAX_WAIT=30
    while [[ $LK_WAITED -lt $LK_MAX_WAIT ]]; do
        if [[ -f "$LIVEKIT_YAML" ]]; then
            ok "livekit.yaml 已渲染: $LIVEKIT_YAML"
            break
        fi
        sleep 1
        LK_WAITED=$((LK_WAITED + 1))
        if [[ $((LK_WAITED % 5)) -eq 0 ]]; then
            info "等待 livekit.yaml 渲染... (${LK_WAITED}s/${LK_MAX_WAIT}s)"
        fi
    done

    if [[ ! -f "$LIVEKIT_YAML" ]]; then
        warn "livekit.yaml 未在 ${LK_MAX_WAIT}s 内渲染，LiveKit 可能无法启动"
        info "请检查后端日志: journalctl -u ridgericetalk -f"
    fi

    # 启动 LiveKit（由主进程托管时跳过独立服务，避免与主进程内嵌 LiveKit 抢端口）
    if livekit_managed_by_main; then
        info "检测到 LiveKit 由主进程托管（RRT_LIVEKIT_AUTOSTART=false 且未显式禁用 RRT_EMBEDDED_DEPS）"
        # 续跑（如 --stage start）时同样确保停用既有的独立单元（受 --dry-run 守卫）
        disable_managed_livekit_unit
    else
        info "启动 livekit 服务..."
        systemctl restart livekit || warn "livekit 启动失败（可能是 livekit.yaml 未就绪）"
    fi

    # 后端健康检查（轮询 60 秒）
    info "等待后端健康检查..."
    HC_WAITED=0
    HC_MAX_WAIT=60
    HC_READY=false
    while [[ $HC_WAITED -lt $HC_MAX_WAIT ]]; do
        sleep 1
        HC_WAITED=$((HC_WAITED + 1))
        if curl -sf "http://localhost:$PORT_API/api/health" >/dev/null 2>&1; then
            HC_READY=true
            break
        fi
        if [[ $((HC_WAITED % 5)) -eq 0 ]]; then
            info "等待后端就绪... (${HC_WAITED}s/${HC_MAX_WAIT}s)"
        fi
    done

    if [[ "$HC_READY" == "true" ]]; then
        ok "后端健康检查通过 (/${HC_WAITED}s)"
    else
        warn "后端在 ${HC_MAX_WAIT}s 内未通过健康检查"
        info "请检查日志: journalctl -u ridgericetalk -f"
    fi

    # 等待 bootstrap token 文件生成（最多 30 秒）
    BOOTSTRAP_TOKEN=""
    TOKEN_FILE="${DEPLOY_DIR}/server/storage/.bootstrap_token"
    for i in $(seq 1 30); do
        if [ -f "$TOKEN_FILE" ]; then
            BOOTSTRAP_TOKEN=$(cat "$TOKEN_FILE")
            break
        fi
        sleep 1
    done

    # 收集 IP 信息
    LOCAL_IPS=$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -v '^$' | grep -v '^127\.' | grep -v '^169\.254\.' || true)
    if [[ -z "$PUBLIC_IP" ]]; then
        # 能力层 lib/net.sh（境外服务 → 云元数据兜底）
        PUBLIC_IP="$(rrt_public_ip || true)"
    fi

    # ============================================================
    # Stage 9.5: HTTPS 配置（--tls selfsigned|acme；none 档零动作）
    # ============================================================
    # 放在服务已启动、健康检查通过之后：TLS 反代需要读取 $ENV_FILE 中的既有端口
    # 配置，配置完成后 setup-lan-https.sh 会重启服务并改写 .env 为 https/wss。
    if [[ "$TLS_MODE" != "none" ]]; then
        stage "9.5" "配置 HTTPS（--tls $TLS_MODE）..."
        if [[ ! -f "$ENV_FILE" ]]; then
            fail "未找到 $ENV_FILE，无法配置 HTTPS（TLS 反代依赖基础部署生成的端口配置）"
            info "请先完成配置生成（--stage config），再执行: sudo $SCRIPT_NAME --stage start --tls $TLS_MODE"
            exit 1
        fi
        case "$TLS_MODE" in
            selfsigned) tls_setup_selfsigned ;;
            acme)       tls_setup_acme ;;
        esac
        echo ""
    fi

    # ============================================================
    # Stage 9.6: 部署收尾自检（N27）：CORS 一致性 + WS 握手探测
    # ============================================================
    # 以「客户端实际使用的对外 origin」（RRT_PUBLIC_ADDRESS）核对 CORS 白名单
    # 并探测 /ws。任一失败 → 判定部署失败并明确报错，不再静默带病上线
    # （N27 事故若有此步会在部署当时暴露）。RRT_WS_PROBE_TOKEN=<accessToken>
    # 时升级为 101 全量自检；默认无凭据走受限自检（CORS 一致性 + 401 探测）。
    stage "9.6" "部署收尾自检（CORS 一致性 + WS 握手，N27）..."
    if ! rrt_ws_deploy_selfcheck "$ENV_FILE" "$PUBLIC_IP" "$PORT_API"; then
        fail "部署自检未通过 —— 本次部署按 FAIL 处理（实时消息链路不可用的故障此前会静默上线）"
        exit 1
    fi
    echo ""

    # 输出部署信息
    echo ""
    banner "RidgeRiceTalk 部署完成"

    echo -e "  ${C_WHITE}部署模式:${C_RESET}  裸机部署（baremetal）"
    echo ""
    echo -e "  ${C_BLUE}端口配置:${C_RESET}"
    echo -e "    API:       ${C_GREEN}$PORT_API${C_RESET}"
    echo -e "    Admin:     ${C_GREEN}$PORT_ADMIN${C_RESET}"
    echo -e "    LiveKit:   ws=${C_GREEN}$PORT_LK_WS${C_RESET}  tcp=${C_GREEN}$PORT_LK_TCP${C_RESET}  udp=${C_GREEN}$PORT_LK_UDP${C_RESET}"
    echo ""

    if [[ -n "$LOCAL_IPS" ]]; then
        echo -e "  ${C_BLUE}本地访问:${C_RESET}"
        echo "$LOCAL_IPS" | while read -r ip; do
            echo -e "    Voice:   ${C_GREEN}${RRT_URL_SCHEME}://$ip:$PORT_API/setup${C_RESET}"
            echo -e "    Admin:   ${C_GREEN}${RRT_URL_SCHEME}://$ip:$PORT_ADMIN/admin${C_RESET}"
        done
    fi

    if [[ -n "$PUBLIC_IP" ]]; then
        echo ""
        echo -e "  ${C_BLUE}公网访问:${C_RESET}"
        echo -e "    Setup:    ${C_GREEN}${RRT_URL_SCHEME}://$PUBLIC_IP:$PORT_API/setup${C_RESET}"
        echo -e "    Voice:    ${C_GREEN}${RRT_URL_SCHEME}://$PUBLIC_IP:$PORT_API${C_RESET}"
        echo -e "    Admin:    ${C_GREEN}${RRT_URL_SCHEME}://$PUBLIC_IP:$PORT_ADMIN/admin${C_RESET}"
    fi

    echo ""
    echo -e "  ${C_BLUE}首次使用步骤:${C_RESET}"
    echo -e "    1. 打开 Admin 页面: ${C_GREEN}${RRT_URL_SCHEME}://${PUBLIC_IP}:${PORT_ADMIN}/admin${C_RESET}"
    echo -e "    2. 输入 Bootstrap Token 完成 Owner 初始化"
    echo -e "    3. 在管理后台配置网络（公网 IP、LiveKit 端口）"
    echo -e "    4. 启用需要的功能模块"
    echo ""
    echo -e "  ${C_BLUE}命令行初始化示例:${C_RESET}"
    echo -e "    ${C_GRAY}# 复制示例文件并替换 bootstrap token${C_RESET}"
    echo -e "    ${C_GRAY}cp ${DEPLOY_DIR}/server/scripts/examples/bootstrap-owner.json /tmp/bootstrap-owner.json${C_RESET}"
    echo -e "    ${C_GRAY}# 编辑 /tmp/bootstrap-owner.json 填入上方 Bootstrap Token${C_RESET}"
    echo -e "    ${C_GRAY}curl -sk -X POST http://${PUBLIC_IP}:${PORT_ADMIN}/api/admin/bootstrap/register \\\${C_RESET}"
    echo -e "    ${C_GRAY}  -H 'Content-Type: application/json' \\\${C_RESET}"
    echo -e "    ${C_GRAY}  -d @/tmp/bootstrap-owner.json${C_RESET}"
    echo ""
    echo -e "  ${C_BLUE}常用命令:${C_RESET}"
    echo -e "    ${C_GRAY}查看后端日志:  journalctl -u ridgericetalk -f${C_RESET}"
    echo -e "    ${C_GRAY}查看 LiveKit:   journalctl -u livekit -f${C_RESET}"
    echo -e "    ${C_GRAY}重启后端:      systemctl restart ridgericetalk${C_RESET}"
    echo -e "    ${C_GRAY}重启 LiveKit:   systemctl restart livekit${C_RESET}"
    if [[ "$WITH_MONITORING" == "true" ]]; then
        echo -e "    ${C_GRAY}Netdata 监控:  http://${PUBLIC_IP}:19999${C_RESET}"
    fi
    echo ""
    # 仅在服务器「尚未初始化」时才提示 Bootstrap Token。
    # 已初始化的服务器上 .bootstrap_token 是首次安装时的遗留文件（不会被清理），
    # 此前在重跑部署时会把这个**过期的秘密**打印到终端与日志里（2026-09-12 生产实测发现），
    # 既无用又扩大暴露面。
    SERVER_INITIALIZED=$(curl -s --max-time 5 "http://127.0.0.1:${PORT_API}/api/v1/server/info" 2>/dev/null \
        | sed -n 's/.*"initialized"[[:space:]]*:[[:space:]]*\(true\|false\).*/\1/p' | head -1 || true)
    if [[ "$SERVER_INITIALIZED" == "true" ]]; then
        echo -e "  ${C_GREEN}服务器已初始化${C_RESET}（无需 Bootstrap Token；如需重置 Owner 请联系管理员）"
    else
        echo -e "  ${C_YELLOW}请访问管理后台完成 Owner 初始化${C_RESET}"
        echo -e "  ${C_GRAY}管理后台: ${RRT_URL_SCHEME}://${PUBLIC_IP}:${PORT_ADMIN}/admin${C_RESET}"
        if [ -n "$BOOTSTRAP_TOKEN" ]; then
            echo -e "  ${C_GREEN}Bootstrap Token: ${BOOTSTRAP_TOKEN}${C_RESET}"
        else
            echo -e "  ${C_RED}警告: Bootstrap token 文件未生成，请检查 journalctl -u ridgericetalk 日志${C_RESET}"
        fi
    fi
    # D4 决策：none（默认）档行为不变，但必须明确告知网页端语音的 HTTPS 前提 ——
    # 浏览器只允许在安全上下文（HTTPS/localhost）中调用 getUserMedia 采集麦克风，
    # 纯 HTTP 部署下网页端语音会静默不可用，用户往往到使用阶段才发现。
    if [[ "$TLS_MODE" == "none" ]]; then
        echo -e "  ${C_YELLOW}提示: 网页端语音（麦克风采集）需要 HTTPS 环境浏览器才允许使用${C_RESET}"
        echo -e "  ${C_GRAY}  局域网/内网: 重跑本脚本加 --tls selfsigned，或执行 sudo $SCRIPT_DIR/setup-lan-https.sh${C_RESET}"
        echo -e "  ${C_GRAY}  公网域名:   重跑本脚本加 --tls acme --domain <域名>（80/443 需公网可达）${C_RESET}"
    fi
    echo ""
    fi
fi

# ============================================================
# 收尾：清理临时数据库口令文件
# ============================================================
# /tmp/.rrt_pg_pwd 只在 Stage 5 本次「真的生成了新口令并写入」时（PG_PWD_WRITTEN=true）
# 才删除；其它情况保留——尤其是「--stage db 之后再来 --stage config」的续跑场景，
# 该文件是 Stage 6 取到口令的唯一来源，误删会导致续跑失败。
# --dry-run 下既不创建也不删除（零副作用）。
if [[ "$DRY_RUN" == "true" ]]; then
    : # dry-run：零副作用
elif [[ "$PG_PWD_WRITTEN" == "true" ]]; then
    rm -f /tmp/.rrt_pg_pwd 2>/dev/null || true
elif [[ -f /tmp/.rrt_pg_pwd ]]; then
    info "保留 /tmp/.rrt_pg_pwd（本次运行未生成新口令；供 --stage db 后 --stage config 续跑使用）"
fi

echo ""
if [[ "$DRY_RUN" == "true" ]]; then
    banner "DRY-RUN 完成"
    info "DRY-RUN 完成，未做任何修改"
    echo ""
    exit 0
fi
banner "部署流程结束"
