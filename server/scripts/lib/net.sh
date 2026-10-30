#!/usr/bin/env bash
# shellcheck shell=bash
# ============================================================
# RidgeRiceTalk 能力层 · lib/net.sh
# 公网 IP 探测（含云元数据兜底）+ 镜像预设（占位）
# ------------------------------------------------------------
# 设计依据：DES-2026-0912-03 §3.1（抽出「能力层」，P1 阶段，G-2）
#           §2.2 D8「公网 IP 探测全依赖境外服务，无云元数据兜底」
#
# 改造要点：
#   1. 收敛：改造前 deploy-baremetal.sh 有**三处**逐字重复的
#      api.ipify.org / ifconfig.me / api.ip.sb 循环（交互式向导、Stage 6 写配置、
#      部署收尾打印），现统一走 rrt_public_ip；
#   2. 兜底：三家境外服务都不可达时（国内受限网络常见），再尝试云元数据
#      169.254.169.254（AWS IMDSv2 → IMDSv1 / OpenStack / 部分 KVM 云）。
#      顺序刻意放在境外服务**之后**：成功路径的返回值与改造前完全一致。
#
# 用法： . "<脚本目录>/lib/net.sh"
# ============================================================

# 依赖 os.sh（日志适配）；用本文件自身路径定位，不依赖调用者 cwd。
RRT_NET_LIB_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
# shellcheck source=/dev/null
. "$RRT_NET_LIB_DIR/os.sh"
unset RRT_NET_LIB_DIR

# ============================================================
# 一、IP 文本校验
# ============================================================
# 判断字符串「像」一个 IP 地址（IPv4 点分四段，或只含 IPv6 合法字符）。
# 目的：挡住把 HTML 错误页 / 代理提示页 / 多行输出当成公网 IP 写进 .env。
# 返回值：0 = 像，1 = 不像
rrt_is_ip_like() {
    local v="${1:-}"
    [[ -n "$v" ]] || return 1
    [[ ${#v} -le 45 ]] || return 1                      # IPv6 最长 45 字符
    [[ "$v" =~ ^[0-9a-fA-F:.]+$ ]] || return 1          # 只允许 IP 合法字符集
    if [[ "$v" == *.* ]]; then
        # 含点号则必须严格是 IPv4 点分四段
        [[ "$v" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || return 1
    fi
    return 0
}

# ============================================================
# 二、境外 IP 探测服务（原三处硬编码循环的唯一来源）
# ============================================================
# 输出：每行一个 URL。可用 RRT_IP_SERVICES 覆盖（空格分隔），供受限网络自定义。
rrt_ip_services() {
    if [[ -n "${RRT_IP_SERVICES:-}" ]]; then
        # shellcheck disable=SC2086  # 允许空格分隔的自定义列表
        printf '%s\n' $RRT_IP_SERVICES
        return 0
    fi
    printf '%s\n' \
        "https://api.ipify.org" \
        "https://ifconfig.me" \
        "https://api.ip.sb/ip"
}

# 依次尝试上述服务；成功打印 IP 并返回 0，全部失败返回 1（不打印）
rrt_public_ip_services() {
    local svc ip
    while IFS= read -r svc; do
        [[ -n "$svc" ]] || continue
        ip="$(curl -sf --max-time 5 "$svc" 2>/dev/null | tr -d '[:space:]' || true)"
        if rrt_is_ip_like "$ip"; then
            printf '%s\n' "$ip"
            return 0
        fi
    done < <(rrt_ip_services)
    return 1
}

# ============================================================
# 三、云元数据兜底（169.254.169.254）
# ============================================================
# ⚠️ 这是**兜底**，只在境外服务全部失败后才尝试；云主机上该地址是链路本地地址，
#    非云环境下通常立刻连接失败（每个请求最多等 3 秒）。
#
# 覆盖：AWS EC2 IMDSv2（需先取 token）→ IMDSv2 取不到时退回 IMDSv1
#       （IMDSv1 亦被 OpenStack / 部分 KVM 云兼容实现）。
# 未覆盖（G-3/P3 再补，需各自专有地址）：阿里云 100.100.100.200、
#       腾讯云 metadata.tencentyun.com、GCP metadata.google.internal。
rrt_metadata_base() { printf '%s\n' "${RRT_METADATA_BASE:-http://169.254.169.254}"; }

# 取元数据公网 IP；成功打印并返回 0，失败返回 1（不打印）
rrt_public_ip_metadata() {
    local base ip="" token=""
    base="$(rrt_metadata_base)"

    # AWS IMDSv2：先取临时 token（实例禁用 IMDSv1 时这是唯一通路）
    token="$(curl -sf -X PUT --max-time 3 \
        -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' \
        "${base}/latest/api/token" 2>/dev/null | tr -d '[:space:]' || true)"
    if [[ -n "$token" ]]; then
        ip="$(curl -sf --max-time 3 \
            -H "X-aws-ec2-metadata-token: $token" \
            "${base}/latest/meta-data/public-ipv4" 2>/dev/null | tr -d '[:space:]' || true)"
    fi

    # IMDSv1（无 token 的兼容实现）
    if ! rrt_is_ip_like "$ip"; then
        ip="$(curl -sf --max-time 3 \
            "${base}/latest/meta-data/public-ipv4" 2>/dev/null | tr -d '[:space:]' || true)"
    fi

    rrt_is_ip_like "$ip" || return 1
    printf '%s\n' "$ip"
}

# ============================================================
# 四、公网 IP（对主流程的唯一入口）
# ============================================================
# 顺序：境外探测服务 → 云元数据兜底。
# 成功打印 IP 并返回 0；全部失败返回 1（调用方决定是交互式询问还是报错）。
rrt_public_ip() {
    local ip=""
    ip="$(rrt_public_ip_services || true)"
    if [[ -n "$ip" ]]; then
        printf '%s\n' "$ip"
        return 0
    fi
    ip="$(rrt_public_ip_metadata || true)"
    if [[ -n "$ip" ]]; then
        printf '%s\n' "$ip"
        return 0
    fi
    return 1
}

# ============================================================
# 五、地址私网判定（FIX-20261003-01 A1）
# ============================================================
# 判定 host 是否为私网/本机地址（RFC1918 + loopback + localhost + ::1）。
# 用途：部署脚本推导 LiveKit rtc.use_external_ip —— 私网/无公网环境开
# use_external_ip 会让 STUN 产出不可达公网候选，ICE 全挂、语音全断（NJ-11）。
# 口径（与 FIX 方案一致，从严不全覆盖）：
#   私网/本机 → 返回 0：localhost、127.0.0.0/8、10.0.0.0/8、172.16.0.0/12、
#               192.168.0.0/16、::1
#   非私网    → 返回 1：公网 IP、域名（域名按公网处理，走 use_external_ip=true）
#   CGNAT 100.64/10、链路本地 169.254/16 刻意**不**算私网：这类机器仍有真实
#   出口公网 IP，STUN 检测反而是正确路径。
rrt_is_private_host() {
    local host="${1:-}"
    [[ -n "$host" ]] || return 1
    # G-5：容忍方括号形式（[2001:db8::1] / [::1]），先归一化再判定
    host="${host#\[}"; host="${host%\]}"
    case "$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]')" in
        localhost|localhost.|::1) return 0 ;;
    esac
    # G-5：IPv6 私网段（fc00::/7 唯一本地、fe80::/10 链路本地）按私网处理；
    # 公网 IPv6（2001:db8:: 等）仍走 use_external_ip=true。
    case "$host" in
        *:*)
            case "$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]')" in
                fc*|fd*|fe8*|fe9*|fea*|feb*) return 0 ;;
            esac
            return 1 ;;
    esac
    # 非 IP 字面量（域名）→ 非私网
    rrt_is_ip_like "$host" || return 1
    # 到此必为 IPv4 点分四段（rrt_is_ip_like 对含点号格式已强校验）
    local o1 o2
    IFS=. read -r o1 o2 _ _ <<< "$host"
    case "$o1" in
        127) return 0 ;;                                    # loopback
        10)  return 0 ;;                                    # 10.0.0.0/8
        172) [[ $o2 -ge 16 && $o2 -le 31 ]] && return 0 ;;  # 172.16.0.0/12
        192) [[ "$o2" == "168" ]] && return 0 ;;            # 192.168.0.0/16
    esac
    return 1
}

# 从 URL/地址串提取 host 部分（供部署脚本推导用）：
#   http://1.2.3.4:8080       → 1.2.3.4
#   https://example.com/      → example.com
#   http://[2001:db8::1]:8080 → 2001:db8::1（G-5：IPv6 方括号须先整体取出）
#   192.168.1.10              → 192.168.1.10（裸 IP 原样）
# 说明：旧实现用 `s#[/:?#].*$##` 一次性剥离，对带方括号的 IPv6 会截成 "[2001"
# （G-5 所列「IPv6 方括号」缺陷），故改为显式分支处理。
rrt_host_of_address() {
    local addr="${1:-}"
    # 去 scheme（无 scheme 时原样保留）
    case "$addr" in
        *://*) addr="${addr#*://}" ;;
    esac
    # 去认证段（user:pass@host）
    addr="${addr##*@}"
    # IPv6 方括号：整体取出括号内地址，之后再无端口/路径可剥
    case "$addr" in
        \[*)
            addr="${addr#\[}"
            printf '%s\n' "${addr%%\]*}"
            return 0
            ;;
    esac
    printf '%s\n' "${addr%%[/:?#]*}"
}

# 把 host 转成「可安全嵌入 URL」的形式（G-5 IPv6 方括号）：
#   2001:db8::1 → [2001:db8::1]；1.2.3.4 / example.com / [::1] 原样返回。
# 用途：拼 RRT_CORS_ORIGINS、RRT_LIVEKIT_PUBLIC_URL 等 URL 时禁止直接插裸 IPv6
# （否则 `ws://2001:db8::1:7880` 无法解析，G-5 IPv6 方括号明确项）。
rrt_url_host() {
    local host="${1:-}"
    case "$host" in
        \[*\]) printf '%s\n' "$host"; return 0 ;;   # 已是方括号形式
        *:*)   printf '[%s]\n' "$host"; return 0 ;; # 裸 IPv6 字面量补方括号
        *)     printf '%s\n' "$host"; return 0 ;;
    esac
}

# 从 URL 中取端口（G-5 NAT：对外端口可≠监听端口）：
#   http://1.2.3.4:50100 → 50100；https://example.com → 空（默认 443/80）
rrt_port_of_address() {
    local addr="${1:-}" rest
    case "$addr" in
        *://*) addr="${addr#*://}" ;;
    esac
    rest="${addr#*@}"
    case "$rest" in
        \[*\])  printf '' ; return 0 ;;   # [v6] 后若无 :port 则无端口
        \[*\]:*) rest="${rest#*\]}" ;;
    esac
    case "$rest" in
        *:*) printf '%s\n' "${rest##*:}" | sed -E 's#[/?#].*$##' ;;
        *)   printf '' ;;
    esac
}

# 按对外地址推导 LiveKit rtc.use_external_ip（FIX-20261003-01 A1 唯一裁决点）：
#   私网/本机地址 → false（打印推导说明）
#   公网/域名     → true（与旧硬编码一致，生成物逐字节兼容）
# 用法: LK_USE_EXTERNAL_IP="$(rrt_derive_use_external_ip "$PUBLIC_ADDRESS")"
rrt_derive_use_external_ip() {
    local host
    host="$(rrt_host_of_address "${1:-}")"
    if rrt_is_private_host "$host"; then
        printf '%s\n' "false"
    else
        printf '%s\n' "true"
    fi
}

# ============================================================
# 六、镜像预设（G-5 已实装：数据表在 lib/mirrors.sh）
# ============================================================
# 取值：cn（国内镜像优先）| global（直连上游）| auto（自动，默认，保持既有回退顺序）
# 消费点：deploy-baremetal.sh / deploy-embedded.sh 的全部下载（Go/Node/LiveKit/
# GitHub 加速/npm），具体 URL 优先序见 lib/mirrors.sh。
rrt_mirror_preset() { printf '%s\n' "${RRT_MIRROR_PRESET:-auto}"; }

rrt_mirror_preset_valid() {
    case "${1:-}" in
        cn|global|auto) return 0 ;;
        *)              return 1 ;;
    esac
}

# 设置镜像预设；非法取值返回 1 并提示（调用方决定是否退出）
rrt_set_mirror_preset() {
    local v="${1:-}"
    if ! rrt_mirror_preset_valid "$v"; then
        rrt_fail "无效的镜像预设: '${v}'（可选: cn / global / auto）"
        return 1
    fi
    RRT_MIRROR_PRESET="$v"
    return 0
}

# ============================================================
# 七、系统包管理器代理注入（G-5「apt 代理注入」）
# ============================================================
# 边界：本组函数**只写代理**（apt.conf.d / dnf.conf 的 proxy=），**不换系统源**；
# 系统源自动切换仍是 G-5b 未做项（与 mirrors.sh 头注释同口径）。
# 调用时机：部署脚本 --system-proxy 且已给 --http-proxy/--https-proxy 时，在
# 安装系统依赖之前调用；失败不中止部署（只告警，避免受限网络整链卡死）。
# 幂等：apt 覆盖写同一文件；dnf 有 proxy= 行则替换、无则追加。
rrt_apply_system_proxy() {
    local http_proxy_arg="${1:-}" https_proxy_arg="${2:-}"
    [[ -n "$http_proxy_arg" || -n "$https_proxy_arg" ]] || return 0
    local proxy="${https_proxy_arg:-$http_proxy_arg}"
    # M-4（reviewer 20261029 审查）：代理串会被明文写进系统配置文件。
    # 含换行可注入额外配置行、含双引号会破坏 apt.conf 语法 —— 直接拒绝。
    if [[ "$proxy" == *$'\n'* || "$proxy" == *'"'* ]]; then
        printf '代理地址含非法字符（换行/双引号），拒绝写入系统配置: %s\n' "$proxy"
        return 1
    fi
    local pm=""
    if declare -F rrt_pkg_manager &>/dev/null; then
        pm="$(rrt_pkg_manager 2>/dev/null || true)"
    fi
    case "$pm" in
        apt)
            local f=/etc/apt/apt.conf.d/99rrt-proxy
            if [[ "${RRT_DRY_RUN:-false}" == "true" ]]; then
                printf '[dry-run] 将写入 %s（http/https Proxy=%s，umask 077 → 0600）\n' "$f" "$proxy"
                return 0
            fi
            # umask 077：代理 URL 可能带 user:pass@ 凭据，配置文件不得 0644 可读
            if ( umask 077 && printf 'Acquire::http::Proxy "%s";\nAcquire::https::Proxy "%s";\n' \
                    "$proxy" "$proxy" > "$f" ) 2>/dev/null; then
                chmod 600 "$f" 2>/dev/null || true
                printf 'apt 代理已注入: %s（0600）\n' "$f"
            else
                printf 'apt 代理注入失败（无权限？跳过）: %s\n' "$f"
                return 1
            fi
            ;;
        dnf)
            local f=/etc/dnf/dnf.conf
            if [[ "${RRT_DRY_RUN:-false}" == "true" ]]; then
                printf '[dry-run] 将向 %s 写入 proxy=%s\n' "$f" "$proxy"
                return 0
            fi
            # 用「临时文件重建」而非 sed -i 替换：proxy 串含 # & \ 时 sed 会改坏配置，
            # 且旧 proxy= 行残留时只做 grep -q 判定会误报成功（M-4）。
            local tmpf n
            tmpf="$(mktemp "${f}.rrt.XXXXXX" 2>/dev/null || true)"
            if [[ -z "$tmpf" ]]; then
                printf 'dnf 代理注入失败（无法创建临时文件）: %s\n' "$f"
                return 1
            fi
            { grep -v '^proxy=' "$f" 2>/dev/null || true; } > "$tmpf"
            printf 'proxy=%s\n' "$proxy" >> "$tmpf"
            if cat "$tmpf" > "$f" 2>/dev/null; then
                rm -f "$tmpf" 2>/dev/null || true
                n=$(grep -c '^proxy=' "$f" 2>/dev/null || true)
                if [[ "${n:-0}" == "1" ]]; then
                    printf 'dnf 代理已注入: %s\n' "$f"
                    return 0
                fi
            else
                rm -f "$tmpf" 2>/dev/null || true
            fi
            printf 'dnf 代理注入失败（无权限？跳过）: %s\n' "$f"
            return 1
            ;;
        *)
            # apk/未知包管理器：只依赖 http_proxy 环境变量（alpine 的 apk 读该变量）
            return 0
            ;;
    esac
    return 0
}
