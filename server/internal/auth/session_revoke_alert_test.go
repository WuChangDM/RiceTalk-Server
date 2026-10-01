package auth

// DES-20261001-01 §5.2「A4-S1 + A4-S2」测试：
//   - revoke-others 端点：删他留己 / 无 session_id 声明 400 / 他人会话无关性 / 审计发生；
//   - shouldAlertNewLogin 异地登录提醒判定（表驱动）；
//   - 登录与 refresh 写入 ip/user_agent；
//   - 登录命中异网段时触发 OnNewLoginAlert 回调（payload 脱敏、except 指向新会话）。
//
// 注意：testutil.MustSetupTestDB 每次调用返回独立的内存 SQLite，每个用例
// 自建 db + Service，互不干扰。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// newSessionTestUser 建一个可用真实密码登录的活跃用户。
func newSessionTestUser(t *testing.T, db *gorm.DB, email string) *model.User {
	t.Helper()
	hash, err := crypto.HashPassword("Password123!")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := &model.User{
		ID:           idgen.GenerateID(idgen.PrefixUser),
		Username:     "u_" + email,
		Email:        email,
		PasswordHash: hash,
		DisplayName:  "Session User",
		Role:         middleware.RoleMember,
		IsActive:     true,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	return user
}

// loginSession 执行一次真实登录并返回响应（IP/UA 由调用方指定）。
func loginSession(t *testing.T, svc *Service, email, ip, userAgent, deviceType, deviceName string) *TokenResponse {
	t.Helper()
	resp, err := svc.Login(&LoginRequest{
		Email:      email,
		Password:   "Password123!",
		DeviceType: deviceType,
		DeviceName: deviceName,
		IP:         ip,
		UserAgent:  userAgent,
	})
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	return resp
}

// activeSessions 返回该用户当前全部未过期会话行。
func activeSessions(t *testing.T, db *gorm.DB, userID string) []model.UserSession {
	t.Helper()
	var rows []model.UserSession
	if err := db.Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		t.Fatalf("query sessions: %v", err)
	}
	return rows
}

// revokeOthersRequest 直接以给定 token 调用 revoke-others 端点，返回响应。
func revokeOthersRequest(t *testing.T, handler *Handler, token string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/auth/sessions/revoke-others", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)
	return w
}

func TestRevokeOthersSessions(t *testing.T) {
	setup := func(t *testing.T) (*gorm.DB, *Service, *Handler, *model.User) {
		t.Helper()
		gin.SetMode(gin.TestMode)
		db := testutil.MustSetupTestDB()
		cfg := testConfig()
		middleware.MarkServerInitialized()
		t.Cleanup(middleware.ResetServerInitialized)
		svc := NewService(db, cfg)
		handler := NewHandlerWithState(db, cfg, nil)
		return db, svc, handler, nil
	}

	t.Run("deletes other sessions and keeps current", func(t *testing.T) {
		db, svc, handler, _ := setup(t)
		user := newSessionTestUser(t, db, "revoke-keep@example.com")

		first := loginSession(t, svc, user.Email, "10.0.0.1", "UA-1", "windows-desktop", "PC-1")
		second := loginSession(t, svc, user.Email, "10.0.1.1", "UA-2", "web", "Browser-2")

		w := revokeOthersRequest(t, handler, first.AccessToken)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Data struct {
				Revoked int64 `json:"revoked"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.Data.Revoked != 1 {
			t.Errorf("expected revoked=1, got %d", resp.Data.Revoked)
		}

		rows := activeSessions(t, db, user.ID)
		if len(rows) != 1 {
			t.Fatalf("expected 1 remaining session, got %d", len(rows))
		}
		if rows[0].ID != sessionIDFromAccessToken(t, testConfig(), first.AccessToken) {
			t.Errorf("remaining session %s, want the caller's current session", rows[0].ID)
		}
		_ = second // second session has been revoked above
	})

	t.Run("token without session_id claim returns 400 AUTH_NO_SESSION_CONTEXT", func(t *testing.T) {
		db, svc, handler, _ := setup(t)
		user := newSessionTestUser(t, db, "revoke-noclaim@example.com")
		loginSession(t, svc, user.Email, "10.0.0.1", "UA-1", "", "")

		// 手工生成无 session_id 声明的旧式 token。
		legacyAccess, _, err := middleware.GenerateTokenPair(
			user.ID, user.Username, user.Email, user.Role, user.TokenVersion, "", "", testConfig())
		if err != nil {
			t.Fatalf("generate legacy token: %v", err)
		}

		w := revokeOthersRequest(t, handler, legacyAccess)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d, body: %s", w.Code, w.Body.String())
		}
		if !bytes.Contains(w.Body.Bytes(), []byte("AUTH_NO_SESSION_CONTEXT")) {
			t.Errorf("expected AUTH_NO_SESSION_CONTEXT in body, got: %s", w.Body.String())
		}
		// 拒绝时不得误删任何会话。
		if rows := activeSessions(t, db, user.ID); len(rows) != 1 {
			t.Errorf("expected sessions untouched, got %d rows", len(rows))
		}
	})

	t.Run("other users' sessions are untouched", func(t *testing.T) {
		db, svc, handler, _ := setup(t)
		u1 := newSessionTestUser(t, db, "revoke-u1@example.com")
		u2 := newSessionTestUser(t, db, "revoke-u2@example.com")

		u1a := loginSession(t, svc, u1.Email, "10.1.0.1", "UA", "", "")
		loginSession(t, svc, u1.Email, "10.1.0.2", "UA", "", "")
		u2a := loginSession(t, svc, u2.Email, "10.2.0.1", "UA", "", "")

		// u2 下线自己的其他设备（没有其他设备 → revoked=0）。
		w := revokeOthersRequest(t, handler, u2a.AccessToken)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if rows := activeSessions(t, db, u1.ID); len(rows) != 2 {
			t.Errorf("u1 sessions must be untouched, got %d rows", len(rows))
		}
		_ = u1a
	})

	t.Run("writes audit event with count only", func(t *testing.T) {
		db, svc, handler, _ := setup(t)
		user := newSessionTestUser(t, db, "revoke-audit@example.com")
		first := loginSession(t, svc, user.Email, "10.3.0.1", "UA-1", "", "")
		loginSession(t, svc, user.Email, "10.3.0.2", "UA-2", "", "")

		w := revokeOthersRequest(t, handler, first.AccessToken)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}

		var logs []model.SecurityAuditLog
		if err := db.Where("action = ?", "auth.sessions.revoke_others").Find(&logs).Error; err != nil {
			t.Fatalf("query audit log: %v", err)
		}
		if len(logs) != 1 {
			t.Fatalf("expected exactly 1 audit entry, got %d", len(logs))
		}
		if logs[0].UserID != user.ID {
			t.Errorf("audit user_id = %s, want %s", logs[0].UserID, user.ID)
		}
		var details map[string]interface{}
		if err := json.Unmarshal([]byte(logs[0].DetailsJSON), &details); err != nil {
			t.Fatalf("decode audit details: %v", err)
		}
		if details["revoked"] != float64(1) {
			t.Errorf("audit details.revoked = %v, want 1", details["revoked"])
		}
		// 审计不得记录任何 token 形态的内容。
		if bytes.Contains([]byte(logs[0].DetailsJSON), []byte("token")) {
			t.Errorf("audit details must not contain tokens: %s", logs[0].DetailsJSON)
		}
	})
}

// sessionIDFromAccessToken 解析 access token 中的 session_id 声明。
func sessionIDFromAccessToken(t *testing.T, cfg *config.Config, accessToken string) string {
	t.Helper()
	claims, err := middleware.ParseToken(accessToken, cfg)
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	return claims.SessionID
}

// TestShouldAlertNewLogin 表驱动覆盖 A4-S2 判定规则：
// 同段不警 / 跨段警 / 空 IP 警 / 首登不警 / IPv6 前缀比较。
func TestShouldAlertNewLogin(t *testing.T) {
	cases := []struct {
		name     string
		existing []sessionRow
		newIP    string
		want     bool
	}{
		{
			name:     "no existing sessions (first login) is silent",
			existing: nil,
			newIP:    "1.2.3.4",
			want:     false,
		},
		{
			// 用 TEST-NET-3（203.0.113.0/24，RFC 5737 文档保留段）作同网段样例；
			// 原样例用了真实生产 IP，K-1 历史 rewrite 后变成占位符字面量破坏了
			// 网段判定语义（unparseable 路径）——测试不该依赖真实基础设施地址。
			name:     "same /24 subnet is silent",
			existing: []sessionRow{{IP: "203.0.113.10"}},
			newIP:    "203.0.113.99",
			want:     false,
		},
		{
			name:     "different /24 subnet alerts",
			existing: []sessionRow{{IP: "115.231.176.10"}},
			newIP:    "115.231.177.133",
			want:     true,
		},
		{
			name:     "empty existing ip is treated as unknown segment and alerts once",
			existing: []sessionRow{{IP: ""}},
			newIP:    "1.2.3.4",
			want:     true,
		},
		{
			name:     "empty new ip alerts when known sessions exist",
			existing: []sessionRow{{IP: "1.2.3.4"}},
			newIP:    "",
			want:     true,
		},
		{
			name:     "any known same-segment session suppresses the alert",
			existing: []sessionRow{{IP: ""}, {IP: "10.0.0.1"}, {IP: "10.9.9.9"}},
			newIP:    "10.0.0.77",
			want:     false,
		},
		{
			name:     "same ipv6 /64 prefix is silent",
			existing: []sessionRow{{IP: "2001:db8:1:2:aaaa::1"}},
			newIP:    "2001:db8:1:2:bbbb::1",
			want:     false,
		},
		{
			name:     "different ipv6 /64 prefix alerts",
			existing: []sessionRow{{IP: "2001:db8:1:2:aaaa::1"}},
			newIP:    "2001:db8:1:3:aaaa::1",
			want:     true,
		},
		{
			name:     "ipv4-mapped ipv6 compares as ipv4",
			existing: []sessionRow{{IP: "::ffff:10.0.0.5"}},
			newIP:    "10.0.0.9",
			want:     false,
		},
		{
			name:     "unparseable ip is treated as different segment",
			existing: []sessionRow{{IP: "not-an-ip"}},
			newIP:    "1.2.3.4",
			want:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAlertNewLogin(tc.existing, tc.newIP); got != tc.want {
				t.Errorf("shouldAlertNewLogin(%v, %q) = %v, want %v", tc.existing, tc.newIP, got, tc.want)
			}
		})
	}
}

// TestSessionIPUserAgentRecorded 覆盖 A4-S1 写入路径：登录写 IP/UA；
// refresh 用当前请求值覆盖 IP/UA（设备字段保持继承）；ListSessions 暴露 ip。
func TestSessionIPUserAgentRecorded(t *testing.T) {
	t.Run("login stores server-captured ip and user agent", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())
		user := newSessionTestUser(t, db, "ipua-login@example.com")

		resp := loginSession(t, svc, user.Email, "42.100.1.2", "RiceTalk/1.6 (Windows)", "windows-desktop", "DEV-1")
		sid := sessionIDFromAccessToken(t, testConfig(), resp.AccessToken)
		row := sessionByID(t, db, sid)
		if row.IP != "42.100.1.2" {
			t.Errorf("session ip = %q, want %q", row.IP, "42.100.1.2")
		}
		if row.UserAgent != "RiceTalk/1.6 (Windows)" {
			t.Errorf("session user_agent = %q, want %q", row.UserAgent, "RiceTalk/1.6 (Windows)")
		}
	})

	t.Run("user agent is sanitized and rune-truncated to 256", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())
		user := newSessionTestUser(t, db, "ipua-truncate@example.com")

		loginSession(t, svc, user.Email, "42.100.1.3", strings.Repeat("a", 300), "", "")
		rows := activeSessions(t, db, user.ID)
		if len(rows) != 1 {
			t.Fatalf("expected 1 session, got %d", len(rows))
		}
		if got := []rune(rows[0].UserAgent); len(got) != 256 {
			t.Errorf("user_agent rune length = %d, want 256", len(got))
		}
	})

	t.Run("refresh overwrites ip and user agent, inherits device", func(t *testing.T) {
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())
		user := newSessionTestUser(t, db, "ipua-refresh@example.com")

		first := loginSession(t, svc, user.Email, "10.0.0.1", "UA-old", "windows-desktop", "DEV-PC")
		oldSid := sessionIDFromAccessToken(t, testConfig(), first.AccessToken)

		// refresh：新 IP/UA 覆盖；设备字段继承旧行（客户端不重复上报）。
		second, err := svc.RefreshToken(first.RefreshToken, &SessionDevice{
			IP:        "10.9.9.9",
			UserAgent: "UA-new",
		})
		if err != nil {
			t.Fatalf("refresh failed: %v", err)
		}
		newSid := sessionIDFromAccessToken(t, testConfig(), second.AccessToken)
		if newSid == oldSid {
			t.Fatal("refresh must rotate the session id")
		}
		row := sessionByID(t, db, newSid)
		if row.IP != "10.9.9.9" {
			t.Errorf("refreshed session ip = %q, want %q (recent-activity IP semantics)", row.IP, "10.9.9.9")
		}
		if row.UserAgent != "UA-new" {
			t.Errorf("refreshed session user_agent = %q, want %q", row.UserAgent, "UA-new")
		}
		if row.DeviceType != "windows-desktop" || row.DeviceName != "DEV-PC" {
			t.Errorf("device fields must be inherited, got type=%q name=%q", row.DeviceType, row.DeviceName)
		}
	})
}

// TestNewLoginAlertCallback 覆盖 A4-S2 触发链路：异网段登录触发回调、
// exceptSessionID 指向新会话、payload 字段齐全且 IP 已脱敏；同段/首登不触发。
func TestNewLoginAlertCallback(t *testing.T) {
	type alertCall struct {
		userID          string
		exceptSessionID string
		payload         map[string]interface{}
	}

	runLogin := func(t *testing.T, onAlert func(alertCall)) *TokenResponse {
		t.Helper()
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())
		svc.OnNewLoginAlert = func(userID, exceptSessionID string, payload map[string]interface{}) {
			onAlert(alertCall{userID: userID, exceptSessionID: exceptSessionID, payload: payload})
		}
		user := newSessionTestUser(t, db, "alert-flow@example.com")

		// 第一次登录：无既有会话（首登）→ 不告警。
		loginSession(t, svc, user.Email, "77.1.1.1", "UA-1", "windows-desktop", "PC-A")

		// 第二次登录：异网段 → 告警。
		return loginSession(t, svc, user.Email, "88.2.2.2", "UA-2", "web", "Browser-B")
	}

	t.Run("different subnet login triggers alert for others", func(t *testing.T) {
		calls := make(chan alertCall, 4)
		resp := runLogin(t, func(c alertCall) { calls <- c })

		select {
		case call := <-calls:
			if call.exceptSessionID == "" {
				t.Error("exceptSessionID must carry the new session id")
			}
			wantSid := sessionIDFromAccessToken(t, testConfig(), resp.AccessToken)
			if call.exceptSessionID != wantSid {
				t.Errorf("exceptSessionID = %s, want %s (the new login itself)", call.exceptSessionID, wantSid)
			}
			if call.payload["kind"] != "new_login" {
				t.Errorf("payload.kind = %v, want new_login", call.payload["kind"])
			}
			if call.payload["deviceType"] != "web" || call.payload["deviceName"] != "Browser-B" {
				t.Errorf("payload device = %v/%v, want web/Browser-B", call.payload["deviceType"], call.payload["deviceName"])
			}
			// 服务端已脱敏：保留前两段与尾段。
			if call.payload["ip"] != "88.2.*.2" {
				t.Errorf("payload.ip = %v, want desensitized 88.2.*.2", call.payload["ip"])
			}
			if _, ok := call.payload["at"].(string); !ok {
				t.Errorf("payload.at must be an RFC3339 string, got %T", call.payload["at"])
			}
		case <-time.After(time.Second):
			t.Fatal("expected an alert call for the different-subnet login")
		}
	})

	t.Run("first login and same-subnet login stay silent", func(t *testing.T) {
		calls := make(chan alertCall, 4)
		db := testutil.MustSetupTestDB()
		svc := NewService(db, testConfig())
		svc.OnNewLoginAlert = func(userID, exceptSessionID string, payload map[string]interface{}) {
			calls <- alertCall{userID: userID, exceptSessionID: exceptSessionID, payload: payload}
		}
		user := newSessionTestUser(t, db, "alert-silent@example.com")

		loginSession(t, svc, user.Email, "77.1.1.1", "UA", "", "") // 首登
		loginSession(t, svc, user.Email, "77.1.1.200", "UA", "", "") // 同 /24

		select {
		case c := <-calls:
			t.Fatalf("unexpected alert: %+v", c)
		case <-time.After(150 * time.Millisecond):
		}
	})
}

// mustSvcFromResp 已移除：sessionIDFromAccessToken 直接接受 *config.Config。

// 防止 errors 包导入未使用的编译问题：本文件断言错误码字符串。
var _ = errors.AUTH_NO_SESSION_CONTEXT
