#!/usr/bin/env bash
# ============================================================
# 上线窗口预检脚本（只读为主，失败不改变系统状态）
# ============================================================
# 用途：在 1.9.0 上线窗口开始时，先在真机跑一遍本脚本，把「能不能上线」
#       压缩成一组 PASS / WARN / FAIL 判定，避免窗口内才发现缺表/端口被占/版本落后。
#
# 默认**只读**：不做备份、不跑迁移、不重启服务。
# 唯一的写操作（数据库备份）必须显式 `--apply-backup` 才执行；不加时只**打印**命令。
#
# 用法：
#   sudo bash server/scripts/preflight-deploy.sh            # 只读预检
#   sudo bash server/scripts/preflight-deploy.sh --apply-backup   # 额外执行 pg_dump 备份
#   bash server/scripts/preflight-deploy.sh --help
#
# 可覆盖的环境变量（便于测试与非常规部署目录）：
#   PREFLIGHT_DEPLOY_DIR   部署根目录（默认 /opt/ridgericetalk）
#   PREFLIGHT_ENV_FILE     env 文件（默认 $PREFLIGHT_DEPLOY_DIR/server/.env.production）
#   PREFLIGHT_API_PORT     API 端口（默认 8080）
#   PREFLIGHT_ADMIN_PORT   Admin 端口（默认 9090）
#   PREFLIGHT_LK_WS        LiveKit WS 端口（默认 7880）
#   PREFLIGHT_LK_TCP       LiveKit TCP 媒体端口（默认 7881，服务端固定）
#   PREFLIGHT_LK_UDP       LiveKit UDP 媒体端口（默认 7882，服务端固定）
#   PREFLIGHT_HEALTH_URL   健康检查 URL（默认 http://127.0.0.1:<API>/api/health）
#   PREFLIGHT_DATABASE_URL 数据库 URL（默认从 env 文件读 RRT_DATABASE_URL）
#   PREFLIGHT_MIGRATIONS_DIR 迁移目录（默认 server/migrations）
#   PREFLIGHT_EXPECT_SCHEMA 期望的 schema 版本（默认 44）
#   PREFLIGHT_SKIP_ROOT=1  跳过 root 判定（测试用）
#   PREFLIGHT_SKIP_DB=1    跳过数据库相关项（无 psql 的演练机）
#
# 退出码：0 = 无 FAIL（可上线）；1 = 至少一项 FAIL（先处理再上线）
# ============================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

DEPLOY_DIR="${PREFLIGHT_DEPLOY_DIR:-/opt/ridgericetalk}"
ENV_FILE="${PREFLIGHT_ENV_FILE:-$DEPLOY_DIR/server/.env.production}"
API_PORT="${PREFLIGHT_API_PORT:-8080}"
ADMIN_PORT="${PREFLIGHT_ADMIN_PORT:-9090}"
LK_WS_PORT="${PREFLIGHT_LK_WS:-7880}"
LK_TCP_PORT="${PREFLIGHT_LK_TCP:-7881}"
LK_UDP_PORT="${PREFLIGHT_LK_UDP:-7882}"
HEALTH_URL="${PREFLIGHT_HEALTH_URL:-http://127.0.0.1:${API_PORT}/api/health}"
DATABASE_URL="${PREFLIGHT_DATABASE_URL:-}"
MIGRATIONS_DIR="${PREFLIGHT_MIGRATIONS_DIR:-$SERVER_DIR/migrations}"
EXPECT_SCHEMA="${PREFLIGHT_EXPECT_SCHEMA:-44}"

APPLY_BACKUP=0
SKIP_ROOT="${PREFLIGHT_SKIP_ROOT:-0}"
SKIP_DB="${PREFLIGHT_SKIP_DB:-0}"

PASS_N=0; WARN_N=0; FAIL_N=0

usage() { sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --apply-backup) APPLY_BACKUP=1; shift ;;
        --help|-h) usage; exit 0 ;;
        *) echo "❌ 未知参数: $1（用 --help）" >&2; exit 2 ;;
    esac
done

pass() { PASS_N=$((PASS_N + 1)); printf '  \033[32m[PASS]\033[0m %s\n' "$*"; }
warn() { WARN_N=$((WARN_N + 1)); printf '  \033[33m[WARN]\033[0m %s\n' "$*"; }
fail() { FAIL_N=$((FAIL_N + 1)); printf '  \033[31m[FAIL]\033[0m %s\n' "$*"; }
info() { printf '  \033[90m[INFO]\033[0m %s\n' "$*"; }
sec()  { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
action() { printf '         └─ 建议动作: %s\n' "$*"; }

have() { command -v "$1" >/dev/null 2>&1; }

# 从 env 文件读取键值（不 source，避免执行任意内容）
env_get() {
    local key="$1"
    [[ -f "$ENV_FILE" ]] || return 0
    grep -E "^[[:space:]]*${key}=" "$ENV_FILE" 2>/dev/null | tail -1 | sed -E "s/^[[:space:]]*${key}=//; s/^[\"']//; s/[\"']$//"
}

# ============================================================
sec "1. 主机环境"
# ============================================================
if [[ -r /etc/os-release ]]; then
    # shellcheck disable=SC1091
    os_name=$(. /etc/os-release 2>/dev/null && echo "${PRETTY_NAME:-${NAME:-unknown}}")
    pass "发行版: ${os_name:-unknown}"
else
    warn "无法读取 /etc/os-release（非标准 Linux 发行版？）"
    action "确认目标机为 Debian/Ubuntu 或 RHEL 系（部署脚本按此适配）"
fi
info "内核: $(uname -r 2>/dev/null || echo unknown) / 架构: $(uname -m 2>/dev/null || echo unknown)"

if [[ "$SKIP_ROOT" == "1" ]]; then
    info "已按 PREFLIGHT_SKIP_ROOT=1 跳过 root 判定"
elif [[ "$(id -u 2>/dev/null || echo 1)" == "0" ]]; then
    pass "以 root 运行"
else
    warn "当前非 root（当前 uid=$(id -u 2>/dev/null || echo '?')）"
    action "上线窗口用 sudo 执行：sudo bash server/scripts/preflight-deploy.sh"
fi

if have systemctl; then
    if ps -p 1 -o comm= 2>/dev/null | grep -qi systemd; then
        pass "systemd 为 PID 1（可用 systemctl 管理服务）"
    else
        fail "systemctl 存在但 PID 1 不是 systemd（容器/非 systemd 环境）"
        action "本预检假设 systemd 部署；容器化部署请改用 docker compose 健康检查"
    fi
else
    fail "缺少 systemctl（无法管理 ridgericetalk/livekit 服务）"
    action "安装 systemd 或改用容器部署流程"
fi

# ============================================================
sec "2. 端口占用（API/Admin/LiveKit）"
# ============================================================
port_state() { # port_state <port> <proto tcp|udp>  → 输出 "OK|OTHER|FREE|UNKNOWN <detail>"
    local port="$1" proto="$2" line
    if have ss; then
        if [[ "$proto" == "udp" ]]; then
            line=$(ss -lnup 2>/dev/null | awk -v p=":${port}$" '$4 ~ p {print $0}' | head -1)
        else
            line=$(ss -lntp 2>/dev/null | awk -v p=":${port}$" '$4 ~ p {print $0}' | head -1)
        fi
    elif have netstat; then
        if [[ "$proto" == "udp" ]]; then
            line=$(netstat -lnup 2>/dev/null | awk -v p=":${port}$" '$4 ~ p {print $0}' | head -1)
        else
            line=$(netstat -lntp 2>/dev/null | awk -v p=":${port}$" '$4 ~ p {print $0}' | head -1)
        fi
    else
        echo "UNKNOWN 缺少 ss/netstat"; return 0
    fi
    if [[ -z "$line" ]]; then echo "FREE 未监听"; return 0; fi
    if echo "$line" | grep -qiE 'ridgericetalk|livekit|ridgetalk'; then
        echo "OK $(echo "$line" | sed -E 's/[[:space:]]+/ /g' | cut -c1-90)"
    else
        echo "OTHER $(echo "$line" | sed -E 's/[[:space:]]+/ /g' | cut -c1-90)"
    fi
}

check_port() { # check_port <port> <proto> <label>
    local port="$1" proto="$2" label="$3" st detail
    st=$(port_state "$port" "$proto"); detail="${st#* }"; st="${st%% *}"
    case "$st" in
        OK)      pass "$label :$port/$proto 由本服务监听（$detail）" ;;
        OTHER)   fail "$label :$port/$proto 被**其它进程**占用（$detail）"
                 action "释放端口或改用其它端口（API/Admin/WS 可参数化；7881/7882 服务端固定必须 1:1 映射）" ;;
        FREE)    warn "$label :$port/$proto 当前未监听（服务未启动或端口改过）"
                 action "若为首次部署可忽略；否则核对 .env.production 中端口与 systemd 启动参数" ;;
        *)       warn "$label :$port/$proto 无法判定（缺少 ss/netstat）" ;;
    esac
}

check_port "$API_PORT" tcp "API"
check_port "$ADMIN_PORT" tcp "Admin"
check_port "$LK_WS_PORT" tcp "LiveKit WS"
check_port "$LK_TCP_PORT" tcp "LiveKit TCP 媒体"
check_port "$LK_UDP_PORT" udp "LiveKit UDP 媒体"

# ============================================================
sec "3. 服务与版本"
# ============================================================
if have systemctl; then
    for svc in ridgericetalk livekit; do
        if systemctl list-unit-files "$svc.service" >/dev/null 2>&1 && \
           systemctl list-unit-files "$svc.service" 2>/dev/null | grep -q "$svc.service"; then
            state=$(systemctl is-active "$svc" 2>/dev/null || true)
            if [[ "$state" == "active" ]]; then
                pass "$svc.service: active"
            else
                fail "$svc.service: ${state:-unknown}（未运行）"
                action "systemctl status $svc 查看原因；首次上线可先启动再复跑本预检"
            fi
        else
            warn "未找到 $svc.service（可能为首次部署）"
            action "首次部署会由 deploy-embedded.sh / deploy-baremetal.sh 安装 unit 文件"
        fi
    done
else
    warn "无 systemctl，跳过服务状态"
fi

if have curl; then
    health_body=$(curl -sf --max-time 5 "$HEALTH_URL" 2>/dev/null || true)
    if [[ -n "$health_body" ]]; then
        ver=$(printf '%s' "$health_body" | grep -oE '"version"[[:space:]]*:[[:space:]]*"[^"]*"' | sed -E 's/.*"([^"]*)"$/\1/')
        commit=$(printf '%s' "$health_body" | grep -oE '"commit"[[:space:]]*:[[:space:]]*"[^"]*"' | sed -E 's/.*"([^"]*)"$/\1/')
        build=$(printf '%s' "$health_body" | grep -oE '"buildTime"[[:space:]]*:[[:space:]]*"[^"]*"' | sed -E 's/.*"([^"]*)"$/\1/')
        pass "健康检查 $HEALTH_URL 返回 200（version=${ver:-unknown} commit=${commit:-unknown} buildTime=${build:-unknown}）"
        [[ -z "${commit:-}" || "$commit" == "unknown" ]] && { warn "健康检查无 commit（版本不可辨识）"; action "/api/health 缺 commit 说明二进制构建时未注入 -ldflags（deploy 脚本 2135 行附近）"; }
    else
        fail "健康检查失败：$HEALTH_URL 无 200 响应"
        action "systemctl status ridgericetalk；journalctl -u ridgericetalk -n 100 --no-pager"
    fi
else
    warn "缺少 curl，跳过健康检查"
fi

# ============================================================
sec "4. 迁移文件与数据库 schema"
# ============================================================
if [[ -d "$MIGRATIONS_DIR" ]]; then
    for f in 000044_message_thread_reads.up.sql 000044_message_thread_reads.down.sql; do
        if [[ -f "$MIGRATIONS_DIR/$f" ]]; then pass "迁移文件存在: $f"
        else fail "迁移文件缺失: $f"; action "确认部署包/仓库包含 migrations 全量（离线包 bundle/server/migrations）"; fi
    done
    max_local=$(ls "$MIGRATIONS_DIR"/*.up.sql 2>/dev/null | sed -E 's#.*/([0-9]{6})_.*#\1#' | sort -n | tail -1)
    info "本地最大迁移号: ${max_local:-unknown}（期望 schema 版本 $EXPECT_SCHEMA）"
else
    fail "迁移目录不存在: $MIGRATIONS_DIR"
    action "传 PREFLIGHT_MIGRATIONS_DIR 指向实际迁移目录"
fi

if [[ "$SKIP_DB" == "1" ]]; then
    info "已按 PREFLIGHT_SKIP_DB=1 跳过数据库项"
else
    [[ -n "$DATABASE_URL" ]] || DATABASE_URL=$(env_get RRT_DATABASE_URL)
    if [[ -z "$DATABASE_URL" ]]; then
        warn "未取得数据库 URL（env 文件无 RRT_DATABASE_URL，且未传 PREFLIGHT_DATABASE_URL）"
        action "确认 $ENV_FILE 存在且含 RRT_DATABASE_URL=postgres://..."
    elif ! have psql; then
        warn "缺少 psql 客户端，无法读取 schema 版本与表存在性"
        action "安装 postgresql-client 后复跑（或加 PREFLIGHT_SKIP_DB=1 明确跳过）"
    else
        schema_row=$(psql "$DATABASE_URL" -tAc "select version, dirty from schema_migrations limit 1" 2>/dev/null || true)
        if [[ -z "$schema_row" ]]; then
            warn "读不到 schema_migrations（库可能为空或未迁移）"
            action "首次上线：先跑 cmd/migrate up（或 RRT_FORCE_AUTOMIGRATE=1）再复跑"
        else
            db_ver=$(printf '%s' "$schema_row" | tr -d ' ' | cut -d'|' -f1)
            db_dirty=$(printf '%s' "$schema_row" | tr -d ' ' | cut -d'|' -f2)
            if [[ "$db_dirty" == "t" || "$db_dirty" == "true" ]]; then
                fail "schema_migrations 为 dirty（version=${db_ver}）"
                action "不要继续上线：先确认失败的那条迁移，手工修复后执行 migrate force <上一版本>"
            elif [[ "${db_ver:-0}" -eq "$EXPECT_SCHEMA" ]]; then
                pass "schema 版本 = ${db_ver}（已是最新）"
            elif [[ "${db_ver:-0}" -lt "$EXPECT_SCHEMA" ]]; then
                warn "schema 版本 ${db_ver} < ${EXPECT_SCHEMA}（上线窗口需执行迁移 0000$((db_ver + 1))~000$EXPECT_SCHEMA）"
                action "备份后执行：server/cmd/migrate up（或按清单执行 systemctl 重启触发的自动迁移）"
            else
                warn "schema 版本 ${db_ver} > ${EXPECT_SCHEMA}（当前部署包比库旧）"
                action "核对部署包版本与库版本是否匹配，避免回滚后代码不认新表"
            fi
        fi
        tbl=$(psql "$DATABASE_URL" -tAc "select to_regclass('public.message_thread_reads') is not null" 2>/dev/null | tr -d ' ')
        if [[ "$tbl" == "t" || "$tbl" == "true" ]]; then
            pass "表 message_thread_reads 存在（A9 二期线程未读可用）"
            cols=$(psql "$DATABASE_URL" -tAc "select count(*) from information_schema.columns where table_name='message_thread_reads'" 2>/dev/null | tr -d ' ')
            [[ "${cols:-0}" -ge 6 ]] && pass "message_thread_reads 列数 = ${cols}（期望 ≥6）" || { warn "message_thread_reads 列数=${cols:-0}，疑似结构不完整"; action "核对 000044 迁移 SQL 是否完整执行（期望 8 列含时间戳）"; }
        else
            if [[ "${db_ver:-0}" -ge "$EXPECT_SCHEMA" ]]; then
                fail "schema 已是 ${db_ver} 但表 message_thread_reads 不存在（迁移未真正生效）"
                action "手工核对 000044；必要时 migrate force <db_ver-1> 后重跑 up"
            else
                warn "表 message_thread_reads 不存在（schema=${db_ver:-?}，迁移后会自动创建）"
                action "按清单执行迁移后复跑本预检，期望变 PASS"
            fi
        fi
    fi
fi

# ============================================================
sec "5. 备份（默认只打印，不执行）"
# ============================================================
ts=$(date '+%Y%m%d-%H%M%S' 2>/dev/null || echo unknown)
backup_path="${PREFLIGHT_BACKUP_DIR:-/var/backups/ridgericetalk}/pre-1.9.0-${ts}.dump"
backup_cmd="pg_dump \"\${RRT_DATABASE_URL:-<db-url>}\" -Fc -f ${backup_path}"
info "迁移前备份命令："
info "  mkdir -p $(dirname "$backup_path") && ${backup_cmd}"
info "  （口令建议放 PGPASSWORD/~/.pgpass；备份文件必须 0600 —— 含全库数据）"
if [[ "$APPLY_BACKUP" == "1" ]]; then
    if [[ -z "$DATABASE_URL" ]]; then
        fail "无法执行备份：缺少数据库 URL"
    elif ! have pg_dump; then
        fail "无法执行备份：缺少 pg_dump"
    else
        mkdir -p "$(dirname "$backup_path")" 2>/dev/null || true
        # M-5（reviewer 20261029 审查）：
        # ① 备份含全库数据，旧行为按 umask 通常是 0644 → 改为 umask 077 + 事后 chmod 600；
        # ② DB 口令原本随 URL 进 argv，`ps` 同机任意用户可见 → 从 URL 拆出密码改用
        #    PGPASSWORD 环境变量，argv 里只保留去掉口令的连接串。
        _db_url="$DATABASE_URL"
        _db_pw=""
        if [[ "$_db_url" =~ ^([a-zA-Z0-9+.-]+)://([^:/@]+):([^@]*)@(.*)$ ]]; then
            _db_pw="${BASH_REMATCH[3]}"
            _db_url="${BASH_REMATCH[1]}://${BASH_REMATCH[2]}@${BASH_REMATCH[4]}"
            info "数据库口令已从命令行移出（改用 PGPASSWORD 环境变量）"
        fi
        run_backup() {
            if [[ -n "$_db_pw" ]]; then
                PGPASSWORD="$_db_pw" pg_dump "$_db_url" -Fc -f "$backup_path"
            else
                pg_dump "$_db_url" -Fc -f "$backup_path"
            fi
        }
        if ( umask 077; run_backup ) 2>/dev/null; then
            chmod 600 "$backup_path" 2>/dev/null || true
            pass "备份已写出: $backup_path（$(wc -c < "$backup_path" 2>/dev/null | tr -d ' ') 字节，权限 0600）"
        else
            fail "pg_dump 执行失败（未写出备份）"
        fi
        unset _db_pw _db_url
    fi
else
    warn "--apply-backup 未开启：备份命令仅打印，未执行（默认只读）"
    action "上线窗口执行：sudo bash $0 --apply-backup"
fi

# ============================================================
sec "6. 对外地址 / NAT / CORS 参数来源"
# ============================================================
if [[ -f "$ENV_FILE" ]]; then
    pass "env 文件存在: $ENV_FILE"
    pub=$(env_get RRT_PUBLIC_ADDRESS); admin_pub=$(env_get RRT_ADMIN_PUBLIC_ADDRESS)
    lk_pub=$(env_get RRT_LIVEKIT_PUBLIC_URL); cors=$(env_get RRT_CORS_ORIGINS)
    e2ee=$(env_get RRT_LIVEKIT_E2EE_ENABLED)
    [[ -n "$pub" ]] && pass "RRT_PUBLIC_ADDRESS=${pub}" || { warn "RRT_PUBLIC_ADDRESS 未设置（用默认推断或反代会错）"; action "在 $ENV_FILE 补 RRT_PUBLIC_ADDRESS（NAT 下必须为外部可达地址）"; }
    [[ -n "$lk_pub" ]] && pass "RRT_LIVEKIT_PUBLIC_URL=${lk_pub}" || warn "RRT_LIVEKIT_PUBLIC_URL 未设置（用 ws://127.0.0.1:${LK_WS_PORT} 默认，依赖桌面端 rewrite）"
    [[ -n "$admin_pub" ]] && info "RRT_ADMIN_PUBLIC_ADDRESS=${admin_pub}" || info "RRT_ADMIN_PUBLIC_ADDRESS 未设置（Admin 与 API 同源时正常）"
    [[ -n "$cors" ]] && pass "RRT_CORS_ORIGINS 已配置（$(printf '%s' "$cors" | tr ',' '\n' | wc -l | tr -d ' ') 个 origin）" || warn "RRT_CORS_ORIGINS 未配置"
    info "RRT_LIVEKIT_E2EE_ENABLED=${e2ee:-<未设置，默认关闭>}"
    mode=$(stat -c '%a' "$ENV_FILE" 2>/dev/null || echo '?')
    case "$mode" in
        600|640|660|400) pass "env 权限 $mode（不含 other 读）" ;;
        *) warn "env 文件权限为 $mode，建议收紧"
           action "chmod 600 $ENV_FILE（含数据库口令）" ;;
    esac
else
    warn "env 文件不存在: $ENV_FILE"
    action "首次部署会由 deploy 脚本生成；已有部署请核对 --deploy-dir"
fi

info "NAT 1:1 提醒：LiveKit 媒体端口由服务端固定（tcp ${LK_TCP_PORT} / udp ${LK_UDP_PORT}），端口映射必须 1:1，不可改外部端口"
info "如需自定义外部端口，只能改 API/Admin/WS（--port-api/--port-admin/--port-lk-ws）"

# ============================================================
sec "7. LiveKit / E2EE 配置"
# ============================================================
lk_yaml=$(find "$DEPLOY_DIR" -maxdepth 3 -name 'livekit.yaml' 2>/dev/null | head -1)
if [[ -n "$lk_yaml" ]]; then
    pass "livekit.yaml: $lk_yaml"
    grep -q "7881" "$lk_yaml" 2>/dev/null && pass "livekit.yaml 含 tcp 7881" || warn "livekit.yaml 未见 tcp 7881（媒体端口可能未渲染）"
    grep -q "7882" "$lk_yaml" 2>/dev/null && pass "livekit.yaml 含 udp 7882" || warn "livekit.yaml 未见 udp 7882"
else
    warn "未找到 livekit.yaml（LiveKit 可能未部署或由外部托管）"
    action "确认 --enable-turn/媒体端口配置；桌面端语音需 LiveKit WS + 7881/7882 可达"
fi

# ============================================================
printf '\n\033[1m== 汇总 ==\033[0m\n'
printf '  PASS=%d  WARN=%d  FAIL=%d\n' "$PASS_N" "$WARN_N" "$FAIL_N"
if [[ "$FAIL_N" -gt 0 ]]; then
    printf '  \033[31m结论：不可上线，先处理上面 %d 项 FAIL\033[0m\n' "$FAIL_N"
    exit 1
fi
printf '  \033[32m结论：预检通过（WARN 项按建议逐条确认后可上线）\033[0m\n'
exit 0
