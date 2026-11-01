package dm

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/message"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// TestDMChannelFlowEndToEnd 是私信（DM）路由注册后的端到端用例，覆盖 1.9.4
// 「私信路由从未在 internal/server/routes.go 注册，线上恒 404」的修复：
//
//  1. 未登录访问 -> 401（而不是 404，证明路由确实已挂上且鉴权生效）；
//  2. A 创建与 B 的会话 -> 200 且 type=DM；
//  3. A 发消息 -> 200；
//  4. B 列表能看到该会话、能读到 A 的消息；
//  5. C（第三方，既不是会话双方）读会话/读消息/发消息 -> 403（越权断言）。
//
// 全流程使用真实 SQLite（testutil.MustSetupTestDB 内存库 + 生产 AutoMigrate
// 清单）、真实 gin 路由与真实 AuthRequired 中间件，不 mock service。
// DM 频道不属于任何 Space（type='DM'），所以请求不带 X-Space-ID 也必须可用——
// 这同时锁定「不要给 DM 挂 RequireSpaceID」的设计约束。
func TestDMChannelFlowEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret-for-dm-e2e-32bytes!!"
	cfg.EncryptionKey = "test-encryption-key-for-dm-e2e-32by"

	msgSvc := message.NewService(db)
	hub := realtime.NewHub(nil)
	handler := NewHandler(db, cfg, msgSvc, hub)

	router := gin.New()
	// 与 internal/server/routes.go 的真实装配一致：同一 handler 同时挂在
	// /api/v1（新前缀）与 /api（legacy 前缀，客户端 web/shared/api-core.ts 用的
	// 是 /api）。
	handler.RegisterRoutes(router.Group("/api/v1"))
	handler.RegisterRoutes(router.Group("/api"))

	// 三个用户：A/B 是私信双方，C 是第三方（不在任何 DM 会话里）。
	users := make([]*model.User, 3)
	for i, name := range []string{"dm-alice", "dm-bob", "dm-carol"} {
		u := &model.User{
			ID:           idgen.NextString(),
			Username:     name,
			Email:        name + "@example.com",
			PasswordHash: "x",
			Role:         middleware.RoleMember,
		}
		if err := db.Create(u).Error; err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		users[i] = u
	}
	alice, bob, carol := users[0], users[1], users[2]

	tokenOf := func(u *model.User) string {
		t.Helper()
		accessToken, _, err := middleware.GenerateTokenPair(
			u.ID, u.Username, u.Email, u.Role, u.TokenVersion, "", "", cfg,
		)
		if err != nil {
			t.Fatalf("generate token for %s: %v", u.Username, err)
		}
		return accessToken
	}
	tokenAlice, tokenBob, tokenCarol := tokenOf(alice), tokenOf(bob), tokenOf(carol)

	do := func(method, path, token string, body interface{}) *httptest.ResponseRecorder {
		t.Helper()
		var reader *bytes.Reader
		if body == nil {
			reader = bytes.NewReader(nil)
		} else {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			reader = bytes.NewReader(raw)
		}
		req, err := http.NewRequest(method, path, reader)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// 1) 未登录 -> 401（修复前这里是 404 SYSTEM_NOT_FOUND，因为路由从未注册）。
	w := do("GET", "/api/v1/dm/channels", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录访问 DM 列表应 401（证明路由已注册且鉴权生效），实际 %d: %s", w.Code, w.Body.String())
	}
	for _, p := range []struct{ method, path string }{
		{"POST", "/api/v1/dm/channels"},
		{"GET", "/api/v1/dm/channels/ch_x/messages"},
		{"POST", "/api/v1/dm/channels/ch_x/messages"},
	} {
		if w := do(p.method, p.path, "", nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("未登录 %s %s 应 401，实际 %d: %s", p.method, p.path, w.Code, w.Body.String())
		}
	}

	// 2) A 创建与 B 的会话。
	w = do("POST", "/api/v1/dm/channels", tokenAlice, map[string]string{"recipientId": bob.ID})
	if w.Code != http.StatusOK {
		t.Fatalf("创建 DM 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var createResp struct {
		Code string                 `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("解析创建响应: %v (%s)", err, w.Body.String())
	}
	channelID, _ := createResp.Data["id"].(string)
	if channelID == "" {
		t.Fatalf("创建响应缺少频道 id: %s", w.Body.String())
	}
	if got, _ := createResp.Data["type"].(string); got != "DM" {
		t.Errorf("DM 频道 type = %q, want %q", got, "DM")
	}

	// 幂等：A 再建一次同会话应返回同一个频道（不重复建）。
	w = do("POST", "/api/v1/dm/channels", tokenAlice, map[string]string{"recipientId": bob.ID})
	if w.Code != http.StatusOK {
		t.Fatalf("重复创建 DM 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var again struct {
		Data map[string]interface{} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &again)
	if id, _ := again.Data["id"].(string); id != channelID {
		t.Errorf("重复创建返回不同频道：%q != %q", id, channelID)
	}

	// 3) A 发消息。
	const content = "hello from alice (dm e2e)"
	w = do("POST", "/api/v1/dm/channels/"+channelID+"/messages", tokenAlice, map[string]string{"content": content})
	if w.Code != http.StatusOK {
		t.Fatalf("A 发私信应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	// 4) B 列表能看到该会话、能读到 A 的消息。
	w = do("GET", "/api/v1/dm/channels", tokenBob, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("B 拉 DM 列表应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("解析 B 列表响应: %v (%s)", err, w.Body.String())
	}
	found := false
	for _, ch := range listResp.Data {
		if id, _ := ch["id"].(string); id == channelID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("B 的 DM 列表应包含会话 %s，实际 %s", channelID, w.Body.String())
	}

	w = do("GET", "/api/v1/dm/channels/"+channelID+"/messages", tokenBob, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("B 读私信消息应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var msgResp struct {
		Data struct {
			Items   []map[string]interface{} `json:"items"`
			HasMore bool                     `json:"hasMore"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &msgResp); err != nil {
		t.Fatalf("解析 B 消息响应: %v (%s)", err, w.Body.String())
	}
	if len(msgResp.Data.Items) != 1 {
		t.Fatalf("B 应读到 1 条消息，实际 %d: %s", len(msgResp.Data.Items), w.Body.String())
	}
	if got, _ := msgResp.Data.Items[0]["content"].(string); got != content {
		t.Errorf("消息内容 = %q, want %q", got, content)
	}
	if got, _ := msgResp.Data.Items[0]["userId"].(string); got != alice.ID {
		t.Errorf("消息作者 = %q, want %q", got, alice.ID)
	}

	// 5) C（第三方）越权：读消息、发消息必须 403。
	w = do("GET", "/api/v1/dm/channels/"+channelID+"/messages", tokenCarol, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("第三方读他人私信应 403，实际 %d: %s", w.Code, w.Body.String())
	}
	w = do("POST", "/api/v1/dm/channels/"+channelID+"/messages", tokenCarol, map[string]string{"content": "intruder"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("第三方往他人私信发消息应 403，实际 %d: %s", w.Code, w.Body.String())
	}
	// C 的列表里不应出现该会话（越权信息泄露也要挡）。
	w = do("GET", "/api/v1/dm/channels", tokenCarol, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("第三方拉自己 DM 列表应 200（空列表），实际 %d: %s", w.Code, w.Body.String())
	}
	var carolList struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &carolList); err != nil {
		t.Fatalf("解析 C 列表响应: %v (%s)", err, w.Body.String())
	}
	for _, ch := range carolList.Data {
		if id, _ := ch["id"].(string); id == channelID {
			t.Fatalf("第三方的 DM 列表不应包含他人会话 %s: %s", channelID, w.Body.String())
		}
	}

	// 补充：C 与 B 建会话后，A 仍不能读该新会话（双向隔离）。
	w = do("POST", "/api/v1/dm/channels", tokenCarol, map[string]string{"recipientId": bob.ID})
	if w.Code != http.StatusOK {
		t.Fatalf("C 与 B 建 DM 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var cbResp struct {
		Data map[string]interface{} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &cbResp)
	cbChannelID, _ := cbResp.Data["id"].(string)
	if cbChannelID == "" || cbChannelID == channelID {
		t.Fatalf("C↔B 会话 id 异常：%q（A↔B 为 %q）", cbChannelID, channelID)
	}
	if w := do("GET", "/api/v1/dm/channels/"+cbChannelID+"/messages", tokenAlice, nil); w.Code != http.StatusForbidden {
		t.Fatalf("A 读 C↔B 私信应 403，实际 %d: %s", w.Code, w.Body.String())
	}

	// 6) legacy /api 前缀同样可用：客户端 web/shared/api-core.ts 走 /api（真实
	// 部署里 registerRoutes 会把同一 handler 同时挂 /api/v1 与 /api）。
	if w := do("GET", "/api/dm/channels", tokenBob, nil); w.Code != http.StatusOK {
		t.Fatalf("legacy /api 前缀拉 DM 列表应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if w := do("GET", "/api/dm/channels/"+channelID+"/messages", tokenCarol, nil); w.Code != http.StatusForbidden {
		t.Fatalf("legacy /api 前缀下第三方读他人私信应 403，实际 %d: %s", w.Code, w.Body.String())
	}
}
