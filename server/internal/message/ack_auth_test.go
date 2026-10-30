package message

// M-1（安全批次 task-23）回归测试：
//   - 无频道权限 → 403 CHANNEL_ACCESS_DENIED 且【ack 未落库】；
//   - 有权限 → 204 且落库（成功响应形状不变）；
//   - 消息不存在/已软删 → 404 MESSAGE_NOT_FOUND，且不落库；
//   - DM 非参与者 → 404 CHANNEL_NOT_FOUND（CheckChannelAccess 语义，先于 OWNER）。
//
// 全部使用真 SQLite（testutil.MustSetupTestDB）+ httptest，不 mock 数据层；
// 权限判定复用 middleware.CheckChannelAccess（禁止在测试里复制口径）。

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// newAckAuthRouter 构造只挂 ACK 路由的 gin 路由，角色由调用方注入。
func newAckAuthRouter(t *testing.T, role string) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	h := NewHandler(db, config.DefaultConfig(), nil, nil)

	auth := func(c *gin.Context) {
		setupAuthContext(c, "user-1", "testuser", role)
		c.Next()
	}
	r := gin.New()
	r.POST("/messages/:id/ack", auth, h.AckMessage)
	return h, r
}

// seedAckChannel 创建一个文本频道（visibility 由调用方指定）。
func seedAckChannel(t *testing.T, db *gorm.DB, id, spaceID, visibility string) *model.Channel {
	t.Helper()
	return seedAuthChannel(t, db, id, spaceID, visibility)
}

// seedAckMessage 创建一条消息。
func seedAckMessage(t *testing.T, db *gorm.DB, id, channelID, userID string) *model.Message {
	t.Helper()
	return seedAuthMessage(t, db, id, channelID, userID)
}

func ackCount(t *testing.T, db *gorm.DB, messageID string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.MessageAck{}).Where("message_id = ?", messageID).Count(&n).Error; err != nil {
		t.Fatalf("count message acks: %v", err)
	}
	return n
}

func doAck(r *gin.Engine, id string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/messages/"+id+"/ack", nil)
	r.ServeHTTP(w, req)
	return w
}

// TestAckMessageRequiresChannelAccess 覆盖 ACK 的鉴权三态与副作用。
func TestAckMessageRequiresChannelAccess(t *testing.T) {
	t.Run("admin-only 频道 + MEMBER → 403 且不落库", func(t *testing.T) {
		h, r := newAckAuthRouter(t, middleware.RoleMember)
		seedAckChannel(t, h.db, "ch-ack-admin", "space-ack", "admin-only")
		seedAckMessage(t, h.db, "msg-ack-1", "ch-ack-admin", "user-other")

		w := doAck(r, "msg-ack-1")

		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
		}
		if code := respCode(t, w); code != "CHANNEL_ACCESS_DENIED" {
			t.Fatalf("code = %q, want CHANNEL_ACCESS_DENIED", code)
		}
		if n := ackCount(t, h.db, "msg-ack-1"); n != 0 {
			t.Fatalf("ack rows = %d, want 0（鉴权失败不得落库）", n)
		}
	})

	t.Run("role-specific 无权限行 + MEMBER → 403 且不落库", func(t *testing.T) {
		h, r := newAckAuthRouter(t, middleware.RoleMember)
		seedAckChannel(t, h.db, "ch-ack-role", "space-ack", "role-specific")
		seedAckMessage(t, h.db, "msg-ack-2", "ch-ack-role", "user-other")

		w := doAck(r, "msg-ack-2")

		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
		}
		if code := respCode(t, w); code != "CHANNEL_ACCESS_DENIED" {
			t.Fatalf("code = %q, want CHANNEL_ACCESS_DENIED", code)
		}
		if n := ackCount(t, h.db, "msg-ack-2"); n != 0 {
			t.Fatalf("ack rows = %d, want 0", n)
		}
	})

	t.Run("role-specific 显式授权 + MEMBER → 204 且落库", func(t *testing.T) {
		h, r := newAckAuthRouter(t, middleware.RoleMember)
		seedAckChannel(t, h.db, "ch-ack-allow", "space-ack", "role-specific")
		seedAckMessage(t, h.db, "msg-ack-3", "ch-ack-allow", "user-other")
		if err := h.db.Create(&model.ChannelRolePermission{
			ID:        "crp-ack-allow",
			ChannelID: "ch-ack-allow",
			Role:      middleware.RoleMember,
			CanView:   true,
		}).Error; err != nil {
			t.Fatalf("create channel role permission: %v", err)
		}

		w := doAck(r, "msg-ack-3")

		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
		}
		if n := ackCount(t, h.db, "msg-ack-3"); n != 1 {
			t.Fatalf("ack rows = %d, want 1", n)
		}
		var ack model.MessageAck
		if err := h.db.Where("message_id = ? AND user_id = ?", "msg-ack-3", "user-1").First(&ack).Error; err != nil {
			t.Fatalf("load ack: %v", err)
		}
		if ack.ChannelID != "ch-ack-allow" {
			t.Fatalf("ack.channel_id = %q, want ch-ack-allow", ack.ChannelID)
		}
	})

	t.Run("public 频道 + MEMBER → 204 且落库（既有行为不回退）", func(t *testing.T) {
		h, r := newAckAuthRouter(t, middleware.RoleMember)
		seedAckChannel(t, h.db, "ch-ack-public", "space-ack", "public")
		seedAckMessage(t, h.db, "msg-ack-pub", "ch-ack-public", "user-other")

		w := doAck(r, "msg-ack-pub")

		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
		}
		if n := ackCount(t, h.db, "msg-ack-pub"); n != 1 {
			t.Fatalf("ack rows = %d, want 1", n)
		}
	})

	t.Run("消息不存在 → 404 MESSAGE_NOT_FOUND", func(t *testing.T) {
		_, r := newAckAuthRouter(t, middleware.RoleMember)

		w := doAck(r, "msg-ack-missing")

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body=%s)", w.Code, w.Body.String())
		}
		if code := respCode(t, w); code != "MESSAGE_NOT_FOUND" {
			t.Fatalf("code = %q, want MESSAGE_NOT_FOUND", code)
		}
	})

	t.Run("消息已软删 → 404 且不落库", func(t *testing.T) {
		h, r := newAckAuthRouter(t, middleware.RoleMember)
		seedAckChannel(t, h.db, "ch-ack-del", "space-ack", "public")
		seedAckMessage(t, h.db, "msg-ack-deleted", "ch-ack-del", "user-other")
		if err := h.db.Delete(&model.Message{}, "id = ?", "msg-ack-deleted").Error; err != nil {
			t.Fatalf("soft delete message: %v", err)
		}

		w := doAck(r, "msg-ack-deleted")

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body=%s)", w.Code, w.Body.String())
		}
		if code := respCode(t, w); code != "MESSAGE_NOT_FOUND" {
			t.Fatalf("code = %q, want MESSAGE_NOT_FOUND", code)
		}
		if n := ackCount(t, h.db, "msg-ack-deleted"); n != 0 {
			t.Fatalf("ack rows = %d, want 0", n)
		}
	})

	t.Run("DM 非参与者 → 404 CHANNEL_NOT_FOUND 且不落库", func(t *testing.T) {
		h, r := newAckAuthRouter(t, middleware.RoleMember)
		dm := &model.Channel{ID: "ch-ack-dm", SpaceID: "space-ack", Name: "dm-ack", Type: "dm", Visibility: "public"}
		if err := h.db.Create(dm).Error; err != nil {
			t.Fatalf("create dm channel: %v", err)
		}
		seedAckMessage(t, h.db, "msg-ack-dm", "ch-ack-dm", "user-other")

		w := doAck(r, "msg-ack-dm")

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body=%s)", w.Code, w.Body.String())
		}
		if code := respCode(t, w); code != "CHANNEL_NOT_FOUND" {
			t.Fatalf("code = %q, want CHANNEL_NOT_FOUND", code)
		}
		if n := ackCount(t, h.db, "msg-ack-dm"); n != 0 {
			t.Fatalf("ack rows = %d, want 0", n)
		}
	})
}

// TestAckMessageIdempotent 保证重复 ACK 仍返回 204 且只保留一行（OnConflict DoNothing 语义不变）。
func TestAckMessageIdempotent(t *testing.T) {
	h, r := newAckAuthRouter(t, middleware.RoleMember)
	seedAckChannel(t, h.db, "ch-ack-idem", "space-ack", "public")
	seedAckMessage(t, h.db, "msg-ack-idem", "ch-ack-idem", "user-other")

	for i := 0; i < 2; i++ {
		w := doAck(r, "msg-ack-idem")
		if w.Code != http.StatusNoContent {
			t.Fatalf("round %d status = %d, want 204 (body=%s)", i, w.Code, w.Body.String())
		}
	}
	if n := ackCount(t, h.db, "msg-ack-idem"); n != 1 {
		t.Fatalf("ack rows = %d, want 1（重复 ACK 应幂等）", n)
	}
}
