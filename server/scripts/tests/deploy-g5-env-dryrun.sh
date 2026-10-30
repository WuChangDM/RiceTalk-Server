#!/usr/bin/env bash
# ============================================================
# G-5「.env 推导」离线演练（dry-run，无需 root / 网络 / 真实服务器）
# ============================================================
# 做法：从 deploy-baremetal.sh 中**原样抽出** .env 生成前的推导块
#       （PUBLIC_ADDRESS_ARG … NAT 清单，不含 heredoc 与磁盘写入），
#       在沙箱里给不同参数赋值后执行，断言四类场景的推导结果。
#       抽出的是脚本真实代码文本，不是复制品 —— 脚本改动会直接反映到本测试。
# 运行：bash server/scripts/tests/deploy-g5-env-dryrun.sh
# ============================================================
set -u
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$SCRIPT_DIR/.."
BARE="$DEPLOY_DIR/deploy-baremetal.sh"

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); printf '  ok  - %s\n' "$1"; }
no()   { FAIL=$((FAIL+1)); printf '  FAIL - %s\n' "$1"; }
eq() { if [[ "$2" == "$3" ]]; then ok "$1"; else no "$1（期望 [$2] 实际 [$3]）"; fi; }

# shellcheck source=/dev/null
. "$DEPLOY_DIR/lib/net.sh"
info() { printf '%s\n' "$*"; }
warn() { printf 'WARN %s\n' "$*" >&2; }

# ---- 抽出脚本真实的推导块（从 PUBLIC_ADDRESS_ARG 分支到 N27 说明注释之前）----
BLOCK="$(awk '
    /^ +if \[\[ -n "\$PUBLIC_ADDRESS_ARG" \]\]/ { on=1 }
    on && /^ +# N27 说明（置于 heredoc 外/ { exit }
    on { print }
' "$BARE")"
if [[ -z "$BLOCK" ]]; then
    echo "FATAL: 未能从 $BARE 抽出 .env 推导块（脚本结构已变？）" >&2
    exit 1
fi
printf '抽出推导块 %d 行\n\n' "$(printf '%s\n' "$BLOCK" | wc -l | tr -d ' ')"

derive() { # $1=scenario标签，其余环境变量由调用方已 export
    (
        # 脚本默认值（与被测脚本一致）：端口 8080/9090/7880/7881/7882
        PORT_API="${PORT_API:-8080}"; PORT_ADMIN="${PORT_ADMIN:-9090}"
        PORT_LK_WS="${PORT_LK_WS:-7880}"; PORT_LK_TCP="${PORT_LK_TCP:-7881}"; PORT_LK_UDP="${PORT_LK_UDP:-7882}"
        TLS_MODE="${TLS_MODE:-none}"; NAT_MODE="${NAT_MODE:-false}"
        PUBLIC_IP="${PUBLIC_IP:-203.0.113.10}"
        eval "$BLOCK"
        printf 'PUBLIC=%s\nADMIN=%s\nLK=%s\nCORS=%s\n' \
            "$RRT_PUBLIC_ADDRESS" "$RRT_ADMIN_PUBLIC_ADDRESS" "$RRT_LIVEKIT_PUBLIC_URL" "$RRT_CORS_VALUE"
    )
}
field() { printf '%s\n' "$1" | sed -n "s/^$2=//p"; }

echo "== 场景 A：不加任何 G-5 参数（必须与旧行为逐字节一致）=="
out="$(PUBLIC_ADDRESS_ARG="" PUBLIC_ADMIN_ADDRESS_ARG="" PUBLIC_LK_URL_ARG="" derive A)"
eq "PUBLIC 旧口径" "$(field "$out" PUBLIC)" "http://203.0.113.10:8080"
eq "ADMIN 由 host+port-admin 推导" "$(field "$out" ADMIN)" "http://203.0.113.10:9090"
eq "LIVEKIT 旧口径 ws://IP:7880" "$(field "$out" LK)" "ws://203.0.113.10:7880"
eq "CORS 旧四段" "$(field "$out" CORS)" \
   "http://203.0.113.10:8080,http://203.0.113.10:9090,http://localhost:8080,http://localhost:9090"

echo "== 场景 B：IPv6 对外地址（方括号归一 + 自动补括号）=="
out="$(PUBLIC_ADDRESS_ARG='http://[2001:db8::10]:8080' PUBLIC_ADMIN_ADDRESS_ARG="" PUBLIC_LK_URL_ARG="" derive B)"
eq "PUBLIC 保留方括号" "$(field "$out" PUBLIC)" "http://[2001:db8::10]:8080"
eq "ADMIN 自动加方括号" "$(field "$out" ADMIN)" "http://[2001:db8::10]:9090"
eq "LIVEKIT 自动加方括号" "$(field "$out" LK)" "ws://[2001:db8::10]:7880"

echo "== 场景 C：纯 NAT（外部端口 ≠ 监听端口，打印映射清单）=="
out="$(PUBLIC_ADDRESS_ARG='http://203.0.113.10:50100' \
       PUBLIC_ADMIN_ADDRESS_ARG='http://203.0.113.10:50103' \
       PUBLIC_LK_URL_ARG='ws://203.0.113.10:50101' \
       NAT_MODE=true derive C)"
eq "PUBLIC 外部端口" "$(field "$out" PUBLIC)" "http://203.0.113.10:50100"
eq "ADMIN 外部端口" "$(field "$out" ADMIN)" "http://203.0.113.10:50103"
eq "LIVEKIT 外部端口" "$(field "$out" LK)" "ws://203.0.113.10:50101"
eq "CORS 用外部地址" "$(field "$out" CORS)" \
   "http://203.0.113.10:50100,http://203.0.113.10:50103,http://localhost:8080,http://localhost:9090"
for needle in "NAT/端口映射清单" "API         8080/tcp → 50100/tcp" \
              "Admin       9090/tcp → 50103/tcp" "LiveKit WS  7880/tcp → 50101/tcp" \
              "LiveKit TCP 7881/tcp → 7881/tcp（服务端固定，必须 1:1）" \
              "LiveKit UDP 7882/udp → 7882/udp（服务端固定，必须 1:1）"; do
    if [[ "$out" == *"$needle"* ]]; then ok "清单行存在：$needle"; else no "清单行缺失：$needle"; fi
done

echo "== 场景 D：域名 + ACME（https 推导 + wss + 反代路径）=="
out="$(PUBLIC_ADDRESS_ARG='https://voice.example.com' PUBLIC_ADMIN_ADDRESS_ARG="" \
       PUBLIC_LK_URL_ARG='wss://voice.example.com/rtc' TLS_MODE=acme derive D)"
eq "PUBLIC https" "$(field "$out" PUBLIC)" "https://voice.example.com"
eq "ADMIN https+域" "$(field "$out" ADMIN)" "https://voice.example.com:9090"
eq "LIVEKIT 反代路径优先" "$(field "$out" LK)" "wss://voice.example.com/rtc"
eq "CORS 含 https 与 localhost https?" "$(field "$out" CORS)" \
   "https://voice.example.com,https://voice.example.com:9090,https://localhost:8080,https://localhost:9090"

echo "== 场景 E：长 IPv6 + NAT 端口（host 解析 + 端口解析组合）=="
out="$(PUBLIC_ADDRESS_ARG='http://[2001:db8:abcd:12::99]:50100' PUBLIC_ADMIN_ADDRESS_ARG="" \
       PUBLIC_LK_URL_ARG="" NAT_MODE=true derive E)"
eq "PUBLIC 长 IPv6" "$(field "$out" PUBLIC)" "http://[2001:db8:abcd:12::99]:50100"
eq "LIVEKIT 长 IPv6 补括号" "$(field "$out" LK)" "ws://[2001:db8:abcd:12::99]:7880"
if [[ "$out" == *"API         8080/tcp → 50100/tcp"* ]]; then ok "NAT 从 IPv6 URL 正确取端口"; else no "NAT 从 IPv6 URL 取端口失败"; fi

echo
printf 'PASS=%d FAIL=%d\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]] || exit 1
