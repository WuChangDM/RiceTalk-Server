# shellcheck shell=bash
# ============================================================
# WS 握手部署自检（N27 收口检查 · 能力层）
# ============================================================
# 背景：N27 事故——部署批次把 .env.production 的 RRT_CORS_ORIGINS 写成
# https-only，HTTP 部署下桌面端 origin-rewrite 的 http://<ip>:<port> 被
# hub CheckOrigin 拒绝 → WS 全 403，实时链路全断。此故障在部署当时完全
# 静默。本库提供部署收尾的最小握手探测，让这类回归在部署当时暴露。
#
# 用法（由 deploy-baremetal.sh / deploy-embedded.sh 在收尾阶段 source 后调用）：
#   rrt_verify_ws_handshake <scheme> <host> <api_port> [jwt]
#
# 依赖宿主脚本已定义日志函数：ok / warn / fail / info。
# 返回：0=通过（含无凭据时的「受限通过」） 1=失败（部署应判 FAIL）。
#
# 裁决规则（与服务端 /ws 鉴权-升级顺序对齐：token 校验在 origin 检查之前）：
#   - 携带 JWT（第 4 参或环境变量 RRT_WS_PROBE_TOKEN，期望 Owner 登录拿到的
#     accessToken）：
#       101            → PASS：origin 白名单 + 鉴权 + 升级链路全通（全量自检）
#       401            → FAIL：token 被拒（无效/过期）
#       403            → FAIL：origin 被 CORS 白名单拒绝（正是 N27 故障签名）
#       其他/连接失败  → FAIL
#   - 无 JWT（全新部署尚无 Owner；.bootstrap_token 不是 JWT，不能过 /ws 鉴权）：
#       401            → 受限 PASS：鉴权层按预期拒绝匿名探测，可证明路由与
#                        中间件链路存活；但 origin 白名单在无凭据时不可证伪，
#                        打印提示引导设置 RRT_WS_PROBE_TOKEN 复检。
#       403            → FAIL（不应出现，出现即异常）
#       其他/连接失败  → FAIL
#
# curl 缺失时告警并跳过（返回 0）：自检不应比部署主流程更硬性。

rrt_verify_ws_handshake() {
    local scheme="$1" host="$2" api_port="$3"
    local jwt="${4:-${RRT_WS_PROBE_TOKEN:-}}"

    if ! command -v curl &>/dev/null; then
        warn "curl 不可用，跳过 WS 握手自检（无法覆盖 N27 类回归，请部署后手工验证实时消息）"
        return 0
    fi
    if [[ -z "$scheme" || -z "$host" || -z "$api_port" ]]; then
        warn "WS 握手自检参数不全（scheme/host/port），跳过"
        return 0
    fi

    local origin="${scheme}://${host}:${api_port}"
    local url="${origin}/ws"
    [[ -n "$jwt" ]] && url="${origin}/ws?token=${jwt}"

    local status curl_rc=0
    status="$(curl -s --max-time 8 -o /dev/null -w '%{http_code}' \
        -H 'Connection: Upgrade' \
        -H 'Upgrade: websocket' \
        -H 'Sec-WebSocket-Version: 13' \
        -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
        -H "Origin: ${origin}" \
        "$url" 2>/dev/null)" || curl_rc=$?

    if [[ $curl_rc -ne 0 || -z "$status" || "$status" == "000" ]]; then
        fail "WS 握手自检失败：无法连接 ${origin}/ws（curl rc=${curl_rc}）"
        info "  后端未监听 / 防火墙拦截 / 反代未转发 Upgrade 头均会导致此结果"
        return 1
    fi

    if [[ "$status" == "101" ]]; then
        ok "WS 握手自检通过（101 Switching Protocols，origin=${origin}）"
        return 0
    fi

    if [[ "$status" == "403" ]]; then
        fail "WS 握手自检失败：HTTP 403 —— origin ${origin} 被 CORS 白名单拒绝（N27 故障签名）"
        info "  排查：检查 .env.production 的 RRT_CORS_ORIGINS 是否包含 ${origin}"
        info "  修复：CORS 白名单应由实际部署协议+端口生成（禁止硬编码 https），改后 systemctl restart ridgericetalk"
        return 1
    fi

    if [[ -n "$jwt" && "$status" == "401" ]]; then
        fail "WS 握手自检失败：HTTP 401 —— 探测 token 被拒（无效或已过期），请换有效 accessToken 重跑"
        return 1
    fi

    if [[ -z "$jwt" && "$status" == "401" ]]; then
        warn "WS 握手自检（受限）通过：401=鉴权层按预期拒绝匿名探测（origin=${origin}）"
        warn "  无 JWT 无法验证 CORS 白名单；设置 RRT_WS_PROBE_TOKEN=<accessToken> 重跑部署可得 101 全量自检"
        return 0
    fi

    fail "WS 握手自检失败：非预期状态码 ${status}（origin=${origin}，预期 101）"
    return 1
}

# ------------------------------------------------------------
# rrt_ws_deploy_selfcheck <env_file> <fallback_host> <fallback_port> [jwt]
# 部署收尾自检（N27 收口，deploy-baremetal.sh / deploy-embedded.sh 共用）：
#   1) 从 env_file 读 RRT_PUBLIC_ADDRESS（缺失回退 http://<fallback_host>:<fallback_port>），
#      解析出「客户端实际使用的对外 origin」（scheme/host/port）；
#   2) CORS 一致性：RRT_CORS_ORIGINS 必须包含该 origin —— 这是 N27 根因
#      （白名单写成 https-only、实际部署是 HTTP）的确定性检测，无凭据也能判；
#   3) WS 握手探测（rrt_verify_ws_handshake）：有 JWT 时要求 101 全量通过，
#      无 JWT 时受限通过（401）。
# 任一步失败返回 1（部署应判 FAIL），由调用方决定退出。
# ------------------------------------------------------------
rrt_ws_deploy_selfcheck() {
    local env_file="$1" fb_host="$2" fb_port="$3"
    local jwt="${4:-${RRT_WS_PROBE_TOKEN:-}}"

    if [[ ! -f "$env_file" ]]; then
        warn "未找到 $env_file，跳过 CORS 一致性检查，仅做 WS 握手探测"
        rrt_verify_ws_handshake "http" "$fb_host" "$fb_port" "$jwt"
        return $?
    fi

    local addr
    addr="$( (grep -E '^RRT_PUBLIC_ADDRESS=' "$env_file" 2>/dev/null | head -1 | cut -d= -f2- | tr -d '[:space:]') || true)"
    [[ -z "$addr" ]] && addr="http://${fb_host}:${fb_port}"

    # 解析 scheme/host/port（支持 https://host:port 与 https://host 两种形态）
    local scheme="http" rest host port
    case "$addr" in
        https://*|wss://*) scheme="https" ;;
    esac
    rest="${addr#*://}"
    rest="${rest%%/*}"
    host="${rest%%:*}"
    port="${rest#*:}"
    [[ "$port" == "$rest" ]] && port=""
    if [[ -z "$port" ]]; then
        if [[ "$scheme" == "https" ]]; then port="443"; else port="80"; fi
    fi

    local origin="${scheme}://${host}:${port}"

    # 2) CORS 一致性（N27 根因的确定性检测）
    local cors
    cors="$( (grep -E '^RRT_CORS_ORIGINS=' "$env_file" 2>/dev/null | head -1 | cut -d= -f2- | tr -d '[:space:]') || true)"
    if [[ -z "$cors" ]]; then
        fail "部署自检失败：$env_file 缺少 RRT_CORS_ORIGINS，WS 必被 403（N27 故障签名）"
        return 1
    fi
    if [[ ",${cors}," != *",${origin},"* ]]; then
        fail "部署自检失败：RRT_CORS_ORIGINS 未包含对外 origin ${origin}（白名单协议/端口与实际部署不一致，N27 根因签名）"
        info "  当前白名单: ${cors}"
        info "  修复：白名单应由 useHttps + 实际端口推导（禁止硬编码协议），补入 ${origin} 后 systemctl restart ridgericetalk，再重跑本脚本"
        return 1
    fi
    ok "CORS 一致性检查通过（RRT_CORS_ORIGINS 含 ${origin}）"

    # 3) WS 握手探测
    rrt_verify_ws_handshake "$scheme" "$host" "$port" "$jwt"
}
