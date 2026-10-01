#!/usr/bin/env bash
# shellcheck shell=bash
# ============================================================
# RidgeRiceTalk 能力层 · lib/pkg.sh
# 逻辑依赖名 → 各发行版包名 的映射表 + 安装抽象
# ------------------------------------------------------------
# 设计依据：DES-2026-0912-03 §3.1（抽出「能力层」，P1 阶段，G-2）
#
# 主流程只写「逻辑依赖名」，不再直接写包管理器命令：
#     rrt_pkg_update
#     rrt_pkg_install postgresql ffmpeg jq curl ...
#
# ⚠️ 当前接通 apt / dnf 两个后端；apk 分支按 G-2 要求「明确报错并退出」，不做任何
#    安装动作 —— 在未验证的发行版上真的去装包会留下半装状态，比直接失败更糟。
#    dnf 后端（G-3，DES-2026-0912-03 P2）：Rocky/Alma/CentOS/Fedora 用 dnf，
#    无 dnf 的老版本（CentOS 7）回退 yum（两者 install/-y CLI 兼容，rrt_pkg_manager
#    已把 yum 归入 dnf 抽象）。apk 列是 **G-3/G-4 的目标映射（尚未实测）**。
#
# 用法： . "<脚本目录>/lib/pkg.sh"
# ============================================================

# 依赖 os.sh（rrt_pkg_manager / rrt_arch / 日志适配）。
# 使用**本文件自身**的路径定位，不依赖调用者的 cwd；重复 source 无副作用。
RRT_PKG_LIB_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
# shellcheck source=/dev/null
. "$RRT_PKG_LIB_DIR/os.sh"
unset RRT_PKG_LIB_DIR

# ============================================================
# 一、包名映射表
# ============================================================
# 列：逻辑依赖名 | apt（Debian/Ubuntu） | dnf（RHEL 系 / Fedora） | apk（Alpine）
# 说明：
#   - 逻辑名一律用脚本里出现的「Debian 侧习惯名」（build-essential / libopus-dev …），
#     它们是稳定的逻辑标识，不代表其它发行版也用这个包名；
#   - 一格可含多个包名，用**英文逗号**连接（如 gcc,gcc-c++,make）——解析按空白
#     分列（rrt_pkg_name 取第 N 列），格内空格会使列错位，故多包必须用逗号；
#   - 特殊值 `-` 表示「该发行版由其它包自带，无需单独安装」（跳过，不报错）；
#   - dnf 列实测范围为 RHEL9 系（Rocky/Alma/CentOS Stream 9）与 Fedora；
#     `epel-release` 供主流程在 RHEL 系预装 EPEL 仓库（opusfile-devel 等依赖）；
#     `nginx`/`certbot` 供 --tls 档使用（certbot 一格含 nginx 插件包）；
#   - apk 列尚未实测（见文件头说明）。
RRT_PKG_MAP='
# 逻辑依赖名          apt                  dnf                       apk
git                   git                  git                       git
postgresql            postgresql           postgresql-server         postgresql
postgresql-contrib    postgresql-contrib   postgresql-contrib        postgresql-contrib
ffmpeg                ffmpeg               ffmpeg                    ffmpeg
jq                    jq                   jq                        jq
curl                  curl                 curl                      curl
openssl               openssl              openssl                   openssl
rsync                 rsync                rsync                     rsync
ca-certificates       ca-certificates      ca-certificates           ca-certificates
wget                  wget                 wget                      wget
epel-release          -                    epel-release              -
nginx                 nginx                nginx                     nginx
certbot               certbot,python3-certbot-nginx  certbot,python3-certbot-nginx  certbot
build-essential       build-essential      gcc,gcc-c++,make          build-base
pkg-config            pkg-config           pkgconf-pkg-config        pkgconf
libopus-dev           libopus-dev          opus-devel                opus-dev
libopusfile-dev       libopusfile-dev      opusfile-devel            opusfile-dev
nodejs                nodejs               nodejs                    nodejs
unzip                 unzip                unzip                     unzip
'

# 单个逻辑名 → 包名（可能为空 = 该发行版无需安装）；未登记的逻辑名返回非 0
# 用法: rrt_pkg_name <apt|dnf|apk> <逻辑名>
rrt_pkg_name() {
    local pm="${1:-}" logical="${2:-}" col
    case "$pm" in
        apt) col=2 ;;
        dnf) col=3 ;;
        apk) col=4 ;;
        *)   return 1 ;;
    esac
    [[ -n "$logical" ]] || return 1
    printf '%s\n' "$RRT_PKG_MAP" | awk -v key="$logical" -v c="$col" '
        /^[[:space:]]*#/ { next }
        NF == 0        { next }
        $1 == key      { print $c; found = 1; exit }
        END            { if (!found) exit 1 }
    '
}

# 多个逻辑名 → 该发行版下的实际包名列表（空格分隔，去掉 "-" 占位并按首次出现顺序去重）
# 用法: rrt_pkg_names <apt|dnf|apk> <逻辑名...>
rrt_pkg_names() {
    local pm="${1:-}"
    shift || true
    [[ $# -gt 0 ]] || return 0
    local logical name seen out=""
    for logical in "$@"; do
        name="$(rrt_pkg_name "$pm" "$logical" || true)"
        if [[ -z "$name" ]]; then
            rrt_fail "逻辑依赖 '${logical}' 未登记在包名映射表中（包管理器: ${pm}）"
            rrt_info "请在 lib/pkg.sh 的 RRT_PKG_MAP 中补齐该依赖的三列包名"
            return 1
        fi
        # 格内多包名的逗号连接还原为空格分隔（见 RRT_PKG_MAP 说明）
        name="${name//,/ }"
        [[ "$name" == "-" ]] && continue   # 该发行版由其它包自带
        # 去重：同一发行版下多个逻辑名可能指向同一个包（不重复安装）
        for seen in $out; do
            [[ "$seen" == "$name" ]] && continue 2
        done
        out="${out:+$out }$name"
    done
    printf '%s\n' "$out"
}

# ============================================================
# 二、安装抽象
# ============================================================
# dnf 抽象的真实命令：dnf 优先；老 RHEL/CentOS 7 无 dnf 时回退 yum
# （两者在 install/-y/makecache 上 CLI 兼容）。
rrt__dnf_cmd() {
    if command -v dnf >/dev/null 2>&1; then
        printf 'dnf\n'
    elif command -v yum >/dev/null 2>&1; then
        printf 'yum\n'
    else
        return 1
    fi
}

# 更新包索引。apt 后端与改造前一致（apt-get update -qq）。
rrt_pkg_update() {
    case "$(rrt_pkg_manager)" in
        apt)
            apt-get update -qq
            ;;
        dnf)
            # dnf 在 install 时会自动刷新过期元数据；这里显式 makecache 与
            # apt-get update 的「先刷索引」语义对齐（失败即中止，与 apt 后端一致）。
            local cmd
            cmd="$(rrt__dnf_cmd)" || {
                rrt_fail "未找到 dnf/yum 命令，无法更新包索引（当前家族: $(rrt_os_family)）"
                exit 1
            }
            "$cmd" -q -y makecache
            ;;
        apk)
            rrt__pkg_unimplemented "$(rrt_pkg_manager)" "更新包索引"
            ;;
        *)
            rrt_fail "无法识别的包管理器，无法更新包索引：请确认系统为 Debian 系（apt）或 RHEL 系（dnf/yum）（当前家族: $(rrt_os_family)）"
            exit 1
            ;;
    esac
}

# 安装逻辑依赖（可传多个逻辑名）。
# 用法: rrt_pkg_install postgresql postgresql-contrib ffmpeg ...
# 注意：调用方自行控制输出重定向（与原脚本各调用点逐字一致）。
rrt_pkg_install() {
    [[ $# -gt 0 ]] || return 0
    case "$(rrt_pkg_manager)" in
        apt)
            rrt__pkg_install_apt "$@"
            ;;
        dnf)
            rrt__pkg_install_dnf "$@"
            ;;
        apk)
            rrt__pkg_unimplemented "$(rrt_pkg_manager)" "安装: $*"
            ;;
        *)
            rrt_fail "无法识别的包管理器，无法安装: $*（当前系统家族: $(rrt_os_family)）"
            rrt_info "本脚本当前支持 Debian 系（apt）与 RHEL 系（dnf/yum）；其它发行版的支持见 G-3/G-4"
            exit 1
            ;;
    esac
}

# apt 后端：与改造前完全一致的安装命令
#   改造前: DEBIAN_FRONTEND=noninteractive apt-get install -y -qq <包...>
rrt__pkg_install_apt() {
    local pkgs
    pkgs="$(rrt_pkg_names apt "$@")" || return 1
    [[ -n "$pkgs" ]] || return 0
    # shellcheck disable=SC2086  # 包名列表按空格分词后逐个作为参数（包名不含空格）
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq $pkgs
}

# dnf 后端（RHEL 系 / Fedora；无 dnf 时回退 yum，见 rrt__dnf_cmd）
rrt__pkg_install_dnf() {
    local pkgs cmd
    pkgs="$(rrt_pkg_names dnf "$@")" || return 1
    [[ -n "$pkgs" ]] || return 0
    cmd="$(rrt__dnf_cmd)" || {
        rrt_fail "未找到 dnf/yum 命令，无法安装: $*"
        return 1
    }
    # shellcheck disable=SC2086  # 包名列表按空格分词后逐个作为参数（包名不含空格）
    "$cmd" install -y $pkgs
}

# 未实现分支：只报错、不动系统
rrt__pkg_unimplemented() {
    local pm="${1:-}" what="${2:-}"
    rrt_fail "包管理器 ${pm} 的安装分支尚未实现（${what}）"
    rrt_info "该发行版支持见 G-3/G-4，尚未验证：本次不做任何安装动作，"
    rrt_info "以避免在未验证的发行版上留下半装状态；请改用 Debian 系或 RHEL 系，或等待 G-3/G-4"
    exit 1
}

# ============================================================
# 三、计划打印（不执行任何命令）
# ============================================================
# 用途：--dry-run 展示、以及「重构前后命令序列一致性」比对测试。
# 用法: rrt_pkg_plan <逻辑名...>
rrt_pkg_plan() {
    local pm pkgs
    pm="$(rrt_pkg_manager)"
    pkgs="$(rrt_pkg_names "$pm" "$@")" || return 1
    [[ -n "$pkgs" ]] || return 0
    case "$pm" in
        apt) printf 'DEBIAN_FRONTEND=noninteractive apt-get install -y -qq %s\n' "$pkgs" ;;
        dnf) printf 'dnf install -y %s\n' "$pkgs" ;;
        apk) printf 'apk add --no-cache %s\n' "$pkgs" ;;
        *)   return 1 ;;
    esac
}
