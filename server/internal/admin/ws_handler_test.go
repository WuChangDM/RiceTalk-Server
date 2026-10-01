package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// ws_handler.go（GET /admin/ws）此前整文件无测试。它承担两件事：
//   1. 鉴权：JWT 有效 + token 版本未撤销 + 角色为 ADMIN/OWNER，否则不升级连接；
//   2. 过滤：升级后只转发 module_status_changed，其他广播（presence_update 等）
//      必须被丢弃 —— 否则管理端口会泄漏用户在线状态这类面向用户的presence数据。

type wsTestEnv struct {
	hub          *realtime.Hub
	cfg          *config.Config
	db           *gorm.DB
	server       *httptest.Server
	router       *gin.Engine
	ownerToken   string
	memberToken  string
	refreshToken string
	memberUserID string
}

func setupWSHandler(t *testing.T) *wsTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"
	log := testutil.TestLogger()

	owner := &model.User{
		ID: idgen.NextString(), Username: "owner", Email: "owner@example.com",
		PasswordHash: "x", Role: middleware.RoleOwner, IsActive: true,
	}
	member := &model.User{
		ID: idgen.NextString(), Username: "member", Email: "member@example.com",
		PasswordHash: "x", Role: middleware.RoleMember, IsActive: true,
	}
	for _, u := range []*model.User{owner, member} {
		if err := db.Create(u).Error; err != nil {
			t.Fatalf("create user %s: %v", u.Username, err)
		}
	}

	ownerToken, refreshToken, err := middleware.GenerateTokenPair(
		owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg,
	)
	if err != nil {
		t.Fatalf("generate owner token: %v", err)
	}
	memberToken, _, err := middleware.GenerateTokenPair(
		member.ID, member.Username, member.Email, member.Role, member.TokenVersion, "", "", cfg,
	)
	if err != nil {
		t.Fatalf("generate member token: %v", err)
	}

	hub := realtime.NewHub(log)
	go hub.Run()

	handler := NewAdminWSHandler(hub, cfg, db, log)
	router := gin.New()
	router.GET("/admin/ws", handler.HandleWS)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &wsTestEnv{
		hub: hub, cfg: cfg, db: db, server: server, router: router,
		ownerToken: ownerToken, memberToken: memberToken, refreshToken: refreshToken,
		memberUserID: member.ID,
	}
}

// getStatus 直接发一个普通 GET（不带 Upgrade 头），用于验证「未升级即拒绝」。
func (e *wsTestEnv) getStatus(t *testing.T, query string, headers map[string]string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/ws"+query, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

func (e *wsTestEnv) wsURL(query string) string {
	return "ws" + strings.TrimPrefix(e.server.URL, "http") + "/admin/ws" + query
}

func TestAdminWSRejectsUnauthenticated(t *testing.T) {
	env := setupWSHandler(t)

	w, resp := env.getStatus(t, "", nil)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if resp["code"] != "AUTH_UNAUTHORIZED" {
		t.Errorf("code = %v, want AUTH_UNAUTHORIZED", resp["code"])
	}
}

func TestAdminWSRejectsInvalidToken(t *testing.T) {
	env := setupWSHandler(t)

	w, resp := env.getStatus(t, "?token=definitely-not-a-jwt", nil)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if resp["code"] != "AUTH_TOKEN_INVALID" {
		t.Errorf("code = %v, want AUTH_TOKEN_INVALID", resp["code"])
	}
}

// refresh token 不能拿来连 WebSocket（只能用来换 access token）。
func TestAdminWSRejectsRefreshToken(t *testing.T) {
	env := setupWSHandler(t)

	w, resp := env.getStatus(t, "?token="+env.refreshToken, nil)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if resp["code"] != "AUTH_TOKEN_INVALID" {
		t.Errorf("code = %v, want AUTH_TOKEN_INVALID", resp["code"])
	}
}

// 普通成员（MEMBER）即使 JWT 有效也不得连管理端口。
func TestAdminWSRejectsNonAdminRole(t *testing.T) {
	env := setupWSHandler(t)

	w, resp := env.getStatus(t, "?token="+env.memberToken, nil)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	if resp["code"] != "AUTH_FORBIDDEN" {
		t.Errorf("code = %v, want AUTH_FORBIDDEN", resp["code"])
	}
}

// token 版本与库中不一致（用户已登出/被撤销）时必须拒绝，否则撤销形同虚设。
func TestAdminWSRejectsRevokedToken(t *testing.T) {
	env := setupWSHandler(t)

	if err := env.db.Model(&model.User{}).Where("id = ?", env.memberUserID).
		Update("token_version", gorm.Expr("token_version + 1")).Error; err != nil {
		t.Fatalf("bump token version: %v", err)
	}

	w, resp := env.getStatus(t, "?token="+env.memberToken, nil)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if resp["code"] != "AUTH_TOKEN_INVALID" {
		t.Errorf("code = %v, want AUTH_TOKEN_INVALID", resp["code"])
	}
}

// Sec-WebSocket-Protocol 里的 access_token.<jwt> 优先于 query 参数（与主 /ws 一致）。
func TestAdminWSTokenFromSubprotocolHeader(t *testing.T) {
	env := setupWSHandler(t)

	header := "access_token." + env.ownerToken + ", rrt"
	dialer := websocket.Dialer{Subprotocols: []string{"rrt"}}
	conn, resp, err := dialer.Dial(env.wsURL("?token=invalid-should-be-ignored"), http.Header{
		"Sec-WebSocket-Protocol": []string{header},
	})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial with subprotocol token failed (status=%d): %v", status, err)
	}
	defer conn.Close()
}

// 正向 + 负向过滤一次做完：先证明订阅生效（收到 module_status_changed），
// 再证明其他广播被丢弃（presence_update 不来）。
// A11 注：连接建立时服务端会先推一条 admin_alert_snapshot（活跃告警快照，
// 可能为空数组），读取时先消费它再断言 module_status_changed。
func TestAdminWSForwardsOnlyModuleStatusChanged(t *testing.T) {
	env := setupWSHandler(t)

	conn, _, err := websocket.DefaultDialer.Dial(env.wsURL("?token="+env.ownerToken), nil)
	if err != nil {
		t.Fatalf("dial admin ws: %v", err)
	}
	defer conn.Close()

	// 服务端在 Upgrade 之后才 SubscribeAdmin，dial 返回不代表订阅已建立。
	time.Sleep(200 * time.Millisecond)

	env.hub.Broadcast([]byte(`{"type":"module_status_changed","module":"bots","status":"disabled"}`))

	readEvent := func(timeout time.Duration) (string, map[string]interface{}, error) {
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return "", nil, err
		}
		var m map[string]interface{}
		_ = json.Unmarshal(raw, &m)
		typ, _ := m["type"].(string)
		return typ, m, nil
	}

	// 先消费连接建立时的活跃告警快照（A11 新增行为；无活跃告警时为空数组），
	// 然后必须收到 module_status_changed。
	var got map[string]interface{}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for module_status_changed")
		}
		typ, m, err := readEvent(3 * time.Second)
		if err != nil {
			t.Fatalf("did not receive module_status_changed: %v", err)
		}
		if typ == "admin_alert_snapshot" {
			continue // 快照消费掉，继续等目标事件
		}
		if typ != "module_status_changed" {
			t.Fatalf("event = %q, want module_status_changed", typ)
		}
		got = m
		break
	}
	if got["module"] != "bots" {
		t.Fatalf("received %v, want module_status_changed/bots", got)
	}

	// 与模块状态无关的广播必须被丢掉：管理端口不应泄漏用户 presence 数据。
	env.hub.Broadcast([]byte(`{"type":"presence_update","userId":"u1","status":"online"}`))
	env.hub.Broadcast([]byte(`{"type":"channel_message","channelId":"c1","content":"secret"}`))

	_ = conn.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
	if _, stray, err := conn.ReadMessage(); err == nil {
		t.Errorf("收到不该转发的广播: %s", stray)
	}
}

func TestIsAdminRelevantBroadcast(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  string
		want bool
	}{
		{"module_status_changed 转发", `{"type":"module_status_changed","module":"bots"}`, true},
		{"admin_alert 转发（A11）", `{"type":"admin_alert","payload":{"action":"open"}}`, true},
		{"admin_alert_snapshot 转发（A11）", `{"type":"admin_alert_snapshot","payload":{"alerts":[]}}`, true},
		{"presence_update 丢弃", `{"type":"presence_update","userId":"u1"}`, false},
		{"channel_message 丢弃", `{"type":"channel_message","channelId":"c1"}`, false},
		{"voice_state 丢弃", `{"type":"voice_state","channelId":"c1"}`, false},
		{"无 type 字段丢弃", `{"module":"bots"}`, false},
		{"非 JSON 丢弃", `not-json`, false},
		{"空消息丢弃", ``, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAdminRelevantBroadcast([]byte(tc.msg)); got != tc.want {
				t.Errorf("isAdminRelevantBroadcast(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

func TestExtractAdminWSToken(t *testing.T) {
	header := http.Header{"Sec-Websocket-Protocol": []string{"access_token.hdr-token, rrt"}}

	for _, tc := range []struct {
		name         string
		header       http.Header
		query        string
		wantToken    string
		wantSubproto bool
	}{
		{"仅有 subprotocol", header, "", "hdr-token", true},
		{"仅有 token query", nil, "?token=q-token", "q-token", false},
		{"仅有 access_token query（旧版兼容）", nil, "?access_token=legacy", "legacy", false},
		{"subprotocol 优先于 query", header, "?token=q-token", "hdr-token", true},
		{"都没有", nil, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			req := httptest.NewRequest(http.MethodGet, "/admin/ws"+tc.query, nil)
			for k, v := range tc.header {
				req.Header[k] = v
			}
			c.Request = req

			token, wantSub := extractAdminWSToken(c)
			if token != tc.wantToken {
				t.Errorf("token = %q, want %q", token, tc.wantToken)
			}
			if wantSub != tc.wantSubproto {
				t.Errorf("wantSubprotocol = %v, want %v", wantSub, tc.wantSubproto)
			}
		})
	}
}
