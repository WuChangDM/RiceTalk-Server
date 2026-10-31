#!/usr/bin/env bash
# LiveKit API 密钥助手（task-96，2026-10-31）
#
# 背景：服务端 config 的 LiveKit API Key/Secret 有 devkey/devsecret 默认回退
# （server/internal/config/config.go），而部署链历史上只有 Linux embedded 的
# 「新建 .env」分支会生成随机值；Windows 预构建包、已有 .env 的升级路径、
# 手工部署都会落到默认值 → 生产带着公开可知的密钥上线（LiveKit 已因此拒绝
# 过短 secret，语音链路也会与 livekit.yaml 不一致）。
#
# 本文件只提供纯函数，被以下脚本 source，不单独执行：
#   - server/scripts/deploy-embedded.sh（Linux embedded / 离线包安装）
#   - server/scripts/install-offline.sh（离线安装后守卫）
#
# 设计约束（task-96）：
#   1) 只用 CSPRNG（openssl rand 或 /dev/urandom），不新增第三方依赖；
#   2) 幂等：已有非默认值一律保留，绝不覆盖；
#   3) 不重启、不修改运行中实例 —— 只改磁盘上的 .env/livekit.yaml，重启与否由人决定；
#   4) 输出统一走 [OK]/[WARN]/[INFO] 前缀，便于部署日志比对。

# 服务端默认回退值（server/internal/config/config.go DefaultConfig）。
LK_DEFAULT_KEY="devkey"
LK_DEFAULT_SECRET="devsecret"
# 与 server/internal/config/secrets.go 的最小长度校验对齐：
# LiveKitAPIKey ≥ 16 字符（"ridgericetalk_" + 16 hex = 30），这里取保守下限；
# LiveKitAPISecret ≥ 64 字符（32 字节 hex）。task-96 要求 key ≥12 / secret ≥32。
LK_KEY_MIN_LEN=12
LK_SECRET_MIN_LEN=32

lk__ok()   { printf '  \033[32m[OK]\033[0m %s\n' "$*"; }
lk__warn() { printf '  \033[33m[WARN]\033[0m %s\n' "$*"; }
lk__info() { printf '  \033[90m[INFO]\033[0m %s\n' "$*"; }

# lk_random_hex <字节数> → 输出连续小写十六进制；失败返回 1（无可用 CSPRNG 源）。
lk_random_hex() {
    local n="${1:-32}" out=""
    if command -v openssl >/dev/null 2>&1; then
        out="$(openssl rand -hex "$n" 2>/dev/null || true)"
    fi
    if [[ -z "$out" ]] && [[ -r /dev/urandom ]]; then
        # 精简系统无 openssl 时的回退：直接读 CSPRNG 设备。
        # 禁止 `head -c` 管道形态（pipefail 下 SIGPIPE 会让整体返回 141）。
        out="$(od -An -N "$n" -tx1 /dev/urandom 2>/dev/null | tr -d ' \n' || true)"
    fi
    [[ -n "$out" ]] || return 1
    printf '%s' "$out"
}

# lk_generate_key → "rrt" + 16 hex（19 字符），与服务端 secrets.go 的
# "ridgericetalk_<16hex>" 风格一致且可辨识来源。
lk_generate_key() {
    local h
    h="$(lk_random_hex 8)" || return 1
    printf 'rrt%s' "$h"
}

# lk_generate_secret → 64 字符（32 字节 hex），满足 LiveKit ≥32 字符要求。
lk_generate_secret() {
    lk_random_hex 32
}

# lk_is_default_credentials <key> <secret> → 0 表示「缺失或默认或过短」（不安全）。
lk_is_default_credentials() {
    local k="$1" s="$2"
    [[ -z "$k" || -z "$s" ]] && return 0
    [[ "$k" == "$LK_DEFAULT_KEY" || "$s" == "$LK_DEFAULT_SECRET" ]] && return 0
    # 长度不达标同样视为不安全（LiveKit 会拒绝过短 secret 直接退出）。
    [[ "${#k}" -lt "$LK_KEY_MIN_LEN" || "${#s}" -lt "$LK_SECRET_MIN_LEN" ]] && return 0
    return 1
}

# lk_env_get <env-file> <KEY> → stdout 输出值（无此行返回 1）。
lk_env_get() {
    local f="$1" k="$2"
    [[ -f "$f" ]] || return 1
    awk -v k="$k" '
        { line=$0; sub(/\r$/, "", line) }
        index(line, k "=") == 1 { sub(/^[^=]*=/, "", line); print line; found=1; exit }
        END { if (!found) exit 1 }
    ' "$f"
}

# lk_env_set <env-file> <KEY> <value> → 幂等 upsert（存在则替换整行，不存在则追加）。
# 原子写：临时文件 + mv，避免中途失败留下半个 .env。
lk_env_set() {
    local f="$1" k="$2" v="$3" tmp
    [[ -f "$f" ]] || { lk__warn "env 文件不存在：$f"; return 1; }
    tmp="$(mktemp "${f}.tmp.XXXXXX" 2>/dev/null)" || return 1
    if ! awk -v k="$k" -v v="$v" '
        { line=$0; sub(/\r$/, "", line)
          if (index(line, k "=") == 1) { print k "=" v; done=1; next }
          print line }
        END { if (!done) print k "=" v }
    ' "$f" > "$tmp"; then
        rm -f "$tmp" 2>/dev/null || true
        return 1
    fi
    chmod --reference="$f" "$tmp" 2>/dev/null || chmod 600 "$tmp" 2>/dev/null || true
    mv -f "$tmp" "$f"
}

# lk_env_credentials <env-file> → "key<TAB>secret"
lk_env_credentials() {
    local f="$1" k s
    k="$(lk_env_get "$f" RRT_LIVEKIT_API_KEY 2>/dev/null || true)"
    s="$(lk_env_get "$f" RRT_LIVEKIT_API_SECRET 2>/dev/null || true)"
    printf '%s\t%s\n' "$k" "$s"
}

# lk_print_rotation_hint <env-file> <livekit-yaml> <api-port>
# 输出「复制即用」的轮换步骤（不执行任何动作）。
lk_print_rotation_hint() {
    local envf="$1" yamlf="${2:-}" apiport="${3:-8080}" ts
    ts="$(date -u +%Y%m%dT%H%M%SZ 2>/dev/null || echo backup)"
    printf '%s\n' \
        "  检测到默认 LiveKit 密钥（devkey/devsecret）——生产环境必须覆盖！" \
        "  轮换步骤（复制即用；期间语音会短暂中断）：" \
        "    1) 停服：sudo systemctl stop ridgericetalk（非 systemd 部署：结束 ridgericetalk 进程）" \
        "    2) 备份：sudo cp -a '$envf' '$envf.bak.$ts'" \
        "    3) 生成并写入（同一条命令，两处值由服务端启动时自动同步到 livekit.yaml）：" \
        "       NEW_K=\"rrt\$(openssl rand -hex 8)\"; NEW_S=\"\$(openssl rand -hex 32)\"" \
        "       sudo sed -i \"s|^RRT_LIVEKIT_API_KEY=.*|RRT_LIVEKIT_API_KEY=\$NEW_K|\" '$envf'" \
        "       sudo sed -i \"s|^RRT_LIVEKIT_API_SECRET=.*|RRT_LIVEKIT_API_SECRET=\$NEW_S|\" '$envf'" \
        "    4) 起服：sudo systemctl start ridgericetalk（服务端启动时会按新密钥重写 livekit.yaml）" \
        "    5) 自检：curl -fsS http://127.0.0.1:$apiport/api/health && 客户端重新进入语音频道" \
        "  说明：若使用外部 LiveKit（RRT_LIVEKIT_AUTOSTART=false），需把同一对值同步到该实例的 keys 段。"
}

# lk_ensure_env_credentials <env-file> [--rotate] [api-port]
# 返回值：
#   0 = 合规（原有非默认值保留）或已补齐缺失值（写入 env，重启后生效）
#   2 = 检测到默认值（按 task-96 只告警 + 给轮换命令，不自动改）；
#       仅在显式 --rotate 时才生成新值并写回（返回 0）
#   1 = 无法处理（文件缺失 / CSPRNG 不可用 / 写入失败）
lk_ensure_env_credentials() {
    local envf="$1" rotate="false" apiport="${2:-8080}"
    if [[ "${2:-}" == "--rotate" ]]; then
        rotate="true"; apiport="${3:-8080}"
    fi
    [[ -f "$envf" ]] || { lk__warn "env 文件不存在：$envf"; return 1; }

    local k s
    IFS=$'\t' read -r k s <<< "$(lk_env_credentials "$envf")"

    if ! lk_is_default_credentials "$k" "$s"; then
        lk__ok "LiveKit API 密钥已配置（非默认，幂等保留）"
        return 0
    fi

    # 缺失（空）与「默认值」分开处理：
    #   - 缺失：从未生成过 → 直接补齐（部署脚本会在本次部署后重启服务，无运行中不一致问题）；
    #   - 默认值：可能是刻意保持一致的历史现场 → 只告警，除非显式 --rotate。
    local missing="false"
    [[ -z "$k" || -z "$s" ]] && missing="true"

    if [[ "$missing" == "true" && "$rotate" != "true" ]]; then
        local nk ns
        nk="$(lk_generate_key)" || { lk__warn "无法生成 LiveKit API Key（openssl 与 /dev/urandom 均不可用）"; return 1; }
        ns="$(lk_generate_secret)" || { lk__warn "无法生成 LiveKit API Secret"; return 1; }
        lk_env_set "$envf" RRT_LIVEKIT_API_KEY "$nk" || return 1
        lk_env_set "$envf" RRT_LIVEKIT_API_SECRET "$ns" || return 1
        lk__ok "已生成随机 LiveKit API 密钥并写入 $(basename "$envf")（服务端重启后生效）"
        return 0
    fi

    if [[ "$rotate" == "true" ]]; then
        local nk ns
        nk="$(lk_generate_key)" || return 1
        ns="$(lk_generate_secret)" || return 1
        local ts; ts="$(date -u +%Y%m%dT%H%M%SZ 2>/dev/null || echo backup)"
        cp -a "$envf" "$envf.bak.$ts" 2>/dev/null || true
        lk_env_set "$envf" RRT_LIVEKIT_API_KEY "$nk" || return 1
        lk_env_set "$envf" RRT_LIVEKIT_API_SECRET "$ns" || return 1
        lk__ok "已轮换 LiveKit API 密钥并写入 $(basename "$envf")（备份：$(basename "$envf").bak.$ts）"
        lk__warn "需要重启服务端使新密钥生效；启动时会自动重写 livekit.yaml"
        return 0
    fi

    lk__warn "当前 LiveKit API 密钥为默认值（$LK_DEFAULT_KEY / ${LK_DEFAULT_SECRET:0:4}…）——生产环境禁止使用"
    lk_print_rotation_hint "$envf" "${3:-}" "${apiport}"
    return 2
}

# lk_assert_no_default_in_yaml <file> → 0 合规 / 1 命中默认值（仅用于自检与打包门禁）。
lk_assert_no_default_in_yaml() {
    local f="$1"
    [[ -f "$f" ]] || return 0
    if grep -Eq '(^|[^A-Za-z0-9_])(devkey|devsecret)([^A-Za-z0-9_]|$)' "$f" 2>/dev/null; then
        return 1
    fi
    return 0
}
