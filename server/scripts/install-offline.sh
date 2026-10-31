#!/usr/bin/env bash
# ============================================================
# G-7 离线安装器（配合 build-offline-bundle.sh 产出的离线包）
# ============================================================
# 设计原则：**不复制**在线部署脚本的任何逻辑。
#   install-offline.sh 只做四件事：① 校验 ② 解包 ③ 转调 deploy-embedded.sh
#   （--skip-build --skip-frontend）④ 安装后静态资源自检（fail-closed）。
#   因此 systemd unit / .env 模板永远只有一份，在线与离线安装不会出现
#   「两份脚本各改各的」漂移。
#
# 用法（两种，都在离线包里可直接跑）：
#   # A. 从 tar.gz 安装（推荐；强制校验外层 sha256 + 包内 SHA256SUMS）
#   ./install-offline.sh --bundle ridgetalk-offline-0.4.0.tar.gz --deploy-dir /opt/ridgericetalk
#   # B. 已手工解包，直接在当前目录安装
#   ./install-offline.sh --deploy-dir /opt/ridgericetalk
#
# 选项：
#   --bundle <tar.gz>     指定离线包（会先解到临时目录再安装）
#   --from <dir>          指定已解包目录（默认为本脚本所在目录）
#   --deploy-dir <path>   部署目录（默认 /opt/ridgericetalk，透传 deploy-embedded.sh）
#   --port-api/--port-admin/--port-lk-ws <p>  端口透传
#   --public-address <url> / --nat / --public-lk-url <ws>  网络参数透传（见 deploy-embedded.sh --help）
#   --force               透传（允许覆盖已有 .env.production）
#   --allow-embedded-deps-download
#                         透传：离线安装默认写入 RRT_EMBEDDED_DEPS=false（不在启动期
#                         下载依赖）。仅在确认目标机可出网时才加本开关让服务端下载，
#                         否则无网机器会卡在下载、健康检查超时（task-49）。
#   --skip-checksum       跳过【包内】SHA256SUMS 校验（仅排障用，不推荐）
#   --no-outer-checksum   跳过【外层】整包 sha256 校验（包旁缺 .sha256 时用；不安全，不推荐）
#   --no-network          离线模式（默认）：跳过需要外网的预检步骤，仅打印需预置的系统包清单。
#                         注意：它**不启防火墙隔离、也不阻断出网流量**，只影响本脚本的预检路径；
#                         真正的断网请在目标机上自行隔离网络。同时导出 RRT_OFFLINE=true，
#                         让 deploy-embedded.sh 在缺内置 LiveKit 等离线必备件时直接失败。
#   --dry-run             只打印将执行的步骤与目标机预检清单，不写任何文件
#   --skip-static-check   跳过安装后静态资源自检（仅排障用，默认开启）
#                         自检内容：<部署目录>/server/webhost/dist/admin/index.html 必须存在，
#                         并用 curl 打 /admin/ 确认非 404（task-48：离线包布局缺陷曾导致 admin 全 404）
#   --help
# ============================================================
set -euo pipefail

BUNDLE=""
SRC_DIR=""
DEPLOY_DIR="/opt/ridgericetalk"
PORT_API=""
PORT_ADMIN=""
SKIP_CHECKSUM=0
SKIP_OUTER_CHECKSUM=0
SKIP_STATIC_CHECK=0
DRY_RUN=0
NO_NETWORK=1
PASSTHROUGH=()

show_help() { sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --bundle)          BUNDLE="$2"; shift 2 ;;
        --from)            SRC_DIR="$2"; shift 2 ;;
        --deploy-dir)      DEPLOY_DIR="$2"; PASSTHROUGH+=(--deploy-dir "$2"); shift 2 ;;
        --port-api)        PORT_API="$2"; PASSTHROUGH+=(--port-api "$2"); shift 2 ;;
        --port-admin)      PORT_ADMIN="$2"; PASSTHROUGH+=(--port-admin "$2"); shift 2 ;;
        --port-lk-ws|--public-address|--public-admin-address|--public-lk-url|--mirror-preset|--http-proxy|--https-proxy)
                           PASSTHROUGH+=("$1" "$2"); shift 2 ;;
        --nat|--system-proxy|--enable-turn|--allow-embedded-deps-download)
                           PASSTHROUGH+=("$1"); shift ;;
        --force)           PASSTHROUGH+=(--force); shift ;;
        --skip-checksum)   SKIP_CHECKSUM=1; shift ;;
        --no-outer-checksum) SKIP_OUTER_CHECKSUM=1; shift ;;
        --skip-static-check) SKIP_STATIC_CHECK=1; shift ;;
        --no-network)      NO_NETWORK=1; shift ;;
        --dry-run)         DRY_RUN=1; shift ;;
        --help|-h)         show_help; exit 0 ;;
        *) echo "[install-offline] ❌ 未知参数: $1" >&2; exit 2 ;;
    esac
done

say()  { echo "[install-offline] $*"; }
ok()   { echo "[install-offline] ✅ $*"; }
fail() { echo "[install-offline] ❌ $*" >&2; exit 1; }

sha_of() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
    else
        # 缺 hash 工具时若返回占位串，后续 `want == got` 会假通过 —— 必须硬失败（L-4）。
        echo "[install-offline] ❌ 缺少 sha256sum/shasum，无法校验离线包完整性（请先安装 coreutils）" >&2
        return 1
    fi
}

# 解包（若给了 --bundle）：先校验外层 sha256，再解到临时目录，然后自我转调
if [[ -n "$BUNDLE" ]]; then
    [[ -f "$BUNDLE" ]] || fail "找不到离线包: $BUNDLE"
    if [[ -f "$BUNDLE.sha256" ]]; then
        want=$(awk '{print $1}' "$BUNDLE.sha256")
        got=$(sha_of "$BUNDLE")
        [[ "$want" == "$got" ]] || fail "外层 sha256 不匹配（want=$want got=$got）"
        ok "外层 sha256 校验通过"
    elif [[ "$SKIP_OUTER_CHECKSUM" == "1" ]]; then
        say "⚠ 已按 --no-outer-checksum 跳过外层校验（无法证明整包未被篡改/截断）"
    else
        # M-2：旧行为是「缺 .sha256 就静默跳过」，等于整包校验默认 fail-open。
        fail "未找到 $BUNDLE.sha256（整包校验缺失）。确认来源可信时可显式加 --no-outer-checksum 跳过"
    fi
    # P3（reviewer/coordinator 20261029）：解包前校验成员列表，拒绝绝对路径与 `..` 越界，
    # 并要求所有条目落在同一顶层目录内 —— 恶意包无法写出目标目录。
    # 该检查只读（tar -tzf），因此放在 DRY-RUN 分支之前也能保持「dry-run 不写文件」承诺。
    say "校验包成员（拒绝绝对路径/.. 越界）…"
    _members=$(tar -tzf "$BUNDLE") || fail "无法读取包成员列表（包损坏或不是 tar.gz）"
    _bad=$(printf '%s\n' "$_members" | grep -nE '^/|(^|/)\.\.(/|$)' | head -1 || true)
    if [[ -n "$_bad" ]]; then
        fail "离线包含非法成员（绝对路径或 .. 越界），已拒绝解包：$(printf '%s' "$_bad" | cut -c1-160)"
    fi
    _tops=$(printf '%s\n' "$_members" | sed -e 's#^\./##' | awk -F/ 'NF > 0 && $1 != "" { print $1 }' | sort -u)
    _ntops=$(printf '%s\n' "$_tops" | grep -c . || true)
    if [[ "$_ntops" != "1" ]]; then
        fail "离线包结构异常：期望单一顶层目录，实际 ${_ntops} 个（$(printf '%s' "$_tops" | tr '\n' ' ')）"
    fi
    _top=$(printf '%s\n' "$_tops")
    _outside=$(printf '%s\n' "$_members" | grep -vcE "^(\./)?${_top}(/|$)" || true)
    if [[ "$_outside" != "0" ]]; then
        fail "离线包含越界成员（不在 $_top/ 下），已拒绝解包：$(printf '%s\n' "$_members" | grep -vE "^(\./)?${_top}(/|$)" | head -1)"
    fi
    ok "包成员校验通过（顶层 $_top/，$(printf '%s\n' "$_members" | wc -l | tr -d ' ') 个成员）"

    if [[ "$DRY_RUN" == "1" ]]; then
        say "DRY-RUN：将解包 $BUNDLE 到临时目录后执行包内 install-offline.sh（成员已校验）"
        exit 0
    fi
    TMP=$(mktemp -d)
    cleanup_tmp() { rm -rf "$TMP" 2>/dev/null || true; }
    trap cleanup_tmp EXIT
    say "解包到临时目录…"
    tar -xzf "$BUNDLE" -C "$TMP"
    # 解包后再兜一层：所有条目必须落在 $TMP 内（防符号链接逃逸写入）
    _escaped=$(find "$TMP" -type l 2>/dev/null | while IFS= read -r _lnk; do
        _dest=$(readlink -f "$_lnk" 2>/dev/null || true)
        [[ -n "$_dest" && "$_dest" != "$TMP"/* ]] && printf '%s\n' "$_lnk" || true
    done | head -1 || true)
    [[ -z "$_escaped" ]] || fail "离线包含指向临时目录之外的符号链接，已拒绝安装：$_escaped"
    inner=$(find "$TMP" -maxdepth 1 -mindepth 1 -type d | head -1)
    [[ -n "$inner" ]] || fail "离线包结构异常（无顶层目录）"
    # M-6：这里不能用 exec —— exec 会替换掉当前进程，EXIT trap 永不触发，
    # 每次安装都会在 /tmp 残留 30MB+ 的解包目录（内含二进制）。改为子进程 + 显式清理。
    rc=0
    bash "$inner/install-offline.sh" --from "$inner" "${PASSTHROUGH[@]}" || rc=$?
    cleanup_tmp
    trap - EXIT
    exit "$rc"
fi

# 定位包根
[[ -n "$SRC_DIR" ]] || SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
[[ -d "$SRC_DIR" ]] || fail "目录不存在: $SRC_DIR"
BUNDLE_TREE="$SRC_DIR/bundle/server"
[[ -d "$BUNDLE_TREE" ]] || fail "包结构异常：缺少 bundle/server（$BUNDLE_TREE）"

# 离线模式：向 deploy-embedded.sh 传递「无外网可用」的事实（M-1 的可执行版本）
export RRT_OFFLINE=true

say "包根: $SRC_DIR"
say "部署树: $BUNDLE_TREE"
say "目标目录: $DEPLOY_DIR"
say "离线模式: $([[ "$NO_NETWORK" == "1" ]] && echo 是 || echo 否)"
if [[ "$NO_NETWORK" == "1" ]]; then
    say "提示：--no-network 只跳过联网预检，不启防火墙隔离、不阻断出网；如需真断网请在目标机自行隔离"
fi

# ---------- 目标机预检（离线模式只打印，不联网） ----------
say "── 目标机预检 ──"
miss=0
for cmd in tar systemctl; do
    if command -v "$cmd" >/dev/null 2>&1; then ok "命令可用: $cmd"; else say "⚠ 缺少命令: $cmd"; miss=$((miss + 1)); fi
done
# H-1/H-2：离线包应自带主二进制、迁移器、迁移 SQL 与 LiveKit
if [[ -f "$BUNDLE_TREE/ridgericetalk-server" ]]; then
    ok "预编译二进制存在: bundle/server/ridgericetalk-server"
elif [[ -f "$BUNDLE_TREE/ridgericetalk" ]]; then
    ok "预编译二进制存在: bundle/server/ridgericetalk"
else
    say "⚠ bundle 内无预编译二进制 → 目标机需 Go 工具链（1.25+）自行构建，离线场景请重新打包时提供二进制"
fi
if [[ -f "$BUNDLE_TREE/ridgericetalk-migrate" ]]; then
    ok "迁移器存在: bundle/server/ridgericetalk-migrate"
else
    say "⚠ bundle 内无迁移器（ridgericetalk-migrate）→ 数据库迁移将失败，请重新打包"
    miss=$((miss + 1))
fi
if [[ -d "$SRC_DIR/bundle/migrations" ]]; then
    ok "迁移 SQL 存在: bundle/migrations（$(find "$SRC_DIR/bundle/migrations" -name '*.up.sql' | wc -l | tr -d ' ') 个 up 迁移）"
else
    say "⚠ bundle 内无 migrations/ → 离线机无法执行版本化迁移"
    miss=$((miss + 1))
fi
if [[ -d "$SRC_DIR/bundle/livekit" ]]; then
    ok "内置 LiveKit 存在: bundle/livekit"
else
    say "⚠ bundle 内无 livekit/ → 离线机拿不到 LiveKit（RRT_OFFLINE=true 下 deploy-embedded 会直接失败）"
fi
[[ -d "$SRC_DIR/offline-assets" ]] && ok "offline-assets: $(ls -1 "$SRC_DIR/offline-assets" 2>/dev/null | wc -l | tr -d ' ') 个文件" || say "⚠ 无 offline-assets（LiveKit/TTS 需另备）"
say "需预置的系统包（离线机不会自动 apt/dnf install）：curl tar xz-utils ca-certificates nginx (可选) tzdata"
(( miss > 0 )) && say "⚠ 有 $miss 项缺失，请先在目标机离线安装"

# ---------- 校验包内容 ----------
if [[ "$SKIP_CHECKSUM" == "1" ]]; then
    say "⏭ 已按 --skip-checksum 跳过包内校验（不推荐）"
else
    [[ -f "$SRC_DIR/SHA256SUMS" ]] || fail "缺少 SHA256SUMS（离线包不完整）"
    say "校验包内文件（$(wc -l < "$SRC_DIR/SHA256SUMS" | tr -d ' ') 个）…"
    if command -v sha256sum >/dev/null 2>&1; then
        ( cd "$SRC_DIR" && sha256sum -c SHA256SUMS --quiet ) || fail "包内容校验失败（可能传输损坏或被篡改）"
    else
        say "⚠ 无 sha256sum，改用 shasum 逐行校验"
        ( cd "$SRC_DIR" && awk '{print $1"  "$2}' SHA256SUMS | shasum -a 256 -c - --quiet ) || fail "包内容校验失败"
    fi
    ok "包内容校验通过"
fi

DEPLOY_SCRIPT="$BUNDLE_TREE/scripts/deploy-embedded.sh"
[[ -f "$DEPLOY_SCRIPT" ]] || fail "包内缺少 deploy-embedded.sh（$DEPLOY_SCRIPT）"

# 权限自愈候选：在 Windows 上打包时 chmod 不落盘（NTFS/git-bash），tar 内可能记录
# 0644 → Linux 解包后二进制不可执行，安装时统一补 +x（幂等）。
# P2-2（reviewer 20261029）：自愈会写文件元数据，必须在 DRY-RUN 分支之后执行，
# 否则违背「dry-run 未写任何文件」的承诺。
HEAL_BINS=("$BUNDLE_TREE/ridgericetalk-server" "$BUNDLE_TREE/ridgericetalk-migrate")

if [[ "$DRY_RUN" == "1" ]]; then
    echo
    say "DRY-RUN：将执行"
    say "  RRT_OFFLINE=true bash $DEPLOY_SCRIPT --skip-build --skip-frontend ${PASSTHROUGH[*]:-}"
    say "（--skip-build：用包内预编译二进制 server/ridgericetalk-server；--skip-frontend：用包内 webhost/dist）"
    say "（deploy-embedded.sh 会按 ridgericetalk → ridgericetalk-server 顺序探测主二进制，H-1）"
    for _bin in "${HEAL_BINS[@]}"; do
        if [[ -f "$_bin" && ! -x "$_bin" ]]; then
            say "（dry-run 未执行）将补可执行位: $(basename "$_bin")"
        fi
    done
    say "DRY-RUN 结束（未写任何文件）"
    say "（安装后将自检静态资源：<部署目录>/server/webhost/dist/admin/index.html 存在 + curl /admin/ 非 404，失败 fail-closed）"
    exit 0
fi

# 真实安装路径：补 +x 后必须校验，不能「chmod || true 就当成功」
# （reviewer 在 NTFS 实测：chmod 打印成功但文件仍 644）。
for _bin in "${HEAL_BINS[@]}"; do
    [[ -f "$_bin" ]] || continue
    if [[ ! -x "$_bin" ]]; then
        chmod +x "$_bin" 2>/dev/null || true
    fi
    if [[ ! -x "$_bin" ]]; then
        fail "无法为 $(basename "$_bin") 设置可执行位（目标文件系统可能 noexec 挂载或权限不足）: $_bin"
    fi
    ok "二进制可执行: $(basename "$_bin")"
done

# ---------- 安装后静态资源自检（fail-closed） ----------
# 背景（task-48，2026-10-29 演练机实测）：离线包只含 bundle/server|migrations|livekit，
# 不含 services/netease-api/package.json 标记 → config.ServerDir() 兜底到 exe 目录，
# 使 webhostDir("admin") 多一层 server，导致 admin UI 全 404（API 正常）。
# 布局已在 paths.go 修复（42d245f：新增 <d>/server/webhost/dist 判据），此处再兜一层，
# 让「装了但打不开后台」在安装期暴露，而不是等用户发现。
# 例外：--skip-static-check（仅排障用，需在报告中说明）。
check_static_assets() {
    local admin_index="$DEPLOY_DIR/server/webhost/dist/admin/index.html"
    # 注意：admin UI 由 Admin 端口提供（不是 API 端口）；默认与 deploy-embedded 的 9090 对齐
    local admin_port="${PORT_ADMIN:-9090}"
    local url="http://127.0.0.1:${admin_port}/admin/"
    say "── 安装后静态资源自检 ──"
    if [[ ! -f "$admin_index" ]]; then
        fail "安装目录缺少 admin 静态资源：$admin_index（离线包布局异常；如确认不需要交互页面可加 --skip-static-check 跳过）"
    fi
    ok "admin 静态资源就位: $admin_index"
    if ! command -v curl >/dev/null 2>&1; then
        say "⚠ 无 curl，跳过 HTTP 探针（静态文件已在位）"
        return 0
    fi
    local code="000" i
    for i in $(seq 1 20); do
        code=$(curl -sS -o /dev/null -m 3 -w '%{http_code}' "$url" 2>/dev/null || echo 000)
        case "$code" in
            200|204|301|302|304|308) break ;;
        esac
        sleep 1
    done
    case "$code" in
        200|204|301|302|304|308) ok "HTTP 探针通过: GET $url → $code" ;;
        000) say "⚠ HTTP 探针未连通（$url：端口与 --port-admin 不一致或服务尚未起）；静态文件已在位，请按实际端口复核" ;;
        404) fail "HTTP 探针 404：GET $url —— 离线包布局缺陷（task-48）；确认无碍后可用 --skip-static-check 跳过" ;;
        *)   say "⚠ HTTP 探针返回 $code（非 404，记录后继续）" ;;
    esac
}

# ---------- 转调在线部署脚本（共用同一套模板） ----------
say "转调 deploy-embedded.sh（复用 systemd/.env 模板）…"
# M-6 风格：不能用 exec（替换进程后无法做安装后自检），改为子进程 + 判定 rc
rc=0
bash "$DEPLOY_SCRIPT" --skip-build --skip-frontend "${PASSTHROUGH[@]}" || rc=$?
if [[ "$rc" -ne 0 ]]; then
    say "❌ deploy-embedded.sh 失败（rc=$rc）"
    exit "$rc"
fi
if [[ "$SKIP_STATIC_CHECK" == "1" ]]; then
    say "⏭ 已按 --skip-static-check 跳过安装后静态资源自检（排障用，需在报告中说明）"
else
    check_static_assets
fi

# ---------- 安装后守卫：LiveKit 默认密钥（task-96） ----------
# 离线安装常见于「装一次用很久」的现场，默认密钥（devkey/devsecret）一旦对外开放
# 就是公开可知的凭据。这里只读 .env.production 并告警 + 打印轮换命令，不自动改
# （改密钥需要重启服务端/LiveKit，不应由安装脚本静默完成）。
LK_HELPER="$(dirname "$DEPLOY_SCRIPT")/lib/livekit-keys.sh"
LK_ENV_FILE="$DEPLOY_DIR/server/.env.production"
if [[ -f "$LK_HELPER" && -f "$LK_ENV_FILE" ]]; then
    # shellcheck source=lib/livekit-keys.sh
    source "$LK_HELPER"
    say "── LiveKit API 密钥自检（task-96）──"
    LK_RC=0
    lk_ensure_env_credentials "$LK_ENV_FILE" "" "${PORT_ADMIN:-9090}" || LK_RC=$?
    if [[ "$LK_RC" == "2" ]]; then
        say "⚠ 检测到默认 LiveKit 密钥：安装已完成，但请先按上面命令轮换，再对外开放语音"
    fi
fi

exit 0
