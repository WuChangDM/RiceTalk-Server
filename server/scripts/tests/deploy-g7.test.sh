#!/usr/bin/env bash
# ============================================================
# G-7 离线安装包桩测试（无需 root / 无需 Linux / 不联网）
# ============================================================
# 覆盖：
#   1. 脚本 bash -n 语法
#   2. build-offline-bundle.sh --dry-run 输出结构（树/排除自检/校验计划/体积门禁）
#   3. 敏感与垃圾自检必须全 0（.env / *.db / 密钥证书 / 备份 / node_modules / storage / logs）
#   4. 关键内容自检：migrations/、webhost/dist、livekit/、迁移器、主二进制（H-2/H-3/H-4/H-1）
#   5. install-offline.sh --dry-run 输出结构（预检 → 校验 → 将执行的转调命令，含 RRT_OFFLINE）
#   6. 安装器参数与错误处理（未知参数、缺 bundle 树、缺外层 sha256 = M-2、无 exec 泄漏 = M-6）
#   7. 静态接线断言（SHA256SUMS / MANIFEST / 共享 deploy-embedded.sh / 排除清单 / 二进制回退）
#   8. 加固点静态断言（H-1 SERVER_BIN 回退、H-4 skip-build 报错、M-4 代理转义、M-5 备份权限）
#
# 用法：bash tests/deploy-g7.test.sh
# 说明：本文件只证明「脚本逻辑与产物结构」；真实安装链路由
#       tests/deploy-window + 真机上线窗口验证（桩测试覆盖不到，见 L-9）。
# ============================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD="$SCRIPT_DIR/build-offline-bundle.sh"
INSTALL="$SCRIPT_DIR/install-offline.sh"
EMBED="$SCRIPT_DIR/deploy-embedded.sh"
NETLIB="$SCRIPT_DIR/lib/net.sh"
PREFLIGHT="$SCRIPT_DIR/preflight-deploy.sh"
PASS=0
FAIL=0

ok()   { echo "  ok  - $1"; PASS=$((PASS + 1)); }
bad()  { echo "  FAIL- $1"; FAIL=$((FAIL + 1)); }
hdr()  { echo "== $1 =="; }

assert_contains()     { if grep -qF -- "$2" <<< "$1"; then ok "$3"; else bad "$3（缺: $2）"; fi; }
assert_not_contains() { if grep -qF -- "$2" <<< "$1"; then bad "$3（不应出现: $2）"; else ok "$3"; fi; }
assert_file_has()     { if grep -qE -- "$2" "$1"; then ok "$3"; else bad "$3（$1 未匹配: $2）"; fi; }
assert_file_lacks()   { if grep -qE -- "$2" "$1"; then bad "$3（$1 不应匹配: $2）"; else ok "$3"; fi; }

TMPROOT=$(mktemp -d)
cleanup() { rm -rf "$TMPROOT" 2>/dev/null || true; }
trap cleanup EXIT

# ---------- 1. 语法 ----------
hdr "1. 语法检查（bash -n）"
for f in "$BUILD" "$INSTALL" "$EMBED" "$NETLIB" "$PREFLIGHT"; do
    if bash -n "$f" 2>"$TMPROOT/syn.err"; then ok "语法通过: $(basename "$f")"; else bad "语法错误: $(basename "$f") $(cat "$TMPROOT/syn.err")"; fi
done

# ---------- 2. 构建脚本 dry-run ----------
hdr "2. build-offline-bundle.sh --dry-run"
DRY=$(bash "$BUILD" --dry-run --skip-download 2>&1 || true)
assert_contains "$DRY" "DRY-RUN（不下载 / 不复制 / 不打包）" "声明 dry-run 不落盘"
assert_contains "$DRY" "── bundle/（部署树，已应用排除清单）──" "打印 bundle 文件树"
assert_contains "$DRY" "文件数:" "统计 bundle 文件数"
assert_contains "$DRY" "── 排除清单（命中项不进包）──" "打印排除清单"
assert_contains "$DRY" "── offline-assets/ ──" "打印离线资产计划"
assert_contains "$DRY" "── 校验计划 ──" "打印校验计划"
assert_contains "$DRY" "SHA256SUMS" "声明包内校验文件"
assert_contains "$DRY" "MANIFEST.txt" "声明内容清单"
assert_contains "$DRY" "DRY-RUN 结束（未产生任何文件）" "结束语声明零落盘"
assert_contains "$DRY" "版本: 0.4.1" "版本号从 core/version 解析"

# ---------- 3. 敏感/垃圾自检全 0（H-5） ----------
hdr "3. 敏感与垃圾自检（必须全 0）"
assert_contains "$DRY" "真实 env（不含 .example） 0 个" "真实 env 文件 0 个（保留 .env.example）"
assert_contains "$DRY" "数据库文件        0 个" "数据库文件 0 个"
assert_contains "$DRY" "node_modules           0 个" "node_modules 0 个"
assert_contains "$DRY" "运行时数据 storage/.data 0 个" "storage/.data 0 个"
assert_contains "$DRY" "日志文件           0 个" "日志文件 0 个"
assert_contains "$DRY" "Windows 产物 *.exe/*.dll 0 个" "Windows 产物 0 个"
assert_contains "$DRY" "密钥/证书类       0 个" "H-5: 密钥/证书类 0 个"
assert_contains "$DRY" "备份类 *.bak/*.dump 0 个" "H-5: 备份类 0 个"
assert_contains "$DRY" "其它本地产物     0 个" "无历史本地产物二进制"

# 排除清单里确实写了这些条目
assert_file_has "$BUILD" "^    '\\.env'" "排除清单含 .env"
assert_file_has "$BUILD" "'\\*\\.db' '\\*\\.db-\\*'" "排除清单含 *.db / *.db-*"
assert_file_has "$BUILD" "'node_modules'" "排除清单含 node_modules"
assert_file_has "$BUILD" "'storage' 'logs' '\\.data'" "排除清单含 storage/logs/.data"
assert_file_has "$BUILD" "'build' 'browser_test_\\*'" "排除清单含 build/ 与测试二进制"
assert_file_has "$BUILD" "'\\*\\.pem' '\\*\\.key' '\\*\\.crt'" "H-5: 排除清单含密钥/证书模式"
assert_file_has "$BUILD" "'id_rsa\\*' 'id_ed25519\\*'" "H-5: 排除清单含 SSH 私钥模式"
assert_file_has "$BUILD" "'\\*\\.bak' '\\*\\.dump'" "H-5: 排除清单含备份模式"

# ---------- 4. 关键内容自检（H-1~H-4） ----------
hdr "4. 关键内容自检（离线安装必备件）"
assert_contains "$DRY" "✅ migrations/ 版本化迁移（000044）" "H-2: 打包树含 migrations/"
assert_contains "$DRY" "✅ webhost 管理后台产物（H-3）" "H-3: 打包树含 webhost/dist/admin/index.html"
assert_contains "$DRY" "✅ webhost 网页端产物（H-3）" "H-3: 打包树含 webhost/dist/voice/index.html"
assert_contains "$DRY" "✅ LiveKit 内置二进制（H-2）" "H-2: 打包树含 livekit/bin/linux-*/livekit-server"
assert_contains "$DRY" "✅ 迁移器 server/ridgericetalk-migrate（H-4）" "H-4: 打包树含迁移器"
assert_contains "$DRY" "✅ 主服务二进制 server/ridgericetalk-server（H-1）" "H-1: 打包树含主二进制"
assert_contains "$DRY" "✅ livekit/" "体积扫描覆盖 livekit/"
# 裸 'dist' 排除项必须已删除，否则 webhost/dist 会被 GNU tar 整棵剪掉
assert_file_lacks "$BUILD" "'\\.so\\.\\*' 'dist' 'release'" "H-3: 排除清单已移除裸 'dist'"
assert_file_has "$BUILD" "BUNDLE_TOPS=\\('server' 'migrations' 'livekit'\\)" "H-2: 打包顶层目录含 migrations/livekit"

# ---------- 5. 离线安装器 dry-run ----------
hdr "5. install-offline.sh --dry-run"
STAGE="$TMPROOT/pkg"
mkdir -p "$STAGE/bundle" "$STAGE/offline-assets"
# 用符号链接代替整树复制（server/ 含大二进制，UNC 盘复制慢且无必要）
ln -sfn "$SCRIPT_DIR/.." "$STAGE/bundle/server"
ln -sfn "$SCRIPT_DIR/../../migrations" "$STAGE/bundle/migrations"
ln -sfn "$SCRIPT_DIR/../../livekit" "$STAGE/bundle/livekit"
cp "$INSTALL" "$STAGE/install-offline.sh"
INS=$(bash "$INSTALL" --from "$STAGE" --dry-run --skip-checksum --deploy-dir /opt/ridgericetalk \
        --port-api 9099 --nat --public-address "http://203.0.113.7:9099" 2>&1 || true)
assert_contains "$INS" "── 目标机预检 ──" "打印目标机预检"
assert_contains "$INS" "需预置的系统包" "打印离线预置包清单"
assert_contains "$INS" "离线模式: 是" "默认离线模式"
assert_contains "$INS" "不启防火墙隔离" "M-1: 明确 --no-network 不启防火墙隔离"
assert_contains "$INS" "预编译二进制存在" "预检发现包内主二进制"
assert_contains "$INS" "迁移器存在" "预检发现包内迁移器（H-4）"
assert_contains "$INS" "迁移 SQL 存在: bundle/migrations" "预检发现包内迁移 SQL（H-2）"
assert_contains "$INS" "内置 LiveKit 存在: bundle/livekit" "预检发现包内 LiveKit（H-2）"
assert_contains "$INS" "RRT_OFFLINE=true bash" "M-1: 向 deploy-embedded 传递离线事实（取值 true）"
assert_contains "$INS" "--skip-build --skip-frontend" "转调在线脚本并复用模板"
assert_contains "$INS" "--deploy-dir /opt/ridgericetalk" "透传 deploy-dir"
assert_contains "$INS" "--port-api 9099" "透传端口"
assert_contains "$INS" "--nat" "透传 NAT 模式"
assert_contains "$INS" "DRY-RUN 结束（未写任何文件）" "安装器 dry-run 零写入"

# ---------- 6. 安装器参数与错误处理 ----------
hdr "6. 安装器错误处理"
ERR1=$(bash "$INSTALL" --nonsense 2>&1 || true)
assert_contains "$ERR1" "未知参数" "未知参数报错"
ERR2=$(bash "$INSTALL" --from "$TMPROOT/not-exist" --dry-run 2>&1 || true)
assert_contains "$ERR2" "目录不存在" "缺失目录报错"
MISS=$(bash "$INSTALL" --from "$TMPROOT" --dry-run 2>&1 || true)
assert_contains "$MISS" "包结构异常" "缺失 bundle/server 时报错"
# M-2：缺外层 sha256 必须失败（旧行为是静默跳过 = fail-open）
BUNDLE_NOSHA="$TMPROOT/fake-offline.tar.gz"
printf 'dummy\n' > "$BUNDLE_NOSHA"
NOSHA=$(bash "$INSTALL" --bundle "$BUNDLE_NOSHA" --dry-run 2>&1 || true)
assert_contains "$NOSHA" "未找到 $BUNDLE_NOSHA.sha256" "M-2: 缺外层 sha256 直接失败"
NOSHA2=$(bash "$INSTALL" --bundle "$BUNDLE_NOSHA" --no-outer-checksum --dry-run 2>&1 || true)
assert_contains "$NOSHA2" "已按 --no-outer-checksum 跳过外层校验" "M-2: 显式逃生开关可用"
# M-6：外层解包后不得再用 exec（否则 trap 不执行、/tmp 残留）
assert_file_lacks "$INSTALL" 'exec bash "\$inner/install-offline\.sh"' "M-6: 外层解包后不再用 exec 泄漏临时目录"
assert_file_has "$INSTALL" "cleanup_tmp" "M-6: 显式清理解包临时目录"

# ---------- 7. 静态接线 ----------
hdr "7. 静态接线断言"
assert_file_has "$BUILD" "--dry-run\\)" "构建脚本解析 --dry-run"
assert_file_has "$BUILD" "--include-vendor\\)" "构建脚本解析 --include-vendor"
assert_file_has "$BUILD" "--allow-large\\)" "构建脚本解析 --allow-large"
assert_file_has "$BUILD" "sha256sum -c|shasum -a 256" "构建脚本生成校验文件"
assert_file_has "$BUILD" "xargs -0 sha256sum" "SHA256SUMS 逐文件生成"
assert_file_has "$INSTALL" "sha256sum -c SHA256SUMS" "安装器强制校验包内容"
assert_file_has "$INSTALL" "deploy-embedded\.sh" "安装器转发到在线部署脚本"
assert_file_has "$INSTALL" "install-offline\.sh" "外层包解包后自我转调"
# 禁止把在线脚本逻辑复制进安装器（只允许 --skip-* 透传）
if grep -qE 'heredoc|\.env\.production\s*<<' "$INSTALL"; then bad "安装器不应自己渲染 env 模板（会与在线脚本漂移）"; else ok "安装器未复制 env/systemd 模板逻辑"; fi

# ---------- 8. 加固点静态断言（H-1/H-4/M-4/M-5） ----------
hdr "8. 加固点静态断言"
assert_file_has "$EMBED" 'SERVER_BIN' "H-1: deploy-embedded 引入 SERVER_BIN 变量"
assert_file_has "$EMBED" 'ridgericetalk-server' "H-1: --skip-build 探测 ridgericetalk-server"
assert_file_has "$EMBED" 'nohup "\$SERVER_BIN"' "H-1: 启动用 \$SERVER_BIN 而非硬编码名"
assert_file_has "$EMBED" "离线包应包含 ridgericetalk-migrate" "H-4: skip-build 缺迁移器给可读错误"
assert_file_has "$EMBED" 'RRT_OFFLINE' "M-1: 离线模式缺 LiveKit 直接失败"
assert_file_has "$NETLIB" 'umask 077' "M-4: apt 代理配置文件 0600"
assert_file_has "$NETLIB" '代理地址含非法字符' "M-4: 代理串含换行/引号时拒绝写入"
assert_file_lacks "$NETLIB" 'sed -i -E "s#\^proxy=' "M-4: dnf 不再用 sed 直接替换 proxy="
assert_file_has "$PREFLIGHT" 'umask 077' "M-5: 备份文件 umask 077"
assert_file_has "$PREFLIGHT" 'PGPASSWORD' "M-5: DB 口令移出 argv"
assert_file_has "$EMBED" 'chmod \+x "\$SERVER_BIN"' "H-1: 二进制缺执行位时自愈（Windows 打包 tar 记录 0644）"
assert_file_has "$INSTALL" 'chmod \+x "\$_bin"' "H-1: 安装时补可执行位"

# ---------- 9. 体积门禁 ----------
hdr "9. 体积门禁（GitHub 100MB 硬限制）"
assert_file_has "$BUILD" 'LARGE_HARD_BYTES=\$\(\(100 \* 1024 \* 1024\)\)' "100MB 硬限制常量"
assert_file_has "$BUILD" 'LARGE_WARN_BYTES=\$\(\(50 \* 1024 \* 1024\)\)' "50MB 提示常量"
assert_file_has "$BUILD" "单文件超过 100MB" "超限文件给出拒收提示"
assert_file_has "$BUILD" "allow-large" "提供 --allow-large 例外"

echo
echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]] || exit 1

# ---------- 10. task-45 加固（P1-2 / P2-1 / P2-2 / P3） ----------
hdr "10. task-45 加固（自愈校验 / dry-run 无副作用 / 排除通配 / 成员校验）"
assert_file_has "$INSTALL" '\[\[ ! -x "\$_bin" \]\]' "install-offline: chmod 后校验 -x"
assert_file_has "$INSTALL" '设置可执行位（目标文件系统可能 noexec' "install-offline: 自愈失败有可读报错"
assert_file_has "$EMBED" '\[\[ ! -x "\$SERVER_BIN" \]\]' "deploy-embedded: chmod 后校验 -x"
assert_file_has "$INSTALL" '（dry-run 未执行）将补可执行位' "install-offline: dry-run 只打印自愈计划"
assert_file_has "$BUILD" "'\.env\.\*'" "build: 排除 .env.* 通配"
assert_file_has "$BUILD" "'\*\.sqlite\*'" "build: 排除 *.sqlite*（含 -wal/-shm）"
assert_file_has "$BUILD" 'selfcheck_files\(\)' "build: 共用敏感自检函数"
assert_file_has "$BUILD" '_dg_fail' "build: dry-run 自检累计失败"
assert_file_has "$BUILD" '打包前自检失败' "build: 真实打包前 fail-closed"
assert_file_has "$INSTALL" '包成员校验通过' "install-offline: 对合规包打印成员校验通过"

# 动态 1：selfcheck_files 对坏列表返回非 0、对干净列表返回 0
eval "$(sed -n '/^selfcheck_files() {/,/^}/p' "$BUILD")"
printf 'bundle/server/.env.staging\nbundle/server/x.pem\nbundle/server/a.sqlite-wal\n' > "$TMPROOT/bad.txt"
printf 'bundle/server/app\nbundle/server/webhost/dist/admin/index.html\n' > "$TMPROOT/ok.txt"
_sens_bad=0; selfcheck_files "$TMPROOT/bad.txt" >/dev/null || _sens_bad=$?
_sens_ok=0;  selfcheck_files "$TMPROOT/ok.txt"  >/dev/null || _sens_ok=$?
if [[ "$_sens_bad" -ge 3 ]]; then ok "selfcheck: 3 类敏感命中累计 rc=$_sens_bad"; else bad "selfcheck: 敏感命中未累计 rc=$_sens_bad"; fi
if [[ "$_sens_ok" -eq 0 ]]; then ok "selfcheck: 干净列表 rc=0"; else bad "selfcheck: 干净列表误报 rc=$_sens_ok"; fi

# 动态 2：恶意 tar 成员（../ 与绝对路径）必须被拒绝
mkdir -p "$TMPROOT/evil/logs" && echo x > "$TMPROOT/evil/x"
( cd "$TMPROOT/evil" && tar -czf "$TMPROOT/dotdot.tgz" --transform='s#^#../#' x ) >/dev/null 2>&1
( cd "$TMPROOT/evil/logs" && tar -czf "$TMPROOT/abs.tgz" -P "$TMPROOT/evil/x" ) >/dev/null 2>&1
_out_dotdot=$(bash "$INSTALL" --bundle "$TMPROOT/dotdot.tgz" --no-outer-checksum --dry-run 2>&1 || true)
_out_abs=$(bash "$INSTALL" --bundle "$TMPROOT/abs.tgz" --no-outer-checksum --dry-run 2>&1 || true)
assert_contains "$_out_dotdot" "非法成员（绝对路径或 .. 越界）" "拒绝 .. 越界成员"
assert_contains "$_out_abs" "非法成员（绝对路径或 .. 越界）" "拒绝绝对路径成员"

# 动态 3：--from --dry-run 不得改写权限、不得实际自愈
FAKE="$TMPROOT/fake"
mkdir -p "$FAKE/bundle/server/webhost/dist/admin" "$FAKE/bundle/migrations" \
         "$FAKE/bundle/livekit/bin/linux-amd64" "$FAKE/bundle/server/scripts"
echo app > "$FAKE/bundle/server/ridgericetalk-server"
echo mig > "$FAKE/bundle/server/ridgericetalk-migrate"
echo sql > "$FAKE/bundle/migrations/000044_x.up.sql"
echo lk  > "$FAKE/bundle/livekit/bin/linux-amd64/livekit-server"
echo html > "$FAKE/bundle/server/webhost/dist/admin/index.html"
echo 'echo stub' > "$FAKE/bundle/server/scripts/deploy-embedded.sh"
chmod 644 "$FAKE/bundle/server/ridgericetalk-server" "$FAKE/bundle/server/ridgericetalk-migrate" 2>/dev/null || true
_before=$(stat -c %a "$FAKE/bundle/server/ridgericetalk-server" 2>/dev/null || echo unknown)
_out_dry_from=$(bash "$INSTALL" --from "$FAKE" --dry-run --no-network --skip-checksum 2>&1 || true)
_after=$(stat -c %a "$FAKE/bundle/server/ridgericetalk-server" 2>/dev/null || echo unknown)
if [[ "$_before" == "$_after" ]]; then ok "dry-run 未改写二进制权限（$_before → $_after）"; else bad "dry-run 改写了权限（$_before → $_after）"; fi
assert_contains   "$_out_dry_from" "（dry-run 未执行）将补可执行位" "dry-run 只打印自愈计划"
assert_not_contains "$_out_dry_from" "已补可执行位" "dry-run 未实际执行自愈"

# 动态 4（N-1）：离线标志取值一致性 —— install 导出 true，deploy-embedded 的守卫
# 必须接受 true 与 1（向后兼容手工 RRT_OFFLINE=1），且 false/空 不触发。
assert_file_has "$INSTALL" 'export RRT_OFFLINE=true' "N-1: install-offline 导出 RRT_OFFLINE=true"
_guard=$(grep -m1 'RRT_OFFLINE:-false' "$EMBED" | sed -E 's/^[[:space:]]*if \[\[ (.*) \]\]; then$/\1/')
if [[ -n "$_guard" ]]; then
    for _v in true 1; do
        if RRT_OFFLINE=$_v bash -c "[[ $_guard ]]" 2>/dev/null; then
            ok "N-1: RRT_OFFLINE=$_v 触发离线缺 LiveKit 守卫"
        else
            bad "N-1: RRT_OFFLINE=$_v 未触发离线守卫（守卫成死代码）"
        fi
    done
    for _v in false 0; do
        if RRT_OFFLINE=$_v bash -c "[[ $_guard ]]" 2>/dev/null; then
            bad "N-1: RRT_OFFLINE=$_v 误触发离线守卫"
        else
            ok "N-1: RRT_OFFLINE=$_v 不触发离线守卫"
        fi
    done
    if env -u RRT_OFFLINE bash -c "[[ $_guard ]]" 2>/dev/null; then
        bad "N-1: 未设置 RRT_OFFLINE 时误触发离线守卫"
    else
        ok "N-1: 未设置 RRT_OFFLINE 时不触发离线守卫"
    fi
else
    bad "N-1: 未能在 deploy-embedded.sh 中定位 RRT_OFFLINE 守卫条件"
fi


# ---------- 11. 发布门禁：交叉编译 + 二进制平台/vcs 硬校验（task-46） ----------
hdr "11. 发布门禁（linux/amd64 交叉编译 + ELF/vcs 断言 + 缺件 fail）"

# 11.1 静态接线断言
assert_file_has "$BUILD" 'CGO_ENABLED=0 GOOS=linux GOARCH=amd64' "门禁: 脚本自带交叉编译（GOOS=linux GOARCH=amd64）"
assert_file_has "$BUILD" '\-trimpath' "门禁: 交叉编译带 -trimpath"
assert_file_has "$BUILD" 'ridgericetalk/core/version.Commit' "门禁: 用 -ldflags 写入 Commit"
assert_file_has "$BUILD" 'bin_platform_issues' "门禁: 平台断言函数存在"
assert_file_has "$BUILD" 'bin_vcs_issues' "门禁: vcs 断言函数存在"
assert_file_has "$BUILD" 'vcs\.modified' "门禁: 断言 vcs.modified"
assert_file_has "$BUILD" 'gate_binaries' "门禁: 打包前/后两次门禁"
assert_file_has "$BUILD" 'use-existing-binaries' "门禁: 提供 --use-existing-binaries 排障开关"
assert_file_lacks "$BUILD" '未发现预编译二进制' "门禁: 缺主二进制不再只 warn（改 fail）"
assert_file_lacks "$BUILD" '未发现迁移器' "门禁: 缺迁移器不再只 warn（改 fail）"

# 11.2 门禁函数动态用例（抽脚本原文求值 + 假 file/go 命令）
eval "$(sed -n '/^BIN_NAMES=(/p' "$BUILD")"
eval "$(sed -n '/^bin_platform_issues() {/,/^}/p' "$BUILD")"
eval "$(sed -n '/^bin_vcs_issues() {/,/^}/p' "$BUILD")"
GATE_DIR="$TMPROOT/gate"; mkdir -p "$GATE_DIR"
echo x > "$GATE_DIR/ridgericetalk-server"; echo y > "$GATE_DIR/ridgericetalk-migrate"
GIT_HEAD=$(git -C "$BUILD/../.." rev-parse HEAD 2>/dev/null || echo deadbeef)

cat > "$TMPROOT/file_shim" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "${FSHIM_DESC:-ASCII text}"
SH
cat > "$TMPROOT/go_shim" <<'SH'
#!/usr/bin/env bash
if [[ "$1" == "version" && "$2" == "-m" ]]; then
    [[ -n "${GSHIM_EMPTY:-}" ]] && exit 0
    printf '\tbuild\tGOOS=%s\n\tbuild\tGOARCH=%s\n' "${GSHIM_GOOS:-linux}" "${GSHIM_GOARCH:-amd64}"
    [[ -n "${GSHIM_REV:-}" ]] && printf '\tbuild\tvcs.revision=%s\n' "$GSHIM_REV"
    [[ -n "${GSHIM_MODIFIED:-}" ]] && printf '\tbuild\tvcs.modified=%s\n' "$GSHIM_MODIFIED"
    exit 0
fi
exit 1
SH
chmod +x "$TMPROOT/file_shim" "$TMPROOT/go_shim"
FILE_CMD="$TMPROOT/file_shim"; GO_BIN="$TMPROOT/go_shim"

_p_pe=0; FSHIM_DESC="PE32+ executable (console) x86-64, for MS Windows" bin_platform_issues "$GATE_DIR" >/dev/null || _p_pe=$?
if (( _p_pe >= 2 )); then ok "门禁: PE 二进制被拒（rc=$_p_pe）"; else bad "门禁: PE 二进制未被拒（rc=$_p_pe）"; fi
_p_elf=0; FSHIM_DESC="ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, BuildID[sha1]=x" bin_platform_issues "$GATE_DIR" >/dev/null || _p_elf=$?
if (( _p_elf == 0 )); then ok "门禁: linux/amd64 静态 ELF 通过"; else bad "门禁: ELF 误判失败（rc=$_p_elf）"; fi
_p_dyn=0; FSHIM_DESC="ELF 64-bit LSB pie executable, x86-64, dynamically linked, interpreter /lib64/ld-linux-x86-64.so.2" bin_platform_issues "$GATE_DIR" >/dev/null || _p_dyn=$?
if (( _p_dyn >= 2 )); then ok "门禁: 动态链接 ELF 被拒（要求 statically linked）"; else bad "门禁: 动态链接 ELF 未被拒（rc=$_p_dyn）"; fi
_p_macho=0; FSHIM_DESC="Mach-O 64-bit executable x86_64" bin_platform_issues "$GATE_DIR" >/dev/null || _p_macho=$?
if (( _p_macho >= 2 )); then ok "门禁: Mach-O 二进制被拒"; else bad "门禁: Mach-O 未被拒（rc=$_p_macho）"; fi

_v_ok=0; GSHIM_REV="$GIT_HEAD" GSHIM_MODIFIED=false bin_vcs_issues "$GATE_DIR" >/dev/null || _v_ok=$?
if (( _v_ok == 0 )); then ok "门禁: revision=HEAD 且 modified=false 通过"; else bad "门禁: 正确 vcs 信息被误判（rc=$_v_ok）"; fi
_v_bad=0; GSHIM_REV="deadbeefdeadbeef" GSHIM_MODIFIED=false bin_vcs_issues "$GATE_DIR" >/dev/null || _v_bad=$?
if (( _v_bad >= 2 )); then ok "门禁: revision 与 HEAD 不一致被拒（rc=$_v_bad）"; else bad "门禁: revision 不一致未被拒（rc=$_v_bad）"; fi
_v_mod=0; GSHIM_REV="$GIT_HEAD" GSHIM_MODIFIED=true bin_vcs_issues "$GATE_DIR" >/dev/null || _v_mod=$?
if (( _v_mod >= 2 )); then ok "门禁: vcs.modified=true 被拒"; else bad "门禁: vcs.modified=true 未被拒（rc=$_v_mod）"; fi
_v_os=0; GSHIM_REV="$GIT_HEAD" GSHIM_MODIFIED=false GSHIM_GOOS=windows bin_vcs_issues "$GATE_DIR" >/dev/null || _v_os=$?
if (( _v_os >= 2 )); then ok "门禁: GOOS≠linux 被拒"; else bad "门禁: GOOS≠linux 未被拒（rc=$_v_os）"; fi
_v_none=0; GSHIM_EMPTY=1 bin_vcs_issues "$GATE_DIR" >/dev/null || _v_none=$?
if (( _v_none >= 2 )); then ok "门禁: 读不到 build info 被拒"; else bad "门禁: 无 build info 未被拒（rc=$_v_none）"; fi

# 11.3 集成：假仓库 PE 二进制 / 缺件 → 打包 fail 且无 tar.gz 产出
_fake="$TMPROOT/fakerepo"; rm -rf "$_fake"; mkdir -p "$_fake/server"
printf 'MZ\220\000 fake\n' > "$_fake/server/ridgericetalk-server"
printf 'MZ\220\000 fake\n' > "$_fake/server/ridgericetalk-migrate"
_out_pe="$TMPROOT/out-pe"; rm -rf "$_out_pe"
_out_pe_log=$(RRT_OFFLINE_REPO_ROOT="$_fake" bash "$BUILD" --use-existing-binaries --skip-download -o "$_out_pe" 2>&1 || true)
if printf '%s' "$_out_pe_log" | grep -q '二进制门禁失败'; then ok "集成: PE 二进制触发门禁失败"; else bad "集成: PE 二进制未触发门禁失败"; fi
if compgen -G "$_out_pe/*.tar.gz" >/dev/null; then bad "集成: 门禁失败仍产出 tar.gz"; else ok "集成: 门禁失败未产出 tar.gz"; fi
rm -f "$_fake/server/ridgericetalk-migrate"
_out_miss="$TMPROOT/out-miss"; rm -rf "$_out_miss"
_out_miss_log=$(RRT_OFFLINE_REPO_ROOT="$_fake" bash "$BUILD" --use-existing-binaries --skip-download -o "$_out_miss" 2>&1 || true)
if printf '%s' "$_out_miss_log" | grep -q '缺失 ridgericetalk-migrate'; then ok "集成: 缺迁移器明确报缺失"; else bad "集成: 缺迁移器未报缺失"; fi
if printf '%s' "$_out_miss_log" | grep -q '二进制门禁失败'; then ok "集成: 缺件触发门禁失败"; else bad "集成: 缺件未触发门禁失败"; fi
if compgen -G "$_out_miss/*.tar.gz" >/dev/null; then bad "集成: 缺件仍产出 tar.gz"; else ok "集成: 缺件未产出 tar.gz"; fi


# ---------- 12. admin 双路径产物一致性门禁（task-47：rc1 事故防复发） ----------
hdr "12. admin 双路径产物一致性（webhost/dist/admin vs server/webhost/dist/admin）"

# 12.1 静态接线断言
assert_file_has "$BUILD" 'ADMIN_DIST_PATHS=' "门禁: 定义 admin 双路径常量"
assert_file_has "$BUILD" 'admin_dist_issues' "门禁: 一致性校验函数存在"
assert_file_has "$BUILD" 'admin_dist_issues "\$REPO_ROOT" \|\| fail' "门禁: 真实打包前不一致即 fail（不产出包）"
assert_file_has "$BUILD" 'admin 双路径产物不一致' "门禁: 输出两路径 sha 与修复提示"
assert_file_has "$BUILD" 'admin 双路径产物一致性（门禁 3' "门禁: dry-run 计划段列出该门禁"
assert_file_lacks "$BUILD" 'cp -a "\$REPO_ROOT/webhost/dist/admin' "门禁: 不自动同步（只校验，语义不变）"

# 12.2 动态用例（抽脚本原文求值 + 临时目录桩，不依赖真实产物）
eval "$(sed -n '/^sha_of() {/,/^}/p' "$BUILD")"
eval "$(sed -n '/^ADMIN_DIST_PATHS=(/p' "$BUILD")"
eval "$(sed -n '/^admin_dist_issues() {/,/^}/p' "$BUILD")"

AD_ROOT="$TMPROOT/admin-dual"
rm -rf "$AD_ROOT"
mkdir -p "$AD_ROOT/webhost/dist/admin" "$AD_ROOT/server/webhost/dist/admin"
printf '<html>same</html>\n' > "$AD_ROOT/webhost/dist/admin/index.html"
cp "$AD_ROOT/webhost/dist/admin/index.html" "$AD_ROOT/server/webhost/dist/admin/index.html"
_ad_rc=0; _ad_out=$(admin_dist_issues "$AD_ROOT" 2>&1) || _ad_rc=$?
if (( _ad_rc == 0 )) && printf '%s' "$_ad_out" | grep -q 'admin 双路径一致'; then
    ok "admin 门禁: 两路径一致 → 通过"
else
    bad "admin 门禁: 两路径一致未通过（rc=$_ad_rc）"
fi

printf '<html>drift</html>\n' > "$AD_ROOT/webhost/dist/admin/index.html"
_ad_rc=0; _ad_out=$(admin_dist_issues "$AD_ROOT" 2>&1) || _ad_rc=$?
_h1=$(sha_of "$AD_ROOT/webhost/dist/admin/index.html"); _h2=$(sha_of "$AD_ROOT/server/webhost/dist/admin/index.html")
if (( _ad_rc == 1 )) && printf '%s' "$_ad_out" | grep -q "$_h1" && printf '%s' "$_ad_out" | grep -q "$_h2" && printf '%s' "$_ad_out" | grep -q 'cp -a webhost/dist/admin'; then
    ok "admin 门禁: 不一致 → fail 且打印两 sha + 修复提示"
else
    bad "admin 门禁: 不一致未按预期 fail 或未打印两 sha（rc=$_ad_rc）"
fi

cp "$AD_ROOT/webhost/dist/admin/index.html" "$AD_ROOT/server/webhost/dist/admin/index.html"
rm -f "$AD_ROOT/server/webhost/dist/admin/index.html"
_ad_rc=0; _ad_out=$(admin_dist_issues "$AD_ROOT" 2>&1) || _ad_rc=$?
if (( _ad_rc == 1 )) && printf '%s' "$_ad_out" | grep -q '缺失' && printf '%s' "$_ad_out" | grep -q 'server/webhost/dist/admin/index.html'; then
    ok "admin 门禁: server 侧缺失 → fail 且指出缺失路径"
else
    bad "admin 门禁: server 侧缺失未按预期 fail（rc=$_ad_rc）"
fi

# ---------- 13. task-49：离线安装默认 RRT_EMBEDDED_DEPS=false（无网不下载） ----------
hdr "13. task-49 离线安装默认关闭运行期依赖下载（RRT_EMBEDDED_DEPS）"
assert_file_lacks "$EMBED" '^RRT_EMBEDDED_DEPS=true$' "deploy-embedded: env 不再硬编码 true"
assert_file_has  "$EMBED" 'RRT_EMBEDDED_DEPS=\$EMBEDDED_DEPS_VALUE' "deploy-embedded: env 落盘使用解析后的变量"
assert_file_has  "$EMBED" '离线安装默认关闭服务端启动期依赖下载' "deploy-embedded: env 注释说明离线默认"
assert_file_has  "$EMBED" 'allow-embedded-deps-download' "deploy-embedded: 提供 --allow-embedded-deps-download"
assert_file_has  "$EMBED" '^resolve_embedded_deps_value\(\) \{' "deploy-embedded: 定义 resolve_embedded_deps_value"
assert_file_has  "$EMBED" '^assert_offline_embedded_deps_safe\(\) \{' "deploy-embedded: 定义离线 fail-fast 断言"
assert_file_has  "$INSTALL" 'allow-embedded-deps-download' "install-offline: 透传 --allow-embedded-deps-download"

# 动态：抽取脚本内两个函数的原文并求值（与脚本逐字一致，不改实现）
_ed_src=$(sed -n '/^resolve_embedded_deps_value() {/,/^}/p' "$EMBED")
_fa_src=$(sed -n '/^assert_offline_embedded_deps_safe() {/,/^}/p' "$EMBED")
if [[ -n "$_ed_src" && -n "$_fa_src" ]]; then
    eval "$_ed_src"
    eval "$_fa_src"
    info() { printf '%s\n' "$*"; }
    fail() { printf '%s\n' "$*"; }

    # ③ 回归：非离线路径默认仍为 true
    _v=$( unset RRT_OFFLINE RRT_EMBEDDED_DEPS; resolve_embedded_deps_value ); _rc=$?
    if (( _rc == 0 )) && [[ "$_v" == "true" ]]; then ok "回归: 非离线（未设 RRT_OFFLINE）默认 true"; else bad "在线默认应为 true（rc=$_rc v=$_v）"; fi

    # ① 离线安装默认 false
    _v=$(RRT_OFFLINE=true RRT_EMBEDDED_DEPS= resolve_embedded_deps_value); _rc=$?
    if (( _rc == 0 )) && [[ "$_v" == "false" ]]; then ok "默认值: 离线安装 RRT_OFFLINE=true → false"; else bad "离线默认应为 false（rc=$_rc v=$_v）"; fi

    _v=$(RRT_OFFLINE=1 RRT_EMBEDDED_DEPS= resolve_embedded_deps_value); _rc=$?
    if (( _rc == 0 )) && [[ "$_v" == "false" ]]; then ok "默认值: 离线标记 RRT_OFFLINE=1 → false"; else bad "RRT_OFFLINE=1 应为 false（rc=$_rc v=$_v）"; fi

    _v=$(RRT_OFFLINE=false RRT_EMBEDDED_DEPS= resolve_embedded_deps_value); _rc=$?
    if (( _rc == 0 )) && [[ "$_v" == "true" ]]; then ok "在线安装 RRT_OFFLINE=false → true"; else bad "在线应为 true（rc=$_rc v=$_v）"; fi

    # 显式值优先（离线也尊重用户显式 true，但由 fail-fast 把关）
    _v=$(RRT_OFFLINE=true RRT_EMBEDDED_DEPS=true resolve_embedded_deps_value); _rc=$?
    if (( _rc == 0 )) && [[ "$_v" == "true" ]]; then ok "显式 RRT_EMBEDDED_DEPS=true 优先于离线默认"; else bad "显式 true 应生效（rc=$_rc v=$_v）"; fi

    _v=$(RRT_OFFLINE=true RRT_EMBEDDED_DEPS=yes resolve_embedded_deps_value 2>/dev/null); _rc=$?
    if (( _rc != 0 )); then ok "非法取值（yes）→ 返回非 0 由调用方 fail"; else bad "非法取值应返回非 0（v=$_v）"; fi

    # ④ 离线 + true + 缺依赖 → fail-fast（明确提示缺什么、如何补）
    _EMPTY="$TMPROOT/deps-empty"; mkdir -p "$_EMPTY"
    _out=$(SERVER_DIR="$_EMPTY/server" DEPLOY_DIR="$_EMPTY" ALLOW_EMBEDDED_DEPS_DOWNLOAD=false RRT_OFFLINE=true assert_offline_embedded_deps_safe true 2>&1); _rc=$?
    if (( _rc == 1 )) && printf '%s' "$_out" | grep -q '缺失' && printf '%s' "$_out" | grep -q '修复'; then
        ok "离线+true+缺依赖 → fail-fast 并列出缺失与修复路径"
    else
        bad "离线+true+缺依赖应 fail-fast（rc=$_rc）"
    fi

    # 依赖齐全时放行
    _SAFE="$TMPROOT/deps-safe"; mkdir -p "$_SAFE/server/third_party" "$_SAFE/server/models" "$_SAFE/services/netease-api"
    _out=$(SERVER_DIR="$_SAFE/server" DEPLOY_DIR="$_SAFE" ALLOW_EMBEDDED_DEPS_DOWNLOAD=false RRT_OFFLINE=true assert_offline_embedded_deps_safe true 2>&1); _rc=$?
    (( _rc == 0 )) && ok "离线+true+deps 齐全 → 放行（可联网下载）" || bad "deps 齐全应放行（rc=$_rc）"

    # 显式 --allow-embedded-deps-download 时放行并提示风险
    _out=$(SERVER_DIR="$_EMPTY/server" DEPLOY_DIR="$_EMPTY" ALLOW_EMBEDDED_DEPS_DOWNLOAD=true RRT_OFFLINE=true assert_offline_embedded_deps_safe true 2>&1); _rc=$?
    if (( _rc == 0 )) && printf '%s' "$_out" | grep -q '允许运行期依赖下载'; then
        ok "--allow-embedded-deps-download 放行并打印风险提示"
    else
        bad "显式允许下载应放行（rc=$_rc）"
    fi

    # 无网默认路径（false）不进入下载分支
    _out=$(SERVER_DIR="$_EMPTY/server" DEPLOY_DIR="$_EMPTY" ALLOW_EMBEDDED_DEPS_DOWNLOAD=false RRT_OFFLINE=true assert_offline_embedded_deps_safe false 2>&1); _rc=$?
    if (( _rc == 0 )) && [[ -z "$_out" ]]; then ok "默认 false 路径不触发下载检查（零输出）"; else bad "false 应为静默放行（rc=$_rc out=$_out）"; fi
else
    bad "未能从 deploy-embedded.sh 抽取 resolve_embedded_deps_value / assert_offline_embedded_deps_safe"
fi

echo
echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]] || exit 1
