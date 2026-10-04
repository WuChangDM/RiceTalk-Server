#!/usr/bin/env bash
# RidgeRiceTalk 服务端二进制回滚脚本
# ---------------------------------------------------------------
# 用途：把服务端二进制回滚到指定备份（--to）或最新备份，并可靠地验证结果。
#
# 为什么这样写（实测踩坑，务必理解）：
#   1. 直接 cp 覆盖正在运行的二进制会失败（ETXTBSY: Text file busy），
#      而随后的 systemctl restart 却会「成功」——只看重启成功会误判回滚成功。
#      本脚本先停服务，再「复制到临时文件 → 校验 md5 → mv 替换 → 再校验活文件 md5」，
#      任一步 md5 不一致立即失败并中止，绝不在未验证的情况下报告成功。
#   2. 覆盖后必须显式还原属主/权限为 ridgericetalk:ridgericetalk 755：
#      历史 .bak 可能是 root:root，天真用 cp -a 会把活文件属主改错。
#   3. /api/health 的 version 字段现在是构建期注入值（形如 <语义版本>+<短commit>），
#      并新增 commit / buildTime 两个字段（见 core/version/version.go）。
#      未注入的旧构建仍显示 0.2.2 且新字段为空串——因此：
#      能拿到 commit 时以它为准，否则以二进制 md5 作为判据。
#   4. 回滚前若当前二进制尚未留存备份，先自动备份当前版本，避免把当前版本弄丢。
#   5. 备份目录为空时不再直接失败退出，而是「自举」出首份备份：
#      此前部署侧从不往该目录写备份，导致刚部署完的机器上按文档执行回滚必然失败
#      （实测：须人工塞一个符合命名规则的文件才能跑）。现在改为先备份当前二进制，
#      以 0 退出并提示可再次执行；部署侧（deploy_server_binary.sh）也已写入标准备份，
#      两端命名/权限口径一致，形成「部署 → 回滚」闭环。
#   6. 备份统一 root:root 600 并写同名 .md5 旁文件：备份目录虽 root 私有，但历史备份
#      属主是本服务用户，服务用户仍能 O_TRUNC 覆写自己的备份文件；若脚本不记录预期
#      md5，被改过的备份仍会被当作「校验通过」。故备份改为 root 属主 + 旁文件双重校验。
#
# Usage: sudo ./rollback-binary.sh [OPTIONS]
#
# 依赖：bash、coreutils、systemd、curl、tar 无关

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
C_WHITE='\033[97m'

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
SCRIPT_NAME="rollback-binary.sh"
DEPLOY_DIR="${RRT_DEPLOY_DIR:-/opt/ridgericetalk}"
SERVER_DIR="$DEPLOY_DIR/server"
BIN="${RRT_BIN:-$SERVER_DIR/ridgericetalk}"
BACKUP_DIR="${RRT_BIN_BACKUP_DIR:-/var/backups/ridgericetalk/bin}"
SERVICE="${RRT_SERVICE:-ridgericetalk}"
OWNER="${RRT_BIN_OWNER:-ridgericetalk:ridgericetalk}"
MODE="755"
HEALTH_PATH="/api/health"
PATTERN='ridgericetalk-bin-*'

# ============================================================
# 参数默认值
# ============================================================
TO=""
LIST_ONLY=false
DRY_RUN=false
PORT="${RRT_API_PORT:-5000}"
TIMEOUT="${RRT_ROLLBACK_TIMEOUT:-60}"

# ============================================================
# 帮助信息
# ============================================================
show_help() {
    cat <<'EOF'
Usage: rollback-binary.sh [OPTIONS]

RidgeRiceTalk 服务端二进制回滚（含 md5 校验与健康检查）。

Options:
  --to <file>        指定备份文件（默认取备份目录中最新的一个）。
                     也可指向部署时留在部署目录的 *.bak.* 文件
                     （仍会校验 ELF magic；无 .md5 旁文件时还要求可执行位，
                      缺位时按提示 chmod +x）
  --list             列出可用备份（只读，不修改任何东西；无备份时给出建立首份备份的指引）
  --dry-run          只打印将要执行的动作，零副作用（不启停服务、不改文件）
  --port <port>      健康检查端口（默认 5000）
  --timeout <sec>    健康检查等待秒数（默认 60）
  --backup-dir <dir> 备份目录（默认 /var/backups/ridgericetalk/bin）
  --help, -h         显示此帮助

环境变量覆盖（可选）:
  RRT_BIN               服务端二进制路径（默认 /opt/ridgericetalk/server/ridgericetalk）
  RRT_BIN_BACKUP_DIR    备份目录
  RRT_SERVICE           服务单元名（默认 ridgericetalk）
  RRT_BIN_OWNER         还原的属主:属组（默认 ridgericetalk:ridgericetalk）
  RRT_API_PORT          健康检查端口
  RRT_ROLLBACK_TIMEOUT  健康检查等待秒数

说明:
  - 回滚是否生效：优先比对二进制 md5；若 /api/health 返回的 `commit` 非空，
    也可用它核对批次（version 现在是构建期注入的 <版本>+<短commit>，
    未注入的旧构建仍为 0.2.2 且 commit 为空串）。
  - 若备份目录中没有任何可用备份，本脚本不会失败退出：会先把当前运行的二进制
    按标准命名备份进去，然后以 0 退出并提示「已建立首份备份」，再次执行即可回滚。
  - 备份统一命名 ridgericetalk-bin-<UTC时间戳>-<md5前8位>，属主 root、权限 600，
    并写同名 .md5 旁文件；回滚时会同时校验旁文件，不一致即拒绝（备份可能被篡改）。

Examples:
  sudo ./rollback-binary.sh --list
  sudo ./rollback-binary.sh                 # 回滚到最新备份
  sudo ./rollback-binary.sh --to /var/backups/ridgericetalk/bin/ridgericetalk-bin-20260912-000000-abcd1234
  sudo ./rollback-binary.sh --to /opt/ridgericetalk/server/ridgericetalk.bak.20260912
  sudo ./rollback-binary.sh --dry-run
EOF
}

# ============================================================
# 参数解析
# ============================================================
while [[ $# -gt 0 ]]; do
    case "$1" in
        --help|-h)   show_help; exit 0 ;;
        --to)        TO="${2:-}"; shift 2 ;;
        --list)      LIST_ONLY=true; shift ;;
        --dry-run)   DRY_RUN=true; shift ;;
        --port)      PORT="${2:-}"; shift 2 ;;
        --timeout)   TIMEOUT="${2:-}"; shift 2 ;;
        --backup-dir) BACKUP_DIR="${2:-}"; shift 2 ;;
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
banner "RidgeRiceTalk 二进制回滚"

if [[ "${BASH_VERSINFO[0]}" -lt 4 ]]; then
    fail "需要 bash 4.0+（当前 ${BASH_VERSION}）"
    exit 1
fi

if [[ $EUID -ne 0 ]]; then
    fail "此脚本必须以 root 身份运行（需停/启 systemd 服务并写部署目录）"
    info "请使用: sudo $SCRIPT_NAME"
    exit 1
fi
ok "以 root 身份运行"

for bin in systemctl curl md5sum od stat; do
    if ! have "$bin"; then
        fail "缺少依赖命令: $bin"
        info "请安装对应软件包后重试"
        exit 1
    fi
done
ok "依赖命令齐全（systemctl / curl / md5sum / od）"

if ! [[ "$PORT" =~ ^[0-9]+$ ]]; then
    fail "--port 必须是数字，当前: $PORT"
    exit 2
fi
if ! [[ "$TIMEOUT" =~ ^[0-9]+$ ]] || [[ "$TIMEOUT" -lt 1 ]]; then
    fail "--timeout 必须是 >=1 的整数，当前: $TIMEOUT"
    exit 2
fi

# ============================================================
# 工具函数
# ============================================================
md5_of() { md5sum "$1" 2>/dev/null | awk '{print $1}'; }

find_backups() {
    # 列出备份目录下的标准备份。
    # 必须排除同名 .md5 旁文件：旁文件名同样以 ridgericetalk-bin- 开头，会被 PATTERN 匹到。
    [[ -d "$BACKUP_DIR" ]] || return 0
    find "$BACKUP_DIR" -maxdepth 1 -type f -name "$PATTERN" ! -name '*.md5' 2>/dev/null
}

sidecar_md5_of() {
    # $1 = 备份文件；同名 .md5 旁文件存在则打印其记录的 md5（只取第一列），否则无输出
    local side="$1.md5" rec
    [[ -f "$side" ]] || return 0
    rec="$(awk 'NF {print $1; exit}' "$side" 2>/dev/null || true)"
    printf '%s' "$rec"
}

have_backup_with_md5() {
    # $1 = 目标 md5；找到同 md5 的备份则打印其路径，否则无输出
    local want="$1" f
    while IFS= read -r f; do
        if [[ "$(md5_of "$f")" == "$want" ]]; then
            printf '%s' "$f"
            return 0
        fi
    done < <(find_backups | LC_ALL=C sort)
    return 0
}

make_backup() {
    # $1 = 源二进制；$2 = 目标备份路径（应在 $BACKUP_DIR 下）
    # 成功返回 0；cp 失败返回 1；md5 校验不一致返回 2。
    # 备份统一 root:root 600（防服务用户 O_TRUNC 篡改），并写同名 .md5 旁文件。
    # dry-run 由调用方守卫，本函数本身不做 dry-run 判断（调用即写盘）。
    local src="$1" dst="$2" want got
    want="$(md5_of "$src")"
    mkdir -p "$BACKUP_DIR"
    if ! cp "$src" "$dst" 2>/dev/null; then
        rm -f -- "$dst" "$dst.md5" 2>/dev/null || true
        return 1
    fi
    if ! chown root:root "$dst" 2>/dev/null; then
        warn "chown root:root 失败（备份已生成但属主非 root，存在被篡改风险）: $dst"
    fi
    chmod 600 "$dst" 2>/dev/null || true
    got="$(md5_of "$dst")"
    if [[ -z "$want" || "$got" != "$want" ]]; then
        rm -f -- "$dst" "$dst.md5" 2>/dev/null || true
        return 2
    fi
    if ! printf '%s\n' "$got" > "$dst.md5" 2>/dev/null; then
        warn "写入 md5 旁文件失败: $dst.md5（回滚时仍会校验实际 md5）"
    fi
    chown root:root "$dst.md5" 2>/dev/null || true
    chmod 600 "$dst.md5" 2>/dev/null || true
    return 0
}

show_bootstrap_hint() {
    info "尚未创建任何备份；首次执行本脚本时会先把当前运行的二进制按标准命名备份到该目录，"
    info "随后以 0 退出并提示「已建立首份备份」；再次执行本脚本即可回滚到该备份。"
}

bootstrap_backup() {
    # 备份目录为空时的自举路径：备份当前运行二进制并建立首份备份，之后以 0 退出。
    # 无法自举（当前二进制不存在）时以 1 退出。--dry-run 下只打印计划、零副作用。
    banner "备份目录为空：建立首份备份（自举）"
    if [[ ! -f "$BIN" ]]; then
        fail "备份目录中没有可用备份，且当前二进制不存在: $BIN"
        info "没有可备份的二进制，无法建立首份备份，因而也无法回滚"
        info "请确认部署目录（RRT_DEPLOY_DIR / RRT_BIN），或用 --to 指定一个备份文件后重试"
        exit 1
    fi
    local cur_md5 ts dest rc=0
    cur_md5="$(md5_of "$BIN")"
    if [[ -z "$cur_md5" ]]; then
        fail "无法计算当前二进制 md5: $BIN"
        exit 1
    fi
    ts="$(date -u +%Y%m%d-%H%M%S)"
    dest="$BACKUP_DIR/ridgericetalk-bin-${ts}-${cur_md5:0:8}"

    if [[ "$DRY_RUN" == "true" ]]; then
        info "当前没有任何备份，回滚将无事可做；计划自举首份备份："
        info "  将执行: 备份 $BIN -> $dest（root:root 600，并写同名 .md5 旁文件）"
        info "  随后本脚本以 0 退出并提示「已建立首份备份」；再次执行即可回滚"
        ok "dry-run 结束（未做任何改动）"
        exit 0
    fi

    info "备份目录为空，先把当前运行的二进制按标准命名备份: $dest"
    make_backup "$BIN" "$dest" || rc=$?
    if (( rc != 0 )); then
        fail "建立首份备份失败（cp 失败或 md5 校验不一致），未改动任何运行中的文件"
        exit 1
    fi
    ok "已建立首份备份: $dest（md5=$cur_md5，root:root 600）"
    banner "首份备份已建立"
    info "本次未执行回滚（此前没有任何可回滚的备份）"
    ok "已建立首份备份，可再次执行本脚本进行回滚: sudo $SCRIPT_NAME"
    exit 0
}

list_backups() {
    if [[ ! -d "$BACKUP_DIR" ]]; then
        info "备份目录不存在: $BACKUP_DIR"
        show_bootstrap_hint
        return 0
    fi
    local -a files=()
    local f
    while IFS= read -r f; do
        files+=("$f")
    done < <(find_backups | LC_ALL=C sort)

    if (( ${#files[@]} == 0 )); then
        info "备份目录为空: $BACKUP_DIR"
        show_bootstrap_hint
        return 0
    fi

    local cur_md5=""
    if [[ -f "$BIN" ]]; then cur_md5="$(md5_of "$BIN")"; fi

    ok "可用备份（目录: $BACKUP_DIR，共 ${#files[@]} 份）:"
    local path md5 size fname
    for path in "${files[@]}"; do
        fname="$(basename "$path")"
        md5="$(md5_of "$path")"
        size="$(stat -c %s "$path" 2>/dev/null || echo 0)"
        size="$(numfmt --to=iec --suffix=B "$size" 2>/dev/null || echo "${size}B")"
        if [[ -n "$cur_md5" && "$md5" == "$cur_md5" ]]; then
            echo -e "    ${C_WHITE}$fname${C_RESET}  ($size)  md5=$md5  <= 当前运行版本"
        else
            echo -e "    $fname  ($size)  md5=$md5"
        fi
    done
    return 0
}

validate_backup() {
    # $1 = 备份文件；通过返回 0
    local f="$1" magic rec actual
    if [[ ! -f "$f" ]]; then
        fail "找不到备份文件: $f"
        return 1
    fi
    if [[ ! -s "$f" ]]; then
        fail "备份文件为空，拒绝回滚: $f"
        return 1
    fi
    magic="$(od -An -tx1 -N4 "$f" 2>/dev/null | tr -d ' \n')"
    if [[ "$magic" != "7f454c46" ]]; then
        fail "备份不是有效的 ELF 可执行文件（已损坏或非二进制），拒绝回滚: $f"
        return 1
    fi
    # 旁文件校验：若存在同名 .md5，则其记录的 md5 必须与实际一致，否则视为被篡改。
    rec="$(sidecar_md5_of "$f")"
    if [[ -n "$rec" ]]; then
        actual="$(md5_of "$f")"
        if [[ "$rec" != "$actual" ]]; then
            fail "备份 md5 与旁文件不一致，备份可能被篡改，拒绝回滚: $f"
            info "旁文件 $f.md5 记录 $rec，实际 $actual"
            return 1
        fi
    fi
    # 可执行位校验：
    #   - 无旁文件的备份（如部署时留下的 *.bak.*）沿用既有严格校验，缺位即拒绝；
    #   - 有旁文件的标准备份按 root:root 600 保存（防服务用户篡改），天然无可执行位，
    #     这是预期行为（回滚时步骤 6 会显式把活文件 chmod 755），故仅提示不拒绝。
    if [[ ! -x "$f" ]]; then
        if [[ -n "$rec" ]]; then
            info "标准备份无可执行位（按 600 保存，属预期）；回滚时会显式 chmod $MODE 活文件"
        else
            fail "备份文件缺少可执行权限，拒绝回滚: $f"
            info "如确认无误，请先: chmod +x \"$f\""
            return 1
        fi
    fi
    return 0
}

# --list 为只读操作，单独提前处理
if [[ "$LIST_ONLY" == "true" ]]; then
    list_backups
    exit 0
fi

# ============================================================
# 选定目标备份
# ============================================================
if [[ -n "$TO" ]]; then
    TARGET="$TO"
else
    latest=""
    if [[ -d "$BACKUP_DIR" ]]; then
        latest="$(find_backups | LC_ALL=C sort | tail -1)"
    fi
    if [[ -z "$latest" ]]; then
        # 备份目录为空：不直接失败，改为自举首份备份（内部必然 exit，不会走到下面）
        bootstrap_backup
        exit 1
    fi
    TARGET="$latest"
fi

if ! validate_backup "$TARGET"; then
    exit 1
fi
ok "目标备份: $TARGET"

TARGET_MD5="$(md5_of "$TARGET")"
TARGET_SIZE="$(stat -c %s "$TARGET")"
TARGET_SIZE_H="$(numfmt --to=iec --suffix=B "$TARGET_SIZE" 2>/dev/null || echo "${TARGET_SIZE}B")"
info "备份大小: $TARGET_SIZE_H   md5=$TARGET_MD5"

# ============================================================
# 检查当前二进制状态 + 是否需要自动备份当前版本
# ============================================================
CUR_EXISTS=false
CUR_MD5=""
CUR_OWNER=""
CUR_MODE=""
NEED_AUTOBACKUP=false
EXISTING_BACKUP=""
NEW_BACKUP=""

if [[ -e "$BIN" ]]; then
    CUR_EXISTS=true
    CUR_MD5="$(md5_of "$BIN")"
    CUR_OWNER="$(stat -c '%U:%G' "$BIN" 2>/dev/null || echo '?')"
    CUR_MODE="$(stat -c '%a' "$BIN" 2>/dev/null || echo '?')"
    info "当前二进制: $BIN  ($CUR_OWNER $CUR_MODE)  md5=$CUR_MD5"

    if [[ "$CUR_MD5" == "$TARGET_MD5" ]]; then
        warn "当前二进制的 md5 与目标备份一致，回滚后将无任何变化"
    fi

    EXISTING_BACKUP="$(have_backup_with_md5 "$CUR_MD5")"
    if [[ -n "$EXISTING_BACKUP" ]]; then
        info "当前版本已有备份: $EXISTING_BACKUP"
    else
        NEED_AUTOBACKUP=true
        TS_AUTO="$(date -u +%Y%m%d-%H%M%S)"
        NEW_BACKUP="$BACKUP_DIR/ridgericetalk-bin-${TS_AUTO}-${CUR_MD5:0:8}"
        info "当前版本尚无备份，将自动备份到: $NEW_BACKUP"
    fi
else
    warn "当前二进制不存在: $BIN（跳过自动备份，直接安装目标备份）"
    NEED_AUTOBACKUP=false
fi

TMP_FILE="${BIN}.rollback.tmp.$$"
HEALTH_URL="http://127.0.0.1:${PORT}${HEALTH_PATH}"

# ============================================================
# dry-run：只打印将要做什么，零副作用
# ============================================================
if [[ "$DRY_RUN" == "true" ]]; then
    banner "DRY-RUN（不启停服务、不创建/修改/删除任何文件）"
    if [[ "$NEED_AUTOBACKUP" == "true" ]]; then
        info "1. 将自动备份当前版本: $BIN -> $NEW_BACKUP（root:root 600，并写同名 .md5 旁文件）"
    elif [[ -n "$EXISTING_BACKUP" ]]; then
        info "1. 当前版本已有备份，跳过自动备份: $EXISTING_BACKUP"
    else
        info "1. 当前二进制不存在，跳过自动备份"
    fi
    info "2. 将停止服务: systemctl stop $SERVICE"
    info "3. 将复制备份到临时文件并校验 md5: $TARGET -> $TMP_FILE"
    info "4. 校验通过后替换: mv $TMP_FILE -> $BIN"
    info "5. 将显式还原属主/权限: chown $OWNER $BIN && chmod $MODE $BIN"
    info "6. 将再次校验活文件 md5 是否等于备份 md5（$TARGET_MD5）"
    info "7. 将重启服务: systemctl restart $SERVICE"
    info "8. 将轮询 $HEALTH_URL 直到 HTTP 200（最长 ${TIMEOUT}s）"
    warn "注意: version 为构建期注入值（<版本>+<短commit>），未注入的旧构建仍为 0.2.2；判据以 md5 为准，commit 非空时可用它核对批次"
    ok "dry-run 结束（未做任何改动）"
    exit 0
fi

# ============================================================
# 执行回滚（破坏性动作前均先打印）
# ============================================================
SERVICE_STOPPED=false
cleanup_on_err() {
    set +e
    if [[ "$SERVICE_STOPPED" == "true" ]]; then
        warn "脚本异常退出，尝试重新启动 $SERVICE ..."
        systemctl start "$SERVICE" >/dev/null 2>&1 || warn "启动失败，请人工检查: systemctl status $SERVICE"
    fi
}
trap cleanup_on_err ERR

# --- 步骤 1：自动备份当前版本 ---
if [[ "$NEED_AUTOBACKUP" == "true" ]]; then
    info "步骤 1/8: 备份当前版本 -> $NEW_BACKUP"
    autobackup_rc=0
    make_backup "$BIN" "$NEW_BACKUP" || autobackup_rc=$?
    if (( autobackup_rc != 0 )); then
        fail "备份当前版本失败（cp 失败或 md5 校验不一致），已中止回滚（原二进制未改动）"
        exit 1
    fi
    ok "已备份当前版本（md5=$CUR_MD5，root:root 600，附同名 .md5 旁文件）"
else
    info "步骤 1/8: 跳过自动备份（当前版本已有备份或二进制不存在）"
fi

# --- 步骤 2：停止服务（规避 ETXTBSY） ---
info "步骤 2/8: 停止服务 $SERVICE"
if systemctl is-active --quiet "$SERVICE" 2>/dev/null; then
    SERVICE_STOPPED=true
    if ! systemctl stop "$SERVICE"; then
        fail "停止服务失败，已中止回滚"
        exit 1
    fi
    ok "服务已停止"
else
    info "服务当前未运行，跳过停止"
fi

# --- 步骤 3：复制备份到同目录临时文件 ---
info "步骤 3/8: 复制备份到临时文件 $TMP_FILE"
if ! cp "$TARGET" "$TMP_FILE" 2>/dev/null; then
    fail "复制备份到临时文件失败，已中止回滚（原二进制未改动）"
    cleanup_on_err
    exit 1
fi

# --- 步骤 4：校验临时文件 md5 ---
info "步骤 4/8: 校验临时文件 md5"
TMP_MD5="$(md5_of "$TMP_FILE")"
if [[ "$TMP_MD5" != "$TARGET_MD5" ]]; then
    fail "临时副本 md5 与备份不一致：期望 $TARGET_MD5，实际 $TMP_MD5"
    warn "已中止回滚，原二进制未被替换"
    rm -f -- "$TMP_FILE"
    cleanup_on_err
    exit 1
fi
ok "临时副本 md5 校验通过"

# --- 步骤 5：替换活文件 ---
info "步骤 5/8: 替换 $BIN"
if ! mv -f "$TMP_FILE" "$BIN"; then
    fail "替换二进制失败，已中止回滚"
    rm -f -- "$TMP_FILE" 2>/dev/null || true
    cleanup_on_err
    exit 1
fi

# --- 步骤 6：显式还原属主/权限 ---
info "步骤 6/8: 还原属主/权限为 $OWNER $MODE"
if ! chown "$OWNER" "$BIN" 2>/dev/null; then
    fail "chown $OWNER 失败（是否缺少用户/组？）"
    cleanup_on_err
    exit 1
fi
chmod "$MODE" "$BIN"
NEW_MD5="$(md5_of "$BIN")"
if [[ "$NEW_MD5" != "$TARGET_MD5" ]]; then
    fail "覆盖后活文件 md5 与备份不一致：期望 $TARGET_MD5，实际 $NEW_MD5"
    warn "回滚结果不可信，已中止（服务可能仍处于停止状态，请人工介入）"
    # 显式 exit 不触发 ERR trap，必须显式调用恢复逻辑（尝试 systemctl start）
    cleanup_on_err
    exit 1
fi
ok "覆盖后 md5 校验通过（$NEW_MD5），属主 $(stat -c '%U:%G' "$BIN") 权限 $(stat -c '%a' "$BIN")"

# --- 步骤 7：重启服务 ---
info "步骤 7/8: 重启服务 $SERVICE"
if ! systemctl restart "$SERVICE"; then
    fail "systemctl restart $SERVICE 失败"
    warn "服务当前可能处于已停止状态，尝试恢复 ..."
    info "排查: journalctl -u $SERVICE -n 50 --no-pager"
    # 显式 exit 不触发 ERR trap，必须显式调用恢复逻辑（尝试 systemctl start）
    cleanup_on_err
    exit 1
fi
SERVICE_STOPPED=false
ok "服务已重启"

# --- 步骤 8：健康检查轮询 ---
info "步骤 8/8: 轮询健康检查 $HEALTH_URL（最长 ${TIMEOUT}s）"
HEALTH_OK=false
HEALTH_VER=""
HEALTH_COMMIT=""
waited=0
while [[ "$waited" -lt "$TIMEOUT" ]]; do
    resp=""
    if resp="$(curl -s --max-time 3 -w $'\n%{http_code}' "$HEALTH_URL" 2>/dev/null)"; then
        code="${resp##*$'\n'}"
        body="${resp%$'\n'*}"
        if [[ "$code" == "200" ]]; then
            HEALTH_OK=true
            HEALTH_VER="$(printf '%s' "$body" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1 || true)"
            # commit 由构建期注入（未注入的旧构建返回空串）
            HEALTH_COMMIT="$(printf '%s' "$body" | sed -n 's/.*"commit"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1 || true)"
            break
        fi
    fi
    sleep 1
    waited=$(( waited + 1 ))
    if (( waited % 5 == 0 )); then
        info "等待后端就绪 ... ${waited}s/${TIMEOUT}s"
    fi
done

trap - ERR

if [[ "$HEALTH_OK" != "true" ]]; then
    fail "健康检查在 ${TIMEOUT}s 内未返回 HTTP 200"
    warn "回滚二进制已就位（md5 $NEW_MD5），但服务未确认可用"
    info "排查: journalctl -u $SERVICE -n 80 --no-pager"
    exit 1
fi

# ============================================================
# 结果汇总
# ============================================================
banner "回滚完成"
ok "已回滚到: $TARGET"
info "活文件: $BIN"
info "活文件 md5: $NEW_MD5"
info "备份 md5  : $TARGET_MD5  （一致）"
info "属主/权限 : $(stat -c '%U:%G %a' "$BIN")"
info "健康检查  : HTTP 200（${waited}s）"
[[ -n "$HEALTH_VER" ]] && info "服务自报 version: $HEALTH_VER"
[[ -n "$HEALTH_COMMIT" ]] && info "服务自报 commit : $HEALTH_COMMIT"
if [[ -n "$HEALTH_COMMIT" ]]; then
    info "判据: 活文件 md5 与备份一致（见上）+ commit=$HEALTH_COMMIT 可用于核对批次"
else
    warn "注意: 本构建未注入 commit（version 显示 0.2.2 即未注入），无法区分批次；确认回滚生效以二进制 md5 为准"
fi
