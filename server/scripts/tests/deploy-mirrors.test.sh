#!/usr/bin/env bash
# ============================================================
# G-5 桩测试：lib/mirrors.sh 镜像预设（DES-2026-0912-03 §8）
# 口径对齐 deploy-safety.test.sh：纯函数级断言，无需 root/网络。
# 运行：bash scripts/tests/deploy-mirrors.test.sh
# ============================================================
set -u
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB_DIR="$SCRIPT_DIR/../lib"

PASS=0; FAIL=0
# 命名避开 os.sh 的宿主协议（rrt_fail 会转发到宿主 fail，勿同名）
t_ok()   { PASS=$((PASS+1)); printf '  ok  - %s\n' "$1"; }
t_fail() { FAIL=$((FAIL+1)); printf '  FAIL - %s\n' "$1"; }
ok()   { t_ok "$1"; }
fail() { t_fail "$1"; }
assert_eq() { # desc expected actual
    if [[ "$2" == "$3" ]]; then ok "$1"; else fail "$1（期望 [$2] 实际 [$3]）"; fi
}
assert_first() { # desc expected url_list_output
    local first
    first="$(printf '%s\n' "$3" | head -1)"
    assert_eq "$1" "$2" "$first"
}
assert_nth() { # desc expected nth(0-based) url_list_output
    local -a arr=()
    mapfile -t arr <<<"$4"
    assert_eq "$1" "$2" "${arr[$3]:-<missing>}"
}

# shellcheck source=/dev/null
. "$LIB_DIR/net.sh"
# shellcheck source=/dev/null
. "$LIB_DIR/mirrors.sh"

echo "== 1. preset 取值 =="
assert_eq "默认 preset=auto" "auto" "$(rrt_mirror_preset)"
RRT_MIRROR_PRESET=cn
assert_eq "环境变量 cn 生效" "cn" "$(rrt_mirror_preset)"
rrt_set_mirror_preset auto
# 断言拒绝行为：rrt_fail 会经宿主协议转发到本测试的 fail 计数器，
# 断言期间临时屏蔽转发，只验证返回码与错误文本本身。
rrt_set_mirror_preset_bogus() {
    fail() { :; }
    rrt_set_mirror_preset bogus >/dev/null 2>&1
    local rc=$?
    unset -f fail
    return $rc
}
if rrt_set_mirror_preset_bogus; then fail "非法 preset 应被拒绝"; else ok "非法 preset 被拒绝"; fi
rrt_set_mirror_preset cn && assert_eq "set 后 RRT_MIRROR_PRESET=cn" "cn" "${RRT_MIRROR_PRESET}"
rrt_set_mirror_preset auto

echo "== 2. github_clone 前缀列表 =="
assert_first "auto: 直连优先" "" "$(rrt_mirror_urls github_clone)"
RRT_MIRROR_PRESET=cn
assert_first "cn: 镜像优先" "https://gh-proxy.com/" "$(rrt_mirror_urls github_clone)"
assert_eq "cn: 仍保留直连兜底（列表末项）" "" "$(rrt_mirror_urls github_clone | tail -1)"
rrt_set_mirror_preset auto
RRT_MIRROR_PRESET=global
assert_first "global: 直连优先" "" "$(rrt_mirror_urls github_clone)"
rrt_set_mirror_preset auto

echo "== 3. go_tarball 列表 =="
TB="go1.22.0.linux-amd64.tar.gz"
assert_first "auto: 国内优先" "https://golang.google.cn/dl/${TB}" "$(rrt_mirror_urls go_tarball "$TB")"
RRT_MIRROR_PRESET=global
assert_first "global: 官方优先" "https://go.dev/dl/${TB}" "$(rrt_mirror_urls go_tarball "$TB")"
rrt_set_mirror_preset auto
RRT_MIRROR_PRESET=cn
assert_first "cn: 国内优先" "https://golang.google.cn/dl/${TB}" "$(rrt_mirror_urls go_tarball "$TB")"
rrt_set_mirror_preset auto
RRT_MIRROR_PRESET=global
assert_eq "tarball 名注入正确" "https://go.dev/dl/${TB}" "$(rrt_mirror_urls go_tarball "$TB" | head -1)"
rrt_set_mirror_preset auto
rrt_set_mirror_preset auto

echo "== 4. node_setup / node_bin 列表 =="
assert_first "auto: NodeSource 优先" "https://deb.nodesource.com/setup_20.x" "$(rrt_mirror_urls node_setup)"
RRT_MIRROR_PRESET=cn
assert_first "cn: npmmirror 优先" "https://npmmirror.com/mirrors/node/setup_20.x" "$(rrt_mirror_urls node_setup)"
rrt_set_mirror_preset auto
RRT_MIRROR_PRESET=global
assert_first "global: NodeSource 优先" "https://deb.nodesource.com/setup_20.x" "$(rrt_mirror_urls node_setup)"
rrt_set_mirror_preset auto
assert_first "auto(node_bin): npmmirror 优先" \
    "https://npmmirror.com/mirrors/node/v20.18.1/node-v20.18.1-linux-x64.tar.xz" \
    "$(rrt_mirror_urls node_bin v20.18.1 node-v20.18.1-linux-x64.tar.xz)"
RRT_MIRROR_PRESET=global
assert_first "global(node_bin): 官方优先" \
    "https://nodejs.org/dist/v20.18.1/node-v20.18.1-linux-x64.tar.xz" \
    "$(rrt_mirror_urls node_bin v20.18.1 node-v20.18.1-linux-x64.tar.xz)"
rrt_set_mirror_preset auto

echo "== 5. npm registry 附加参数 =="
assert_eq "auto: 不注入（空）" "" "$(rrt_mirror_npm_registry)"
RRT_MIRROR_PRESET=global
assert_eq "global: 不注入（空）" "" "$(rrt_mirror_npm_registry)"
rrt_set_mirror_preset auto
RRT_MIRROR_PRESET=cn
assert_eq "cn: 注入 npmmirror" "https://registry.npmmirror.com/" "$(rrt_mirror_npm_registry)"
rrt_set_mirror_preset auto

echo "== 6. 未知 kind =="
if rrt_mirror_urls bogus_kind >/dev/null 2>&1; then fail "未知 kind 应返回 1"; else ok "未知 kind 返回 1"; fi

echo
echo "结果: PASS=$PASS FAIL=$FAIL"
[[ $FAIL -eq 0 ]]
