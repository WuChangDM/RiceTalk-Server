#!/usr/bin/env bash
# shellcheck shell=bash
# ============================================================
# RidgeRiceTalk 能力层 · lib/os.sh
# 发行版 / 包管理器 / init 系统 / 架构 探测
# ------------------------------------------------------------
# 设计依据：DES-2026-0912-03 §3.1「抽出能力层」（P1 阶段，G-2）
#
# 约定（三个 lib 共同遵守）：
#   1. 本层只做**只读探测**，绝不修改系统状态；
#   2. 探测函数把**规范化的值**打印到 stdout，调用方用
#      v="$(rrt_xxx)" 取用；探测失败返回非 0（调用方可用 || true 忽略）；
#   3. 不依赖调用者的 cwd —— 只依赖被 source 时的路径；
#   4. 可重复 source（幂等：仅重复定义函数，不使用 readonly 变量）。
#
# 用法： . "<脚本目录>/lib/os.sh"
# ============================================================

# os-release 路径可覆盖（供测试注入桩数据；生产环境勿改）
RRT_OS_RELEASE_FILE="${RRT_OS_RELEASE_FILE:-/etc/os-release}"

# ============================================================
# 一、os-release 解析（带缓存）
# ============================================================
rrt_os_release_available() { [[ -r "$RRT_OS_RELEASE_FILE" ]]; }

# 取 os-release 中某个键的值（去掉两侧引号）；取不到返回非 0
rrt__os_release_get() {
    local key="${1:-}" line val
    [[ -n "$key" ]] || return 1
    [[ -r "$RRT_OS_RELEASE_FILE" ]] || return 1
    # 注意用 "^KEY=" 精确匹配，避免 ID 命中 ID_LIKE
    line="$(grep -m1 -E "^${key}=" "$RRT_OS_RELEASE_FILE" 2>/dev/null || true)"
    [[ -n "$line" ]] || return 1
    val="${line#*=}"
    val="${val%\"}"; val="${val#\"}"
    val="${val%\'}"; val="${val#\'}"
    printf '%s' "$val"
}

# 首次调用时把常用的几个键读进缓存，避免重复读文件 / 重复 grep
rrt__os_load() {
    [[ "${RRT_OS_LOADED:-}" == "1" ]] && return 0
    RRT_OS_LOADED=1
    RRT_OS__ID="$(rrt__os_release_get ID || true)"
    RRT_OS__VERSION="$(rrt__os_release_get VERSION_ID || true)"
    RRT_OS__ID_LIKE="$(rrt__os_release_get ID_LIKE || true)"
    RRT_OS__PRETTY="$(rrt__os_release_get PRETTY_NAME || true)"
    [[ -n "$RRT_OS__PRETTY" ]] || RRT_OS__PRETTY="$RRT_OS__ID"
    return 0
}

# 发行版 ID（小写），如 debian / ubuntu / rocky / alpine；取不到时输出 unknown
rrt_os_id() {
    rrt__os_load
    printf '%s\n' "${RRT_OS__ID:-unknown}"
}

# 发行版版本号，如 12 / 22.04 / 9.4；取不到时输出空串
rrt_os_version() {
    rrt__os_load
    printf '%s\n' "${RRT_OS__VERSION:-}"
}

# ID_LIKE（如 ubuntu → debian；rocky → rhel centos fedora）；取不到时输出空串
rrt_os_id_like() {
    rrt__os_load
    printf '%s\n' "${RRT_OS__ID_LIKE:-}"
}

# PRETTY_NAME（如 "Debian GNU/Linux 12 (bookworm)"）
rrt_os_pretty_name() {
    rrt__os_load
    printf '%s\n' "${RRT_OS__PRETTY:-}"
}

# ============================================================
# 二、发行版家族
# ============================================================
# 规范化家族：debian | rhel | alpine | suse | unknown
# 说明：家族只用于「能力归类」，是否放行仍由调用方的闸门决定
#       （deploy-baremetal.sh 的闸门在 G-3 批次放宽为 Debian 系 + RHEL 系）。
rrt_os_family() {
    local id like
    id="$(rrt_os_id)"
    like="$(rrt_os_id_like)"
    case "$id" in
        debian|ubuntu|deepin|uos|kylin|linuxmint|pop|kali|raspbian|elementary|zorin|neon|devuan|mx)
            printf 'debian\n' ; return 0 ;;
        rhel|centos|rocky|almalinux|alma|fedora|ol|oracle|amzn|scientific|virtuozzo)
            printf 'rhel'   ; return 0 ;;
        alpine)
            printf 'alpine\n' ; return 0 ;;
        opensuse*|sles|sled|suse)
            printf 'suse'   ; return 0 ;;
    esac
    # 兜底：用 ID_LIKE 归类（Deepin/麒麟等衍生版常只有 ID_LIKE）
    case " ${like} " in
        *" debian "*|*" ubuntu "*) printf 'debian\n' ; return 0 ;;
        *" rhel "*|*" centos "*|*" fedora "*) printf 'rhel\n' ; return 0 ;;
        *" alpine "*) printf 'alpine\n' ; return 0 ;;
        *" suse "*|*" opensuse "*) printf 'suse\n' ; return 0 ;;
    esac
    printf 'unknown\n'
}

# Debian 系判定（0=是）。供 G-3 放宽闸门时使用（主脚本闸门已放宽为 Debian 系 + RHEL 系）。
rrt_os_is_debian_family() { [[ "$(rrt_os_family)" == "debian" ]]; }

# RHEL 系判定（0=是）。供 G-3 放宽闸门、EPEL 预装与 SELinux 分支使用。
rrt_os_is_rhel_family() { [[ "$(rrt_os_family)" == "rhel" ]]; }

# ============================================================
# 二·五、语义版本比较（纯函数）
# ============================================================
# 判定 $1 >= $2（sort -V 口径，与主脚本 Go 版本比较一致）。
# 任一参数为空返回假 —— 调用方（如发行版闸门）须对「VERSION_ID 取不到」
# 自行决定兜底策略，本函数不替调用方猜测。
rrt_version_ge() {
    [[ -n "$1" && -n "$2" ]] || return 1
    [[ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -1)" == "$2" ]]
}

# ============================================================
# 三、包管理器探测
# ============================================================
# 规范化取值：apt | dnf | apk | unknown
#
# 以「实际存在的二进制」为准（能力探测），而不是只看发行版 ID：
#   - Debian 系衍生（Deepin/UOS/麒麟/Mint…）ID 各异但都有 apt-get；
#   - RHEL 系老版本只有 yum（是 dnf 的软链/前身），统一归入 dnf 抽象；
#   - 容器 / 精简镜像可能既无 ID 也无包管理器 → unknown（调用方应明确报错）。
rrt_pkg_manager() {
    if command -v apt-get >/dev/null 2>&1; then
        printf 'apt\n'
    elif command -v dnf >/dev/null 2>&1; then
        printf 'dnf\n'
    elif command -v apk >/dev/null 2>&1; then
        printf 'apk\n'
    elif command -v yum >/dev/null 2>&1; then
        printf 'dnf\n'   # 老 RHEL/CentOS 7：yum 即 dnf 的前身，归同一抽象
    else
        printf 'unknown\n'
    fi
}

# ============================================================
# 四、init 系统探测
# ============================================================
# 规范化取值：systemd | openrc | none
# 判定顺序：systemd 必须同时满足「有 systemctl」且「/run/systemd/system 存在」
# （容器 / WSL 里装了 systemctl 但 PID 1 不是 systemd 的假阳性很常见）。
rrt_init_system() {
    if [[ -d /run/systemd/system ]] && command -v systemctl >/dev/null 2>&1; then
        printf 'systemd\n'
    elif [[ -d /run/openrc ]] || command -v rc-service >/dev/null 2>&1; then
        printf 'openrc\n'
    else
        printf 'none\n'
    fi
}

# 是否由 systemd 托管（0=是）。供主流程做「有/无 systemd」分级降级时使用。
rrt_has_systemd() { [[ "$(rrt_init_system)" == "systemd" ]]; }

# ============================================================
# 五、架构探测
# ============================================================
# 规范化取值：Debian 命名（amd64 / arm64 / …），与下载产物命名一致。
#
# ⚠️ dpkg 安全兜底（P0 止血修复，禁止退回直接调用 dpkg）：
# 部分发行版（RHEL 系 / Alpine 等）没有 dpkg，若直接执行
# `dpkg --print-architecture`，会在 set -euo pipefail 下立刻以
# "command not found" 退出，导致发行版闸门的友好提示永远打不出来。
# 因此：先探测 dpkg 是否存在，缺失时回退 uname -m → Debian 架构名映射。
rrt_arch() {
    local arch=""
    if command -v dpkg >/dev/null 2>&1; then
        arch="$(dpkg --print-architecture 2>/dev/null || true)"
    fi
    if [[ -z "$arch" ]]; then
        case "$(uname -m 2>/dev/null || echo unknown)" in
            x86_64|amd64)  arch="amd64" ;;
            aarch64|arm64) arch="arm64" ;;
            *)             arch="$(uname -m 2>/dev/null || echo unknown)" ;;
        esac
    fi
    printf '%s\n' "$arch"
}

# ============================================================
# 六、环境报告（一行摘要，供 --dry-run / 部署日志 / 后续「环境报告」用）
# ============================================================
rrt_os_report() {
    printf 'os=%s %s (%s) family=%s pkg=%s init=%s arch=%s' \
        "$(rrt_os_id)" "$(rrt_os_version)" "$(rrt_os_pretty_name)" \
        "$(rrt_os_family)" "$(rrt_pkg_manager)" "$(rrt_init_system)" "$(rrt_arch)"
}

# ============================================================
# 七、SELinux 适配（D6：RHEL 系 enforcing 下自定义二进制/端口/HOME 会被 AVC 拒绝）
# ============================================================
# ⚠️ 本文件头约定「只做只读探测」——rrt_selinux_adapt 是**唯一的例外**：
#    它是主流程显式调用的修改性动作，且自带两重守卫：
#      1) 仅 getenforce = Enforcing 时才动手（Permissive/Disabled/无 SELinux 零动作）；
#      2) 调用方定义了 dry_run 函数时（--dry-run）只打印计划、不执行。
#    放在本文件而非 lib/pkg.sh：它跟「系统是否启用 SELinux」这一 OS 能力强相关，
#    且不查包名映射表（缺工具时的安装按需探测 rrt_pkg_install，不构成加载期依赖）。

# SELinux 当前模式：enforcing | permissive | disabled | none（无 getenforce/未装 SELinux）
rrt_selinux_mode() {
    if ! command -v getenforce >/dev/null 2>&1; then
        printf 'none\n'; return 0
    fi
    case "$(getenforce 2>/dev/null | tr '[:upper:]' '[:lower:]')" in
        enforcing)  printf 'enforcing\n' ;;
        permissive) printf 'permissive\n' ;;
        *)          printf 'disabled\n' ;;
    esac
}

# 内部：dry-run 适配 —— 调用方（主脚本）定义了 dry_run 时打印「将执行」并透传其返回值；
# 未定义（lib 独立 source / 单测）时返回 1，等价于「非 dry-run、照常执行」。
rrt__selinux_dry_run() {
    if declare -F dry_run >/dev/null 2>&1; then
        dry_run "$@"
    else
        return 1
    fi
}

# RHEL 系 SELinux enforcing 适配；非 enforcing 零动作；失败不致命（返回 1，由调用方告警）。
# 用法: rrt_selinux_adapt <server_dir> <livekit_dir> <服务用户HOME> <LiveKit_TCP端口> <LiveKit_UDP端口>
# 动作清单（均幂等，可重复执行）：
#   ① 部署目录二进制 → bin_t：/opt 下自定义二进制默认 usr_t，enforcing 下受限域
#      执行/内存映射可能被 AVC 拒绝，逐个 semanage fcontext + restorecon；
#   ② LiveKit 媒体端口 → http_port_t（LiveKit 官方 self-hosting 的常见做法）：
#      enforcing 下自定义端口默认 unreserved_port_t，服务 bind 会被 AVC 拒绝；
#   ③ 服务用户 HOME（/var/lib/ridgericetalk）→ user_home_t 语义放行。
rrt_selinux_adapt() {
    local server_dir="${1:-}" lk_dir="${2:-}" svc_home="${3:-}" lk_tcp="${4:-7881}" lk_udp="${5:-7882}"
    local bin_path port_spec proto port

    [[ "$(rrt_selinux_mode)" == "enforcing" ]] || return 0
    rrt_info "SELinux 为 Enforcing：为 RidgeRiceTalk 部署配置文件/端口标签（D6）"

    # 前置：semanage（由 policycoreutils-python-utils 提供）。缺则经能力层安装抽象装上。
    if ! command -v semanage >/dev/null 2>&1; then
        if declare -F rrt_pkg_install >/dev/null 2>&1; then
            # dry-run 下只打印安装计划，不 return：后续动作各自打印各自的计划
            if ! rrt__selinux_dry_run "安装 policycoreutils-python-utils（semanage/restorecon 前置）"; then
                rrt_info "安装 policycoreutils-python-utils（SELinux 管理工具）..."
                rrt_pkg_install policycoreutils-python-utils >/dev/null 2>&1 || {
                    rrt_warn "policycoreutils-python-utils 安装失败：SELinux 适配跳过"
                    rrt_info "请手动安装后重跑: dnf install -y policycoreutils-python-utils"
                    return 1
                }
            fi
        else
            rrt_warn "缺少 semanage（policycoreutils-python-utils）且能力层安装抽象不可用：SELinux 适配跳过"
            rrt_info "请手动安装: dnf install -y policycoreutils-python-utils"
            return 1
        fi
    fi

    # ① 部署目录二进制 → bin_t（文件不存在时跳过；fcontext 已有记录时 -a 报错，|| true 保持幂等）
    for bin_path in \
        "$server_dir/ridgericetalk" \
        "$server_dir/ridgericetalk-migrate" \
        "$server_dir/tts-worker" \
        "$lk_dir/livekit-server"; do
        [[ -e "$bin_path" ]] || continue
        if rrt__selinux_dry_run "semanage fcontext -a -t bin_t $bin_path && restorecon -v $bin_path（部署二进制 SELinux 标签）"; then
            continue
        fi
        semanage fcontext -a -t bin_t "$bin_path" 2>/dev/null || true
        restorecon -v "$bin_path" 2>/dev/null || true
    done

    # ② LiveKit 媒体端口 → http_port_t（已在列表中的端口跳过；被定义为其它类型时提示不中断）
    for port_spec in "tcp $lk_tcp" "udp $lk_udp"; do
        proto="${port_spec%% *}"
        port="${port_spec#* }"
        [[ -n "$port" ]] || continue
        if semanage port -l 2>/dev/null | grep -Eq "^http_port_t[[:space:]]+${proto}[[:space:]].*\b${port}\b"; then
            rrt_info "端口 $port/$proto 已在 http_port_t 中，跳过"
            continue
        fi
        if rrt__selinux_dry_run "semanage port -a -t http_port_t -p $proto $port（LiveKit 媒体端口 SELinux 放行）"; then
            continue
        fi
        if ! semanage port -a -t http_port_t -p "$proto" "$port" 2>/dev/null; then
            rrt_warn "semanage port -a -t http_port_t -p $proto $port 失败（可能已被定义为其它类型）"
            rrt_info "确认无冲突后可强制调整: semanage port -m -t http_port_t -p $proto $port"
        fi
    done

    # ③ 服务用户 HOME 在 /var/lib 下（如 /var/lib/ridgericetalk）→ user_home_t 放行
    if [[ -d "$svc_home" ]]; then
        if rrt__selinux_dry_run "semanage fcontext -a -t user_home_t '$svc_home(/.*)?' && restorecon -R $svc_home（服务用户 HOME 标签）"; then
            return 0
        fi
        semanage fcontext -a -t user_home_t "${svc_home}(/.*)?" 2>/dev/null || true
        restorecon -R "$svc_home" 2>/dev/null || true
    fi

    rrt_info "SELinux 适配完成（如服务仍启动失败，排查: ausearch -m avc -ts recent）"
    return 0
}

# ============================================================
# 八、日志适配（能力层共用）
# ============================================================
# 库文件自己不认识主脚本的彩色输出函数，但又不能假设主脚本一定存在
# （lib 需要能独立 source / 单测）。这里做一层适配：
#   - 主脚本已定义 info/warn/fail → 直接复用（输出与改造前逐字一致）；
#   - 否则退回纯文本前缀，便于单测与排错。
rrt_info() { if declare -F info >/dev/null 2>&1; then info "$@"; else echo "  [INFO] $*"; fi; }
rrt_warn() { if declare -F warn >/dev/null 2>&1; then warn "$@"; else echo "  [WARN] $*"; fi; }
rrt_fail() { if declare -F fail >/dev/null 2>&1; then fail "$@"; else echo "  [FAIL] $*"; fi; }
