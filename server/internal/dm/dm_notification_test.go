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

// TestDMMessageCreatesNotification 锁定 1.9.4 task-118 的私信通知落库：
// A 给 B 发私信后，notifications 表里必须出现一条 B 的 dm 通知，
// refType=dm_channel、refId=会话频道 ID，且与 B 是否在线（hub 连接）无关。
func TestDMMessageCreatesNotification(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret-for-dm-notif-32bytes!!"
	cfg.EncryptionKey = "test-encryption-key-for-dm-notif-32by"

	msgSvc := message.NewService(db)
	handler := NewHandler(db, cfg, msgSvc, realtime.NewHub(nil))

	router := gin.New()
	handler.RegisterRoutes(router.Group("/api/v1"))

	users := make([]*model.User, 2)
	for i, name := range []string{"dm-notif-alice", "dm-notif-bob"} {
		u := &model.User{
			ID:           idgen.NextString(),
			Username:     name,
			Email:        name + "@example.com",
			PasswordHash: "x",
			Role:         middleware.RoleMember,
			DisplayName:  map[string]string{"dm-notif-alice": "Alice", "dm-notif-bob": "Bob"}[name],
		}
		if err := db.Create(u).Error; err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		users[i] = u
	}
	alice, bob := users[0], users[1]

	tokenOf := func(u *model.User) string {
		t.Helper()
		token, _, err := middleware.GenerateTokenPair(u.ID, u.Username, u.Email, u.Role, u.TokenVersion, "", "", cfg)
		if err != nil {
			t.Fatalf("generate token for %s: %v", u.Username, err)
		}
		return token
	}
	do := func(method, path, token string, body interface{}) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if body != nil {
			var err error
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
		}
		req, err := http.NewRequest(method, path, bytes.NewReader(raw))
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

	// A 与 B 建会话。
	w := do("POST", "/api/v1/dm/channels", tokenOf(alice), map[string]string{"recipientId": bob.ID})
	if w.Code != http.StatusOK {
		t.Fatalf("创建 DM 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var createResp struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("解析创建响应: %v (%s)", err, w.Body.String())
	}
	channelID, _ := createResp.Data["id"].(string)
	if channelID == "" {
		t.Fatalf("创建响应缺少频道 id: %s", w.Body.String())
	}

	// A 发一条私信。
	const content = "bob，这条是私信通知用例"
	w = do("POST", "/api/v1/dm/channels/"+channelID+"/messages", tokenOf(alice), map[string]string{"content": content})
	if w.Code != http.StatusOK {
		t.Fatalf("A 发私信应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	// B 必须收到一条 dm 通知（落库，不依赖 WS 在线）。
	var bobs []model.Notification
	if err := db.Where("user_id = ?", bob.ID).Find(&bobs).Error; err != nil {
		t.Fatalf("查询 B 的通知: %v", err)
	}
	if len(bobs) != 1 {
		t.Fatalf("B 应收到 1 条 dm 通知，实际 %d 条", len(bobs))
	}
	n := bobs[0]
	if n.Type != "dm" || n.RefType != "dm_channel" || n.RefID != channelID {
		t.Fatalf("dm 通知字段不符：type=%q refType=%q refID=%q（期望 dm/dm_channel/%s）", n.Type, n.RefType, n.RefID, channelID)
	}
	if n.Body != content {
		t.Fatalf("通知正文应为 %q，实际 %q", content, n.Body)
	}
	if n.Title != "Alice 给你发了一条私信" {
		t.Fatalf("通知标题应为 %q，实际 %q", "Alice 给你发了一条私信", n.Title)
	}
	if n.ReadAt != nil {
		t.Fatalf("新私信通知应为未读（read_at=null）")
	}

	// 发送者本人不应收到 dm 通知。
	var aliceNotifs int64
	if err := db.Model(&model.Notification{}).Where("user_id = ?", alice.ID).Count(&aliceNotifs).Error; err != nil {
		t.Fatalf("统计 A 的通知: %v", err)
	}
	if aliceNotifs != 0 {
		t.Fatalf("发送者不应收到 dm 通知，实际 %d 条", aliceNotifs)
	}
}
