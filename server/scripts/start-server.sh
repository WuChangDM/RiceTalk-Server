#!/usr/bin/env bash
# RidgeRiceTalk Server Start Script (Linux/macOS)
# One-click launcher with dependency checks, progress display, and access info
# ---------------------------------------------------------------
# Usage: ./scripts/start-server.sh
#        ./start.sh

set -euo pipefail

# Colors
C_RESET='\033[0m'
C_RED='\033[91m'
C_GREEN='\033[92m'
C_YELLOW='\033[93m'
C_CYAN='\033[96m'
C_WHITE='\033[97m'
C_GRAY='\033[90m'
C_BLUE='\033[94m'
C_MAGENTA='\033[95m'

stage() { echo -e "\n${C_BLUE}[$1/5]${C_RESET} ${C_CYAN}$2${C_RESET}"; }
ok()    { echo -e "  ${C_GREEN}[OK]${C_RESET} $1"; }
warn()  { echo -e "  ${C_YELLOW}[WARN]${C_RESET} $1"; }
fail()  { echo -e "  ${C_RED}[FAIL]${C_RESET} $1"; }
info()  { echo -e "  ${C_GRAY}[INFO]${C_RESET} $1"; }
banner() {
    echo -e "\n${C_MAGENTA}========================================${C_RESET}"
    echo -e "${C_MAGENTA}  $1${C_RESET}"
    echo -e "${C_MAGENTA}========================================${C_RESET}"
}

# Resolve paths
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
PROJECT_DIR="$(cd "$SERVER_DIR/../.." && pwd)"
STORAGE_DIR="$SERVER_DIR/storage"
LOGS_DIR="$SERVER_DIR/logs"
ENV_FILE="$SERVER_DIR/.env"
cd "$SERVER_DIR"

# ================================================================
# Stage 1/5: Dependency Checks
# ================================================================
stage 1 "Checking dependencies..."
HAS_FATAL=false

# 1.1 Go
if command -v go &>/dev/null; then
    GV=$(go version)
    ok "Go runtime: $GV"
else
    fail "Go runtime not found. RidgeRiceTalk requires Go 1.25+"
    info "Install: https://go.dev/dl/ or 'sudo apt install golang-go'"
    HAS_FATAL=true
fi

# 1.2 FFmpeg
if command -v ffmpeg &>/dev/null; then
    FV=$(ffmpeg -version 2>/dev/null | head -1 | sed 's/ffmpeg version //' | awk '{print $1}')
    ok "FFmpeg: $FV"
else
    if [ "${RRT_EMBEDDED_DEPS:-true}" = "true" ]; then
        info "FFmpeg not found in PATH; embedded deps enabled, server will download it automatically."
    else
        warn "FFmpeg not found. Music bot and TTS will be unavailable."
        info "Install: https://ffmpeg.org/download.html or 'sudo apt install ffmpeg'"
    fi
fi

# 1.3 Ports
API_PORT="${RRT_PORT:-8080}"
ADMIN_PORT="${RRT_ADMIN_PORT:-9090}"

if command -v ss &>/dev/null; then
    PORT_CHECK="ss -tlnp"
elif command -v netstat &>/dev/null; then
    PORT_CHECK="netstat -tlnp"
else
    PORT_CHECK=""
fi

check_port() {
    local port="$1"
    local name="$2"
    if [ -n "$PORT_CHECK" ]; then
        if $PORT_CHECK 2>/dev/null | grep -q ":$port "; then
            PID_INFO=$($PORT_CHECK 2>/dev/null | grep ":$port " | head -1)
            fail "Port $port ($name) is already in use."
            info "$PID_INFO"
            info "Set a different port: export RRT_${3}_PORT=<port>"
            HAS_FATAL=true
        else
            ok "Port $port ($name) is available"
        fi
    else
        warn "Cannot check port availability (ss/netstat not found)"
    fi
}

check_port "$API_PORT" "API" "PORT"
check_port "$ADMIN_PORT" "Admin" "ADMIN"

if [ -n "$PORT_CHECK" ] && $PORT_CHECK 2>/dev/null | grep -q ":7880 "; then
    warn "Port 7880 (LiveKit) is already in use. Voice may conflict."
else
    ok "Port 7880 (LiveKit) is available"
fi

# 1.4 Storage writable
mkdir -p "$STORAGE_DIR"
if [ -w "$STORAGE_DIR" ]; then
    ok "Storage directory writable"
else
    fail "Storage directory not writable: $STORAGE_DIR"
    HAS_FATAL=true
fi

if [ "$HAS_FATAL" = true ]; then
    echo -e "\n${C_RED}[ABORTED]${C_RESET} Please fix the issues above and run again."
    exit 1
fi

# ================================================================
# Stage 2/5: Environment Preparation
# ================================================================
stage 2 "Preparing environment..."

mkdir -p "$STORAGE_DIR" "$LOGS_DIR" "$SERVER_DIR/webhost/dist"
ok "Directories ready"

# Frontend dist
VOICE_SRC="$PROJECT_DIR/web/voice/dist"
ADMIN_SRC="$PROJECT_DIR/web/admin/dist"

if [ -d "$VOICE_SRC" ]; then
    mkdir -p "$SERVER_DIR/webhost/dist/voice"
    cp -r "$VOICE_SRC/"* "$SERVER_DIR/webhost/dist/voice/"
    ok "Voice frontend copied"
else
    warn "Voice frontend dist missing. Run: cd web/voice && npm run build"
fi

if [ -d "$ADMIN_SRC" ]; then
    mkdir -p "$SERVER_DIR/webhost/dist/admin"
    cp -r "$ADMIN_SRC/"* "$SERVER_DIR/webhost/dist/admin/"
    ok "Admin frontend copied"
else
    warn "Admin frontend dist missing. Run: cd web/admin && npm run build"
fi

# Secrets
SECRETS_CHANGED=false
if [ -f "$ENV_FILE" ]; then
    set -a
    # shellcheck source=/dev/null
    source "$ENV_FILE"
    set +a
    ok "Environment loaded from .env"
fi

if [ -z "${RRT_JWT_SECRET:-}" ] || [[ "$RRT_JWT_SECRET" == *"your-super-secret"* ]]; then
    export RRT_JWT_SECRET=$(openssl rand -hex 16 2>/dev/null || cat /dev/urandom | tr -dc 'a-z0-9' | head -c 32)
    SECRETS_CHANGED=true
    ok "JWT secret generated"
fi


if [ "$SECRETS_CHANGED" = true ]; then
    {
        if [ -f "$ENV_FILE" ]; then
            grep -v "^RRT_JWT_SECRET=" "$ENV_FILE" | grep -v "^RRT_CSRF_TOKEN_SECRET="
        fi
        echo "RRT_JWT_SECRET=$RRT_JWT_SECRET"
        echo "RRT_CSRF_TOKEN_SECRET=$RRT_CSRF_TOKEN_SECRET"
    } > "$ENV_FILE.tmp"
    mv "$ENV_FILE.tmp" "$ENV_FILE"
    ok "Secrets saved to .env"
fi

# Defaults
export RRT_ENV="${RRT_ENV:-development}"
export RRT_PORT="${RRT_PORT:-8080}"
export RRT_ADMIN_PORT="${RRT_ADMIN_PORT:-9090}"
export RRT_LOG_LEVEL="${RRT_LOG_LEVEL:-info}"
export RRT_DATABASE_URL="${RRT_DATABASE_URL:-$STORAGE_DIR/ridgericetalk.db}"
export RRT_DB_DRIVER="${RRT_DB_DRIVER:-sqlite}"
export RRT_LOCAL_DATA_PATH="${RRT_LOCAL_DATA_PATH:-$STORAGE_DIR}"
export RRT_PUBLIC_ADDRESS="${RRT_PUBLIC_ADDRESS:-http://localhost:$RRT_PORT}"
export RRT_EMBEDDED_DEPS="${RRT_EMBEDDED_DEPS:-true}"
export RRT_THIRD_PARTY_DIR="${RRT_THIRD_PARTY_DIR:-$SERVER_DIR/third_party}"
export RRT_MODELS_DIR="${RRT_MODELS_DIR:-$SERVER_DIR/models}"

ok "Environment configured"
info "  API Port: $RRT_PORT | Admin Port: $RRT_ADMIN_PORT | DB: $RRT_DB_DRIVER | Env: $RRT_ENV"

# ================================================================
# Stage 3/5: Pre-Launch
# ================================================================
stage 3 "Pre-launch check..."
if [ ! -f "$RRT_DATABASE_URL" ]; then
    warn "First run detected — database will be initialized"
    IS_FIRST_RUN=true
else
    ok "Database exists"
    IS_FIRST_RUN=false
fi

# ================================================================
# Stage 3.5: Start LiveKit (if not already running)
# ================================================================
LIVEKIT_PID=""
LIVEKIT_YAML="$SERVER_DIR/storage/livekit.yaml"

if [ -n "$PORT_CHECK" ] && $PORT_CHECK 2>/dev/null | grep -q ":7880 "; then
    ok "LiveKit already running on port 7880"
else
    # Try native binary first
    LIVEKIT_BIN=""
    for candidate in \
        "$SERVER_DIR/../livekit/livekit-server" \
        "$(command -v livekit-server 2>/dev/null)"; do
        if [ -f "$candidate" ] && [ -x "$candidate" ]; then
            LIVEKIT_BIN="$candidate"
            break
        fi
    done

    if [ -n "$LIVEKIT_BIN" ]; then
        info "Starting LiveKit (native binary)..."
        "$LIVEKIT_BIN" --config "$LIVEKIT_YAML" --dev \
            >> "$LOGS_DIR/livekit.log" 2>&1 &
        LIVEKIT_PID=$!
        sleep 2
        if kill -0 "$LIVEKIT_PID" 2>/dev/null; then
            ok "LiveKit started (PID $LIVEKIT_PID)"
        else
            warn "LiveKit failed to start — voice features unavailable"
            LIVEKIT_PID=""
        fi
    else
        warn "LiveKit not found (no binary) — voice features unavailable"
        info "Install: brew install livekit OR download from https://github.com/livekit/livekit/releases"
    fi
fi

# ================================================================
# Stage 4/5: Start Server
# ================================================================
stage 4 "Starting RidgeRiceTalk server..."
info "Press Ctrl+C to stop the server"

# Choose binary or dev mode — use array to handle spaces in path
if [ -f "$SERVER_DIR/ridgericetalk" ]; then
    CMD=("$SERVER_DIR/ridgericetalk")
    info "Mode: production binary (ridgericetalk)"
else
    CMD=(go run ./cmd/server)
    info "Mode: development (go run)"
fi

# Start server, capture output to log
LOG_FILE="$LOGS_DIR/server_startup.log"
rm -f "$LOG_FILE"

"${CMD[@]}" > >(tee -a "$LOG_FILE") 2>&1 &
SERVER_PID=$!

# Wait for server to become ready
READY=false
BOOTSTRAP_TOKEN=""
MAX_WAIT=60
WAITED=0

while [ $WAITED -lt $MAX_WAIT ]; do
    sleep 1
    WAITED=$((WAITED + 1))

    # Read bootstrap token from file (secure, not from logs)
    # H35: aligned with start-server.ps1 behavior — main.go writes the token
    # to $STORAGE_DIR/.bootstrap_token and does NOT print it to stdout/logs.
    TOKEN_FILE="$STORAGE_DIR/.bootstrap_token"
    if [ -f "$TOKEN_FILE" ]; then
        TOKEN_LINE=$(cat "$TOKEN_FILE" 2>/dev/null | tr -d '[:space:]' || true)
        if [ -n "$TOKEN_LINE" ]; then
            BOOTSTRAP_TOKEN="$TOKEN_LINE"
        fi
    fi

    # Check health
    if curl -sf "http://localhost:$RRT_PORT/api/health" > /dev/null 2>&1; then
        READY=true
        break
    fi

    # Check if process exited
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
        break
    fi

    if [ $((WAITED % 5)) -eq 0 ]; then
        info "Waiting for server to start... (${WAITED}s)"
    fi
done

# Check if process is still alive
if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    wait "$SERVER_PID"
    EXIT_CODE=$?
    fail "Server process exited unexpectedly (code: $EXIT_CODE). Check logs: $LOG_FILE"
    if [ -f "$LOG_FILE" ]; then tail -30 "$LOG_FILE"; fi
    exit 1
fi

if [ "$READY" != true ]; then
    warn "Server did not respond to health check within ${MAX_WAIT}s. It may still be starting."
fi

# ================================================================
# Stage 5/5: Service Ready — Display Access Info
# ================================================================
stage 5 "Service ready!"

# Collect IPs
LOCAL_IPS=()
while IFS= read -r ip; do
    LOCAL_IPS+=("$ip")
done < <(hostname -I 2>/dev/null | tr ' ' '\n' | grep -v '^$' | grep -v '^127\.' | grep -v '^169\.254\.' || true)

if [ ${#LOCAL_IPS[@]} -eq 0 ]; then
    LOCAL_IPS+=("localhost")
fi

PUBLIC_IP=""
if command -v curl &>/dev/null; then
    PUBLIC_IP=$(curl -sf --max-time 5 https://api.ipify.org 2>/dev/null || true)
fi

banner "RidgeRiceTalk Server is Running"

echo -e "  ${C_WHITE}Environment:${C_RESET}  $RRT_ENV"
echo -e "  ${C_WHITE}API Port:${C_RESET}     $RRT_PORT"
echo -e "  ${C_WHITE}Admin Port:${C_RESET}   $RRT_ADMIN_PORT"
echo -e "  ${C_WHITE}LiveKit:${C_RESET}      ws://localhost:7880"
echo ""

echo -e "  ${C_BLUE}Local Access:${C_RESET}"
for ip in "${LOCAL_IPS[@]}"; do
    echo -e "    Voice:   ${C_GREEN}http://$ip:$RRT_PORT${C_RESET}"
    echo -e "    Admin:   ${C_GREEN}http://$ip:$RRT_ADMIN_PORT/admin${C_RESET}"
done

if [ -n "$PUBLIC_IP" ]; then
    echo ""
    echo -e "  ${C_BLUE}Public Access:${C_RESET}"
    echo -e "    Voice:   ${C_GREEN}http://$PUBLIC_IP:$RRT_PORT${C_RESET}"
    echo -e "    Admin:   ${C_GREEN}http://$PUBLIC_IP:$RRT_ADMIN_PORT/admin${C_RESET}"
else
    echo ""
    echo -e "  ${C_GRAY}(Public IP could not be detected)${C_RESET}"
fi

echo ""
echo -e "  ${C_BLUE}WebSocket:${C_RESET}  ${C_GRAY}ws://localhost:$RRT_PORT/ws${C_RESET}"
echo -e "  ${C_BLUE}Metrics:${C_RESET}    ${C_GRAY}http://localhost:$RRT_ADMIN_PORT/metrics${C_RESET}"

if [ -n "$BOOTSTRAP_TOKEN" ]; then
    echo ""
    echo -e "  ${C_YELLOW}>>> FIRST RUN — Bootstrap Token <<<${C_RESET}"
    echo -e "  ${C_YELLOW}Token: ${C_WHITE}$BOOTSTRAP_TOKEN${C_RESET}"
    echo -e "  ${C_YELLOW}Use this token to create the admin account at:${C_RESET}"
    echo -e "  ${C_YELLOW}http://localhost:$RRT_ADMIN_PORT/admin${C_RESET}"
    echo -e "  ${C_GRAY}(This token will not be shown again)${C_RESET}"
fi

echo ""
echo -e "  ${C_GRAY}(Press Ctrl+C to stop the server)${C_RESET}"
echo ""

# Wait for the server process — Ctrl+C sends SIGINT which kills both
wait "$SERVER_PID"
EXIT_CODE=$?

echo ""
banner "Server Stopped"
echo -e "  RidgeRiceTalk server has exited (code: $EXIT_CODE)."
echo ""
