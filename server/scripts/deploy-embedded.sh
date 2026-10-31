#!/usr/bin/env bash
# RidgeRiceTalk 嵌入式依赖一键部署脚本（Linux/macOS）
# 不依赖 systemd，适合单台 VPS、树莓派、WSL、macOS 等环境
# ---------------------------------------------------------------
# 用法：
#   sudo ./deploy-embedded.sh                          # 自动探测公网 IP
#   sudo ./deploy-embedded.sh --public-address http://YOUR_IP:8080
#   ./deploy-embedded.sh --public-address http://localhost:8080

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

stage()  { echo -e "\n${C_BLUE}[$1/$2]${C_RESET} ${C_CYAN}$3${C_RESET}"; }
ok()     { echo -e "  ${C_GREEN}[OK]${C_RESET} $1"; }
warn()   { echo -e "  ${C_YELLOW}[WARN]${C_RESET} $1"; }
fail()   { echo -e "  ${C_RED}[FAIL]${C_RESET} $1"; }
info()   { echo -e "  ${C_GRAY}[INFO]${C_RESET} $1"; }
banner() {
    echo -e "\n${C_MAGENTA}========================================${C_RESET}"
    echo -e "${C_MAGENTA}  $1${C_RESET}"
    echo -e "${C_MAGENTA}========================================${C_RESET}"
}

# A2（FIX-20261003-01 NJS-1）：sudo / 非登录 shell 下 PATH 常不含已装 Go 的
# 标准安装位置（Go 实际在 /usr/local/go/bin，用户 PATH 有、root PATH 无），
# 只查 PATH 会误判「未安装」并中止部署，报错文案引导 go.dev 在无外网 VM 是死路。
# 失败前依次探测标准位置，找到即前置并入 PATH（复验即可用）；PATH 正常者零变化。
rrt_ensure_go_on_path() {
    command -v go &>/dev/null && return 0
    local cand dir
    for cand in \
        "/usr/local/go/bin" \
        /usr/lib/go*/bin \
        "${HOME:-/root}/go/bin" \
        "/opt/homebrew/bin"; do
        # /usr/lib/go*/bin 需经 glob 展开；未命中时字面量传入，-x 判定自然失败
        for dir in $cand; do
            if [[ -x "$dir/go" ]]; then
                local gv
                gv="$("$dir/go" version 2>/dev/null || echo 'go')"
                export PATH="$dir:$PATH"
                info "PATH 中无 go，已从标准位置并入: $dir（$gv）"
                return 0
            fi
        done
    done
    return 1
}

# ============================================================
# 常量与默认值
# ============================================================
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
# task-96：LiveKit API 密钥助手（生成/幂等校验/轮换提示）。缺失时不影响部署主流程，
# 只在环境配置阶段给出告警 —— 部署脚本本身的健壮性优先于新特性。
if [[ -f "$SCRIPT_DIR/lib/livekit-keys.sh" ]]; then
    # shellcheck source=lib/livekit-keys.sh
    source "$SCRIPT_DIR/lib/livekit-keys.sh"
fi
DEPLOY_DIR="${DEPLOY_DIR:-/opt/ridgericetalk}"
SERVER_DIR="$DEPLOY_DIR/server"
WEB_DIR="$DEPLOY_DIR/web"
STORAGE_DIR="$SERVER_DIR/storage"
ENV_FILE="$SERVER_DIR/.env.production"
# H-1（reviewer 20261029 审查）：在线构建产物名 `ridgericetalk`、离线包内交付名
# `ridgericetalk-server`（build-offline-bundle.sh 只打后者）。两者都必须认，否则
# --skip-build 路径（install-offline.sh 恒走此路）离线安装 100% 失败。
# 具体取值在 Stage 4（构建/复用后端）结束时确定。
SERVER_BIN=""

PUBLIC_ADDRESS=""
PUBLIC_ADDRESS_EXPLICIT=false
# G-5（网络适配收口）：与 deploy-baremetal.sh 同口径的参数。
# 全部默认空 / false —— 不传时生成的 .env 与改动前逐字节一致（可回滚）。
PUBLIC_ADMIN_ADDRESS_ARG=""
PUBLIC_LK_URL_ARG=""
MIRROR_PRESET_ARG=""
HTTP_PROXY_ARG=""
HTTPS_PROXY_ARG=""
NAT_MODE=false
SYSTEM_PROXY=false
PORT_API=8080
PORT_ADMIN=9090
PORT_LK_WS=7880
PORT_LK_TCP=7881
PORT_LK_UDP=7882
FORCE=false
SKIP_FRONTEND=false
SKIP_BUILD=false
# task-49（rc3 真机实测）：离线安装下 RRT_EMBEDDED_DEPS 默认 false —— 离线包自带/
# 系统预置依赖，若仍让服务端启动期联网下载（FFmpeg/LiveKit/netease），无外网机器
# 会卡在下载（进程活着但不监听 API）→ 健康检查 60s 超时 → 安装 RC=1。
# 在线安装/开发者路径默认仍为 true（行为不变）。本开关仅用于离线场景显式放行下载。
ALLOW_EMBEDDED_DEPS_DOWNLOAD=false
# N28（DES-20261002-01 §3.4）：TURN 参数化开关（--enable-turn），**默认关**。
# 本脚本不直接生成 livekit.yaml —— 服务端首次启动按 livekit/livekit.yaml.template
# 渲染（文件已存在则跳过渲染）。开启时在部署树内的模板追加 turn 块（udp 3478），
# 首启渲染即带出；证书场景（tls_port 5349）需要证书文件路径，embedded 轻量定位
# 不内置 TLS 三档，暂只支持 TURN over UDP。
ENABLE_TURN=false
TURN_UDP_PORT=3478
TURN_TLS_PORT=5349

# ============================================================
# 帮助信息
# ============================================================
show_help() {
    cat <<'EOF'
Usage: deploy-embedded.sh [OPTIONS]

RidgeRiceTalk 嵌入式依赖一键部署脚本（Linux/macOS）

Options:
  --help                    显示此帮助
  --public-address <url>    对外访问地址（可省略，自动探测；例如 http://203.0.113.7:8080）
  --public-admin-address <url> G-5：Admin 对外地址（默认不加入 CORS 白名单；显式给出才加，
                            例如 NAT 下 Admin 外部端口与 API 不同时）
  --public-lk-url <ws-url>  G-5：LiveKit 对外 WebSocket URL（默认 ws://127.0.0.1:<ws>，
                            桌面端会把 localhost 重写为 API 主机；反代/独立域名场景显式给）
  --mirror-preset <mode>    G-5：下载镜像预设 cn|global|auto（默认 auto，规则同 baremetal）
  --http-proxy <url>        G-5：经代理出网（curl/npm/git 等，导出 http_proxy）
  --https-proxy <url>       G-5：同上（HTTPS 流量，导出 https_proxy）
  --nat                     G-5：NAT/端口映射模式（必须显式 --public-address；
                            强制 use_external_ip=false 并打印端口映射清单）
  --system-proxy            G-5：把代理注入系统包管理器（apt/dnf，默认关，不改软件源）
  --deploy-dir <path>       部署目录（默认 /opt/ridgericetalk）
  --port-api <port>         API 端口（默认 8080）
  --port-admin <port>       Admin 端口（默认 9090）
  --port-lk-ws <port>       LiveKit WebSocket 端口（默认 7880）
  --port-lk-tcp <port>      LiveKit TCP 端口（默认 7881）
  --port-lk-udp <port>      LiveKit UDP 端口（默认 7882）
  --enable-turn             启用 LiveKit TURN over UDP（默认关，N28；3478/udp 需防火墙放行；
                            仅对首次渲染的 livekit.yaml 生效，已有 livekit.yaml 需先删除）
  --skip-frontend           跳过前端构建
  --skip-build              跳过后端构建（直接复制已有二进制）
  --force                   强制覆盖 .env.production
  --rotate-livekit-keys     轮换 LiveKit API Key/Secret（task-96）：检测到默认值
                            devkey/devsecret 时默认只告警+打印轮换命令；本开关才
                            真正生成新值写回 .env.production（备份 .bak.<UTC>，
                            需重启服务端生效；不改运行中实例）
  --allow-embedded-deps-download
                            离线安装下仍允许服务端启动期联网下载依赖（默认禁止）。
                            仅在确认目标机可出网时使用；否则无网机器会卡在下载。
EOF
}

# ============================================================
# 参数解析
# ============================================================
while [[ $# -gt 0 ]]; do
    case "$1" in
        --help) show_help; exit 0 ;;
        --public-address) PUBLIC_ADDRESS="$2"; PUBLIC_ADDRESS_EXPLICIT=true; shift 2 ;;
        --public-admin-address) PUBLIC_ADMIN_ADDRESS_ARG="$2"; shift 2 ;;
        --public-lk-url) PUBLIC_LK_URL_ARG="$2"; shift 2 ;;
        --mirror-preset) MIRROR_PRESET_ARG="$2"; shift 2 ;;
        --http-proxy) HTTP_PROXY_ARG="$2"; shift 2 ;;
        --https-proxy) HTTPS_PROXY_ARG="$2"; shift 2 ;;
        --nat) NAT_MODE=true; shift ;;
        --system-proxy) SYSTEM_PROXY=true; shift ;;
        --deploy-dir) DEPLOY_DIR="$2"; shift 2 ;;
        --port-api) PORT_API="$2"; shift 2 ;;
        --port-admin) PORT_ADMIN="$2"; shift 2 ;;
        --port-lk-ws) PORT_LK_WS="$2"; shift 2 ;;
        --port-lk-tcp) PORT_LK_TCP="$2"; shift 2 ;;
        --port-lk-udp) PORT_LK_UDP="$2"; shift 2 ;;
        --enable-turn) ENABLE_TURN=true; shift ;;
        --skip-frontend) SKIP_FRONTEND=true; shift ;;
        --skip-build) SKIP_BUILD=true; shift ;;
        --force) FORCE=true; shift ;;
        # task-96：显式轮换 LiveKit API 密钥（默认只告警，不自动改已有部署）。
        --rotate-livekit-keys) ROTATE_LIVEKIT_KEYS=true; shift ;;
        # task-49：离线安装默认关闭运行期依赖下载；显式允许下载需本人签字式开关。
        --allow-embedded-deps-download) ALLOW_EMBEDDED_DEPS_DOWNLOAD=true; shift ;;
        *) fail "未知参数: $1"; show_help; exit 1 ;;
    esac
done

# --deploy-dir 生效修正（实测教训）：SERVER_DIR/STORAGE_DIR/ENV_FILE 原本在常量区
# 按默认值先行赋值，参数解析改了 DEPLOY_DIR 也不会传导 —— 表现为「--deploy-dir 只
# 影响复制目标，构建/启动仍落在默认 /opt/ridgericetalk」，会在现网部署树里编译并
# 试图覆盖运行中的二进制。必须在参数解析后重算全部派生路径。
SERVER_DIR="$DEPLOY_DIR/server"
WEB_DIR="$DEPLOY_DIR/web"
STORAGE_DIR="$SERVER_DIR/storage"
ENV_FILE="$SERVER_DIR/.env.production"

# LiveKit 媒体端口语义澄清：rtc 的 udp/tcp 端口由服务端渲染 livekit.yaml 时固定为
# 7882/7881（internal/config DefaultLiveKitPorts，无环境变量配置面），只有 WS 端口
# （RRT_LIVEKIT_PORT）可错开 —— --port-lk-tcp/udp 仅作为防火墙放行清单参考。
if [[ "$PORT_LK_TCP" != "7881" ]] || [[ "$PORT_LK_UDP" != "7882" ]]; then
    warn "LiveKit 媒体端口由服务端固定为 7881(tcp)/7882(udp)，--port-lk-tcp/--port-lk-udp 仅影响 WS（$PORT_LK_WS 可配）与放行清单提示"
fi

# ============================================================
# task-49（rc3 真机实测缺陷）：RRT_EMBEDDED_DEPS 落盘值解析
# 离线安装（install-offline.sh 导出 RRT_OFFLINE=true）默认 false：离线包自带/
# 系统预置依赖，若仍让服务端在启动期下载 FFmpeg/LiveKit/netease，无外网机器会
# 卡在下载（进程活着但不监听 API）→ 健康检查 60s 超时 → 安装 RC=1。
# 在线安装与开发者路径默认保持 true（行为不变）。
# 优先级：显式 RRT_EMBEDDED_DEPS（true|false）> 离线默认 false > 在线默认 true。
# ============================================================
resolve_embedded_deps_value() {
    local v="${RRT_EMBEDDED_DEPS:-}"
    if [[ -n "$v" ]]; then
        case "$v" in
            true|false) printf '%s' "$v"; return 0 ;;
            *) return 1 ;;
        esac
    fi
    if [[ "${RRT_OFFLINE:-false}" == "true" || "${RRT_OFFLINE:-false}" == "1" ]]; then
        printf 'false'
    else
        printf 'true'
    fi
}

# 离线安装 + 运行期依赖下载 = 矛盾组合：离线包不含 third_party/models/services
# 依赖时，无网机器会静默卡在下载。此组合下 fail-fast 并给出补齐路径。
assert_offline_embedded_deps_safe() {
    local value="$1"
    [[ "$value" == "true" ]] || return 0
    if [[ "${RRT_OFFLINE:-false}" != "true" && "${RRT_OFFLINE:-false}" != "1" ]]; then
        return 0
    fi
    if [[ "$ALLOW_EMBEDDED_DEPS_DOWNLOAD" == "true" ]]; then
        info "离线安装但显式允许运行期依赖下载（--allow-embedded-deps-download）：请确认目标机可出网，否则服务端启动会卡在下载"
        return 0
    fi
    local missing=()
    [[ -d "$SERVER_DIR/third_party" ]] || missing+=("$SERVER_DIR/third_party（FFmpeg 等第三方依赖）")
    [[ -d "$SERVER_DIR/models" ]] || missing+=("$SERVER_DIR/models（TTS 等模型）")
    [[ -d "$DEPLOY_DIR/services/netease-api" ]] || missing+=("$DEPLOY_DIR/services/netease-api（网易云 API 服务）")
    (( ${#missing[@]} == 0 )) && return 0
    fail "离线安装下 RRT_EMBEDDED_DEPS=true 且缺少本地依赖：服务端会尝试联网下载，无网机器将卡死"
    local m
    for m in "${missing[@]}"; do
        fail "  缺失: $m"
    done
    fail "修复（三选一）："
    fail "  ① 推荐：不要显式设置 RRT_EMBEDDED_DEPS，离线安装默认即 false（不下载）"
    fail "  ② 把上述依赖放到对应目录（离线包可用 build-offline-bundle.sh 重新构建携带）"
    fail "  ③ 确认目标机可出网时，加 --allow-embedded-deps-download 重新执行"
    return 1
}

# --public-address 可省略（DES-2026-0912-03 §5.3 短板③）：
# 未显式提供时探测公网/本机 IP 作为默认值，交互终端下询问确认，
# 非交互场景（CI、SSH 管道）直接采用默认值 —— 绝不读 /dev/tty，
# 否则非交互 SSH 下 read /dev/tty 会直接崩脚本（N1 实测教训）。
# A1（FIX-20261003-01）：lib/net.sh 提前无条件加载 —— 私网判定（rrt_is_private_host）
# 在显式地址场景也需要，不能再只在「未显式给地址」分支里加载。
if [[ -f "$SCRIPT_DIR/lib/net.sh" ]]; then
    # shellcheck source=/dev/null
    source "$SCRIPT_DIR/lib/net.sh"
fi
# G-5 网络适配（2026-10-07 阿里云 RHEL 真机实证）：国内 VPS 上 go 默认走
# proxy.golang.org（被墙）会无限期卡死在模块下载。默认补 GOPROXY=goproxy.cn；
# 海外/已有代理环境设 RRT_GOPROXY=off 跳过，或 RRT_GOPROXY=<url> 覆盖。
if [[ -z "${GOPROXY:-}" && "${RRT_GOPROXY:-}" != "off" ]]; then
    export GOPROXY="${RRT_GOPROXY:-https://goproxy.cn,direct}"
fi
# G-5：镜像预设（cn|global|auto）——与 baremetal 同口径，数据表在 lib/mirrors.sh；
# 本脚本的下载点（Go/Node/LiveKit/GitHub/npm）统一读 rrt_mirror_preset。
if [[ -n "${MIRROR_PRESET_ARG}" ]]; then
    if declare -F rrt_mirror_preset_valid &>/dev/null && ! rrt_mirror_preset_valid "$MIRROR_PRESET_ARG"; then
        fail "--mirror-preset 仅支持 cn|global|auto（当前: $MIRROR_PRESET_ARG）"
        exit 2
    fi
    RRT_MIRROR_PRESET="$MIRROR_PRESET_ARG"
    info "镜像预设: $RRT_MIRROR_PRESET"
fi
# G-5：代理参数 → 标准环境变量（与 baremetal 同口径：小写优先，兼顾大写变体）
if [[ -n "${HTTP_PROXY_ARG}" ]]; then
    export http_proxy="$HTTP_PROXY_ARG" HTTP_PROXY="$HTTP_PROXY_ARG"
    info "已设置代理: http_proxy=$HTTP_PROXY_ARG"
fi
if [[ -n "${HTTPS_PROXY_ARG}" ]]; then
    export https_proxy="$HTTPS_PROXY_ARG" HTTPS_PROXY="$HTTPS_PROXY_ARG"
    info "已设置代理: https_proxy=$HTTPS_PROXY_ARG"
fi
# G-5：--system-proxy → 注入 apt/dnf 代理（默认关；失败只告警，不中止部署）
if [[ "$SYSTEM_PROXY" == "true" ]]; then
    if declare -F rrt_apply_system_proxy &>/dev/null; then
        RRT_DRY_RUN="${DRY_RUN:-false}" rrt_apply_system_proxy "${HTTP_PROXY_ARG}" "${HTTPS_PROXY_ARG}" \
            || warn "系统包管理器代理注入失败（已忽略，继续部署；可手动配置 apt/dnf 代理）"
    else
        warn "能力层缺少 rrt_apply_system_proxy，跳过系统代理注入"
    fi
fi
if [[ -z "$PUBLIC_ADDRESS" ]]; then
    DETECTED_IP=""
    if declare -F rrt_public_ip &>/dev/null; then
        # 复用能力层公网 IP 探测（境外服务 → 云元数据兜底），只复用不改
        DETECTED_IP="$(rrt_public_ip 2>/dev/null || true)"
    fi
    if [[ -z "$DETECTED_IP" ]]; then
        # 回退：本机首个 IPv4（无外网/探测服务不可达时仍可局域网使用）
        DETECTED_IP="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
    fi
    if [[ -z "$DETECTED_IP" ]]; then
        fail "--public-address 是必填参数（本次自动探测也未得到可用 IP）"
        info "示例：--public-address http://$PORT_API 或 --public-address http://<服务器IP>:$PORT_API"
        exit 1
    fi

    DEFAULT_PUBLIC="http://$DETECTED_IP:$PORT_API"
    warn "未指定 --public-address，探测到默认地址: $DEFAULT_PUBLIC"
    if [[ -t 0 ]]; then
        # 仅交互终端询问；默认回车 = 确认
        ANSWER=""
        read -r -p "使用该地址？[Y/n] " ANSWER || ANSWER=""
        case "$ANSWER" in
            n*|N*)
                fail "已取消部署。请用 --public-address 指定地址后重试"
                exit 1
                ;;
            *)
                PUBLIC_ADDRESS="$DEFAULT_PUBLIC"
                ;;
        esac
    else
        warn "非交互模式：已采用探测地址，可用 --public-address 覆盖"
        PUBLIC_ADDRESS="$DEFAULT_PUBLIC"
    fi
fi

# task-100：RRT_PUBLIC_ADDRESS 必须带实际对外端口，否则服务端拼出的附件/文件 URL
# 缺端口、外网打不开（t46 实测 502：http://t46.sjcmc.cn/... 应带 :21103）。
# 规则与桌面部署脚本一致：
#   1) 显式写了非默认端口 → 原样保留；
#   2) http 且未写端口 → 补 API 端口（直连/预构建包，对外端口就是 PORT_API）；
#   3) https 且未写端口 → 视为反代（443 对外），不补端口。
if declare -F rrt_port_of_address &>/dev/null; then
    _pub_addr="${PUBLIC_ADDRESS%/}"
    _pub_scheme="${_pub_addr%%://*}"
    _pub_port="$(rrt_port_of_address "$_pub_addr" 2>/dev/null || true)"
    if [[ -z "$_pub_port" && "$_pub_scheme" == "http" ]]; then
        PUBLIC_ADDRESS="${_pub_addr}:${PORT_API}"
        info "对外地址未写端口，已按 API 端口补全：$PUBLIC_ADDRESS"
    fi
fi

# A1（FIX-20261003-01 NJ-11 Critical）：LiveKit rtc.use_external_ip 按对外地址推导。
# 此前服务端默认渲染 use_external_ip: true，LAN/无公网环境 STUN 检测失败会产出
# 不可达公网候选 → ICE 全超时、语音全断（8080 实例手工改 false 后恢复，实证）。
# 规则：
#   显式 --public-address 为私网/本机（RFC1918/localhost）→ false
#   显式 --public-address 为公网/域名                     → true（与旧行为逐字节一致）
#   未显式给地址（探测地址，可能是 NAT 内网 IP）          → 保守 false
# 落点：向 .env.production 写 RRT_LIVEKIT_USE_EXTERNAL_IP（服务端渲染 livekit.yaml
# 时读取该变量）；为 true 时不写任何行 —— 服务端默认即 true，生成物不变。
LK_USE_EXTERNAL_IP=""
if declare -F rrt_derive_use_external_ip &>/dev/null; then
    LK_USE_EXTERNAL_IP="$(rrt_derive_use_external_ip "$PUBLIC_ADDRESS")"
    if [[ "$LK_USE_EXTERNAL_IP" == "false" ]]; then
        info "对外地址 $(rrt_host_of_address "$PUBLIC_ADDRESS") 为私网/本机 → use_external_ip=false（避免 STUN 产出不可达候选，NJ-11）"
    fi
fi
if [[ "$PUBLIC_ADDRESS_EXPLICIT" != "true" ]]; then
    # 未显式指定：探测到的「IP」可能是内网网卡地址（公网探测失败时回退 hostname -I），
    # 无法确认真有公网出口 → 保守关闭
    LK_USE_EXTERNAL_IP="false"
fi

# G-5：--nat 模式收口。NAT 后本机网卡上没有公网地址，自动探测不可靠 →
# 必须显式 --public-address；use_external_ip 一律 false；并打印端口映射清单。
if [[ -n "$PUBLIC_LK_URL_ARG" && "$PUBLIC_LK_URL_ARG" != ws://* && "$PUBLIC_LK_URL_ARG" != wss://* ]]; then
    fail "--public-lk-url 必须以 ws:// 或 wss:// 开头：$PUBLIC_LK_URL_ARG"
    exit 2
fi
if [[ "$NAT_MODE" == "true" ]]; then
    if [[ "$PUBLIC_ADDRESS_EXPLICIT" != "true" ]]; then
        fail "--nat 需要显式 --public-address <对外URL>（NAT 下自动探测不可靠）"
        exit 2
    fi
    LK_USE_EXTERNAL_IP="false"
    info "--nat 模式：use_external_ip=false。需在路由器/云安全组逐条映射并放通："
    printf '    API         %s/tcp\n' "$PORT_API"
    printf '    Admin       %s/tcp\n' "$PORT_ADMIN"
    printf '    LiveKit WS  %s/tcp\n' "$PORT_LK_WS"
    printf '    LiveKit TCP %s/tcp（服务端固定，必须 1:1）\n' "$PORT_LK_TCP"
    printf '    LiveKit UDP %s/udp（服务端固定，必须 1:1）\n' "$PORT_LK_UDP"
fi

TOTAL_STAGES=7
[[ "$SKIP_FRONTEND" == "true" ]] && TOTAL_STAGES=$((TOTAL_STAGES - 1))
[[ "$SKIP_BUILD" == "true" ]] && TOTAL_STAGES=$((TOTAL_STAGES - 1))

# ============================================================
# Stage 1/7: 检查依赖与端口
# ============================================================
stage 1 "$TOTAL_STAGES" "Checking dependencies and ports"

HAS_FATAL=false

# A2（FIX-20261003-01 NJS-1）：先尝试并入标准位置的 Go 再判定，杜绝
# 「Go 已装但 sudo PATH 无」被误判为未安装而中止（报错文案在无外网 VM 是死路）
if rrt_ensure_go_on_path; then
    GV=$(go version)
    ok "Go runtime: $GV"
else
    # N30：裸机自动安装 Go（国内优先 golang.google.cn 官方镜像，失败回退 go.dev）。
    # root 直接装；非 root 时借助 sudo（交互终端会提示密码，无人值守可用 sudo -n 预配置）。
    _GO_VER=1.25.10  # 对齐 go.mod toolchain，避免构建期再拉工具链
    case "$(uname -m)" in aarch64|arm64) _go_arch=arm64 ;; *) _go_arch=amd64 ;; esac
    _tar_as_root() {
        if [[ "$(id -u)" == "0" ]]; then
            rm -rf /usr/local/go && tar -C /usr/local -xzf "$1"
        elif sudo -n true 2>/dev/null; then
            sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf "$1"
        else
            sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf "$1"
        fi
    }
    _go_ok=false
    for _base in "https://golang.google.cn/dl" "https://go.dev/dl"; do
        info "Go 未安装，尝试自动安装 $_GO_VER（$_base → /usr/local/go）…"
        if curl -fsSL --connect-timeout 10 -o /tmp/rrt-go.tar.gz "$_base/go$_GO_VER.linux-$_go_arch.tar.gz" \
            && _tar_as_root /tmp/rrt-go.tar.gz \
            && /usr/local/go/bin/go version >/dev/null 2>&1; then
            export PATH="/usr/local/go/bin:$PATH"
            ok "Go runtime（自动安装）: $(go version)"
            _go_ok=true
            break
        fi
    done
    rm -f /tmp/rrt-go.tar.gz
    if [[ "$_go_ok" != "true" ]]; then
        [[ -x /usr/local/go/bin/go ]] && export PATH="/usr/local/go/bin:$PATH" \
            && ok "Go runtime（已装于 /usr/local/go）: $(go version)" && _go_ok=true
    fi
    if [[ "$_go_ok" != "true" ]]; then
        fail "Go runtime not found. Please install Go 1.25+ from https://go.dev/dl/"
        info "若已安装：标准位置（/usr/local/go/bin、/usr/lib/go*/bin、~/go/bin）均未找到，请确认安装路径或 export PATH=<go/bin>:\$PATH"
        info "自动安装失败原因通常是外网不可达：可手动下载 go$_GO_VER.linux-$_go_arch.tar.gz 后解压到 /usr/local"
        HAS_FATAL=true
    fi
fi

if command -v node &>/dev/null; then
    NV=$(node -v)
    ok "Node.js: $NV"
else
    warn "Node.js not found. Netease API and frontend build will be unavailable."
    info "Install: https://nodejs.org/ or 'sudo apt install nodejs'"
fi

if command -v npm &>/dev/null; then
    NV=$(npm -v)
    ok "npm: $NV"
else
    warn "npm not found. Netease API npm install will fail."
fi

if command -v git &>/dev/null; then
    ok "git: $(git --version)"
else
    warn "git not found. Cannot verify repository state."
fi

check_port() {
    local port="$1"
    local name="$2"
    if command -v ss &>/dev/null; then
        if ss -tln 2>/dev/null | awk '{print $4}' | grep -qE ":${port}$"; then
            fail "Port $port ($name) is already in use"
            HAS_FATAL=true
            return
        fi
    elif command -v netstat &>/dev/null; then
        if netstat -tln 2>/dev/null | awk '{print $4}' | grep -qE ":${port}$"; then
            fail "Port $port ($name) is already in use"
            HAS_FATAL=true
            return
        fi
    fi
    ok "Port $port ($name) is available"
}

# A5（FIX-20261003-01 NJS-2/N35）：UDP 端口占用检查 —— ss -tln / netstat -tln
# 只列 TCP 监听，此前 7882/udp 完全不在检查范围内（多实例共存时必然漏报）
check_port_udp() {
    local port="$1"
    local name="$2"
    if command -v ss &>/dev/null; then
        if ss -uln 2>/dev/null | awk '{print $4}' | grep -qE ":${port}$"; then
            fail "Port $port/udp ($name) is already in use"
            HAS_FATAL=true
            return
        fi
    elif command -v netstat &>/dev/null; then
        if netstat -uln 2>/dev/null | awk '{print $4}' | grep -qE ":${port}$"; then
            fail "Port $port/udp ($name) is already in use"
            HAS_FATAL=true
            return
        fi
    fi
    ok "Port $port/udp ($name) is available"
}

check_port "$PORT_API" "API"
check_port "$PORT_ADMIN" "Admin"
check_port "$PORT_LK_WS" "LiveKit WebSocket"
check_port "$PORT_LK_TCP" "LiveKit TCP"
check_port_udp "$PORT_LK_UDP" "LiveKit UDP"

# A5（FIX-20261003-01 NJS-2/N35）：多实例共存预警 —— LiveKit 媒体端口（7881/tcp、
# 7882/udp）由服务端固定渲染、无环境变量配置面，两套实例无法同时提供语音；
# 检测到既有 ridgericetalk 实例（systemd 活动单元或默认部署目录）时提前明示，
# 避免新手部署到一半才发现要与现网实例打架（实测 N35 旅程被迫 systemctl stop 现网）。
_COEXIST_REASON=""
if command -v systemctl &>/dev/null && systemctl is-active ridgericetalk &>/dev/null; then
    _COEXIST_REASON="systemd 服务 ridgericetalk 正在运行（既有部署）"
elif [[ -d "/opt/ridgericetalk" && "$DEPLOY_DIR" != "/opt/ridgericetalk" ]]; then
    _COEXIST_REASON="默认部署目录 /opt/ridgericetalk 已存在（疑似另一套部署）"
fi
if [[ -n "$_COEXIST_REASON" ]]; then
    warn "多实例共存警告：$_COEXIST_REASON"
    warn "  LiveKit 媒体端口固定为 7881(tcp)/7882(udp)（服务端写死，--port-lk-tcp/udp 改不了），两套实例不能同时跑语音 —— 后启动者 LiveKit 起不来或媒体中断"
    info "  处置：先停既有实例再继续（systemctl stop ridgericetalk；或 kill \$(cat /opt/ridgericetalk/server/storage/server.pid)），并确认本次部署的端口规划与其余端口（API/Admin/LK-WS）不冲突"
fi

# 幂等重跑（与 Stage 7 的「已在运行跳过启动」配套）：pid 文件里的进程是
# 本部署自己的服务，它占用自己的端口是预期而非冲突，不判 fatal —— 否则
# 服务一旦跑起来，重跑脚本必然死在 Stage 1，永远走不到 Stage 7 的幂等路径。
ALREADY_RUNNING=false
if [[ -f "$STORAGE_DIR/server.pid" ]]; then
    _running_pid="$(cat "$STORAGE_DIR/server.pid" 2>/dev/null || true)"
    if [[ -n "$_running_pid" ]] && kill -0 "$_running_pid" 2>/dev/null; then
        ALREADY_RUNNING=true
        info "检测到本部署已在运行 (pid $_running_pid)"
    fi
fi

if [[ "$HAS_FATAL" == "true" ]] && [[ "$ALREADY_RUNNING" != "true" ]]; then
    echo -e "\n${C_RED}[ABORTED]${C_RESET} Please install missing dependencies or free occupied ports and rerun."
    exit 1
fi
if [[ "$HAS_FATAL" == "true" ]] && [[ "$ALREADY_RUNNING" == "true" ]]; then
    warn "端口被占用，但本部署服务已在运行 —— 按幂等重跑继续（如为其他程序占用请先排查）"
fi

# ============================================================
# Stage 2/7: 准备部署目录
# ============================================================
stage 2 "$TOTAL_STAGES" "Preparing deployment directory"

# Safety: refuse to delete well-known system paths or paths outside /opt
if [[ -z "$DEPLOY_DIR" ]] || [[ "$DEPLOY_DIR" == "/" ]] || [[ "$DEPLOY_DIR" == "/root" ]] || [[ "$DEPLOY_DIR" == "/home" ]]; then
    fail "Refusing to deploy to unsafe directory: $DEPLOY_DIR"
    exit 1
fi

if [[ "$DEPLOY_DIR" != "$PROJECT_DIR" ]]; then
    info "Copying project to $DEPLOY_DIR ..."
    mkdir -p "$DEPLOY_DIR"
    # 排除项必须锚定 /server/ 前缀：这些是**运行时**目录（third_party/models/
    # storage/logs 都在 server/ 下），只该在 server/ 顶层匹配 —— 曾经用裸名字
    # exclude，把源码包 server/internal/storage 一并排除，导致部署树缺包、
    # go build 报「is not in std」的诡异失败（实测教训）。.git/node_modules/
    # .env* 任意层都该排，保留裸名。
    if command -v rsync &>/dev/null; then
        rsync -a --exclude='.git' --exclude='node_modules' --exclude='/server/third_party' \
            --exclude='/server/models' --exclude='/server/storage' --exclude='/server/logs' \
            --exclude='.env*' \
            "$PROJECT_DIR/" "$DEPLOY_DIR/"
    else
        # cp -r would copy .git etc, so use tar for a clean copy
        mkdir -p "$DEPLOY_DIR"
        (cd "$PROJECT_DIR" && tar -cf - \
            --exclude='./.git' --exclude='node_modules' --exclude='./server/third_party' \
            --exclude='./server/models' --exclude='./server/storage' --exclude='./server/logs' \
            --exclude='.env*' \
            . | tar -xf - -C "$DEPLOY_DIR")
    fi
    ok "Project copied to $DEPLOY_DIR"
else
    info "Deploy directory is the project directory; skipping copy"
fi

mkdir -p "$STORAGE_DIR" "$SERVER_DIR/logs" "$SERVER_DIR/webhost/dist"
ok "Directories ready"

# ============================================================
# Stage 3/7: 构建前端
# ============================================================
if [[ "$SKIP_FRONTEND" != "true" ]]; then
    stage 3 "$TOTAL_STAGES" "Building frontend"

    if ! command -v npm &>/dev/null; then
        warn "npm not found, skipping frontend build"
    else
        build_frontend() {
            local name="$1"
            local src="$WEB_DIR/$name"
            local dst="$SERVER_DIR/webhost/dist/$name"

            if [[ ! -d "$src" ]]; then
                warn "$name frontend source not found at $src"
                return
            fi

            info "Building $name frontend..."
            if ! (cd "$src" && npm ci); then
                fail "npm ci failed for $name"
                info "Common causes: package-lock.json out of sync, network issues, or missing native build tools"
                exit 1
            fi
            if ! (cd "$src" && npm run build); then
                fail "npm run build failed for $name"
                exit 1
            fi

            mkdir -p "$dst"
            cp -r "$src/dist/"* "$dst/"
            ok "$name frontend built and copied"
        }

        build_frontend "voice"
        build_frontend "admin"
    fi
else
    info "Skipping frontend build as requested"
    if [[ -d "$PROJECT_DIR/web/voice/dist" ]]; then
        cp -r "$PROJECT_DIR/web/voice/dist/"* "$SERVER_DIR/webhost/dist/voice/"
        ok "Voice frontend dist copied"
    fi
    if [[ -d "$PROJECT_DIR/web/admin/dist" ]]; then
        cp -r "$PROJECT_DIR/web/admin/dist/"* "$SERVER_DIR/webhost/dist/admin/"
        ok "Admin frontend dist copied"
    fi
fi

# ============================================================
# Stage 4/7: 构建后端
# ============================================================
BUILD_STAGE=4
[[ "$SKIP_FRONTEND" == "true" ]] && BUILD_STAGE=3

if [[ "$SKIP_BUILD" != "true" ]]; then
    stage "$BUILD_STAGE" "$TOTAL_STAGES" "Building backend"
    cd "$SERVER_DIR"
    # -buildvcs=false：tar 分发/非 git 目录部署时 go build 默认的 VCS stamping 会
    # 因读不到 .git 直接失败（实测 exit status 128）；版本信息改由下方 ldflags 注入
    # （源目录是 git 仓库时），否则保持 version.go 默认值 —— 与 M7 已知的
    # 「embedded 未注入」口径一致，但 git 分发场景现在也能注入了。
    BUILD_LDFLAGS=""
    if command -v git &>/dev/null && [[ -d "$PROJECT_DIR/.git" ]]; then
        _build_commit="$(git -C "$PROJECT_DIR" rev-parse --short HEAD 2>/dev/null || true)"
        if [[ -n "$_build_commit" ]]; then
            BUILD_LDFLAGS="-X ridgericetalk/core/version.Commit=$_build_commit -X ridgericetalk/core/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
            ok "版本注入: commit=$_build_commit"
        fi
    fi
    # 音乐机器人的 opus 编码硬依赖 CGO：交叉编译（如在 Windows/macOS 上 GOOS=linux）
    # 时 go 会自动 CGO_ENABLED=0，产物能启动但播放必挂（实测 2026-10-04 新手实例
    # nocgo 二进制，playnow 报 "music bot requires CGO"）。显式设闸防静默回归。
    if [[ "${CGO_ENABLED:-}" == "0" ]]; then
        fail "检测到 CGO_ENABLED=0：主程序音乐机器人需要 CGO(opus)，请原生构建或配置 C 交叉工具链"
        exit 1
    fi
    if ! command -v gcc >/dev/null 2>&1 && ! command -v cc >/dev/null 2>&1; then
        fail "未找到 C 编译器(gcc/cc)：音乐机器人 opus 编码硬依赖 CGO，请先安装 build-essential"
        exit 1
    fi
    # RHEL 真机缺口（2026-10-07 阿里云实证）：embedded 此前只检不装 opus 开发库，
    # 全新 RHEL 机器上 CGO 编译必挂「Package 'opus', required by 'virtual:world',
    # not found」。检测缺失时经 lib/pkg.sh 映射自动安装（libopus-dev→opus-devel 等）。
    if ! pkg-config --exists opus 2>/dev/null || ! pkg-config --exists opusfile 2>/dev/null; then
        info "opus/opusfile 开发库缺失（音乐机器人 CGO 依赖），尝试自动安装…"
        if [[ -f "$SCRIPT_DIR/lib/pkg.sh" ]]; then
            # shellcheck source=/dev/null
            source "$SCRIPT_DIR/lib/pkg.sh"
            rrt_pkg_install libopus-dev libopusfile-dev || \
                warn "opus 开发库自动安装失败——请手动安装（Debian: libopus-dev libopusfile-dev；RHEL: opus-devel opusfile-devel）"
        else
            warn "lib/pkg.sh 不可用——请手动安装 opus/opusfile 开发库后再试"
        fi
    fi
    # BUILD_LDFLAGS 含空格，必须整体作为 -ldflags 的单个参数传递
    # （教训：unquoted 展开会让 -X 直达 go 顶层参数，报 "flag provided but not defined: -X"）
    #
    # N2（ETXTBSY）：已部署机器服务在跑时，go build/cp 直接写运行中的二进制
    # 必报 "text file busy" 而中断——先构建到 .new 临时名再 mv 原子换装
    # （mv 只换目录项，运行中进程继续持有旧 inode），重跑不再依赖先停服务。
    if [[ -n "$BUILD_LDFLAGS" ]]; then
        go build -ldflags "$BUILD_LDFLAGS" -buildvcs=false -o ridgericetalk.new ./cmd/server
    else
        go build -buildvcs=false -o ridgericetalk.new ./cmd/server
    fi
    mv -f ridgericetalk.new ridgericetalk
    SERVER_BIN="$SERVER_DIR/ridgericetalk"
    ok "Backend built: $SERVER_BIN（已原子换装；重启服务后生效）"

    # 迁移工具随主二进制一起构建（对齐 deploy-baremetal.sh 编译先例）；
    # 刚编译的产物必为本机格式，无需 baremetal 的 ELF magic 校验
    info "Building migration tool: go build -o ridgericetalk-migrate ./cmd/migrate/"
    if [[ -n "$BUILD_LDFLAGS" ]]; then
        go build -ldflags "$BUILD_LDFLAGS" -buildvcs=false -o ridgericetalk-migrate.new ./cmd/migrate/
    else
        go build -buildvcs=false -o ridgericetalk-migrate.new ./cmd/migrate/
    fi
    mv -f ridgericetalk-migrate.new ridgericetalk-migrate
    ok "Migration tool built: $SERVER_DIR/ridgericetalk-migrate"
else
    stage "$BUILD_STAGE" "$TOTAL_STAGES" "Skipping backend build"
    # H-1：离线包内是 ridgericetalk-server，在线产物是 ridgericetalk —— 依次探测。
    for _cand in "$SERVER_DIR/ridgericetalk" "$SERVER_DIR/ridgericetalk-server"; do
        if [[ -f "$_cand" ]]; then
            SERVER_BIN="$_cand"
            break
        fi
    done
    # 在 Windows 上打包（git-bash/NTFS）时 chmod 不落盘，tar 内可能记录 0644 →
    # Linux 解包后不可执行。这里补一次 +x，避免「文件在但起不来」的隐蔽失败。
    if [[ -n "$SERVER_BIN" && ! -x "$SERVER_BIN" ]]; then
        chmod +x "$SERVER_BIN" 2>/dev/null || true
        # P1-2（reviewer 20261029）：chmod 后必须校验 -x，不能只靠 `|| true` 就当成功
        # （NTFS 实测：chmod 返回 0 但 mode 仍 0644，随后启动静默失败）。
        if [[ ! -x "$SERVER_BIN" ]]; then
            fail "无法为 $SERVER_BIN 设置可执行位（目标文件系统可能 noexec 挂载或权限不足）"
        fi
    fi
    if [[ -z "$SERVER_BIN" ]]; then
        fail "No existing binary at $SERVER_DIR/ridgericetalk or $SERVER_DIR/ridgericetalk-server and --skip-build requested"
        exit 1
    fi
    ok "Using existing binary: $SERVER_BIN"
fi

# ============================================================
# Stage 5/7: 生成环境配置
# ============================================================
ENV_STAGE=5
[[ "$SKIP_FRONTEND" == "true" ]] && ENV_STAGE=$((ENV_STAGE - 1))
[[ "$SKIP_BUILD" == "true" ]] && ENV_STAGE=$((ENV_STAGE - 1))

stage "$ENV_STAGE" "$TOTAL_STAGES" "Generating environment configuration"

if [[ -f "$ENV_FILE" ]] && [[ "$FORCE" != "true" ]]; then
    warn "$ENV_FILE already exists. Use --force to overwrite."
else
    # Generate a random JWT secret
    # 回退分支禁止「cat | tr | head」管道形态：set -o pipefail 下 head 提前关闭
    # 管道会让命令替换整体返回 141（SIGPIPE），set -e 直接杀死脚本——精简系统
    # （无 openssl 的容器/最小化镜像）必现，实测于 Debian 12 容器矩阵。
    # 改用重定向读 urandom + 整体 || true 兜底。
    JWT_SECRET="$(openssl rand -hex 32 2>/dev/null || tr -dc 'a-z0-9' < /dev/urandom 2>/dev/null | head -c 64 || true)"
    CSRF_SECRET="$(openssl rand -hex 32 2>/dev/null || tr -dc 'a-z0-9' < /dev/urandom 2>/dev/null | head -c 64 || true)" 
    # LiveKit API 密钥对：服务端渲染 livekit.yaml 与签发语音 token 共用（D18 一致性）。
    # 不生成的话回落 devkey + 默认短 secret，LiveKit 启动自校验直接退出
    # （"secret is too short, should be at least 32 characters" 实测）。
    LK_API_KEY="rrt$(openssl rand -hex 6 2>/dev/null || tr -dc 'a-z0-9' < /dev/urandom 2>/dev/null | head -c 12 || true)"
    LK_API_SECRET="$(openssl rand -hex 24 2>/dev/null || tr -dc 'a-z0-9' < /dev/urandom 2>/dev/null | head -c 48 || true)"

    # N27：CORS 白名单协议由实际对外地址推导（useHttps + 实际端口），禁止硬编码
    # 协议。PUBLIC_ADDRESS 默认 http://... → 推导结果与旧硬编码逐字节一致。
    CORS_LOCAL_SCHEME="http"
    case "$PUBLIC_ADDRESS" in
        https://*|wss://*) CORS_LOCAL_SCHEME="https" ;;
    esac
    # G-5：CORS 白名单。默认与改动前逐字节一致（PUBLIC_ADDRESS + localhost 双端口）；
    # 仅显式 --public-admin-address 时追加 Admin 对外地址（NAT 外部端口不同场景）。
    if [[ -n "${PUBLIC_ADMIN_ADDRESS_ARG}" ]]; then
        CORS_PUBLIC_VALUE="$PUBLIC_ADDRESS,$PUBLIC_ADMIN_ADDRESS_ARG,${CORS_LOCAL_SCHEME}://localhost:$PORT_API,${CORS_LOCAL_SCHEME}://localhost:$PORT_ADMIN"
    else
        CORS_PUBLIC_VALUE="$PUBLIC_ADDRESS,${CORS_LOCAL_SCHEME}://localhost:$PORT_API,${CORS_LOCAL_SCHEME}://localhost:$PORT_ADMIN"
    fi
    # G-5：LiveKit 对外 WS URL。默认保持 ws://127.0.0.1:<ws>（嵌入式单机口径：
    # 桌面端会把 localhost/127.0.0.1 重写为 API 主机，见 ridgericetalk-win
    # voiceStore/rewriteLiveKitUrl + remoteAssistViewer.rewriteAssistLiveKitUrl），
    # 仅 --public-lk-url 显式给出才覆盖 → 老部署生成物逐字节不变。
    if [[ -n "${PUBLIC_LK_URL_ARG}" ]]; then
        LK_PUBLIC_URL_VALUE="$PUBLIC_LK_URL_ARG"
    else
        LK_PUBLIC_URL_VALUE="ws://127.0.0.1:$PORT_LK_WS"
    fi

    # task-49：离线安装默认 false（不联网下载依赖），在线/开发者默认 true。
    EMBEDDED_DEPS_VALUE="$(resolve_embedded_deps_value)" || {
        fail "RRT_EMBEDDED_DEPS 取值非法（只接受 true/false）：${RRT_EMBEDDED_DEPS:-}"
        exit 1
    }
    assert_offline_embedded_deps_safe "$EMBEDDED_DEPS_VALUE" || exit 1

    cat > "$ENV_FILE" <<EOF
# RidgeRiceTalk 生产环境配置（由 deploy-embedded.sh 自动生成）
# 生成时间: $(date -u +"%Y-%m-%dT%H:%M:%SZ")

RRT_ENV=production
RRT_LOG_LEVEL=info
RRT_DEPLOY_MODE=embedded
RRT_PUBLIC_ADDRESS=$PUBLIC_ADDRESS

RRT_PORT=$PORT_API
RRT_ADMIN_PORT=$PORT_ADMIN

RRT_CORS_ORIGINS=$CORS_PUBLIC_VALUE

RRT_DATABASE_URL=$STORAGE_DIR/ridgericetalk.db
RRT_DB_DRIVER=sqlite

RRT_JWT_SECRET=$JWT_SECRET
RRT_CSRF_TOKEN_SECRET=$CSRF_SECRET
RRT_ENCRYPTION_KEY=

# task-49：离线安装默认关闭服务端启动期依赖下载（离线包/系统已预置 deps）；
# 在线安装默认 true。无外网机器请勿改回 true —— 服务端会卡在下载且不监听 API。
RRT_EMBEDDED_DEPS=$EMBEDDED_DEPS_VALUE
RRT_THIRD_PARTY_DIR=$SERVER_DIR/third_party
RRT_MODELS_DIR=$SERVER_DIR/models

RRT_LIVEKIT_AUTOSTART=true
RRT_LIVEKIT_PORT=$PORT_LK_WS
RRT_LIVEKIT_URL=ws://127.0.0.1:$PORT_LK_WS
RRT_LIVEKIT_PUBLIC_URL=$LK_PUBLIC_URL_VALUE
RRT_LIVEKIT_API_KEY=$LK_API_KEY
RRT_LIVEKIT_API_SECRET=$LK_API_SECRET
RRT_NETEASE_API_ENDPOINT=http://127.0.0.1:3300
# 网易云登录 cookie（可选，默认留空）：匿名搜索可能触发上游风控（-462 人机验证），
# 配置有效登录 cookie 后即消除。取值为 MUSIC_U=xxx 或完整 cookie 串。
# 获取方式：浏览器登录 music.163.com → 开发者工具 → 应用/存储 → Cookie → 复制 MUSIC_U 的值。
# 与客户端内「扫码登录」共用同一效果，二者配其一即可（建议只留一处）。
#RRT_NETEASE_COOKIE=

RRT_STORAGE_TYPE=local
RRT_LOCAL_DATA_PATH=$STORAGE_DIR

RRT_ALLOW_REGISTER=true
RRT_MAX_USERS=1000
EOF
    ok "Created $ENV_FILE"
    info "Remember to back up JWT/CSRF secrets from $ENV_FILE"
    # N28：--enable-turn 时追加 TURN 端口放行说明（默认关 → 不追加，.env 与
    # 改动前逐字节一致）。
    if [[ "$ENABLE_TURN" == "true" ]]; then
        cat >> "$ENV_FILE" <<EOF

# ============================================================
# TURN 中继（N28：--enable-turn 开启，livekit.yaml 模板已追加 turn 块）
# 需在防火墙/安全组放行：${TURN_UDP_PORT}/udp（TURN over UDP）
# ============================================================
RRT_LIVEKIT_TURN_ENABLED=true
EOF
    fi
fi

# A1（FIX-20261003-01 NJ-11）：按推导结果确保 use_external_ip 覆盖行存在。
# - false → 追加 RRT_LIVEKIT_USE_EXTERNAL_IP=false（服务端渲染 livekit.yaml 时读取；
#   置于生成块之外：已有 .env 未加 --force 的重跑场景也要能补上该行，幂等）。
# - true  → 不写任何行：服务端默认即 true，.env 与渲染产物和改动前逐字节一致（硬验收）。
if [[ "$LK_USE_EXTERNAL_IP" == "false" ]] \
   && ! grep -q '^RRT_LIVEKIT_USE_EXTERNAL_IP=' "$ENV_FILE" 2>/dev/null; then
    cat >> "$ENV_FILE" <<EOF

# FIX-20261003-01 A1（NJ-11）：对外地址 $(rrt_host_of_address "$PUBLIC_ADDRESS") 为私网/本机
# （或未显式指定）。LiveKit use_external_ip 必须关闭 —— STUN 公网检测在无公网
# 环境会产出不可达候选，语音 ICE 全挂。
RRT_LIVEKIT_USE_EXTERNAL_IP=false
EOF
    ok "已写入 RRT_LIVEKIT_USE_EXTERNAL_IP=false（按对外地址推导，NJ-11）"
fi

# A1：已渲染的 livekit.yaml 不会自动重渲染（服务端检测文件存在即跳过）。
# 此前部署生成的 use_external_ip: true 需删除文件后重渲染才会带上 false —— 必须明示。
if [[ "$LK_USE_EXTERNAL_IP" == "false" && -f "$STORAGE_DIR/livekit.yaml" ]] \
   && grep -q 'use_external_ip: *true' "$STORAGE_DIR/livekit.yaml" 2>/dev/null; then
    warn "检测到已渲染的 $STORAGE_DIR/livekit.yaml 仍为 use_external_ip: true（渲染只发生一次，本次写入的覆盖行不会自动生效）"
    info "  如需修正语音：停止服务 → 删除该文件 → 重跑本脚本（或直接重启服务由服务端按新 env 重渲染）"
fi

# ============================================================
# task-96：LiveKit 默认密钥守卫
# 背景：config 有 devkey/devsecret 默认回退（server/internal/config/config.go）。
#  - 新建 .env：上面已生成随机 RRT_LIVEKIT_API_KEY/SECRET，本段只做终检；
#  - 已存在 .env（升级/重跑）：不自动改运行中的实例，只在检测到默认值时醒目
#    WARN 并打印复制即用的轮换命令；显式 --rotate-livekit-keys 才写回新值。
#  - livekit.yaml 由服务端启动时按 env 重写（livekitmgr.generateConfig），
#    因此密钥一致性由「env 单一真源 + 重启」保证；外部 LiveKit 需人工同步 keys。
# ============================================================
if declare -F lk_ensure_env_credentials >/dev/null 2>&1 && [[ -f "$ENV_FILE" ]]; then
    LK_ROTATE_ARG=""
    [[ "${ROTATE_LIVEKIT_KEYS:-false}" == "true" ]] && LK_ROTATE_ARG="--rotate"
    lk_ensure_env_credentials "$ENV_FILE" $LK_ROTATE_ARG "$PORT_API" || LK_GUARD_RC=$?
    if [[ "${LK_GUARD_RC:-0}" == "2" ]]; then
        warn "部署可以继续，但生产环境请尽快按上面的命令轮换（本次未改动运行中实例）"
    elif [[ "${LK_GUARD_RC:-0}" == "1" ]]; then
        warn "LiveKit 密钥守卫未能完成检查（可能缺少 openssl//dev/urandom 或 .env 不可写），请人工确认"
    fi
    if [[ -f "$STORAGE_DIR/livekit.yaml" ]] && declare -F lk_assert_no_default_in_yaml >/dev/null 2>&1; then
        if ! lk_assert_no_default_in_yaml "$STORAGE_DIR/livekit.yaml"; then
            warn "已渲染的 $STORAGE_DIR/livekit.yaml 仍含默认密钥；重启服务端后会按 .env 中的新密钥重写（外部 LiveKit 部署需手工同步 keys 段）"
        fi
    fi
fi

# ============================================================
# Stage 6/7: 初始化数据目录
# ============================================================
INIT_STAGE=6
[[ "$SKIP_FRONTEND" == "true" ]] && INIT_STAGE=$((INIT_STAGE - 1))
[[ "$SKIP_BUILD" == "true" ]] && INIT_STAGE=$((INIT_STAGE - 1))

stage "$INIT_STAGE" "$TOTAL_STAGES" "Initializing storage"

mkdir -p "$STORAGE_DIR" "$SERVER_DIR/third_party" "$SERVER_DIR/models"
touch "$STORAGE_DIR/.write_test" && rm "$STORAGE_DIR/.write_test"
ok "Storage directory writable: $STORAGE_DIR"

# ============================================================
# NJ-33：EasyTier 虚拟局域网支持（embedded）。baremetal 由 Stage 7.7 处理
# （密钥生成/复用 + systemd 节点），embedded 历史上完全缺失 → 虚拟网模块
# 恒为禁用（/api/virtualnet/status 报 enabled:false / hasSecret:false，
# 实测 2026-10-04 新手实例）。紧凑对齐：①生成/复用密钥并写入 .env.production；
# ②二进制在则拉起服务端 hub 节点（udp/5007），缺失则给出修复指引，不阻断部署。
# ============================================================
if ! grep -q '^RRT_EASYTIER_SECRET=' "$ENV_FILE" 2>/dev/null; then
    ET_SECRET_FILE="$STORAGE_DIR/easytier/secret"
    mkdir -p "$(dirname "$ET_SECRET_FILE")"
    if [[ -f "$ET_SECRET_FILE" ]]; then
        ET_SECRET=$(tr -d '[:space:]' < "$ET_SECRET_FILE")
        ok "复用已存在的 EasyTier 网络密钥: $ET_SECRET_FILE"
    else
        ET_SECRET=$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
        printf '%s' "$ET_SECRET" > "$ET_SECRET_FILE"
        chmod 600 "$ET_SECRET_FILE"
        ok "已生成 EasyTier 网络密钥: $ET_SECRET_FILE"
    fi
    {
        echo ""
        echo "# NJ-33：EasyTier 虚拟局域网（网络密钥，客户端经 /api/virtualnet/connect 获取）"
        echo "RRT_EASYTIER_SECRET=$ET_SECRET"
    } >> "$ENV_FILE"
    ok "已写入 RRT_EASYTIER_SECRET"
fi
_ET_BIN="$(command -v easytier-core || true)"
[[ -z "$_ET_BIN" && -x /usr/local/bin/easytier-core ]] && _ET_BIN=/usr/local/bin/easytier-core
if [[ -n "$_ET_BIN" ]]; then
    if ! pgrep -x easytier-core >/dev/null 2>&1; then
        _ET_SECRET_LINE="$(grep '^RRT_EASYTIER_SECRET=' "$ENV_FILE" | head -1 | cut -d= -f2)"
        mkdir -p "$SERVER_DIR/logs"
        nohup "$_ET_BIN" --network-name ridgericetalk --network-secret "$_ET_SECRET_LINE" \
            --hostname rrt-server --mtu 1380 -l "udp://0.0.0.0:5007" --rpc-portal 127.0.0.1:11210 \
            >> "$SERVER_DIR/logs/easytier.log" 2>&1 &
        ok "已拉起 easytier-core hub 节点（udp/5007，网络 ridgericetalk）"
    else
        ok "easytier-core 已在运行，跳过拉起"
    fi
else
    warn "未找到 easytier-core：虚拟局域网客户端将无可加入的 hub。修复：安装 EasyTier 后手动执行"
    info "  easytier-core --network-name ridgericetalk --network-secret <密钥> --hostname rrt-server -l udp://0.0.0.0:5007 --rpc-portal 127.0.0.1:11210"
fi

# 仓库内置 LiveKit 离线物归位（DES §2.1 离线物清单：livekit/bin/linux-<arch>）。
# 服务端 EnsureLiveKit 只认 <RRT_THIRD_PARTY_DIR>/livekit/livekit-server，找不到就会
# 转向 GitHub 下载（国内网络常不可达）。仓库里有就离线归位，没有再由服务端下载。
case "$(uname -m)" in
    aarch64|arm64) _lk_arch="arm64" ;;
    *)             _lk_arch="amd64" ;;
esac
_BUILTIN_LK="$PROJECT_DIR/livekit/bin/linux-$_lk_arch/livekit-server"
_TK_LK_DIR="$SERVER_DIR/third_party/livekit"
if [[ -f "$_BUILTIN_LK" ]] && [[ ! -f "$_TK_LK_DIR/livekit-server" ]]; then
    mkdir -p "$_TK_LK_DIR"
    cp "$_BUILTIN_LK" "$_TK_LK_DIR/livekit-server"
    chmod +x "$_TK_LK_DIR/livekit-server"
    ok "LiveKit 离线物已归位: $_TK_LK_DIR/livekit-server ($_lk_arch)"
elif [[ -f "$_TK_LK_DIR/livekit-server" ]]; then
    ok "LiveKit 已就位: $_TK_LK_DIR/livekit-server"
else
    # 离线安装（install-offline.sh 会导出 RRT_OFFLINE=true）没有外网可下载，
    # 缺内置 LiveKit 必须直接失败，而不是「先装好再等运行时静默降级」（M-1）。
    if [[ "${RRT_OFFLINE:-false}" == "true" || "${RRT_OFFLINE:-false}" == "1" ]]; then
        fail "离线模式要求包内自带 LiveKit，但 $PROJECT_DIR/livekit/bin/linux-$_lk_arch/livekit-server 不存在"
        exit 1
    fi
    warn "仓库无内置 LiveKit 离线物（$_BUILTIN_LK 不存在），服务端将尝试联网下载"
fi

# N28：--enable-turn → 在**部署树内**的 livekit.yaml 模板追加 turn 块。
# 服务端首次启动按该模板渲染 livekit.yaml（文件已存在则跳过渲染），追加的
# 字面值会原样带出。默认（不开开关）零动作 —— 模板与改动前逐字节一致。
# 注意：仅对「首次渲染」生效；已有 livekit.yaml 的现场需删除后重跑。
_LK_TEMPLATE="$DEPLOY_DIR/livekit/livekit.yaml.template"
if [[ "$ENABLE_TURN" == "true" ]]; then
    if [[ ! -f "$_LK_TEMPLATE" ]]; then
        warn "未找到 livekit.yaml 模板（$_LK_TEMPLATE），TURN 未启用"
    elif grep -q '^turn:' "$_LK_TEMPLATE"; then
        ok "livekit.yaml 模板已含 turn 块，跳过追加（幂等重跑）"
    else
        cat >> "$_LK_TEMPLATE" <<EOF

turn:
  enabled: true
  udp_port: $TURN_UDP_PORT
EOF
        ok "livekit.yaml 模板已追加 turn 块（udp_port=$TURN_UDP_PORT；首启渲染生效）"
        info "  TURN over UDP 需放行 ${TURN_UDP_PORT}/udp；tls_port（5349）需证书，embedded 轻量档未内置，必要时改用 baremetal 部署 --tls + --enable-turn"
        if [[ -f "$STORAGE_DIR/livekit.yaml" ]]; then
            warn "检测到已渲染的 $STORAGE_DIR/livekit.yaml —— 渲染只发生一次，TURN 不会自动带上"
            info "  如需对既有部署启用 TURN：停止服务 → 删除该文件 → 重跑本脚本 --enable-turn"
        fi
    fi
fi

# 数据库迁移（DES-2026-0912-03 §5.3 短板①）：部署必须落库，不能只复制二进制。
# 迁移幂等（已应用的版本自动跳过，可重复执行）；失败则中止部署。
# SQLite 走 GORM AutoMigrate 兜底（cmd/migrate 仅支持 up，无 down）。
# 连接经环境变量传入（见 cmd/migrate/main.go 头部 Environment 说明）。
MIGRATE_BIN="$SERVER_DIR/ridgericetalk-migrate"
# H-2：服务端按相对路径 ../migrations 找版本化 SQL（migrate_versioned.go MatsDir）。
# 离线包现在自带 migrations/，但老包/手工同步可能缺 —— 缺了必须显式提示，
# 否则只看到一句「迁移失败」无从排查。
if [[ ! -d "$DEPLOY_DIR/migrations" ]]; then
    warn "未找到迁移目录: $DEPLOY_DIR/migrations（离线包应含 migrations/；可用 RRT_MIGRATIONS_PATH 指向包内目录）"
fi
if [[ -f "$MIGRATE_BIN" && ! -x "$MIGRATE_BIN" ]]; then
    chmod +x "$MIGRATE_BIN" 2>/dev/null || true
fi
if [[ ! -x "$MIGRATE_BIN" ]]; then
    # H-4：离线包应自带迁移器；--skip-build（install-offline.sh 恒用）下目标机没有
    # Go 工具链，就地补编译必以「go: command not found」惨败 —— 直接给可读错误。
    if [[ "$SKIP_BUILD" == "true" ]]; then
        fail "迁移器不可用: $MIGRATE_BIN（离线包应包含 ridgericetalk-migrate；请用新版 build-offline-bundle.sh 重新打包）"
        exit 1
    fi
    # 在线模式下就地补编译（baremetal ensure_migrate_binary 的轻量版；刚编译产物
    # 必为本机格式，故不做 ELF magic 校验）
    warn "迁移工具不存在: $MIGRATE_BIN，尝试补编译..."
    (cd "$SERVER_DIR" && go build -buildvcs=false -o ridgericetalk-migrate ./cmd/migrate/) || {
        fail "迁移工具编译失败，无法执行数据库迁移"
        exit 1
    }
fi

info "Applying database migrations (idempotent)..."
if ! (cd "$SERVER_DIR" && \
      RRT_DATABASE_URL="$STORAGE_DIR/ridgericetalk.db" RRT_DB_DRIVER=sqlite \
      ./ridgericetalk-migrate up); then
    fail "数据库迁移失败，部署中止"
    info "排查：确认 $STORAGE_DIR 可写、二进制完整，重跑本脚本即可续迁"
    exit 1
fi

# 迁移完成后读取版本号，输出可辨识的完成信息（失败不阻断——仅影响展示）
MIG_VERSION="$(RRT_DATABASE_URL="$STORAGE_DIR/ridgericetalk.db" RRT_DB_DRIVER=sqlite \
    "$MIGRATE_BIN" version 2>/dev/null | grep -oE 'version: [0-9]+' | awk '{print $2}' || true)"
ok "数据库迁移完成 (v${MIG_VERSION:-0})"

# ============================================================
# Stage 7/7: 完成
# ============================================================
FINAL_STAGE=7
[[ "$SKIP_FRONTEND" == "true" ]] && FINAL_STAGE=$((FINAL_STAGE - 1))
[[ "$SKIP_BUILD" == "true" ]] && FINAL_STAGE=$((FINAL_STAGE - 1))

stage "$FINAL_STAGE" "$TOTAL_STAGES" "Deployment complete"

# ------------------------------------------------------------
# 启动服务并做健康轮询（DES-2026-0912-03 §5.3 短板②）：
# 此前部署完只打印提示就 exit 0，服务并未真正跑起来 —— 一键部署必须
# 「部署完即可访问」。PID 记录在 $STORAGE_DIR/server.pid，供停止/重启使用。
# ------------------------------------------------------------
PID_FILE="$STORAGE_DIR/server.pid"
LOG_FILE="$SERVER_DIR/logs/server.log"
HEALTH_URL="http://127.0.0.1:$PORT_API/api/health"

# 已在运行则不重复启动（幂等重跑）：打印 pid 与停止方法后继续走健康检查
if [[ -f "$PID_FILE" ]]; then
    RUNNING_PID="$(cat "$PID_FILE" 2>/dev/null || true)"
    if [[ -n "$RUNNING_PID" ]] && kill -0 "$RUNNING_PID" 2>/dev/null; then
        info "服务已在运行 (pid $RUNNING_PID)，跳过启动"
        info "停止方法: kill \$(cat $PID_FILE)"
    else
        # 残留 pid（上次异常退出）：清理后正常启动
        rm -f "$PID_FILE"
    fi
fi

if [[ ! -f "$PID_FILE" ]]; then
    if [[ ! -f "$ENV_FILE" ]]; then
        fail "环境配置缺失: $ENV_FILE，无法启动服务"
        exit 1
    fi
    info "Starting RidgeRiceTalk server..."
    # cwd 必须是部署根（对齐 baremetal systemd WorkingDirectory 先例）：
    # 服务端按相对路径读 livekit/livekit.yaml.template 等部署根资产，
    # 以 server/ 为 cwd 会找不到模板（实测 WARN「渲染 livekit.yaml 失败」）
    cd "$DEPLOY_DIR"
    # 以环境变量方式注入配置（.env.production 为 KEY=VALUE 行，set -a 导出后子进程可见）
    set -a
    # shellcheck source=/dev/null
    source "$ENV_FILE"
    set +a
    nohup "$SERVER_BIN" > "$LOG_FILE" 2>&1 &
    SERVER_PID=$!
    echo "$SERVER_PID" > "$PID_FILE"
    ok "Server started (pid $SERVER_PID, pid file: $PID_FILE)"
    # 保留 SERVER_PID 供下方健康轮询做进程存活检查（崩溃时提前失败，不必等满超时）
else
    SERVER_PID="$(cat "$PID_FILE" 2>/dev/null || true)"
fi

# 健康轮询：每 2s 一次，最多 90 次（180s）。
# curl 连接失败返回非 0 是常态（服务未就绪），必须走 if 分支而非裸命令，
# 否则 set -e 会提前中止脚本。
# 60s 窗口实测不够：低配 VM/容器环境主服务就绪需 ~56s（NAS 虚拟机容器矩阵实测
# 15:15:37 启动 → 15:16:34 才监听），贴边超时误报失败 —— 放宽到 180s。
# 精简系统（Alpine 最小镜像等）可能没有 curl：回退 busybox wget。
info "Waiting for health check: $HEALTH_URL"
health_probe() {
    if command -v curl &>/dev/null; then
        curl -sf --max-time 3 "$HEALTH_URL" > /dev/null 2>&1
    else
        wget -q -O /dev/null -T 3 "$HEALTH_URL" 2>/dev/null
    fi
}
READY=false
for ((i = 1; i <= 90; i++)); do
    if health_probe; then
        READY=true
        break
    fi
    # 进程已退出则无需继续等（立即失败并给出日志）
    if [[ -n "${SERVER_PID:-}" ]] && ! kill -0 "$SERVER_PID" 2>/dev/null; then
        break
    fi
    sleep 2
done

if [[ "$READY" != "true" ]]; then
    fail "健康检查未通过（60s 超时或服务进程已退出）: $HEALTH_URL"
    if [[ -f "$LOG_FILE" ]]; then
        info "最近 30 行日志 ($LOG_FILE):"
        tail -30 "$LOG_FILE" || true
    else
        warn "日志文件不存在: $LOG_FILE"
    fi
    exit 1
fi

# 从 /api/health 提取版本信息打印（无 jq 依赖，字段缺失时兜底 unknown）
if command -v curl &>/dev/null; then
    HEALTH_JSON="$(curl -sf --max-time 5 "$HEALTH_URL" 2>/dev/null || true)"
else
    HEALTH_JSON="$(wget -q -O- -T 5 "$HEALTH_URL" 2>/dev/null || true)"
fi
HEALTH_VERSION="$(printf '%s' "$HEALTH_JSON" | grep -o '"version":"[^"]*"' | head -1 | cut -d'"' -f4 || true)"
HEALTH_COMMIT="$(printf '%s' "$HEALTH_JSON" | grep -o '"commit":"[^"]*"' | head -1 | cut -d'"' -f4 || true)"
ok "服务健康检查通过 (version=${HEALTH_VERSION:-unknown}, commit=${HEALTH_COMMIT:-unknown})"

# ============================================================
# 部署收尾自检（N27）：CORS 一致性 + WS 握手探测
# ============================================================
# 以「客户端实际使用的对外 origin」（RRT_PUBLIC_ADDRESS）核对 CORS 白名单并
# 探测 /ws，任一失败 → 判定部署失败（N27 此前会静默带病上线）。
# RRT_WS_PROBE_TOKEN=<accessToken> 时升级为 101 全量自检。
if [[ -f "$SCRIPT_DIR/lib/wscheck.sh" ]]; then
    # shellcheck source=/dev/null
    source "$SCRIPT_DIR/lib/wscheck.sh"
    info "部署收尾自检（CORS 一致性 + WS 握手，N27）..."
    if ! rrt_ws_deploy_selfcheck "$ENV_FILE" "$(printf '%s' "$PUBLIC_ADDRESS" | sed -E 's#^[a-z]+://##; s#:[0-9]+/?$##')" "$PORT_API"; then
        fail "部署自检未通过 —— 本次部署按 FAIL 处理（实时消息链路不可用的故障此前会静默上线）"
        exit 1
    fi
else
    warn "缺少 scripts/lib/wscheck.sh，跳过 WS 部署自检（无法覆盖 N27 类回归）"
fi

banner "RidgeRiceTalk Embedded Deployment Ready"

echo -e "  ${C_WHITE}Deploy directory:${C_RESET}  $DEPLOY_DIR"
echo -e "  ${C_WHITE}Server binary:${C_RESET}     ${SERVER_BIN:-$SERVER_DIR/ridgericetalk}"
echo -e "  ${C_WHITE}Public address:${C_RESET}    $PUBLIC_ADDRESS"
echo -e "  ${C_WHITE}API port:${C_RESET}          $PORT_API"
echo -e "  ${C_WHITE}Admin port:${C_RESET}        $PORT_ADMIN"
echo -e "  ${C_WHITE}Database:${C_RESET}          SQLite ($STORAGE_DIR/ridgericetalk.db)"
echo -e "  ${C_WHITE}Server log:${C_RESET}        $LOG_FILE   (tail -f 查看实时日志)"
echo -e "  ${C_WHITE}PID file:${C_RESET}          $PID_FILE"
echo ""
echo -e "  ${C_BLUE}Service control:${C_RESET}"
echo -e "    停止:   ${C_GREEN}kill \$(cat $PID_FILE)${C_RESET}"
echo -e "    重启:   ${C_GREEN}cd $DEPLOY_DIR && (set -a; source server/.env.production; set +a; nohup server/ridgericetalk > $LOG_FILE 2>&1 & echo \$! > $PID_FILE)${C_RESET}"
echo -e "    或使用助手脚本: ${C_GREEN}$SERVER_DIR/scripts/start-server.sh${C_RESET}"
echo ""

# H-4（P2-3）：管理页在独立 Admin 端口 —— RRT_ENV=production 时 AdminPortShared 被
# 服务端强制忽略，主 API 端口**不服务** /admin 静态页，按 API 端口拼出的
# $PUBLIC_ADDRESS/admin 会 404。故从 PUBLIC_ADDRESS 剥掉端口后拼 Admin 端口。
# PUBLIC_ADDRESS 不带端口（如 https://example.com）时剥不出主机部分（会剥成 "http"），
# 此时回退为打印「IP:Admin端口」形态的占位说明。
ADMIN_BASE="${PUBLIC_ADDRESS%:*}"
case "$ADMIN_BASE" in
    http|https|ws|wss) ADMIN_BASE="" ;;   # 无端口可剥：不拼 URL，走下方占位提示
esac
echo -e "  ${C_BLUE}First run:${C_RESET}"
if [[ -n "$ADMIN_BASE" ]]; then
    echo -e "    Visit ${C_GREEN}$ADMIN_BASE:$PORT_ADMIN/admin${C_RESET}（注意是独立 Admin 端口，不是 API 端口）"
else
    echo -e "    Visit ${C_GREEN}http://<服务器IP>:$PORT_ADMIN/admin${C_RESET}（独立 Admin 端口，不是 API 端口 $PORT_API）"
fi
echo -e "    and use the bootstrap token to create the owner account."
# A4（FIX-20261003-01 NJ-4/N33）：token root:0600 且不打印，新手无从得知。
# 单管理员自托管场景，部署收尾直接打印路径 + 完整 token（打印即用），并提示保管。
echo -e "    Token 位置: ${C_GREEN}$STORAGE_DIR/.bootstrap_token${C_RESET}（首次运行自动生成，0600 权限）"
BOOTSTRAP_TOKEN="$(cat "$STORAGE_DIR/.bootstrap_token" 2>/dev/null || true)"
if [[ -n "$BOOTSTRAP_TOKEN" ]]; then
    echo -e "    Bootstrap Token: ${C_GREEN}$BOOTSTRAP_TOKEN${C_RESET}"
    echo -e "    ${C_YELLOW}请妥善保管：token 等同 Owner 初始化凭证，泄漏应立即删除上述文件并重启服务重新生成${C_RESET}"
else
    echo -e "    Token 当前不可读或尚未生成（服务首启时写入），查看: ${C_GREEN}sudo cat $STORAGE_DIR/.bootstrap_token${C_RESET}"
fi
echo ""

# M2 观测性：nohup 方式下 server.log 无界增长 —— 尽力安装 logrotate 轮转
# （systemd/journald 部署不受影响；非 root 时给出手工配置指引）。
if [[ ! -d /etc/systemd/system/ridgericetalk.service.d ]] && ! systemctl cat ridgericetalk >/dev/null 2>&1; then
    if [[ -d /etc/logrotate.d ]] && [[ "$(id -u)" == "0" ]]; then
        cat > /etc/logrotate.d/ridgericetalk <<EOF
$SERVER_DIR/logs/*.log {
    daily
    rotate 7
    maxsize 100M
    compress
    missingok
    notifempty
    copytruncate
}
EOF
        ok "已配置日志轮转（/etc/logrotate.d/ridgericetalk：daily + 100M 上限，保留 7 份）"
    else
        info "日志轮转：服务日志位于 $SERVER_DIR/logs/（nohup 方式无自动轮转）。建议配置 logrotate："
        info "  $SERVER_DIR/logs/*.log { daily; rotate 7; maxsize 100M; compress; copytruncate }"
    fi
fi

# Optionally print reminder about firewall
if command -v ufw &>/dev/null || command -v firewall-cmd &>/dev/null || command -v iptables &>/dev/null; then
    TURN_FW_NOTE=""
    [[ "$ENABLE_TURN" == "true" ]] && TURN_FW_NOTE=", $TURN_UDP_PORT/udp(TURN)"
    info "Reminder: ensure ports $PORT_API, $PORT_ADMIN, $PORT_LK_WS, $PORT_LK_TCP, $PORT_LK_UDP${TURN_FW_NOTE} are open in your firewall."
fi

# DES-2026-0912-03 D4：embedded 定位轻量，不内置 TLS 三档，但必须提示网页端语音
# 的 HTTPS 前提（浏览器仅在安全上下文允许 getUserMedia 采集麦克风）。
info "提示: 网页端语音（麦克风采集）需要 HTTPS，可执行 server/scripts/setup-lan-https.sh 配置自签名 HTTPS 反代。"

exit 0
