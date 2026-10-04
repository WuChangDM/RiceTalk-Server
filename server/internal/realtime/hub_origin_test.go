package realtime

import (
	"net/http/httptest"
	"testing"
)

// N18：CheckOrigin 对「空 Origin + 鉴权凭据」的放行契约。
// 背景：桌面客户端（Electron file:// 渲染层）的 WebSocket 握手不带 Origin 头，
// 生产模式下（RRT_ENV=production）空 Origin 一律被拒，实时链路（消息/typing/
// presence）在所有生产部署上全断（T26 双客户端实测发现）。本文件锁定：
// 空 Origin 但携带 token 查询参数 / Authorization / Sec-WebSocket-Protocol
// 时放行；空 Origin 且无凭据在生产拒绝（开发放行，维持旧行为）。
func TestCheckOrigin_EmptyOriginWithCredentials(t *testing.T) {
	t.Setenv("RRT_ENV", "production")
	t.Setenv("RRT_PUBLIC_ADDRESS", "")
	t.Setenv("RRT_CORS_ORIGINS", "")

	req := httptest.NewRequest("GET", "http://server.example/ws?token=abc", nil)
	if !Upgrader.CheckOrigin(req) {
		t.Fatal("empty origin + token query must be allowed (desktop client, N18)")
	}

	req = httptest.NewRequest("GET", "http://server.example/ws?access_token=abc", nil)
	if !Upgrader.CheckOrigin(req) {
		t.Fatal("empty origin + access_token query must be allowed (N18)")
	}

	req = httptest.NewRequest("GET", "http://server.example/ws", nil)
	req.Header.Set("Authorization", "Bearer abc")
	if !Upgrader.CheckOrigin(req) {
		t.Fatal("empty origin + Authorization header must be allowed (N18)")
	}

	req = httptest.NewRequest("GET", "http://server.example/ws", nil)
	req.Header.Set("Sec-WebSocket-Protocol", "access_token.abc")
	if !Upgrader.CheckOrigin(req) {
		t.Fatal("empty origin + Sec-WebSocket-Protocol must be allowed (N18)")
	}

	// 空 Origin 且无任何凭据：生产拒绝（维持旧行为——无凭据请求反正会被
	// 鉴权中间件拒绝，这里不放宽）。
	req = httptest.NewRequest("GET", "http://server.example/ws", nil)
	if Upgrader.CheckOrigin(req) {
		t.Fatal("empty origin without credentials must be rejected in production")
	}
}

// 恶意/陌生 Origin 在生产仍然拒绝（不因 N18 放宽）。
func TestCheckOrigin_DisallowedOriginStillRejected(t *testing.T) {
	t.Setenv("RRT_ENV", "production")
	t.Setenv("RRT_PUBLIC_ADDRESS", "")
	t.Setenv("RRT_CORS_ORIGINS", "")

	req := httptest.NewRequest("GET", "http://server.example/ws?token=abc", nil)
	req.Header.Set("Origin", "https://evil.example")
	if Upgrader.CheckOrigin(req) {
		t.Fatal("disallowed browser origin must stay rejected even with token")
	}
}

// 配置放行（RRT_CORS_ORIGINS）对非空 Origin 继续生效（file:// 部署变通路径）。
func TestCheckOrigin_ConfiguredOriginAllowed(t *testing.T) {
	t.Setenv("RRT_ENV", "production")
	t.Setenv("RRT_PUBLIC_ADDRESS", "")
	t.Setenv("RRT_CORS_ORIGINS", "file://,null")

	req := httptest.NewRequest("GET", "http://server.example/ws?token=abc", nil)
	req.Header.Set("Origin", "file://")
	if !Upgrader.CheckOrigin(req) {
		t.Fatal("configured file:// origin must be allowed")
	}
}
