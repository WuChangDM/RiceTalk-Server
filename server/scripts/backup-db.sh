#!/usr/bin/env bash
# RidgeRiceTalk 数据库备份脚本（PostgreSQL custom 格式）
# ---------------------------------------------------------------
# 用途：把生产数据库（默认 ridgericetalk）备份为 pg_dump custom 格式（-Fc）dump，
#       备份完成后立即自检（pg_restore --list 可解析且 TABLE DATA 条目数 > 0），
#       自检不通过则以非 0 退出并提示「备份不可用」。
#
# 为什么这样写（实测踩坑）：
#   1. 口令只通过 PGPASSWORD 环境变量传给 pg_dump，不出现在 ps 命令行，也绝不回显。
#   2. 默认输出目录 /var/backups/ridgericetalk，不用 /root：
#      /root 是 drwx------ root:root（700），service 用户 postgres 无法穿越读取，
#      把 dump 放在 /root 下后 pg_restore 会报 "could not open input file: Permission denied"。
#   3. 自检（pg_restore --list）必须做，否则无法区分「备份成功」与「生成了一个坏文件」。
#
# Usage: sudo ./backup-db.sh [OPTIONS]
#
# 依赖：bash、coreutils、postgresql-client（pg_dump / pg_restore）

set -euo pipefail

# ============================================================
# 颜色输出与前缀（与 deploy-baremetal.sh 保持一致）
# ============================================================
C_RESET='\033[0m'
C_RED='\033[91m'
C_GREEN='\033[92m'
C_YELLOW='\033[93m'
C_CYAN='\033[96m'
C_GRAY='\033[90m'
C_MAGENTA='\033[95m'
C_BLUE='\033[94m'

ok()     { echo -e "  ${C_GREEN}[OK]${C_RESET} $1"; }
warn()   { echo -e "  ${C_YELLOW}[WARN]${C_RESET} $1"; }
fail()   { echo -e "  ${C_RED}[FAIL]${C_RESET} $1"; }
info()   { echo -e "  ${C_GRAY}[INFO]${C_RESET} $1"; }
banner() {
    echo -e "\n${C_MAGENTA}========================================${C_RESET}"
    echo -e "${C_MAGENTA}  $1${C_RESET}"
    echo -e "${C_MAGENTA}========================================${C_RESET}"
}
have() { command -v "$1" >/dev/null 2>&1; }

# ============================================================
# 常量（均可用环境变量覆盖，便于换机器/换目录）
# ============================================================
SCRIPT_NAME="backup-db.sh"
DEPLOY_DIR="${RRT_DEPLOY_DIR:-/opt/ridgericetalk}"
SERVER_DIR="$DEPLOY_DIR/server"
ENV_FILE="${RRT_ENV_FILE:-$SERVER_DIR/.env.production}"
DEFAULT_OUT_DIR="${RRT_BACKUP_DIR:-/var/backups/ridgericetalk}"
DEFAULT_KEEP=7

# ============================================================
# 参数默认值
# ============================================================
OUT_DIR="$DEFAULT_OUT_DIR"
KEEP="$DEFAULT_KEEP"
DRY_RUN=false

# ============================================================
# 帮助信息
# ============================================================
show_help() {
    cat <<'EOF'
Usage: backup-db.sh [OPTIONS]

RidgeRiceTalk 数据库备份（PostgreSQL custom 格式，含备份后自检）。

Options:
  --out-dir <dir>   备份输出目录（默认 /var/backups/ridgericetalk，不要用 /root）
  --keep <n>        保留最近 n 份备份，自动清理更旧的自身产物（默认 7）
  --dry-run         只打印将要执行的动作，不做任何修改（零副作用）
  --help, -h        显示此帮助

环境变量覆盖（可选）:
  RRT_ENV_FILE        配置文件路径（默认 /opt/ridgericetalk/server/.env.production）
  RRT_BACKUP_DIR      默认备份输出目录
  RRT_DB_HOST / RRT_DB_PORT / RRT_DB_NAME / RRT_DB_USER   数据库连接参数
  POSTGRES_PASSWORD   数据库口令（已设置则优先于配置文件中的值）

Examples:
  sudo ./backup-db.sh
  sudo ./backup-db.sh --out-dir /data/backups --keep 14
  sudo ./backup-db.sh --dry-run
EOF
}

# ============================================================
# 参数解析
# ============================================================
while [[ $# -gt 0 ]]; do
    case "$1" in
        --help|-h) show_help; exit 0 ;;
        --out-dir) OUT_DIR="${2:-}"; shift 2 ;;
        --keep)    KEEP="${2:-}"; shift 2 ;;
        --dry-run) DRY_RUN=true; shift ;;
        *)
            fail "未知参数: $1"
            show_help
            exit 2
            ;;
    esac
done

# ============================================================
# 前置检查
# ============================================================
banner "RidgeRiceTalk 数据库备份"

if [[ "${BASH_VERSINFO[0]}" -lt 4 ]]; then
    fail "需要 bash 4.0+（当前 ${BASH_VERSION}）"
    exit 1
fi

if [[ $EUID -ne 0 ]]; then
    fail "此脚本必须以 root 身份运行（需读取 0600 的 .env.production 与写 /var/backups）"
    info "请使用: sudo $SCRIPT_NAME"
    exit 1
fi
ok "以 root 身份运行"

for bin in pg_dump pg_restore md5sum; do
    if ! have "$bin"; then
        fail "缺少依赖命令: $bin"
        info "请安装 postgresql-client，例如: apt-get install -y postgresql-client"
        exit 1
    fi
done
ok "依赖命令齐全（pg_dump / pg_restore）"

if [[ ! -f "$ENV_FILE" ]]; then
    fail "配置文件不存在: $ENV_FILE"
    info "请确认部署目录，或用 RRT_ENV_FILE 指定正确路径"
    exit 1
fi
ok "配置文件: $ENV_FILE"

if ! [[ "$KEEP" =~ ^[0-9]+$ ]] || [[ "$KEEP" -lt 1 ]]; then
    fail "--keep 必须是 >=1 的整数，当前: $KEEP"
    exit 2
fi

# ============================================================
# 从 .env.production 读取配置（绝不打印口令值）
# ============================================================
env_get() {
    # 读取 .env.production 中某个 KEY 的值；找不到返回非 0
    local key="$1" line
    [[ -f "$ENV_FILE" ]] || return 1
    line="$(grep -E "^[[:space:]]*${key}=" "$ENV_FILE" 2>/dev/null | head -1 || true)"
    [[ -n "$line" ]] || return 1
    line="${line#*=}"
    line="${line%$'\r'}"
    # 去掉两端可能存在的双引号
    line="${line%\"}"
    line="${line#\"}"
    printf '%s' "$line"
}

read_pgpass() {
    # 口令优先级：显式环境变量 > .env.production 的 POSTGRES_PASSWORD > RRT_DB_PASSWORD
    local v=""
    if [[ -n "${POSTGRES_PASSWORD:-}" ]]; then
        printf '%s' "$POSTGRES_PASSWORD"
        return 0
    fi
    v="$(env_get POSTGRES_PASSWORD || true)"
    [[ -n "$v" ]] || v="$(env_get RRT_DB_PASSWORD || true)"
    printf '%s' "$v"
}

# 解析连接参数：环境变量 > RRT_DATABASE_URL > 已知默认值 ridgericetalk
DB_URL="$(env_get RRT_DATABASE_URL || true)"
DB_NAME="${RRT_DB_NAME:-${PGDATABASE:-}}"
DB_USER="${RRT_DB_USER:-${PGUSER:-}}"
DB_HOST="${RRT_DB_HOST:-${PGHOST:-}}"
DB_PORT="${RRT_DB_PORT:-${PGPORT:-}}"

# postgres://user:pass@host:port/dbname?params —— 只取 user/host/port/dbname，
# 口令优先使用 POSTGRES_PASSWORD（避免 URL 百分号编码问题），此处不回显。
if [[ "$DB_URL" =~ ^postgres(ql)?://([^:/@]+)(:([^@]*))?@([^:/@]+)(:([0-9]+))?/([^?]+) ]]; then
    if [[ -z "$DB_USER" ]]; then DB_USER="${BASH_REMATCH[2]}"; fi
    if [[ -z "$DB_HOST" ]]; then DB_HOST="${BASH_REMATCH[5]}"; fi
    if [[ -z "$DB_PORT" ]]; then DB_PORT="${BASH_REMATCH[7]}"; fi
    if [[ -z "$DB_NAME" ]]; then DB_NAME="${BASH_REMATCH[8]}"; fi
fi
DB_NAME="${DB_NAME:-ridgericetalk}"
DB_USER="${DB_USER:-ridgericetalk}"
DB_HOST="${DB_HOST:-127.0.0.1}"
DB_PORT="${DB_PORT:-5432}"

PGPASS="$(read_pgpass)"

ok "数据库: ${DB_NAME}  用户: ${DB_USER}  地址: ${DB_HOST}:${DB_PORT}"
if [[ -n "$PGPASS" ]]; then
    ok "已从配置解析到数据库口令（长度 ${#PGPASS}，值不回显）"
else
    warn "未在配置中找到数据库口令，将回退为本地 socket（以 postgres 身份，peer 认证）"
fi

# /root 权限陷阱提示
case "$OUT_DIR" in
    /root|/root/*)
        warn "输出目录位于 /root 下：/root 是 0700，postgres 用户无法穿越读取，"
        warn "后续 pg_restore 会报 Permission denied。建议改用 /var/backups/ridgericetalk"
        ;;
esac

# ============================================================
# 计算输出文件（UTC 时间戳）
# ============================================================
TS="$(date -u +%Y%m%d-%H%M%S)"
OUT_FILE="${OUT_DIR}/ridgericetalk-db-${TS}.dump"
PATTERN='ridgericetalk-db-*.dump'

# ============================================================
# dry-run：只打印计划，零副作用
# ============================================================
if [[ "$DRY_RUN" == "true" ]]; then
    banner "DRY-RUN（不会创建/修改/删除任何文件）"
    info "将连接数据库: ${DB_NAME} @ ${DB_HOST}:${DB_PORT}（用户 ${DB_USER}）"
    info "将执行: pg_dump -Fc（custom 格式）"
    info "输出目录: $OUT_DIR"
    info "输出文件: $OUT_FILE"
    info "备份后自检: pg_restore --list 校验 TABLE DATA 条目数 > 0"
    info "产物权限: chmod 600（含敏感数据）"
    info "清理策略: 仅清理匹配 ${OUT_DIR}/${PATTERN} 的自身产物，保留最近 ${KEEP} 份"
    ok "dry-run 结束（未做任何改动）"
    exit 0
fi

# ============================================================
# 准备输出目录
# ============================================================
if [[ ! -d "$OUT_DIR" ]]; then
    info "创建输出目录: $OUT_DIR"
    mkdir -p "$OUT_DIR"
fi

# ============================================================
# 执行 pg_dump（优先 TCP+口令；失败回退本地 socket）
# ============================================================
CONN_MODE=""
run_pg_dump() {
    # $1 = 目标文件路径
    local out="$1" err=""

    # 方式 A：TCP + 口令（口令经 PGPASSWORD 传给子进程，不出现在命令行）
    if [[ -n "$PGPASS" ]]; then
        if err="$(PGPASSWORD="$PGPASS" pg_dump -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" \
                    -d "$DB_NAME" -Fc -f "$out" 2>&1)"; then
            CONN_MODE="tcp"
            return 0
        fi
        warn "TCP 方式 pg_dump 失败: $(printf '%s' "$err" | head -1)"
    fi

    # 方式 B：本地 socket，以 postgres 身份（peer 认证，无需口令）
    #         stdout 重定向到文件由 root 创建，stderr 单独捕获
    if [[ $EUID -eq 0 ]] && have runuser; then
        if err="$(runuser -u postgres -- pg_dump -Fc -d "$DB_NAME" 2>&1 >"$out")"; then
            CONN_MODE="socket(postgres)"
            return 0
        fi
        warn "socket 方式 pg_dump 失败: $(printf '%s' "$err" | head -1)"
    fi

    return 1
}

info "开始备份数据库到: $OUT_FILE"
if ! run_pg_dump "$OUT_FILE"; then
    fail "pg_dump 执行失败，未生成有效备份"
    info "常见原因：库名/用户名/口令不正确，或 PostgreSQL 未运行"
    info "排查: systemctl status postgresql"
    # 清理可能产生的半成品
    [[ -e "$OUT_FILE" ]] && rm -f -- "$OUT_FILE"
    exit 1
fi
ok "pg_dump 完成（连接方式: ${CONN_MODE}）"

chmod 600 "$OUT_FILE" 2>/dev/null || true

# ============================================================
# 备份自检：pg_restore --list 可解析 且 TABLE DATA 条目数 > 0
# ============================================================
verify_dump() {
    # $1 = dump 文件；成功时把 TABLE DATA 条目数打印到 stdout
    local f="$1" listings count
    if ! listings="$(pg_restore --list "$f" 2>/dev/null)"; then
        return 1
    fi
    count="$(printf '%s\n' "$listings" | grep -c 'TABLE DATA' || true)"
    [[ "$count" -gt 0 ]] || return 1
    printf '%s' "$count"
}

TABLE_COUNT="$(verify_dump "$OUT_FILE")" || {
    fail "备份自检失败：pg_restore 无法解析该文件，或 TABLE DATA 条目为 0"
    warn "备份不可用！已删除本次产物: $OUT_FILE"
    rm -f -- "$OUT_FILE"
    exit 1
}
if [[ -z "$TABLE_COUNT" ]]; then
    fail "备份自检失败：未解析到任何 TABLE DATA 条目"
    warn "备份不可用！已删除本次产物: $OUT_FILE"
    rm -f -- "$OUT_FILE"
    exit 1
fi

SIZE_BYTES="$(stat -c %s "$OUT_FILE" 2>/dev/null || echo 0)"
SIZE_HUMAN="$(numfmt --to=iec --suffix=B "$SIZE_BYTES" 2>/dev/null || echo "${SIZE_BYTES}B")"
MD5="$(md5sum "$OUT_FILE" | awk '{print $1}')"

ok "备份自检通过"
info "文件: $OUT_FILE"
info "大小: $SIZE_HUMAN (${SIZE_BYTES} bytes)"
info "TABLE DATA 条目数: $TABLE_COUNT"
info "md5: $MD5"

# ============================================================
# 清理旧备份（只清理匹配本脚本命名的自身产物，保留最近 KEEP 份）
# ============================================================
cleanup_old() {
    local dir="$1" pattern="$2" keep="$3"
    local -a files=()
    local f
    while IFS= read -r f; do
        files+=("$f")
    done < <(find "$dir" -maxdepth 1 -type f -name "$pattern" -printf '%f\n' 2>/dev/null | LC_ALL=C sort)

    local total="${#files[@]}"
    if (( total <= keep )); then
        info "当前共 ${total} 份备份（保留上限 ${keep}），无需清理"
        return 0
    fi
    local remove_count=$(( total - keep ))
    local i
    for (( i=0; i<remove_count; i++ )); do
        local target="$dir/${files[$i]}"
        if [[ "$DRY_RUN" == "true" ]]; then
            info "[dry-run] 将删除旧备份: $target"
        else
            rm -f -- "$target"
            ok "已删除旧备份: $target"
        fi
    done
}

info "清理旧备份（保留最近 ${KEEP} 份）..."
cleanup_old "$OUT_DIR" "$PATTERN" "$KEEP"

banner "数据库备份完成"
ok "备份文件: $OUT_FILE"
info "恢复示例（请先确认目标库为空或已另行备份）:"
info "  PGPASSWORD=<口令> pg_restore -h 127.0.0.1 -U $DB_USER -d $DB_NAME --clean --if-exists \"$OUT_FILE\""
# 备份文件为 600（仅 root 可读）。若以 postgres 等其它用户执行 pg_restore，
# 会报 'could not open input file: Permission denied'——这并不代表备份损坏。
warn "本备份权限为 600（仅 root 可读）。如需以 postgres 等用户恢复，请先复制为可读副本:"
info "  cp \"$OUT_FILE\" /tmp/rrt-restore.dump && chmod 644 /tmp/rrt-restore.dump && sudo -u postgres pg_restore -d <目标库> /tmp/rrt-restore.dump"
