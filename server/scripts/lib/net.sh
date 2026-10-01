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
# 五、镜像预设（占位；P3「网络适配」才真正接入各下载点）
# ============================================================
# 现状：Go / Node / LiveKit / GitHub 加速 / npm 的镜像列表仍各自硬编码在
# deploy-baremetal.sh 里，本组函数只提供**统一取值入口**，P3 把它们挪到一处。
# 取值：cn（国内镜像优先）| global（直连上游）| auto（自动，默认，保持现状回退顺序）
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
