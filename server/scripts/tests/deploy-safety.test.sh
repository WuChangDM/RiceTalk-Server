#!/usr/bin/env bash
# ============================================================
# deploy-baremetal.sh 部署安全闸门 桩化测试（H17）
# ============================================================
# 测什么（对应 known_issues H17 的三处修复）：
#   ① source_tree_guard     源码树陈旧 / 与预期构建来源不一致时**拒绝继续**（不再静默）
#   ② backup_current_binary 编译前备份现行二进制到标准回滚位置（命名/权限/校验口径）
#   ③ verify_version_injection  编译前识别 `const Server` 导致 -X 静默失效
#
# 怎么测（桩化，不接触任何生产环境）：
#   本脚本用 sed 从 deploy-baremetal.sh 中**整段抽取** H17-GUARD 标记之间的函数定义，
#   再由本脚本提供 info/warn/fail/ok/dry_run 的桩实现与常量，随后逐个场景调用函数，
#   断言「返回码 + 输出内容」落在预期的分支上。全部在 mktemp -d 的临时目录内完成，
#   git 远端也用本地裸仓库模拟（不联网）。
#
# 用法: bash server/scripts/tests/deploy-safety.test.sh
# 退出码: 0 = 全部通过；1 = 有用例失败
# 说明: 本脚本不执行 deploy-baremetal.sh 的主流程（无 root、无 Linux 也能跑）。
# ============================================================
set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$TEST_DIR/../deploy-baremetal.sh"

if [[ ! -f "$SCRIPT" ]]; then
    echo "找不到被测脚本: $SCRIPT"
    exit 1
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# ------------------------------------------------------------
# 一、抽取被测函数 + 桩实现
# ------------------------------------------------------------
EXTRACT="$TMP/h17-guard.extract.sh"
sed -n '/^# >>> H17-GUARD-BEGIN/,/^# <<< H17-GUARD-END/p' "$SCRIPT" > "$EXTRACT"
if ! grep -q "source_tree_guard()" "$EXTRACT"; then
    echo "抽取失败：$SCRIPT 中找不到 H17-GUARD 标记块（标记勿删）"
    exit 1
fi
echo "已从被测脚本抽取函数 $(grep -c '^[a-z_]*()' "$EXTRACT") 个（$(wc -l < "$EXTRACT") 行）"

# 常量（与主脚本同名口径；函数默认参数会用到）
DEPLOY_DIR="$TMP/deploy"
SERVER_DIR="$DEPLOY_DIR/server"
REPO_BRANCH="main"
RRT_SOURCE_BRANCH="main"
RRT_SOURCE_SKIP_FETCH="false"
ALLOW_STALE_SOURCE="false"
DRY_RUN="false"
SCRIPT_NAME="deploy-baremetal.sh"
RRT_BIN_BACKUP_DIR="$TMP/backups"

# 日志桩（与主脚本输出形态一致，便于断言）
info() { echo "  [INFO] $*"; }
warn() { echo "  [WARN] $*"; }
fail() { echo "  [FAIL] $*"; }
ok()   { echo "  [OK] $*"; }
# dry_run 桩：与主脚本语义一致（DRY_RUN=true 时打印计划并返回 0）
dry_run() {
    [[ "$DRY_RUN" == "true" ]] || return 1
    local action
    for action in "$@"; do echo "  [DRY-RUN] 将执行: $action"; done
    return 0
}

# shellcheck source=/dev/null
source "$EXTRACT"

# ------------------------------------------------------------
# 二、断言工具
# ------------------------------------------------------------
PASS=0
FAILED=0
CURRENT=""

case_name() { CURRENT="$1"; echo ""; echo "── 场景: $1"; }

assert_eq() {
    local expected="$1" actual="$2" what="${3:-返回值}"
    if [[ "$expected" == "$actual" ]]; then
        PASS=$((PASS + 1)); echo "   ✓ $what = $actual"
    else
        FAILED=$((FAILED + 1)); echo "   ✗ $what 期望=$expected 实际=$actual"
    fi
}

assert_contains() {
    local needle="$1" haystack="$2" what="${3:-输出}"
    if [[ "$haystack" == *"$needle"* ]]; then
        PASS=$((PASS + 1)); echo "   ✓ $what 含「$needle」"
    else
        FAILED=$((FAILED + 1)); echo "   ✗ $what 不含「$needle」"; echo "     ---- 实际输出 ----"; echo "$haystack"; echo "     ------------------"
    fi
}

assert_not_contains() {
    local needle="$1" haystack="$2" what="${3:-输出}"
    if [[ "$haystack" != *"$needle"* ]]; then
        PASS=$((PASS + 1)); echo "   ✓ $what 不含「$needle」"
    else
        FAILED=$((FAILED + 1)); echo "   ✗ $what 不应含「$needle」"
    fi
}

# ------------------------------------------------------------
# 三、夹具：本地裸仓库 + 克隆（模拟「源码树是 git 仓库」）
# ------------------------------------------------------------
gitq() { git -c core.autocrlf=false -c user.email=t@example.com -c user.name=t "$@"; }

ORIGIN="$TMP/origin.git"
SEED="$TMP/seed"
gitq init --bare -q "$ORIGIN"
# 裸仓库的默认 HEAD 通常指向 refs/heads/master（本仓库用 main），否则克隆会
# 「remote HEAD refers to nonexistent ref, unable to checkout」→ 工作区为空
gitq -C "$ORIGIN" symbolic-ref HEAD refs/heads/main
gitq init -q "$SEED"
# 统一分支名为 main（不依赖 init.defaultBranch 配置；老版本 git 也适用）
gitq -C "$SEED" symbolic-ref HEAD refs/heads/main
mkdir -p "$SEED/server/cmd/server"
echo 'package main' > "$SEED/server/cmd/server/main.go"
if ! ( cd "$SEED" && gitq add -A && gitq commit -qm "c1" && gitq remote add origin "$ORIGIN" && gitq push -q -u origin main ); then
    echo "夹具构建失败（git 不可用？），无法验证 git 相关分支"
    exit 1
fi

# clone-behind：先克隆（此时 origin/main 引用停在 c1），随后推进 origin 一格 →
# 该克隆既落后又引用陈旧（fetch 前 verdict 仍判 git-current，guard 内 fetch 后才暴露落后）
gitq clone -q "$ORIGIN" "$TMP/tree-behind" 2>/dev/null
echo 'more' >> "$SEED/server/cmd/server/main.go"
if ! ( cd "$SEED" && gitq add -A && gitq commit -qm "c2" && gitq push -q origin main ); then
    echo "夹具推进远端失败，无法验证「落后远端」分支"
    exit 1
fi
# clone-in-sync：在推进后的 origin 上克隆 → 真正「未落后远端」
# （若在推进前克隆，guard 内的 fetch 会把引用刷到 c2，照样判落后 —— 夹具顺序不可颠倒）
gitq clone -q "$ORIGIN" "$TMP/tree-in-sync" 2>/dev/null
for d in "$TMP/tree-in-sync" "$TMP/tree-behind"; do
    [[ -f "$d/server/cmd/server/main.go" ]] || { echo "夹具异常: $d 缺少源码"; exit 1; }
done

# ------------------------------------------------------------
# 四、① 源码树闸门
# ------------------------------------------------------------
echo ""
echo "============================================================"
echo "① 源码树新鲜度闸门（source_tree_verdict / source_tree_guard）"
echo "============================================================"

case_name "源码树不存在 → verdict=missing，闸门放行（不干扰克隆路径）"
v="$(source_tree_verdict "$TMP/does-not-exist")"
assert_eq "missing" "$v" "verdict"
out="$(source_tree_guard "$TMP/does-not-exist")"; rc=$?
assert_eq 0 "$rc" "guard 返回码"
assert_eq "" "$out" "guard 输出（应为空）"

case_name "git 仓库且未落后远端 → 放行"
v="$(source_tree_verdict "$TMP/tree-in-sync")"
assert_eq "git-current" "$v" "verdict"
out="$(source_tree_guard "$TMP/tree-in-sync")"; rc=$?
assert_eq 0 "$rc" "guard 返回码"
assert_contains "未落后远端" "$out"

case_name "git 仓库 + 工作区有未提交改动 → 放行但明确告警"
echo 'dirty' >> "$TMP/tree-in-sync/server/cmd/server/main.go"
out="$(source_tree_guard "$TMP/tree-in-sync")"; rc=$?
assert_eq 0 "$rc" "guard 返回码"
assert_contains "未提交改动" "$out"
rm -f "$TMP/tree-in-sync/server/cmd/server/main.go"
( cd "$TMP/tree-in-sync" && gitq checkout -q -- . )

case_name "git 仓库落后远端 1 个提交 → 拒绝（rc=1）并给两条路径"
v="$(source_tree_verdict "$TMP/tree-behind")"
assert_eq "git-current" "$v" "verdict（fetch 前引用未更新，故尚不落后）"
out="$(source_tree_guard "$TMP/tree-behind")"; rc=$?
assert_eq 1 "$rc" "guard 返回码"
assert_contains "落后远端 1 个提交" "$out"
assert_contains "pull --ff-only" "$out" "路径 1"
assert_contains "deploy_server_binary.sh" "$out" "路径 2"
assert_not_contains "本次不中止" "$out" "非 dry-run 下不打印 dry-run 预告"

case_name "git 仓库落后远端 + --allow-stale-source → 显式放行"
ALLOW_STALE_SOURCE="true"
out="$(source_tree_guard "$TMP/tree-behind")"; rc=$?
assert_eq 0 "$rc" "guard 返回码"
assert_contains "显式放行" "$out"
ALLOW_STALE_SOURCE="false"

case_name "git 仓库落后远端 + --dry-run → 只预告拒绝、不中止（保 dry-run 只读语义）"
DRY_RUN="true"
out="$(source_tree_guard "$TMP/tree-behind")"; rc=$?
assert_eq 0 "$rc" "guard 返回码"
assert_contains "真实执行时会" "$out" "dry-run 预告"
assert_contains "git -C" "$out" "dry-run 计划（fetch 被 dry-run 守卫，未真正联网）"
DRY_RUN="false"
assert_eq 1 "$(source_tree_behind_count "$TMP/tree-behind")" "落后提交数（确认 dry-run 未破坏引用）"

case_name "非 git 仓库 + 有 .rrt-commit（生产机实测情形） → 拒绝并给两条可行动路径"
NONGIT_REC="$TMP/tree-nongit-recorded"
mkdir -p "$NONGIT_REC/server/cmd/server"
echo 'package main' > "$NONGIT_REC/server/cmd/server/main.go"
echo '4d8dc25' > "$NONGIT_REC/server/.rrt-commit"
v="$(source_tree_verdict "$NONGIT_REC")"
assert_eq "nongit-recorded" "$v" "verdict"
out="$(source_tree_guard "$NONGIT_REC")"; rc=$?
assert_eq 1 "$rc" "guard 返回码"
assert_contains "本树不是它的源码" "$out"
assert_contains "4d8dc25" "$out" "输出（引用批次记录）"
assert_contains "deploy_server_binary.sh 4d8dc25" "$out" "路径 1"
assert_contains "rm -rf" "$out" "路径 2"
assert_contains "拒绝并退出" "$out" "结论"

case_name "非 git 仓库 + 有 .rrt-commit + --dry-run → 只预告拒绝，不中止（保 dry-run 只读语义）"
DRY_RUN="true"
out="$(source_tree_guard "$NONGIT_REC")"; rc=$?
assert_eq 0 "$rc" "guard 返回码"
assert_contains "真实执行时会" "$out" "dry-run 预告"
DRY_RUN="false"

case_name "非 git 仓库 + 无任何 provenance（手工 rsync 首次部署） → 告警但放行"
NONGIT_UNREC="$TMP/tree-nongit-unrecorded"
mkdir -p "$NONGIT_UNREC/server/cmd/server"
echo 'package main' > "$NONGIT_UNREC/server/cmd/server/main.go"
v="$(source_tree_verdict "$NONGIT_UNREC")"
assert_eq "nongit-unrecorded" "$v" "verdict"
out="$(source_tree_guard "$NONGIT_UNREC")"; rc=$?
assert_eq 0 "$rc" "guard 返回码"
assert_contains "无法校验" "$out"

case_name "set -e 下调用被拒绝的闸门 → 脚本立即终止（证明不是「静默继续」）"
# 注意：不可加 `|| true` —— 处于 `||` 列表中的命令替换是「errexit 被忽略的上下文」，
# 连内层显式 set -e 也会失效（bash 文档化行为），恰好使本用例失去意义。
# 本脚本未开 -e，子 shell 以 1 退出只会体现在赋值返回码上，不会中断测试本身。
subout="$(set -e; source_tree_guard "$NONGIT_REC"; echo "NOT-REACHED")"
assert_not_contains "NOT-REACHED" "$subout" "set -e 子 shell 输出"

# ------------------------------------------------------------
# 五、② 编译前备份现行二进制
# ------------------------------------------------------------
echo ""
echo "============================================================"
echo "② 编译前备份现行二进制（backup_current_binary）"
echo "============================================================"

case_name "现行二进制存在 → 生成标准命名备份 + .md5 旁文件，内容与源一致"
BIN="$TMP/bin-dir/ridgericetalk"
mkdir -p "$(dirname "$BIN")"
printf 'FAKE-ELF-BINARY-CONTENT-v1' > "$BIN"
src_md5="$(md5sum "$BIN" | awk '{print $1}')"
RRT_BIN_BACKUP_DIR="$TMP/backups"
out="$(backup_current_binary "$BIN")"; rc=$?
assert_eq 0 "$rc" "返回码"
assert_contains "编译前已备份现行二进制" "$out"
assert_contains "rollback-binary.sh --list" "$out" "回滚指引"
shopt -s nullglob
baks=("$RRT_BIN_BACKUP_DIR"/ridgericetalk-bin-[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]-[0-9][0-9][0-9][0-9][0-9][0-9]-[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f])
assert_eq 1 "${#baks[@]}" "标准命名备份数量"
if (( ${#baks[@]} == 1 )); then
    assert_eq "$src_md5" "$(md5sum "${baks[0]}" | awk '{print $1}')" "备份内容 md5"
    assert_eq "$src_md5" "$(cat "${baks[0]}.md5")" ".md5 旁文件内容"
    echo "     备份文件名: $(basename "${baks[0]}")"
    echo "     期望后缀（源 md5 前 8 位）: ${src_md5:0:8}"
    case "$(basename "${baks[0]}")" in
        *-"${src_md5:0:8}") PASS=$((PASS + 1)); echo "   ✓ 文件名后缀 = 源 md5 前 8 位" ;;
        *) FAILED=$((FAILED + 1)); echo "   ✗ 文件名后缀不是源 md5 前 8 位" ;;
    esac
fi
shopt -u nullglob

case_name "无现行二进制（全新部署） → 不备份、不报错"
out="$(backup_current_binary "$TMP/bin-dir/not-there")"; rc=$?
assert_eq 0 "$rc" "返回码"
assert_contains "无现行二进制可备份" "$out"

case_name "备份目录无法创建 → 返回 1（调用方据此中止编译，不覆盖运行中的二进制）"
BLOCKED="$TMP/blocked-file"
: > "$BLOCKED"
RRT_BIN_BACKUP_DIR="$BLOCKED/sub"
out="$(backup_current_binary "$BIN")"; rc=$?
assert_eq 1 "$rc" "返回码"
assert_contains "备份目录无法创建" "$out"
RRT_BIN_BACKUP_DIR="$TMP/backups"

# ------------------------------------------------------------
# 六、③ 版本注入可行性校验
# ------------------------------------------------------------
echo ""
echo "============================================================"
echo "③ 版本注入可行性校验（version_symbol_kind / verify_version_injection）"
echo "============================================================"

# 夹具：M7 之前的形态（生产陈旧树上就是这种）
CONST_VGO="$TMP/const-version.go"
cat > "$CONST_VGO" <<'EOF'
package version

const Server = "0.2.2"
EOF

# 夹具：M7 之后的形态（与仓库当前 core/version/version.go 同形）
VAR_VGO="$TMP/var-version.go"
cat > "$VAR_VGO" <<'EOF'
package version

var Server = "0.2.2"

var Commit = ""

var BuildTime = ""
EOF

# 在**当前 shell** 运行 verify_version_injection 并取回输出。
# 不能写 out="$(verify_version_injection …)" —— 命令替换是子 shell，
# 函数设置的 RRT_VERSION_INJECTION_OK 全局变量带不出来；生产主流程是直接调用，
# 故此处也直接调用，stdout 落到临时文件再读回，返回码与输出内容都能断言。
run_vvi() {
    verify_version_injection "$1" > "$TMP/vv.out"
    VVI_OUT="$(cat "$TMP/vv.out")"
}

case_name "符号形态识别（var / const / missing）"
assert_eq "const" "$(version_symbol_kind "$CONST_VGO" Server)" "const 形态"
assert_eq "var" "$(version_symbol_kind "$VAR_VGO" Server)" "var 形态"
assert_eq "var" "$(version_symbol_kind "$VAR_VGO" Commit)" "var Commit"
assert_eq "missing" "$(version_symbol_kind "$VAR_VGO" BuildTime2)" "不存在的符号"
assert_eq "missing" "$(version_symbol_kind "$TMP/nope.go" Server)" "文件不存在"

case_name "version.go 是 var Server/Commit/BuildTime → 校验通过"
RRT_VERSION_INJECTION_OK=""
run_vvi "$VAR_VGO"; rc=$?
out="$VVI_OUT"
assert_eq 0 "$rc" "返回码（永不中断）"
assert_eq "true" "$RRT_VERSION_INJECTION_OK" "RRT_VERSION_INJECTION_OK"
assert_contains "版本注入校验通过" "$out"

case_name "version.go 是 const Server（M7 之前，陈旧树） → 明确告警但继续"
RRT_VERSION_INJECTION_OK=""
run_vvi "$CONST_VGO"; rc=$?
out="$VVI_OUT"
assert_eq 0 "$rc" "返回码（告警但继续，不中断部署）"
assert_eq "false" "$RRT_VERSION_INJECTION_OK" "RRT_VERSION_INJECTION_OK"
assert_contains "const Server" "$out" "输出（指出形态）"
assert_contains "静默无效" "$out" "输出（点明 -X 静默无效）"
assert_contains "不可辨识" "$out" "输出（后果）"
assert_contains "早于 2026-09-12" "$out" "输出（陈旧树旁证）"
assert_contains "deploy_server_binary.sh" "$out" "输出（建议）"

case_name "version.go 不存在 → 告警（回退默认版本号）但继续"
RRT_VERSION_INJECTION_OK=""
run_vvi "$TMP/no-such-version.go"; rc=$?
out="$VVI_OUT"
assert_eq 0 "$rc" "返回码"
assert_eq "false" "$RRT_VERSION_INJECTION_OK" "RRT_VERSION_INJECTION_OK"
assert_contains "未找到" "$out"

case_name "对仓库当前真实 core/version/version.go 校验（应通过）"
REAL_VGO="$(cd "$TEST_DIR/../../core/version" && pwd)/version.go"
RRT_VERSION_INJECTION_OK=""
run_vvi "$REAL_VGO"; rc=$?
out="$VVI_OUT"
assert_eq 0 "$rc" "返回码"
assert_eq "true" "$RRT_VERSION_INJECTION_OK" "RRT_VERSION_INJECTION_OK"
assert_contains "版本注入校验通过" "$out"
echo "     被测文件: $REAL_VGO"

# ------------------------------------------------------------
# 七、汇总
# ------------------------------------------------------------
echo ""
echo "============================================================"
echo "结果: 通过 $PASS 项 / 失败 $FAILED 项"
echo "============================================================"
[[ "$FAILED" -eq 0 ]] || exit 1
exit 0
