#!/usr/bin/env bash
# ============================================================
# G-7 离线安装包打包器（DES-2026-0912-03 §8【G-7】）
# ============================================================
# 目标：产出「一台无外网的 Linux 机器也能装起来」的自包含离线包。
#
# 产物结构（<out>/ridgetalk-offline-<版本>.tar.gz 解包后）：
#   ridgetalk-offline-<版本>/
#     ├── bundle/                 ← 部署树（server/ + migrations/ + livekit/ 三个顶层目录）
#     │    ├── server/            ← 源码+脚本+预编译二进制（ridgericetalk-server / ridgericetalk-migrate）+ webhost/dist
#     │    ├── migrations/        ← 版本化 SQL 迁移（服务端按 ../migrations 查找，H-2）
#     │    └── livekit/           ← 内置 LiveKit 二进制（deploy-embedded 归位到 third_party，H-2）
#     ├── offline-assets/         ← 外网资源：livekit-server、TTS 模型、EasyTier（可选）
#     ├── vendor/                 ← Go 依赖（--include-vendor 时；免去目标机 go mod download）
#     ├── install-offline.sh      ← 自包含安装器（离线跑；复用 deploy-embedded.sh）
#     ├── MANIFEST.txt            ← 内容清单（路径/大小/SHA256 前 12 位）
#     └── SHA256SUMS              ← 全包校验（安装时强制校验）
#   同目录另有 ridgetalk-offline-<版本>.tar.gz.sha256（外层整包校验）
#
# 用法：
#   bash scripts/build-offline-bundle.sh [选项]
#     --dry-run         只打印「将要打包的文件树/体积/排除项/校验计划」，不下载、不复制、不打包
#     --skip-download   不联网下载 offline-assets（仅打包已有文件；缺失项只在清单里标注）
#     --include-vendor  额外打包 go mod vendor 结果（目标机无需外网拉模块）
#     --with-tts-model  打包 TTS 模型（~70MB，默认不打；--skip-download 时需自备）
#     --allow-large     放行单个 >100MB 文件（默认拒绝：GitHub 单文件硬限制，发布仓库会拒收）
#     --use-existing-binaries
#                       跳过交叉编译，复用 server/ 下已有二进制（仍过全部门禁；仅供排障/离线构建）
#     -o <dir>          输出目录（默认当前目录）
#     --help            帮助
#
# 【发布门禁（2026-10-29，事故修复）】脚本自带 linux/amd64 交叉编译：
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-X …Server -X …Commit"
# 并在打包前后做硬校验：`file` 必须是「ELF 64-bit LSB executable, x86-64 … statically linked」，
# `go version -m` 必须 GOOS=linux / GOARCH=amd64 / vcs.modified=false 且 revision（或
# -ldflags Commit）等于当前 git HEAD；缺失二进制或任一断言失败 → exit 1，绝不产出 tar.gz。
# 背景：首批 1.9.0 离线包把 Windows PE / macOS Mach-O 打进 Linux 包，目标机无法启动。
#
# 与在线部署的关系：**不复制** deploy-embedded.sh 的任何逻辑；安装器只做
# 「校验 → 解包 → 转调 deploy-embedded.sh --skip-build --skip-frontend」，
# 因此 systemd/env 模板永远只有一份（避免两份漂移）。
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# 测试/排障缝：允许把仓库根与 server 根指到别处（只读用于打包，不写业务代码）
REPO_ROOT="${RRT_OFFLINE_REPO_ROOT:-$(cd "$SCRIPT_DIR/../.." && pwd)}"
SERVER_DIR="${RRT_OFFLINE_SERVER_DIR:-$REPO_ROOT/server}"
[[ -d "$SERVER_DIR" ]] || { echo "[offline] ❌ server 目录不存在: $SERVER_DIR" >&2; exit 1; }

OUT_DIR="."
SKIP_DL=0
DRY_RUN=0
INCLUDE_VENDOR=0
WITH_TTS=0
ALLOW_LARGE=0
USE_EXISTING_BINS=0

# 交叉编译/门禁用的可执行（可被环境变量替换，便于桩测试）
GO_BIN="${RRT_OFFLINE_GO_CMD:-go}"
FILE_CMD="${RRT_OFFLINE_FILE_CMD:-file}"
BIN_SERVER="$SERVER_DIR/ridgericetalk-server"
BIN_MIGRATE="$SERVER_DIR/ridgericetalk-migrate"

show_help() { sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run)         DRY_RUN=1; shift ;;
        --skip-download)   SKIP_DL=1; shift ;;
        --include-vendor)  INCLUDE_VENDOR=1; shift ;;
        --with-tts-model)  WITH_TTS=1; shift ;;
        --allow-large)     ALLOW_LARGE=1; shift ;;
        --use-existing-binaries) USE_EXISTING_BINS=1; shift ;;
        -o)                OUT_DIR="$2"; shift 2 ;;
        --help|-h)         show_help; exit 0 ;;
        *) echo "[offline] ❌ 未知参数: $1"; echo "用 --help 查看用法"; exit 2 ;;
    esac
done

VERSION=$(grep -m1 'var Server' "$SERVER_DIR/core/version/version.go" 2>/dev/null \
    | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)
VERSION="${VERSION:-dev}"
PKG_NAME="ridgetalk-offline-${VERSION}"
OUT_ARCHIVE="$OUT_DIR/${PKG_NAME}.tar.gz"

# 目标 git 版本：vcs.revision / -ldflags Commit 必须与之一致（仓库不可用回退 dev 并明示）
GIT_HEAD=$(git -C "$REPO_ROOT" rev-parse HEAD 2>/dev/null || true)
if [[ -z "$GIT_HEAD" ]]; then
    GIT_HEAD="dev"
    echo "[offline] ⚠ 无法读取 git HEAD（仓库不可用或非 git 目录）→ 版本签名回退 dev，门禁只校验平台不校验 revision" >&2
fi

# 体积门禁（GitHub 单文件 100MB 硬限制；50-100MB 会在发布仓库触发提示）
LARGE_HARD_BYTES=$((100 * 1024 * 1024))
LARGE_WARN_BYTES=$((50 * 1024 * 1024))

# 排除清单（打包树内）：运行时数据/依赖/日志/数据库/构建垃圾/本地二进制
# H-5（reviewer 20261029 审查）：离线包会随 GitHub Release 公开发布，密钥/证书/
# 备份类必须无条件排除 —— 旧清单只排了 5 个具体 .env 名，`.env.secret`、`*.pem`、
# `id_rsa*` 等会直接漏进公开产物。
EXCLUDES=(
    '.git' 'node_modules' 'storage' 'logs' '.data'
    '*.db' '*.db-*' '*.log' '*.pid'
    '*.exe' '*.exe~' '*.dll' '*.so.*' 'release' 'target'
    '__pycache__' '.venv' '.cache' '.DS_Store'
    # 密钥/证书/备份（H-5）
    '*.pem' '*.key' '*.crt' '*.p12' '*.pfx' '*.jks' 'id_rsa*' 'id_ed25519*'
    '*.sqlite*' '*.bak' '*.dump' 'server/*.sql'
    # 真实环境文件（P2-3 reviewer 20261029：补 `.env.*` 通配，避免 `.env.staging`
    # 这类未列举的名字漏进公开包；`.env.example` 位于仓库根，不在打包顶层内）
    '.env' '.env.*' '.env.local' '.env.production' '.env.development' '.env.test' '.env.secret'
    # 本地构建垃圾（非交付二进制）：开发用 build/、旧命名产物、临时测试二进制
    'build' 'browser_test_*' 'ridge-talk-server*' 'tts-worker'
    # server/ 根下的历史编译产物（只保留交付用的 server/ridgericetalk-server
    # 与 server/ridgericetalk-migrate —— 后者是离线机唯一可用的迁移器，H-4）
    'server/migrate' 'server/ridgericetalk' 'server/ridgericetalk-linux*'
    'server/ridgericetalk-test*' 'server/ridgericetalk-server-*' 'server/ridgericetalk_*' 'server/ridgericetalk_new'
)
# server/ 下的重型目录：模型/预置二进制走 offline-assets，不入 bundle
SERVER_HEAVY_DIRS=('models' 'third_party')
# 打包的顶层目录（H-2：缺 migrations/ 则离线机无法迁移，缺 livekit/ 则拿不到 LiveKit）
BUNDLE_TOPS=('server' 'migrations' 'livekit')

fail() { echo "[offline] ❌ $*" >&2; exit 1; }
ok()   { echo "[offline] ✅ $*"; }
say()  { echo "[offline] $*"; }

build_tar_excludes() { # 输出 tar --exclude 参数序列
    local e
    for e in "${EXCLUDES[@]}"; do printf '%s\0' "--exclude=$e"; done
    local d
    for d in "${SERVER_HEAVY_DIRS[@]}"; do printf '%s\0' "--exclude=server/$d"; done
}

# ---------- 体积扫描：返回 0=通过 ----------
scan_sizes() { # scan_sizes <root>  （dry-run 与实打包共用同一口径）
    local root="$1" big=0 warn=0 path size
    while IFS= read -r -d '' path; do
        size=$(wc -c < "$path" 2>/dev/null || echo 0)
        if (( size > LARGE_HARD_BYTES )); then
            if (( ALLOW_LARGE )); then
                say "⚠ 放行大文件（>100MB，GitHub 会拒收）: ${path#"$root"/} ($(( size / 1024 / 1024 ))MB)"
            else
                echo "[offline] ❌ 单文件超过 100MB: ${path#"$root"/} ($(( size / 1024 / 1024 ))MB)" >&2
                big=1
            fi
        elif (( size > LARGE_WARN_BYTES )); then
            say "⚠ 大文件（50-100MB，发布仓库会提示）: ${path#"$root"/} ($(( size / 1024 / 1024 ))MB)"
            warn=$((warn + 1))
        fi
    done < <(find "$root" -type f -print0 2>/dev/null)
    (( warn > 0 )) && say "共 $warn 个 50-100MB 文件（上传前请确认必要性）"
    return "$big"
}

list_bundle_files() { # 枚举 bundle 将包含的文件（排除清单生效）
    local root="$1"
    tar_args=()
    while IFS= read -r -d '' a; do tar_args+=("$a"); done < <(build_tar_excludes)
    tar -C "$root" "${tar_args[@]}" -cf - "${BUNDLE_TOPS[@]}" 2>/dev/null | tar -tf - 2>/dev/null | sed 's#^#bundle/#'
}

# ============================================================
# 1) 解析离线资产清单（下载或复用；dry-run 不下载）
# ============================================================
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  LK_ARCH="amd64";  ET_ARCH="x86_64"  ;;
    aarch64) LK_ARCH="arm64";  ET_ARCH="aarch64" ;;
    *)       LK_ARCH="amd64";  ET_ARCH="x86_64"  ;;
esac
LK_VER=$(grep -m1 'LIVEKIT_VERSION=' "$SCRIPT_DIR/deploy-baremetal.sh" 2>/dev/null | grep -oE 'v[0-9.]+' | head -1 || true)
LK_VER="${LK_VER:-v1.13.4}"
ET_VER=$(grep -m1 'EASYTIER_VERSION=' "$SCRIPT_DIR/deploy-baremetal.sh" 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)
ET_VER="${ET_VER:-2.6.0}"

ASSET_SPECS=(
    "livekit:livekit-server-linux-${LK_ARCH}:https://github.com/livekit/livekit/releases/download/${LK_VER}/livekit-server-linux-${LK_ARCH}"
    "easytier:easytier-linux-${ET_ARCH}-${ET_VER}.zip:https://github.com/EasyTier/EasyTier/releases/download/${ET_VER}/easytier-linux-${ET_ARCH}-${ET_VER}.zip"
)
(( WITH_TTS )) && ASSET_SPECS+=(
    "tts:vits-melo-tts-zh_en.tar.bz2:https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-melo-tts-zh_en.tar.bz2"
)

# ============================================================
# ---------- 共用自检：敏感项 / 关键内容（P2-1：不合格必须 fail-closed） ----------
# 入参：一个「每行一个相对路径」的列表文件；返回值 = 不合格类别数（敏感）/ 缺失项数（关键内容）
selfcheck_files() { # $1=list-file
    local list="$1" rc=0 label pat n env_real
    chk() {
        local label="$1" pat="$2" n
        n=$(grep -cE "$pat" "$list" || true)
        printf '  %-22s %s\n' "$label" "$n 个"
        if (( n > 0 )); then
            printf '    例子: %s\n' "$(grep -E "$pat" "$list" | head -3 | tr '\n' ' ')"
            rc=$((rc + 1))
        fi
    }
    chk "数据库文件" '\.db(-|$)'
    chk "SQLite 附属文件" '\.sqlite(-|$)|\.sqlite3?-(wal|shm)$'
    chk "node_modules" '(^|/)node_modules(/|$)'
    chk "运行时数据 storage/.data" '(^|/)(storage|\.data|logs)(/|$)'
    chk "日志文件" '\.log$'
    chk "Windows 产物 *.exe/*.dll" '\.(exe|dll)(~)?$'
    chk "密钥/证书类" '\.(pem|key|crt|p12|pfx|jks)$|(^|/)id_(rsa|ed25519)[^/]*$'
    chk "备份类 *.bak/*.dump" '\.(bak|dump)$'
    # 真实 env：排除 .env.example 模板后再计数（H-5 / P2-1）
    env_real=$(grep -E '(^|/)\.env(\.[^/]*)?$' "$list" | grep -vE '(^|/)\.env\.example$' | wc -l | tr -d ' ' || true)
    printf '  %-22s %s\n' "真实 env（不含 .example）" "$env_real 个"
    if (( env_real > 0 )); then
        printf '    例子: %s\n' "$(grep -E '(^|/)\.env(\.[^/]*)?$' "$list" | grep -vE '(^|/)\.env\.example$' | head -3 | tr '\n' ' ')"
        rc=$((rc + 1))
    fi
    return "$rc"
}

needcheck_files() { # $1=list-file；返回值 = 缺失关键项数
    local list="$1" rc=0 label pat
    need() {
        local label="$1" pat="$2"
        if grep -qE "$pat" "$list"; then
            printf '  ✅ %s\n' "$label"
        else
            printf '  ❌ %s\n' "$label"
            rc=$((rc + 1))
        fi
    }
    need "migrations/ 版本化迁移（000044）" '^bundle/migrations/000044_.*\.up\.sql$'
    need "webhost 管理后台产物（H-3）" '^bundle/server/webhost/dist/admin/index\.html$'
    need "webhost 网页端产物（H-3）" '^bundle/server/webhost/dist/voice/index\.html$'
    need "LiveKit 内置二进制（H-2）" '^bundle/livekit/bin/linux-[^/]+/livekit-server$'
    need "迁移器 server/ridgericetalk-migrate（H-4）" '^bundle/server/ridgericetalk-migrate$'
    need "主服务二进制 server/ridgericetalk-server（H-1）" '^bundle/server/ridgericetalk-server$'
    return "$rc"
}

# sha256 跨平台（sha256sum / shasum / 无则 unavailable）；MANIFEST 与门禁共用
sha_of() { # $1=文件
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
    else echo "unavailable"; fi
}

# ---------- admin 双路径产物一致性门禁（rc1 事故防复发）----------
# 事故：admin 重建只落 webhost/dist/admin（入库预构建），没落 server/webhost/dist/admin
# （运行时与离线包真正消费后者）→ rc1 包内是 7-02 旧 admin，且该旧构建含 admin123。
# 语义：两路径 index.html 必须都存在且 sha256 一致；缺失/不一致 → 非 0，禁止打包。
# 本门禁**只校验不自动同步**（不自动 cp，保持构建语义不变）。
ADMIN_DIST_PATHS=('webhost/dist/admin/index.html' 'server/webhost/dist/admin/index.html')
admin_dist_issues() { # $1=仓库根（便于测试注入临时目录）；返回值 = 不合格路径数（0=通过）
    local root="${1:-$REPO_ROOT}" rel a b ha hb rc=0
    for rel in "${ADMIN_DIST_PATHS[@]}"; do
        if [[ ! -f "$root/$rel" ]]; then
            printf '  ❌ 缺失 %s\n' "$root/$rel"
            rc=$((rc + 1))
        fi
    done
    if (( rc > 0 )); then
        printf '  修复：cd web/admin && npm run build（重建 admin），随后把 webhost/dist/admin 同步到 server/webhost/dist/admin\n'
        return "$rc"
    fi
    a="$root/${ADMIN_DIST_PATHS[0]}"
    b="$root/${ADMIN_DIST_PATHS[1]}"
    ha=$(sha_of "$a"); hb=$(sha_of "$b")
    if [[ "$ha" != "$hb" ]]; then
        printf '  ❌ admin 双路径产物不一致（离线包消费 server/webhost/dist/admin）\n'
        printf '     %s  %s\n' "$ha" "${ADMIN_DIST_PATHS[0]}"
        printf '     %s  %s\n' "$hb" "${ADMIN_DIST_PATHS[1]}"
        printf '  修复：cd web/admin && npm run build，然后 cp -a webhost/dist/admin/. server/webhost/dist/admin/\n'
        return 1
    fi
    printf '  ✅ admin 双路径一致（sha256 %s…）\n' "$(printf '%s' "$ha" | cut -c1-12)"
    return 0
}

# ---------- 发布门禁：linux/amd64 交叉编译 + 二进制平台/vcs 硬校验 ----------
# 事故背景：首批离线包直接取本机 Windows 侧预编译件，把 PE/Mach-O 打进 Linux 包。
BIN_NAMES=('ridgericetalk-server' 'ridgericetalk-migrate')

build_linux_binaries() {
    if ! command -v "$GO_BIN" >/dev/null 2>&1; then
        fail "缺少 go 工具链（需要交叉编译 linux/amd64；排障时可用 --use-existing-binaries 复用已编好的二进制）"
    fi
    local ldflags="-X ridgericetalk/core/version.Server=$VERSION"
    if [[ "$GIT_HEAD" != "dev" ]]; then
        ldflags="$ldflags -X ridgericetalk/core/version.Commit=$GIT_HEAD"
    fi
    say "交叉编译 linux/amd64：cmd/server → $BIN_SERVER"
    ( cd "$SERVER_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO_BIN" build -trimpath \
        -ldflags "$ldflags" -o "$BIN_SERVER" ./cmd/server ) \
        || fail "交叉编译 cmd/server 失败（linux/amd64）"
    say "交叉编译 linux/amd64：cmd/migrate → $BIN_MIGRATE"
    ( cd "$SERVER_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO_BIN" build -trimpath \
        -ldflags "$ldflags" -o "$BIN_MIGRATE" ./cmd/migrate ) \
        || fail "交叉编译 cmd/migrate 失败（linux/amd64）"
    ok "交叉编译完成（$VERSION / commit ${GIT_HEAD:0:12}）"
}

# $1=目录；逐个断言「存在 + ELF 64-bit x86-64 + statically linked」，返回不合格个数
bin_platform_issues() {
    local dir="$1" rc=0 name f desc
    for name in "${BIN_NAMES[@]}"; do
        f="$dir/$name"
        if [[ ! -f "$f" ]]; then
            echo "  ❌ 缺失 $name（$dir）"
            rc=$((rc + 1))
            continue
        fi
        desc=$("$FILE_CMD" -b "$f" 2>/dev/null || true)
        if [[ "$desc" == *"ELF 64-bit LSB executable, x86-64"* && "$desc" == *"statically linked"* ]]; then
            printf '  ✅ %s: ELF 64-bit LSB executable, x86-64, statically linked\n' "$name"
        else
            printf '  ❌ %s 平台不合格（需 linux/amd64 静态 ELF）: %s\n' "$name" "${desc:-<file 无法识别>}"
            rc=$((rc + 1))
        fi
    done
    return "$rc"
}

# $1=目录；断言 GOOS=linux / GOARCH=amd64 / vcs.modified=false /
# revision（无则 -ldflags Commit）== GIT_HEAD；返回不合格个数
bin_vcs_issues() {
    local dir="$1" rc=0 name f meta rev ldcommit modified bad
    for name in "${BIN_NAMES[@]}"; do
        f="$dir/$name"
        [[ -f "$f" ]] || { rc=$((rc + 1)); continue; }
        meta=$("$GO_BIN" version -m "$f" 2>/dev/null || true)
        if [[ -z "$meta" ]]; then
            printf '  ❌ %s 读不到 Go build info（go version -m 无输出）\n' "$name"
            rc=$((rc + 1))
            continue
        fi
        rev=$(printf '%s\n' "$meta" | sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.revision=//p' | head -1)
        modified=$(printf '%s\n' "$meta" | sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.modified=//p' | head -1)
        ldcommit=""
        if [[ "$GIT_HEAD" != "dev" ]]; then
            ldcommit=$(printf '%s\n' "$meta" | grep -oE "ridgericetalk/core/version\.Commit=$GIT_HEAD" | head -1 || true)
        fi
        bad=0
        if ! printf '%s\n' "$meta" | grep -q '^[[:space:]]*build[[:space:]]*GOOS=linux$'; then
            echo "  ❌ $name GOOS 不是 linux"; bad=1
        fi
        if ! printf '%s\n' "$meta" | grep -q '^[[:space:]]*build[[:space:]]*GOARCH=amd64$'; then
            echo "  ❌ $name GOARCH 不是 amd64"; bad=1
        fi
        if [[ -n "$modified" && "$modified" != "false" ]]; then
            echo "  ❌ $name vcs.modified=$modified（工作区有未提交改动，请先提交再打包）"; bad=1
        fi
        if [[ "$GIT_HEAD" != "dev" ]]; then
            if [[ -n "$rev" ]]; then
                [[ "$rev" == "$GIT_HEAD" ]] || { echo "  ❌ $name vcs.revision=$rev ≠ HEAD=${GIT_HEAD:0:12}"; bad=1; }
            elif [[ -z "$ldcommit" ]]; then
                echo "  ❌ $name 既无 vcs.revision 也无 -ldflags Commit=HEAD（无法证明来自当前提交）"; bad=1
            fi
        fi
        if (( bad == 0 )); then
            printf '  ✅ %s: GOOS=linux GOARCH=amd64%s%s\n' "$name" \
                "${rev:+ vcs.revision=${rev:0:12}}" "${modified:+ vcs.modified=$modified}"
        else
            rc=$((rc + 1))
        fi
    done
    return "$rc"
}

# 目录级门禁：缺失/平台/vcs 任一不合格 → fail（绝不产出 tar.gz）
gate_binaries() { # $1=目录 $2=场景描述
    local dir="$1" stage="$2" p_fail=0 v_fail=0
    say "二进制门禁（${stage}）：linux/amd64 静态 ELF + vcs 信息"
    bin_platform_issues "$dir" || p_fail=$?
    bin_vcs_issues "$dir" || v_fail=$?
    if (( p_fail > 0 || v_fail > 0 )); then
        fail "二进制门禁失败（${stage}）：平台/缺失不合格 ${p_fail} 个、vcs 信息不合格 ${v_fail} 个 → 不打包"
    fi
    ok "二进制门禁通过（${stage}）"
}

# 2) dry-run：只打清单，不落盘
# ============================================================
if (( DRY_RUN )); then
    say "DRY-RUN（不下载 / 不复制 / 不打包）"
    say "版本: $VERSION   架构: $ARCH（livekit=$LK_ARCH, easytier=$ET_ARCH）"
    say "输出: $OUT_ARCHIVE"
    say "选项: skip-download=$SKIP_DL include-vendor=$INCLUDE_VENDOR with-tts-model=$WITH_TTS allow-large=$ALLOW_LARGE"
    echo
    say "── bundle/（部署树，已应用排除清单）──"
    files=$(list_bundle_files "$REPO_ROOT" || true)
    if [[ -z "$files" ]]; then
        echo "[offline] ❌ 枚举 bundle 文件失败（tar 不可用或 server/ 缺失）" >&2
        exit 1
    fi
    total=$(printf '%s\n' "$files" | wc -l | tr -d ' ')
    say "文件数: $total"
    printf '%s\n' "$files" | sed 's#^bundle/server/#  server/#;s#^bundle/#  #' | head -40
    (( total > 40 )) && say "  …（其余 $((total - 40)) 个文件省略）"
    echo
    say "── 排除清单（命中项不进包）──"
    printf '  %s\n' "${EXCLUDES[@]}" "server/models" "server/third_party"
    echo
    say "── offline-assets/ ──"
    for spec in "${ASSET_SPECS[@]}"; do
        IFS=':' read -r kind name url <<< "$spec"
        if [[ -f "$SERVER_DIR/third_party/$name" || -f "$SCRIPT_DIR/../offline-assets/$name" ]]; then
            say "  复用已有: $name"
        elif (( SKIP_DL )); then
            say "  跳过（--skip-download）: $name"
        else
            say "  将下载: $name ← $url"
        fi
    done
    echo
    say "── 敏感/垃圾自检（离线包绝不含密钥/库/依赖/日志）──"
    _tmp_list=$(mktemp)
    printf '%s\n' "$files" > "$_tmp_list"
    _dg_fail=0; selfcheck_files "$_tmp_list" || _dg_fail=$?
    echo
    say "── 关键内容自检（H-2/H-3/H-4：缺任一项离线安装必失败）──"
    _need_fail=0; needcheck_files "$_tmp_list" || _need_fail=$?
    echo "  交付二进制："
    printf '%s\n' "$files" | grep -E 'bundle/server/ridgericetalk-(server|migrate)$' | sed 's#^bundle/#    ✅ #' || echo "    ⚠ 缺少交付二进制（目标机需 Go 工具链，离线不可用）"
    junk=$(printf '%s\n' "$files" | grep -E 'server/ridgericetalk' | grep -vcE 'server/ridgericetalk-(server|migrate)$' || true)
    printf '  %-22s %s\n' "其它本地产物" "$junk 个（应为 0，只保留交付二进制）"
    echo
    say "── 校验计划 ──"
    say "  包内 SHA256SUMS：所有 bundle/ + offline-assets/ 文件（安装时强制校验）"
    say "  外层校验：${PKG_NAME}.tar.gz.sha256"
    say "  MANIFEST.txt：路径 / 大小 / sha256 前 12 位"
    echo
    say "── 二进制构建与门禁计划（发布门禁，dry-run 不执行）──"
    if (( USE_EXISTING_BINS )); then
        say "  构建: 跳过（--use-existing-binaries，复用 server/ 下已有二进制）"
    else
        say "  构建: CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags \"-X ridgericetalk/core/version.Server=$VERSION -X ridgericetalk/core/version.Commit=${GIT_HEAD}\" -o server/{ridgericetalk-server,ridgericetalk-migrate} ./cmd/{server,migrate}"
    fi
    say "  门禁 1: file 含 'ELF 64-bit LSB executable, x86-64' 且 'statically linked'"
    say "  门禁 2: go version -m → GOOS=linux / GOARCH=amd64 / vcs.modified=false / vcs.revision=$GIT_HEAD"
    say "  缺件或任一不合格 → exit 1，不产出 tar.gz（打包后对 bundle 内副本再验一次）"
    echo
    say "── admin 双路径产物一致性（门禁 3：rc1 事故防复发，dry-run 仅提示不阻断）──"
    admin_dist_issues "$REPO_ROOT" || say "  ⚠ 真实打包会因上述不一致/缺失而中止"
    echo
    say "体积扫描（100MB 硬门禁 / 50MB 提示）："
    _big=0
    for _top in "${BUNDLE_TOPS[@]}"; do
        [[ -e "$REPO_ROOT/$_top" ]] || continue
        if scan_sizes "$REPO_ROOT/$_top" >/dev/null 2>&1; then
            say "  ✅ $_top/"
        else
            say "  ❌ $_top/ 存在 >100MB 文件，实际打包会失败（可 --allow-large 放行）"
            _big=1
        fi
    done
    (( _big == 0 )) || true
    rm -f "$_tmp_list"
    if (( _dg_fail > 0 || _need_fail > 0 || junk > 0 )); then
        echo "[offline] ❌ dry-run 自检失败：敏感项 ${_dg_fail} 类、缺失关键项 ${_need_fail} 项、本地产物 ${junk} 个" >&2
        echo "[offline]    （真实打包会在同一处中止，不会产出离线包）" >&2
        exit 1
    fi
    say "DRY-RUN 结束（未产生任何文件）"
    exit 0
fi

# ============================================================
# 3) 真实打包
# ============================================================
command -v tar >/dev/null 2>&1 || fail "缺少 tar"
mkdir -p "$OUT_DIR"

STAGE=$(mktemp -d)
cleanup() { rm -rf "$STAGE" 2>/dev/null || true; }
trap cleanup EXIT
PKG_ROOT="$STAGE/$PKG_NAME"
mkdir -p "$PKG_ROOT/bundle" "$PKG_ROOT/offline-assets"

say "版本 $VERSION，架构 $ARCH，输出 $OUT_ARCHIVE"

# 3.0 交叉编译 + 打包前二进制门禁（发布门禁：绝不让 PE/Mach-O 混入 Linux 离线包）
if (( USE_EXISTING_BINS )); then
    say "复用已有二进制（--use-existing-binaries），跳过交叉编译（仍过全部门禁）"
else
    build_linux_binaries
fi
gate_binaries "$SERVER_DIR" "打包前"
# admin 双路径产物一致性（打包前门禁；rc1 事故防复发：两条路径长期双份无校验）
admin_dist_issues "$REPO_ROOT" || fail "admin 双路径产物不一致/缺失 → 禁止打包（修复提示见上）"

# 3.1 bundle：server/ + migrations/ + livekit/（应用排除清单）
say "打包部署树（bundle/）…"
tar_args=()
while IFS= read -r -d '' a; do tar_args+=("$a"); done < <(build_tar_excludes)
tar -C "$REPO_ROOT" "${tar_args[@]}" -cf - "${BUNDLE_TOPS[@]}" | tar -C "$PKG_ROOT/bundle" -xf -
[[ -d "$PKG_ROOT/bundle/server" ]] || fail "bundle/server 未生成"
# H-2：缺 migrations/ 则离线机无法迁移（服务端按 ../migrations 查找）；缺 livekit/ 则
# 部署脚本只能 warn「将尝试联网下载」，离线机等于没有 LiveKit。
[[ -d "$PKG_ROOT/bundle/migrations" ]] || fail "bundle/migrations 未生成（离线机将无法执行数据库迁移）"
if [[ -d "$PKG_ROOT/bundle/livekit" ]]; then
    ok "含 livekit/ 内置二进制：$(find "$PKG_ROOT/bundle/livekit" -name 'livekit-server' -type f | wc -l | tr -d ' ') 个"
else
    say "⚠ 未发现 livekit/ 目录（离线机的 LiveKit 需另备）"
fi
ok "bundle/ 就绪（$(find "$PKG_ROOT/bundle" -type f | wc -l | tr -d ' ') 个文件）"
# H-4：迁移器必须随包交付（离线机无 Go 工具链，deploy-embedded 的「就地补编译」必败）
# 缺件不再是 warn：缺任何交付二进制都 fail（发布门禁，2026-10-29 事故修复）
for _bin in "$PKG_ROOT/bundle/server/ridgericetalk-server" "$PKG_ROOT/bundle/server/ridgericetalk-migrate"; do
    [[ -f "$_bin" ]] || fail "离线包缺交付二进制：$(basename "$_bin")（先交叉编译或提供预编译件）"
    chmod 755 "$_bin" 2>/dev/null || true
    ok "含预编译二进制 $(basename "$_bin")（已置 0755）"
done
# 打包后门禁：对 bundle 内副本再验一次平台/vcs（防止复制过程被换包或排除规则误伤）
gate_binaries "$PKG_ROOT/bundle/server" "打包后"

# 3.2 offline-assets
say "准备离线资产（offline-assets/）…"
fetch() { # fetch <name> <url>
    local name="$1" url="$2" dst="$PKG_ROOT/offline-assets/$name"
    if [[ -f "$dst" ]]; then ok "已有 $name"; return 0; fi
    if [[ -f "$SERVER_DIR/third_party/$name" ]]; then
        cp "$SERVER_DIR/third_party/$name" "$dst"; ok "复用 server/third_party/$name"; return 0
    fi
    if (( SKIP_DL )); then say "⏭ 跳过下载 $name（--skip-download）"; return 0; fi
    local u
    for u in "https://ghproxy.net/$url" "$url"; do
        say "下载 $name ← $u"
        if curl -fSL --connect-timeout 15 -o "$dst" "$u" 2>/dev/null; then ok "$name"; return 0; fi
    done
    say "⚠ 下载失败（安装时可回退网络或手工补齐）: $url"
    return 0
}
for spec in "${ASSET_SPECS[@]}"; do
    IFS=':' read -r _kind name url <<< "$spec"
    fetch "$name" "$url"
done

# 3.3 vendor（可选）
if (( INCLUDE_VENDOR )); then
    if command -v go >/dev/null 2>&1 && [[ -f "$SERVER_DIR/go.mod" ]]; then
        say "生成 go module vendor（vendor/）…"
        ( cd "$SERVER_DIR" && GOFLAGS=-mod=mod go mod vendor ) >/dev/null 2>&1 \
            && cp -a "$SERVER_DIR/vendor" "$PKG_ROOT/vendor" && ok "vendor/ 就绪"
        [[ -d "$PKG_ROOT/vendor" ]] || say "⚠ vendor 生成失败（目标机需外网或预置模块缓存）"
    else
        say "⚠ 无 go 命令或 go.mod，跳过 --include-vendor"
    fi
fi

# 3.4 自包含安装器（与在线脚本共享模板，只做校验+解包+转调）
cp "$SCRIPT_DIR/install-offline.sh" "$PKG_ROOT/install-offline.sh"
chmod +x "$PKG_ROOT/install-offline.sh"

# 3.4b 打包前自检（P2-1：敏感项/关键内容不合格直接中止，绝不产出可发布包）
say "打包前自检（敏感/关键内容，fail-closed）…"
_pre_list=$(mktemp)
( cd "$PKG_ROOT" && find . -type f ! -name SHA256SUMS ! -name MANIFEST.txt | sed 's#^\./##' | sort ) > "$_pre_list"
_pre_sens=0; selfcheck_files "$_pre_list" || _pre_sens=$?
_pre_need=0; needcheck_files "$_pre_list" || _pre_need=$?
rm -f "$_pre_list"
if (( _pre_sens > 0 || _pre_need > 0 )); then
    fail "打包前自检失败：敏感项 ${_pre_sens} 类、缺失关键项 ${_pre_need} 项（见上）"
fi
ok "打包前自检通过（敏感项 0、关键内容齐全）"

# 3.5 体积门禁
if ! scan_sizes "$PKG_ROOT" 2>&1 | sed 's/^/  /'; then
    fail "存在 >100MB 文件（见上）；如确需署名可加 --allow-large"
fi

# 3.6 MANIFEST + SHA256SUMS（包内相对路径）
say "生成 MANIFEST.txt / SHA256SUMS …（sha_of 已在上方门禁段定义）"
: > "$PKG_ROOT/MANIFEST.txt"
echo "# ridge-talk 离线包清单（版本 $VERSION / 架构 $ARCH / 生成 $(date '+%Y-%m-%d %H:%M:%S')）" >> "$PKG_ROOT/MANIFEST.txt"
echo "# 格式：<sha256前12位>  <字节数>  <相对路径>" >> "$PKG_ROOT/MANIFEST.txt"
( cd "$PKG_ROOT" && find . -type f ! -name MANIFEST.txt ! -name SHA256SUMS -print0 | sort -z | while IFS= read -r -d '' f; do
    printf '%s  %10s  %s\n' "$(sha_of "$f" | cut -c1-12)" "$(wc -c < "$f" | tr -d ' ')" "${f#./}"
done >> MANIFEST.txt )

if command -v sha256sum >/dev/null 2>&1; then
    ( cd "$PKG_ROOT" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS )
else
    ( cd "$PKG_ROOT" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 shasum -a 256 > SHA256SUMS )
fi
ok "校验文件就绪（$(wc -l < "$PKG_ROOT/SHA256SUMS" | tr -d ' ') 行）"

# 3.7 打 tar.gz + 外层校验
say "打包 $OUT_ARCHIVE …"
tar -C "$STAGE" -czf "$OUT_ARCHIVE" "$PKG_NAME"
OUT_SHA=$(sha_of "$OUT_ARCHIVE")
printf '%s  %s\n' "$OUT_SHA" "$(basename "$OUT_ARCHIVE")" > "$OUT_ARCHIVE.sha256"
ok "离线包: $OUT_ARCHIVE（$(wc -c < "$OUT_ARCHIVE" | tr -d ' ') 字节）"
ok "sha256: $OUT_SHA"
echo
say "安装（目标机，解包后）："
say "  tar -xzf $(basename "$OUT_ARCHIVE") && cd $PKG_NAME"
say "  sudo ./install-offline.sh --deploy-dir /opt/ridgericetalk"
say "  # 或先演练：sudo ./install-offline.sh --dry-run"
