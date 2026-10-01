#!/usr/bin/env bash
# shellcheck shell=bash
# ============================================================
# RidgeRiceTalk 能力层 · lib/mirrors.sh
# 镜像预设数据表（G-5，DES-2026-0912-03 §8）
# ------------------------------------------------------------
# 职责：把散落在 deploy-baremetal.sh 里的下载镜像回退数组收敛到
# 一处数据表，并按 RRT_MIRROR_PRESET（cn | global | auto，默认 auto，
# 定义见 lib/net.sh rrt_mirror_preset）输出「按优先级排序的 URL 列表」。
#
# 语义：
#   auto  = 保持各下载点的既有回退顺序（历史行为，零变化）
#   cn    = 国内镜像优先，官方源兜底（仍保留回退，镜像失联不断粮）
#   global= 官方源优先（海外服务器）
#
# 边界（刻意不做，避免破坏性）：本层**不改** /etc/apt|dnf 的系统源配置——
# 系统源自动切换风险高（发行版格式变体多），cn 档只影响本脚本自身的
# 下载类请求（Go/Node/GitHub 克隆/npm）。系统源优化属后续 G-5b。
#
# 用法： . "<lib目录>/mirrors.sh"   （依赖 net.sh 的 preset 取值）
# ============================================================

RRT_MIRRORS_LIB_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"

# GitHub 克隆加速前缀列表（空串=直连）。cn 档镜像在前。
rrt_mirror_urls_github_clone() {
    if [[ "$(rrt_mirror_preset)" == "cn" ]]; then
        printf '%s\n' \
            "https://gh-proxy.com/" \
            "https://mirror.ghproxy.com/" \
            "https://ghproxy.com/" \
            ""
    else
        # auto / global：直连优先（历史行为）
        printf '%s\n' \
            "" \
            "https://ghproxy.com/" \
            "https://gh-proxy.com/" \
            "https://mirror.ghproxy.com/"
    fi
}

# Go 工具链 tarball 下载列表；$1=tarball 文件名
rrt_mirror_urls_go_tarball() {
    local tb="${1:-}"
    if [[ "$(rrt_mirror_preset)" == "global" ]]; then
        printf '%s\n' \
            "https://go.dev/dl/${tb}" \
            "https://golang.google.cn/dl/${tb}" \
            "https://mirrors.aliyun.com/golang/${tb}"
    else
        # auto / cn：国内优先（历史行为）
        printf '%s\n' \
            "https://golang.google.cn/dl/${tb}" \
            "https://mirrors.aliyun.com/golang/${tb}" \
            "https://go.dev/dl/${tb}"
    fi
}

# NodeSource 安装脚本列表
rrt_mirror_urls_node_setup() {
    if [[ "$(rrt_mirror_preset)" == "cn" ]]; then
        printf '%s\n' \
            "https://npmmirror.com/mirrors/node/setup_20.x" \
            "https://deb.nodesource.com/setup_20.x"
    else
        printf '%s\n' \
            "https://deb.nodesource.com/setup_20.x" \
            "https://npmmirror.com/mirrors/node/setup_20.x"
    fi
}

# Node 二进制 tarball 列表；$1=版本号（v20.18.1）$2=tarball 文件名
rrt_mirror_urls_node_bin() {
    local ver="${1:-}" tb="${2:-}"
    if [[ "$(rrt_mirror_preset)" == "global" ]]; then
        printf '%s\n' \
            "https://nodejs.org/dist/${ver}/${tb}" \
            "https://npmmirror.com/mirrors/node/${ver}/${tb}"
    else
        printf '%s\n' \
            "https://npmmirror.com/mirrors/node/${ver}/${tb}" \
            "https://nodejs.org/dist/${ver}/${tb}"
    fi
}

# npm registry 附加参数值（netease npm install 用）。
# 仅 cn 档返回 npmmirror（供 --registry 注入）；auto/global 返回空串
# （保持历史行为：npm 默认 registry）。调用方以 [[ -n ]] 判定是否追加参数。
rrt_mirror_npm_registry() {
    if [[ "$(rrt_mirror_preset)" == "cn" ]]; then
        printf '%s\n' "https://registry.npmmirror.com/"
    fi
}

# 统一入口：rrt_mirror_urls <kind> [args...]
# 输出：每行一个 URL（按 preset 排序）。未知 kind 返回 1。
rrt_mirror_urls() {
    local kind="${1:-}"; shift || true
    case "$kind" in
        github_clone) rrt_mirror_urls_github_clone ;;
        go_tarball)   rrt_mirror_urls_go_tarball "$@" ;;
        node_setup)   rrt_mirror_urls_node_setup ;;
        node_bin)     rrt_mirror_urls_node_bin "$@" ;;
        *)            return 1 ;;
    esac
}

# URL 可达性探测（HEAD，5s 超时）。0=可达 1=不可达。供调用方在
# cn 档首选失败时决定回退（列表循环本身已含回退，此函数用于
# 预检告警场景）。
rrt_mirror_probe() {
    local url="${1:-}"
    [[ -n "$url" ]] || return 1
    curl -sfI --max-time 5 "$url" >/dev/null 2>&1
}
