#!/usr/bin/env bash
# ============================================================
# G-5 网络适配收口 桩测试（DES-2026-0912-03 §8 / task-26）
# ============================================================
# 口径对齐 deploy-mirrors.test.sh：纯函数级 + 静态断言，无需 root/网络/服务器。
# 覆盖：
#   ① lib/net.sh：IPv6 方括号（rrt_host_of_address / rrt_url_host / rrt_port_of_address）
#   ② lib/net.sh：IPv6 私网判定 + use_external_ip 推导
#   ③ lib/net.sh：rrt_apply_system_proxy 的 dry-run 分支（不写 /etc）
#   ④ 两份部署脚本的 G-5 参数已接线（静态断言：参数名/参数化变量/模板文件）
# 运行：bash server/scripts/tests/deploy-g5.test.sh
# 退出码：0 = 全部通过；1 = 有用例失败
# ============================================================
set -u
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB_DIR="$SCRIPT_DIR/../lib"
DEPLOY_DIR="$SCRIPT_DIR/.."

PASS=0; FAIL=0
t_ok()   { PASS=$((PASS+1)); printf '  ok  - %s\n' "$1"; }
t_fail() { FAIL=$((FAIL+1)); printf '  FAIL - %s\n' "$1"; }
ok()   { t_ok "$1"; }
fail() { t_fail "$1"; }
assert_eq() { # desc expected actual
    if [[ "$2" == "$3" ]]; then ok "$1"; else fail "$1（期望 [$2] 实际 [$3]）"; fi
}
assert_file_has() { # desc file pattern
    if grep -qF -- "$3" "$2" 2>/dev/null; then ok "$1"; else fail "$1（$2 未包含: $3）"; fi
}
assert_file_lacks() { # desc file pattern
    if grep -qF -- "$3" "$2" 2>/dev/null; then fail "$1（$2 仍包含: $3）"; else ok "$1"; fi
}
assert_contains() { # desc haystack needle
    if [[ "$2" == *"$3"* ]]; then ok "$1"; else fail "$1（未包含: $3；实际: $2）"; fi
}

# shellcheck source=/dev/null
. "$LIB_DIR/net.sh"
rrt_fail() { printf '%s\n' "$1" >&2; return 1; }   # net.sh 依赖宿主注入的 rrt_fail

echo "== 1. rrt_host_of_address：IPv6 方括号（G-5 缺陷修复）=="
assert_eq "IPv4 + 端口"                 "1.2.3.4"        "$(rrt_host_of_address 'http://1.2.3.4:8080')"
assert_eq "域名 + 路径"                 "example.com"    "$(rrt_host_of_address 'https://example.com/path/x')"
assert_eq "裸 IPv4"                     "192.168.1.10"   "$(rrt_host_of_address '192.168.1.10')"
assert_eq "IPv6 带方括号 + 端口"        "2001:db8::1"    "$(rrt_host_of_address 'http://[2001:db8::1]:8080')"
assert_eq "IPv6 带方括号 + 路径"        "2001:db8::1"    "$(rrt_host_of_address 'https://[2001:db8::1]/admin')"
assert_eq "IPv6 裸方括号（无 scheme）"  "::1"            "$(rrt_host_of_address '[::1]')"
assert_eq "旧实现会截成 '[2001' 的回归" "2001:db8::10"   "$(rrt_host_of_address 'http://[2001:db8::10]:50100')"
assert_eq "带认证段"                    "example.com"    "$(rrt_host_of_address 'http://user:pass@example.com:8080/x')"

echo "== 2. rrt_url_host：URL 方括号化 =="
assert_eq "裸 IPv6 补方括号"  "[2001:db8::1]" "$(rrt_url_host '2001:db8::1')"
assert_eq "已带方括号保持"    "[::1]"         "$(rrt_url_host '[::1]')"
assert_eq "IPv4 原样"         "1.2.3.4"       "$(rrt_url_host '1.2.3.4')"
assert_eq "域名原样"          "example.com"   "$(rrt_url_host 'example.com')"

echo "== 3. rrt_port_of_address：NAT 外部端口解析 =="
assert_eq "IPv4 + 端口"            "50100" "$(rrt_port_of_address 'http://203.0.113.10:50100')"
assert_eq "无端口（https 默认）"   ""      "$(rrt_port_of_address 'https://voice.example.com')"
assert_eq "IPv6 + 端口 + 路径"     "50101" "$(rrt_port_of_address 'ws://[2001:db8::1]:50101/rtc')"
assert_eq "ws 带端口"              "7880"  "$(rrt_port_of_address 'ws://10.0.0.5:7880')"

echo "== 4. rrt_is_private_host：IPv4 + IPv6 私网 =="
for h in 127.0.0.1 10.1.2.3 172.20.0.1 192.168.1.10 localhost ::1 '[::1]' fc00::1 fd00::5 fe80::1; do
    if rrt_is_private_host "$h"; then ok "私网判定: $h"; else fail "私网判定: $h 应为私网"; fi
done
for h in 8.8.8.8 2001:db8::1 example.com 100.64.0.1; do
    if rrt_is_private_host "$h"; then fail "私网判定: $h 不应为私网"; else ok "非私网判定: $h"; fi
done

echo "== 5. rrt_derive_use_external_ip：NJ-11 推导 =="
assert_eq "公网 IPv4 → true"      "true"  "$(rrt_derive_use_external_ip 'http://203.0.113.10:8080')"
assert_eq "私网 IPv4 → false"     "false" "$(rrt_derive_use_external_ip 'http://192.168.1.10:8080')"
assert_eq "域名 → true"           "true"  "$(rrt_derive_use_external_ip 'https://voice.example.com')"
assert_eq "IPv6 私网 → false"     "false" "$(rrt_derive_use_external_ip 'http://[fd00::1]:8080')"

echo "== 6. rrt_apply_system_proxy：dry-run 不写 /etc =="
rrt_pkg_manager() { printf 'apt\n'; }
out="$(RRT_DRY_RUN=true rrt_apply_system_proxy 'http://127.0.0.1:7890' '' 2>&1)"
assert_contains "apt dry-run 输出含 [dry-run]" "$out" "[dry-run]"
assert_contains "apt dry-run 含目标文件"        "$out" "/etc/apt/apt.conf.d/99rrt-proxy"
rrt_pkg_manager() { printf 'dnf\n'; }
out="$(RRT_DRY_RUN=true rrt_apply_system_proxy '' 'http://127.0.0.1:7891' 2>&1)"
assert_contains "dnf dry-run 含 dnf.conf" "$out" "/etc/dnf/dnf.conf"
out="$(RRT_DRY_RUN=true rrt_apply_system_proxy '' '' 2>&1)"
assert_eq "无代理参数时零输出" "" "$out"
unset -f rrt_pkg_manager

echo "== 7. 部署脚本 G-5 参数已接线（静态断言）=="
assert_file_has "baremetal 支持 --public-address"    "$DEPLOY_DIR/deploy-baremetal.sh" "--public-address)"
assert_file_has "baremetal 支持 --public-admin-address" "$DEPLOY_DIR/deploy-baremetal.sh" "--public-admin-address)"
assert_file_has "baremetal 支持 --public-lk-url"     "$DEPLOY_DIR/deploy-baremetal.sh" "--public-lk-url)"
assert_file_has "baremetal 支持 --nat"               "$DEPLOY_DIR/deploy-baremetal.sh" "--nat)"
assert_file_has "baremetal 支持 --system-proxy"      "$DEPLOY_DIR/deploy-baremetal.sh" "--system-proxy)"
assert_file_has "baremetal CORS 走参数化变量"        "$DEPLOY_DIR/deploy-baremetal.sh" 'RRT_CORS_ORIGINS=$RRT_CORS_VALUE'
assert_file_has "baremetal 使用 rrt_url_host"        "$DEPLOY_DIR/deploy-baremetal.sh" "rrt_url_host"
assert_file_has "baremetal NAT 打印映射清单"         "$DEPLOY_DIR/deploy-baremetal.sh" "NAT/端口映射清单"
assert_file_has "embedded 支持 --mirror-preset"      "$DEPLOY_DIR/deploy-embedded.sh" "--mirror-preset)"
assert_file_has "embedded 支持 --http-proxy"         "$DEPLOY_DIR/deploy-embedded.sh" "--http-proxy)"
assert_file_has "embedded 支持 --nat"                "$DEPLOY_DIR/deploy-embedded.sh" "--nat)"
assert_file_has "embedded 支持 --system-proxy"       "$DEPLOY_DIR/deploy-embedded.sh" "--system-proxy)"
assert_file_has "embedded 使用 CORS 参数化变量"      "$DEPLOY_DIR/deploy-embedded.sh" 'RRT_CORS_ORIGINS=$CORS_PUBLIC_VALUE'
assert_file_lacks "baremetal 不再硬编码 LiveKit 公网 URL" "$DEPLOY_DIR/deploy-baremetal.sh" 'RRT_LIVEKIT_PUBLIC_URL=ws://${PUBLIC_IP}:$PORT_LK_WS'

echo "== 8. TLS 三档模板存在 =="
for f in nginx-tls-selfsigned.conf.tmpl nginx-tls-acme.conf.tmpl caddy-tls.conf.tmpl README.md; do
    if [[ -s "$DEPLOY_DIR/templates/$f" ]]; then ok "模板存在: $f"; else fail "模板缺失: $f"; fi
done

echo
printf 'PASS=%d FAIL=%d\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]] || exit 1
