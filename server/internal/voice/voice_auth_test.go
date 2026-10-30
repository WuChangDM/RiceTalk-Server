package voice

// R-02（安全批次 task-18）语音 token/join 频道可见性回归测试：
//   - MEMBER 对 role-specific 私有频道取 token → 403 CHANNEL_ACCESS_DENIED，且响应体不含 token；
//   - admin-only 频道 → 403；
//   - DM 房间非参与者 → 404 CHANNEL_NOT_FOUND（dm 判定先于 OWNER 放行）；
//   - 有权限成员 → 200 且返回可解析的三段 JWT token；
//   - 频道已软删 → 404 CHANNEL_NOT_FOUND。
//
// 全部真 SQLite（testutil.MustSetupTestDB）+ httptest，注入真实认证上下文，
// 不 mock 数据层；权限判定复用 middleware.CheckChannelAccess（禁止复制口径）。

import (
	"bytes"
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

// newVoiceAuthRouter 构造只挂 token / token-refresh / join 三条路由的 gin 路由。
func newVoiceAuthRouter(t *testing.T, role string) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	h := NewHandler(db, config.DefaultConfig(), nil)

	auth := func(c *gin.Context) {
		c.Set("user_id", "user-1")
		c.Set("username", "tester")
		c.Set("role", role)
		c.Next()
	}
	r := gin.New()
	r.POST("/voice/token", auth, h.GetToken)
	r.POST("/voice/token/refresh", auth, h.RefreshToken)
	r.POST("/voice/join", auth, h.JoinVoice)
	return h, r
}

// seedVoiceSpaceAndMember 建空间 + user-1 的 MEMBER 成员关系。
func seedVoiceSpaceAndMember(t *testing.T, db *gorm.DB, spaceID string) {
	t.Helper()
	if err := db.Create(&model.Space{ID: spaceID, Name: "voice-space", OwnerID: "owner-1"}).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	if err := db.Create(&model.Membership{
		ID: "mb-voice", SpaceID: spaceID, UserID: "user-1", Role: middleware.RoleMember,
	}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
}

// seedVoiceChannelRoom 建频道 + 绑定该频道的语音房间，返回频道 ID 与房间 ID。
func seedVoiceChannelRoom(t *testing.T, db *gorm.DB, spaceID, channelType, visibility string) (string, string) {
	t.Helper()
	ch := &model.Channel{
		ID: "ch-" + visibility + "-" + channelType, SpaceID: spaceID,
		Name: "voice", Type: channelType, Visibility: visibility,
	}
	if err := db.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	roomID := "room-" + ch.ID
	room := &model.VoiceRoom{
		ID: roomID, SpaceID: spaceID, BindChannelID: &ch.ID,
		LiveKitRoom: roomName(roomID), Quality: "standard",
	}
	if err := db.Create(room).Error; err != nil {
		t.Fatalf("create voice room: %v", err)
	}
	return ch.ID, roomID
}

func doVoiceJSON(t *testing.T, r *gin.Engine, path, roomID string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"roomId": roomID})
	req, _ := http.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func respFields(t *testing.T, w *httptest.ResponseRecorder) (string, map[string]interface{}) {
	t.Helper()
	var resp struct {
		Code string                 `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	return resp.Code, resp.Data
}

// assertNoToken 断言响应体里不存在任何 token 字段（不能只看状态码）。
func assertNoToken(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	for _, key := range []string{`"token"`, `"roomToken"`, `"livekitToken"`} {
		if strings.Contains(w.Body.String(), key) {
			t.Fatalf("forbidden response must not contain %s, body=%s", key, w.Body.String())
		}
	}
}

func TestVoiceTokenRequiresChannelAccess(t *testing.T) {
	t.Run("role-specific 无权限行 → 403 且不签发 token", func(t *testing.T) {
		h, r := newVoiceAuthRouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-v1")
		_, roomID := seedVoiceChannelRoom(t, h.db, "space-v1", "voice", "role-specific")

		w := doVoiceJSON(t, r, "/voice/token", roomID)
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
		}
		code, _ := respFields(t, w)
		if code != string(errors.CHANNEL_ACCESS_DENIED) {
			t.Fatalf("expected code CHANNEL_ACCESS_DENIED, got %s", code)
		}
		assertNoToken(t, w)
	})

	t.Run("admin-only 频道 → 403 且不签发 token", func(t *testing.T) {
		h, r := newVoiceAuthRouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-v2")
		_, roomID := seedVoiceChannelRoom(t, h.db, "space-v2", "voice", "admin-only")

		w := doVoiceJSON(t, r, "/voice/token", roomID)
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
		}
		code, _ := respFields(t, w)
		if code != string(errors.CHANNEL_ACCESS_DENIED) {
			t.Fatalf("expected code CHANNEL_ACCESS_DENIED, got %s", code)
		}
		assertNoToken(t, w)
	})

	t.Run("DM 房间非参与者 → 404 CHANNEL_NOT_FOUND", func(t *testing.T) {
		h, r := newVoiceAuthRouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-v3")
		_, roomID := seedVoiceChannelRoom(t, h.db, "space-v3", "dm", "public")

		w := doVoiceJSON(t, r, "/voice/token", roomID)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
		}
		code, _ := respFields(t, w)
		if code != string(errors.CHANNEL_NOT_FOUND) {
			t.Fatalf("expected code CHANNEL_NOT_FOUND, got %s", code)
		}
		assertNoToken(t, w)
	})

	t.Run("有权限成员 → 200 且返回可用 token", func(t *testing.T) {
		h, r := newVoiceAuthRouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-v4")
		chID, roomID := seedVoiceChannelRoom(t, h.db, "space-v4", "voice", "role-specific")
		if err := h.db.Create(&model.ChannelRolePermission{
			ID: "crp-voice", ChannelID: chID, Role: middleware.RoleMember, CanView: true, CanWrite: true,
		}).Error; err != nil {
			t.Fatalf("create channel role permission: %v", err)
		}

		w := doVoiceJSON(t, r, "/voice/token", roomID)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
		}
		_, data := respFields(t, w)
		token, _ := data["token"].(string)
		if token == "" {
			t.Fatalf("expected non-empty token, body=%s", w.Body.String())
		}
		if parts := strings.Split(token, "."); len(parts) != 3 {
			t.Fatalf("expected 3-part JWT, got %d parts", len(parts))
		}
	})

	t.Run("频道已软删 → 404 CHANNEL_NOT_FOUND", func(t *testing.T) {
		h, r := newVoiceAuthRouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-v5")
		chID, roomID := seedVoiceChannelRoom(t, h.db, "space-v5", "voice", "public")
		if err := h.db.Delete(&model.Channel{}, "id = ?", chID).Error; err != nil {
			t.Fatalf("soft delete channel: %v", err)
		}

		w := doVoiceJSON(t, r, "/voice/token", roomID)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
		}
		code, _ := respFields(t, w)
		if code != string(errors.CHANNEL_NOT_FOUND) {
			t.Fatalf("expected code CHANNEL_NOT_FOUND, got %s", code)
		}
		assertNoToken(t, w)
	})
}

func TestVoiceTokenRefreshAndJoinRequireChannelAccess(t *testing.T) {
	t.Run("token/refresh 同样被拒且不签发 token", func(t *testing.T) {
		h, r := newVoiceAuthRouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-v6")
		_, roomID := seedVoiceChannelRoom(t, h.db, "space-v6", "voice", "admin-only")

		w := doVoiceJSON(t, r, "/voice/token/refresh", roomID)
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
		}
		code, _ := respFields(t, w)
		if code != string(errors.CHANNEL_ACCESS_DENIED) {
			t.Fatalf("expected code CHANNEL_ACCESS_DENIED, got %s", code)
		}
		assertNoToken(t, w)
	})

	t.Run("join 未授权 → 403 且不落参与者记录", func(t *testing.T) {
		h, r := newVoiceAuthRouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-v7")
		_, roomID := seedVoiceChannelRoom(t, h.db, "space-v7", "voice", "admin-only")

		w := doVoiceJSON(t, r, "/voice/join", roomID)
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
		}
		code, _ := respFields(t, w)
		if code != string(errors.CHANNEL_ACCESS_DENIED) {
			t.Fatalf("expected code CHANNEL_ACCESS_DENIED, got %s", code)
		}
		var n int64
		if err := h.db.Model(&model.VoiceParticipant{}).Where("room_id = ?", roomID).Count(&n).Error; err != nil {
			t.Fatalf("count participants: %v", err)
		}
		if n != 0 {
			t.Fatalf("participant must not be persisted on 403, got %d rows", n)
		}
	})
}
