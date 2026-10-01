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

# ============================================================
# 常量与默认值
# ============================================================
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
DEPLOY_DIR="${DEPLOY_DIR:-/opt/ridgericetalk}"
SERVER_DIR="$DEPLOY_DIR/server"
WEB_DIR="$DEPLOY_DIR/web"
STORAGE_DIR="$SERVER_DIR/storage"
ENV_FILE="$SERVER_DIR/.env.production"

PUBLIC_ADDRESS=""
PORT_API=8080
PORT_ADMIN=9090
PORT_LK_WS=7880
PORT_LK_TCP=7881
PORT_LK_UDP=7882
FORCE=false
SKIP_FRONTEND=false
SKIP_BUILD=false
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
  --public-address <url>    对外访问地址（可省略，自动探测；例如 http://123.45.67.89:8080）
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
EOF
}

# ============================================================
# 参数解析
# ============================================================
while [[ $# -gt 0 ]]; do
    case "$1" in
        --help) show_help; exit 0 ;;
        --public-address) PUBLIC_ADDRESS="$2"; shift 2 ;;
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

# --public-address 可省略（DES-2026-0912-03 §5.3 短板③）：
# 未显式提供时探测公网/本机 IP 作为默认值，交互终端下询问确认，
# 非交互场景（CI、SSH 管道）直接采用默认值 —— 绝不读 /dev/tty，
# 否则非交互 SSH 下 read /dev/tty 会直接崩脚本（N1 实测教训）。
if [[ -z "$PUBLIC_ADDRESS" ]]; then
    DETECTED_IP=""
    if [[ -f "$SCRIPT_DIR/lib/net.sh" ]]; then
        # 复用能力层公网 IP 探测（境外服务 → 云元数据兜底），只复用不改
        # shellcheck source=/dev/null
        source "$SCRIPT_DIR/lib/net.sh"
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

TOTAL_STAGES=7
[[ "$SKIP_FRONTEND" == "true" ]] && TOTAL_STAGES=$((TOTAL_STAGES - 1))
[[ "$SKIP_BUILD" == "true" ]] && TOTAL_STAGES=$((TOTAL_STAGES - 1))

# ============================================================
# Stage 1/7: 检查依赖与端口
# ============================================================
stage 1 "$TOTAL_STAGES" "Checking dependencies and ports"

HAS_FATAL=false

if command -v go &>/dev/null; then
    GV=$(go version)
    ok "Go runtime: $GV"
else
    fail "Go runtime not found. Please install Go 1.25+ from https://go.dev/dl/"
    HAS_FATAL=true
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

check_port "$PORT_API" "API"
check_port "$PORT_ADMIN" "Admin"
check_port "$PORT_LK_WS" "LiveKit WebSocket"
check_port "$PORT_LK_TCP" "LiveKit TCP"

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
    # BUILD_LDFLAGS 含空格，必须整体作为 -ldflags 的单个参数传递
    # （教训：unquoted 展开会让 -X 直达 go 顶层参数，报 "flag provided but not defined: -X"）
    if [[ -n "$BUILD_LDFLAGS" ]]; then
        go build -ldflags "$BUILD_LDFLAGS" -buildvcs=false -o ridgericetalk ./cmd/server
    else
        go build -buildvcs=false -o ridgericetalk ./cmd/server
    fi
    ok "Backend built: $SERVER_DIR/ridgericetalk"

    # 迁移工具随主二进制一起构建（对齐 deploy-baremetal.sh 编译先例）；
    # 刚编译的产物必为本机格式，无需 baremetal 的 ELF magic 校验
    info "Building migration tool: go build -o ridgericetalk-migrate ./cmd/migrate/"
    if [[ -n "$BUILD_LDFLAGS" ]]; then
        go build -ldflags "$BUILD_LDFLAGS" -buildvcs=false -o ridgericetalk-migrate ./cmd/migrate/
    else
        go build -buildvcs=false -o ridgericetalk-migrate ./cmd/migrate/
    fi
    ok "Migration tool built: $SERVER_DIR/ridgericetalk-migrate"
else
    stage "$BUILD_STAGE" "$TOTAL_STAGES" "Skipping backend build"
    if [[ ! -f "$SERVER_DIR/ridgericetalk" ]]; then
        fail "No existing binary at $SERVER_DIR/ridgericetalk and --skip-build requested"
        exit 1
    fi
    ok "Using existing binary: $SERVER_DIR/ridgericetalk"
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

    cat > "$ENV_FILE" <<EOF
# RidgeRiceTalk 生产环境配置（由 deploy-embedded.sh 自动生成）
# 生成时间: $(date -u +"%Y-%m-%dT%H:%M:%SZ")

RRT_ENV=production
RRT_LOG_LEVEL=info
RRT_DEPLOY_MODE=embedded
RRT_PUBLIC_ADDRESS=$PUBLIC_ADDRESS

RRT_PORT=$PORT_API
RRT_ADMIN_PORT=$PORT_ADMIN

RRT_CORS_ORIGINS=$PUBLIC_ADDRESS,${CORS_LOCAL_SCHEME}://localhost:$PORT_API,${CORS_LOCAL_SCHEME}://localhost:$PORT_ADMIN

RRT_DATABASE_URL=$STORAGE_DIR/ridgericetalk.db
RRT_DB_DRIVER=sqlite

RRT_JWT_SECRET=$JWT_SECRET
RRT_CSRF_TOKEN_SECRET=$CSRF_SECRET
RRT_ENCRYPTION_KEY=

RRT_EMBEDDED_DEPS=true
RRT_THIRD_PARTY_DIR=$SERVER_DIR/third_party
RRT_MODELS_DIR=$SERVER_DIR/models

RRT_LIVEKIT_AUTOSTART=true
RRT_LIVEKIT_PORT=$PORT_LK_WS
RRT_LIVEKIT_URL=ws://127.0.0.1:$PORT_LK_WS
RRT_LIVEKIT_PUBLIC_URL=ws://127.0.0.1:$PORT_LK_WS
RRT_LIVEKIT_API_KEY=$LK_API_KEY
RRT_LIVEKIT_API_SECRET=$LK_API_SECRET
RRT_NETEASE_API_ENDPOINT=http://127.0.0.1:3300

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
if [[ ! -x "$MIGRATE_BIN" ]]; then
    # --skip-build 且首次部署时迁移工具可能缺失：就地补编译
    # （baremetal ensure_migrate_binary 的轻量版；刚编译产物必为本机格式，
    #   故不做 ELF magic 校验）
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
    nohup "$SERVER_DIR/ridgericetalk" > "$LOG_FILE" 2>&1 &
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
echo -e "  ${C_WHITE}Server binary:${C_RESET}     $SERVER_DIR/ridgericetalk"
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
echo -e "    Token 位置: ${C_GREEN}$STORAGE_DIR/.bootstrap_token${C_RESET}（首次运行自动生成，0600 权限；"
echo -e "    出于安全不打印到日志，查看: cat $STORAGE_DIR/.bootstrap_token）"
echo ""

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
