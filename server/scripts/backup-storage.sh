#!/usr/bin/env bash
# RidgeRiceTalk storage 目录备份脚本
# ---------------------------------------------------------------
# 用途：把部署目录下的 server/storage（secrets.json、livekit.yaml、
#       server-state.json、.bootstrap_token、uploads 等运行态数据）打包为 tar.gz。
#
# 安全提示（重要）：
#   storage 内的 secrets.json / livekit.yaml 含密钥，备份包一旦泄露等同于泄露服务端密钥。
#   本脚本会把产物 chmod 600，并在结尾再次提示「不要把该包放到公开位置」。
#
# 实测踩坑：
#   - /root 是 0700，service 用户无法穿越读取；默认输出目录改用 /var/backups/ridgericetalk。
#   - 备份后必须自检（tar -tzf 可列出条目），否则无法区分「打包成功」与「生成了坏包」。
#
# Usage: sudo ./backup-storage.sh [OPTIONS]
#
# 依赖：bash、coreutils、tar（gzip）

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
# 常量（均可用环境变量覆盖）
# ============================================================
SCRIPT_NAME="backup-storage.sh"
DEPLOY_DIR="${RRT_DEPLOY_DIR:-/opt/ridgericetalk}"
SERVER_DIR="$DEPLOY_DIR/server"
STORAGE_DIR="${RRT_STORAGE_DIR:-$SERVER_DIR/storage}"
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
Usage: backup-storage.sh [OPTIONS]

RidgeRiceTalk storage 目录备份（tar.gz，含密钥，备份后自检）。

Options:
  --out-dir <dir>   备份输出目录（默认 /var/backups/ridgericetalk，不要用 /root）
  --keep <n>        保留最近 n 份备份，自动清理更旧的自身产物（默认 7）
  --dry-run         只打印将要执行的动作，不做任何修改（零副作用）
  --help, -h        显示此帮助

环境变量覆盖（可选）:
  RRT_STORAGE_DIR   要备份的 storage 目录（默认 /opt/ridgericetalk/server/storage）
  RRT_BACKUP_DIR    默认备份输出目录

Examples:
  sudo ./backup-storage.sh
  sudo ./backup-storage.sh --out-dir /data/backups --keep 14
  sudo ./backup-storage.sh --dry-run
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
banner "RidgeRiceTalk storage 备份"

if [[ "${BASH_VERSINFO[0]}" -lt 4 ]]; then
    fail "需要 bash 4.0+（当前 ${BASH_VERSION}）"
    exit 1
fi

if [[ $EUID -ne 0 ]]; then
    fail "此脚本必须以 root 身份运行（需读取 ridgericetalk 属主的 storage 文件并写 /var/backups）"
    info "请使用: sudo $SCRIPT_NAME"
    exit 1
fi
ok "以 root 身份运行"

for bin in tar gzip; do
    if ! have "$bin"; then
        fail "缺少依赖命令: $bin"
        info "请安装，例如: apt-get install -y tar gzip"
        exit 1
    fi
done
ok "依赖命令齐全（tar / gzip）"

if [[ ! -d "$STORAGE_DIR" ]]; then
    fail "storage 目录不存在: $STORAGE_DIR"
    info "请确认部署目录，或用 RRT_STORAGE_DIR 指定正确路径"
    exit 1
fi
ok "storage 目录: $STORAGE_DIR"

if ! [[ "$KEEP" =~ ^[0-9]+$ ]] || [[ "$KEEP" -lt 1 ]]; then
    fail "--keep 必须是 >=1 的整数，当前: $KEEP"
    exit 2
fi

case "$OUT_DIR" in
    /root|/root/*)
        warn "输出目录位于 /root 下：非 root 用户无法读取，恢复时会踩权限坑。"
        warn "建议改用 /var/backups/ridgericetalk"
        ;;
esac

# ============================================================
# 计算输出文件（UTC 时间戳）
# ============================================================
TS="$(date -u +%Y%m%d-%H%M%S)"
OUT_FILE="${OUT_DIR}/ridgericetalk-storage-${TS}.tar.gz"
PATTERN='ridgericetalk-storage-*.tar.gz'

# storage 父目录与目录名（用 -C 打包，避免把绝对路径写进归档）
STORAGE_PARENT="$(dirname "$STORAGE_DIR")"
STORAGE_BASE="$(basename "$STORAGE_DIR")"

# ============================================================
# dry-run：只打印计划，零副作用
# ============================================================
if [[ "$DRY_RUN" == "true" ]]; then
    banner "DRY-RUN（不会创建/修改/删除任何文件）"
    info "将打包目录: $STORAGE_DIR"
    info "将执行: tar -czf（含 secrets.json / livekit.yaml 等密钥文件）"
    info "输出目录: $OUT_DIR"
    info "输出文件: $OUT_FILE"
    info "备份后自检: tar -tzf 可列出条目"
    info "产物权限: chmod 600（含密钥，切勿放到公开位置）"
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

# 目录内文件条目数（用于对比自检结果，防止空目录静默通过）
SRC_ENTRIES="$(find "$STORAGE_DIR" -mindepth 1 | wc -l | tr -d '[:space:]')"
if [[ "$SRC_ENTRIES" -eq 0 ]]; then
    warn "storage 目录为空（无文件可备份），仍将生成归档，请确认路径是否正确"
fi

# ============================================================
# 执行打包
# ============================================================
info "开始打包 storage 到: $OUT_FILE"
if ! tar -czf "$OUT_FILE" -C "$STORAGE_PARENT" "$STORAGE_BASE" 2>/dev/null; then
    fail "tar 打包失败"
    [[ -e "$OUT_FILE" ]] && rm -f -- "$OUT_FILE"
    exit 1
fi
chmod 600 "$OUT_FILE" 2>/dev/null || true
ok "tar 打包完成"

# ============================================================
# 备份自检：tar -tzf 可列出条目
# ============================================================
if ! tar -tzf "$OUT_FILE" >/dev/null 2>&1; then
    fail "备份自检失败：tar -tzf 无法读取该归档"
    warn "备份不可用！已删除本次产物: $OUT_FILE"
    rm -f -- "$OUT_FILE"
    exit 1
fi
ARCHIVE_ENTRIES="$(tar -tzf "$OUT_FILE" 2>/dev/null | wc -l | tr -d '[:space:]')"
if [[ "$ARCHIVE_ENTRIES" -eq 0 ]]; then
    fail "备份自检失败：归档内条目数为 0"
    warn "备份不可用！已删除本次产物: $OUT_FILE"
    rm -f -- "$OUT_FILE"
    exit 1
fi

SIZE_BYTES="$(stat -c %s "$OUT_FILE" 2>/dev/null || echo 0)"
SIZE_HUMAN="$(numfmt --to=iec --suffix=B "$SIZE_BYTES" 2>/dev/null || echo "${SIZE_BYTES}B")"

ok "备份自检通过"
info "文件: $OUT_FILE"
info "大小: $SIZE_HUMAN (${SIZE_BYTES} bytes)"
info "归档条目数: $ARCHIVE_ENTRIES（源目录条目数: $SRC_ENTRIES）"
info "文件权限: $(stat -c %a "$OUT_FILE" 2>/dev/null || echo '?')"

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

banner "storage 备份完成"
ok "备份文件: $OUT_FILE"
warn "该备份包含密钥（secrets.json / livekit.yaml），请勿放到公开位置，注意传输与存储安全"
info "恢复示例（会覆盖现有 storage，请先确认）:"
info "  tar -xzf \"$OUT_FILE\" -C $STORAGE_PARENT"
