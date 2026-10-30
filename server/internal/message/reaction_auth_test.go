package message

// R-01（安全批次 task-17）回归测试：
//   - 无频道权限 → 403 CHANNEL_ACCESS_DENIED 且【反应未落库、未广播】；
//   - 有权限 → 200 且落库 + 广播到正确频道；
//   - 消息不存在/已软删 → 404 MESSAGE_NOT_FOUND；
//   - 越权删他人反应 → 403 且原反应仍在。
//
// 全部使用真 SQLite（testutil.MustSetupTestDB）+ httptest，不 mock 数据层；
// 权限判定复用 middleware.CheckChannelAccess（禁止在测试里复制口径）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// newResourceAuthRouter 构造只挂 reaction / 附件下载路由的 gin 路由，角色由调用方注入。
func newResourceAuthRouter(t *testing.T, role string) (*Handler, *gin.Engine, *recordingBroadcaster) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	h := NewHandler(db, config.DefaultConfig(), nil, nil)
	rec := &recordingBroadcaster{}
	h.hub = rec

	auth := func(c *gin.Context) {
		setupAuthContext(c, "user-1", "testuser", role)
		c.Next()
	}
	r := gin.New()
	r.POST("/messages/:id/reactions", auth, h.AddReaction)
	r.DELETE("/messages/:id/reactions", auth, h.RemoveReaction)
	r.GET("/channel-files/:id", auth, h.DownloadAttachment)
	return h, r, rec
}

func seedAuthChannel(t *testing.T, db *gorm.DB, id, spaceID, visibility string) *model.Channel {
	t.Helper()
	ch := &model.Channel{ID: id, SpaceID: spaceID, Name: id, Type: "text", Visibility: visibility}
	if err := db.Create(ch).Error; err != nil {
		t.Fatalf("create channel %s: %v", id, err)
	}
	return ch
}

func seedAuthMessage(t *testing.T, db *gorm.DB, id, channelID, userID string) *model.Message {
	t.Helper()
	msg := &model.Message{ID: id, ChannelID: channelID, UserID: userID, Content: "hello", Type: "text"}
	if err := db.Create(msg).Error; err != nil {
		t.Fatalf("create message %s: %v", id, err)
	}
	return msg
}

// respCode 解析响应体顶层 code 字段（errors.Response）。
func respCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body %q: %v", w.Body.String(), err)
	}
	return body.Code
}

func reactionCount(t *testing.T, db *gorm.DB, messageID string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.Reaction{}).Where("message_id = ?", messageID).Count(&n).Error; err != nil {
		t.Fatalf("count reactions: %v", err)
	}
	return n
}

func reactionBody(emoji string) []byte {
	b, _ := json.Marshal(map[string]string{"emoji": emoji})
	return b
}

// ---------------------------------------------------------------- R-01 加反应

func TestAddReactionRequiresChannelAccess(t *testing.T) {
	t.Run("admin-only 频道 MEMBER 被拒且不落库不广播", func(t *testing.T) {
		h, r, rec := newResourceAuthRouter(t, middleware.RoleMember)
		seedAuthChannel(t, h.db, "ch-r1-admin", "space-r1", "admin-only")
		msg := seedAuthMessage(t, h.db, "msg-r1-admin", "ch-r1-admin", "user-2")

		w := doJSON(r, http.MethodPost, "/messages/"+msg.ID+"/reactions", reactionBody("👍"))
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
		}
		if code := respCode(t, w); code != string(errors.CHANNEL_ACCESS_DENIED) {
			t.Fatalf("expected code CHANNEL_ACCESS_DENIED, got %s", code)
		}
		if n := reactionCount(t, h.db, msg.ID); n != 0 {
			t.Fatalf("reaction must not be persisted on 403, got %d rows", n)
		}
		if len(rec.channelCalls) != 0 {
			t.Fatalf("must not broadcast on 403, got %d calls", len(rec.channelCalls))
		}
	})

	t.Run("role-specific 无权限行被拒且不落库不广播", func(t *testing.T) {
		h, r, rec := newResourceAuthRouter(t, middleware.RoleMember)
		seedAuthChannel(t, h.db, "ch-r1-role", "space-r1", "role-specific")
		msg := seedAuthMessage(t, h.db, "msg-r1-role", "ch-r1-role", "user-2")

		w := doJSON(r, http.MethodPost, "/messages/"+msg.ID+"/reactions", reactionBody("🎉"))
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
		}
		if code := respCode(t, w); code != string(errors.CHANNEL_ACCESS_DENIED) {
			t.Fatalf("expected code CHANNEL_ACCESS_DENIED, got %s", code)
		}
		if n := reactionCount(t, h.db, msg.ID); n != 0 {
			t.Fatalf("reaction must not be persisted on 403, got %d rows", n)
		}
		if len(rec.channelCalls) != 0 {
			t.Fatalf("must not broadcast on 403, got %d calls", len(rec.channelCalls))
		}
	})

	t.Run("role-specific 授予 can_view 后允许并落库广播", func(t *testing.T) {
		h, r, rec := newResourceAuthRouter(t, middleware.RoleMember)
		ch := seedAuthChannel(t, h.db, "ch-r1-grant", "space-r1", "role-specific")
		if err := h.db.Create(&model.ChannelRolePermission{
			ID: "crp-r1", ChannelID: ch.ID, Role: middleware.RoleMember, CanView: true, CanWrite: true,
		}).Error; err != nil {
			t.Fatalf("create channel role permission: %v", err)
		}
		msg := seedAuthMessage(t, h.db, "msg-r1-grant", ch.ID, "user-2")

		w := doJSON(r, http.MethodPost, "/messages/"+msg.ID+"/reactions", reactionBody("👏"))
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
		}
		if n := reactionCount(t, h.db, msg.ID); n != 1 {
			t.Fatalf("reaction must be persisted, got %d rows", n)
		}
		if len(rec.channelCalls) != 1 {
			t.Fatalf("expected exactly 1 channel broadcast, got %d", len(rec.channelCalls))
		}
		if rec.channelCalls[0].channelID != ch.ID {
			t.Fatalf("broadcast target = %q, want %q", rec.channelCalls[0].channelID, ch.ID)
		}
		if !strings.Contains(string(rec.channelCalls[0].data), "message_reaction_added") {
			t.Fatalf("broadcast payload missing event type: %s", string(rec.channelCalls[0].data))
		}
	})

	t.Run("admin-only 频道 ADMIN 角色允许", func(t *testing.T) {
		h, r, _ := newResourceAuthRouter(t, middleware.RoleAdmin)
		seedAuthChannel(t, h.db, "ch-r1-admin2", "space-r1", "admin-only")
		msg := seedAuthMessage(t, h.db, "msg-r1-admin2", "ch-r1-admin2", "user-2")

		w := doJSON(r, http.MethodPost, "/messages/"+msg.ID+"/reactions", reactionBody("🔥"))
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
		}
		if n := reactionCount(t, h.db, msg.ID); n != 1 {
			t.Fatalf("reaction must be persisted for ADMIN, got %d rows", n)
		}
	})

	t.Run("消息不存在 404", func(t *testing.T) {
		_, r, rec := newResourceAuthRouter(t, middleware.RoleMember)
		w := doJSON(r, http.MethodPost, "/messages/msg-r1-missing/reactions", reactionBody("👍"))
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
		}
		if code := respCode(t, w); code != string(errors.MESSAGE_NOT_FOUND) {
			t.Fatalf("expected code MESSAGE_NOT_FOUND, got %s", code)
		}
		if len(rec.channelCalls) != 0 {
			t.Fatalf("must not broadcast for missing message, got %d calls", len(rec.channelCalls))
		}
	})

	t.Run("消息已软删 404", func(t *testing.T) {
		h, r, _ := newResourceAuthRouter(t, middleware.RoleMember)
		seedAuthChannel(t, h.db, "ch-r1-del", "space-r1", "public")
		msg := seedAuthMessage(t, h.db, "msg-r1-del", "ch-r1-del", "user-2")
		if err := h.db.Delete(&model.Message{}, "id = ?", msg.ID).Error; err != nil {
			t.Fatalf("soft delete message: %v", err)
		}

		w := doJSON(r, http.MethodPost, "/messages/"+msg.ID+"/reactions", reactionBody("👍"))
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
		}
		if code := respCode(t, w); code != string(errors.MESSAGE_NOT_FOUND) {
			t.Fatalf("expected code MESSAGE_NOT_FOUND, got %s", code)
		}
	})
}

// ---------------------------------------------------------------- R-01 删反应

func TestRemoveReactionRequiresChannelAccess(t *testing.T) {
	t.Run("越权删他人反应被拒且原反应仍在", func(t *testing.T) {
		h, r, rec := newResourceAuthRouter(t, middleware.RoleMember)
		seedAuthChannel(t, h.db, "ch-r1-rm", "space-r1", "admin-only")
		msg := seedAuthMessage(t, h.db, "msg-r1-rm", "ch-r1-rm", "user-2")
		if err := h.db.Create(&model.Reaction{
			ID: "rc-other", MessageID: msg.ID, UserID: "user-2", Emoji: "👍",
		}).Error; err != nil {
			t.Fatalf("seed reaction: %v", err)
		}

		w := doJSON(r, http.MethodDelete, "/messages/"+msg.ID+"/reactions", reactionBody("👍"))
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
		}
		if code := respCode(t, w); code != string(errors.CHANNEL_ACCESS_DENIED) {
			t.Fatalf("expected code CHANNEL_ACCESS_DENIED, got %s", code)
		}
		if n := reactionCount(t, h.db, msg.ID); n != 1 {
			t.Fatalf("reaction must stay on 403, got %d rows", n)
		}
		if len(rec.channelCalls) != 0 {
			t.Fatalf("must not broadcast on 403, got %d calls", len(rec.channelCalls))
		}
	})

	t.Run("有权限删自己的反应成功", func(t *testing.T) {
		h, r, rec := newResourceAuthRouter(t, middleware.RoleMember)
		ch := seedAuthChannel(t, h.db, "ch-r1-rm2", "space-r1", "role-specific")
		if err := h.db.Create(&model.ChannelRolePermission{
			ID: "crp-r1-rm2", ChannelID: ch.ID, Role: middleware.RoleMember, CanView: true, CanWrite: true,
		}).Error; err != nil {
			t.Fatalf("create channel role permission: %v", err)
		}
		msg := seedAuthMessage(t, h.db, "msg-r1-rm2", ch.ID, "user-2")
		if err := h.db.Create(&model.Reaction{
			ID: "rc-own", MessageID: msg.ID, UserID: "user-1", Emoji: "👍",
		}).Error; err != nil {
			t.Fatalf("seed reaction: %v", err)
		}

		w := doJSON(r, http.MethodDelete, "/messages/"+msg.ID+"/reactions", reactionBody("👍"))
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
		}
		if n := reactionCount(t, h.db, msg.ID); n != 0 {
			t.Fatalf("reaction must be deleted, got %d rows", n)
		}
		if len(rec.channelCalls) != 1 {
			t.Fatalf("expected 1 channel broadcast, got %d", len(rec.channelCalls))
		}
	})
}
