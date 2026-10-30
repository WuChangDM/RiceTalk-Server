package voice

// R-04（安全批次 task-22）E2EE 密钥与语音参与者名单的频道可见性回归测试：
//   - 私有房（role-specific 无权限行）取 key → 403 CHANNEL_ACCESS_DENIED，响应无 key 字段且未生成密钥行；
//   - DM 非参与者 → 404 CHANNEL_NOT_FOUND（dm 判定先于 OWNER 放行）；
//   - 频道已软删 → 404 CHANNEL_NOT_FOUND；
//   - 有权限成员 → 200，server 模式 key 为 64 位 hex；
//   - 口令房（passphrase 模式判定顺序不变）→ 200 只返回 magic、绝不返回 key；
//   - GET /voice/participants/:id 同一套判定：403 / 404 / 200。
//
// 全部真 SQLite（testutil.MustSetupTestDB）+ httptest，注入真实认证上下文，不 mock 数据层；
// 频道判定复用 middleware.CheckChannelAccess（经 authorizeVoiceRoomAccess），禁止复制口径。

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// newVoiceE2EERouter 构造只挂 e2ee/key 与 participants 两条路由的 gin 路由。
func newVoiceE2EERouter(t *testing.T, role string) (*Handler, *gin.Engine) {
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
	r.GET("/voice/e2ee/key", auth, h.GetE2EEKey)
	r.GET("/voice/participants/:id", auth, h.GetParticipants)
	return h, r
}

func getVoice(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// assertNoKeyMaterial 逐字断言响应体里不存在任何密钥材料字段（不能只看状态码）。
func assertNoKeyMaterial(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	for _, key := range []string{`"key"`, `"e2eeKey"`, `"secret"`, `"magic"`} {
		if strings.Contains(w.Body.String(), key) {
			t.Fatalf("forbidden response must not contain %s, body=%s", key, w.Body.String())
		}
	}
}

func countE2EEKeys(t *testing.T, h *Handler, roomID string) int64 {
	t.Helper()
	var n int64
	if err := h.db.Model(&model.E2EEKey{}).Where("room_id = ?", roomID).Count(&n).Error; err != nil {
		t.Fatalf("count e2ee keys: %v", err)
	}
	return n
}

func TestGetE2EEKeyRequiresChannelAccess(t *testing.T) {
	t.Run("role-specific 无权限 → 403 且不下发密钥材料、不落密钥行", func(t *testing.T) {
		h, r := newVoiceE2EERouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-e1")
		_, roomID := seedVoiceChannelRoom(t, h.db, "space-e1", "voice", "role-specific")

		w := getVoice(t, r, "/voice/e2ee/key?roomId="+roomID)
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
		}
		code, _ := respFields(t, w)
		if code != string(errors.CHANNEL_ACCESS_DENIED) {
			t.Fatalf("expected code CHANNEL_ACCESS_DENIED, got %s", code)
		}
		assertNoKeyMaterial(t, w)
		// 403 时必须在读库/生成密钥之前返回：不得留下 e2ee_keys 行。
		if n := countE2EEKeys(t, h, roomID); n != 0 {
			t.Fatalf("forbidden request must not create e2ee key rows, got %d", n)
		}
	})

	t.Run("DM 房间非参与者 → 404 CHANNEL_NOT_FOUND", func(t *testing.T) {
		h, r := newVoiceE2EERouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-e2")
		_, roomID := seedVoiceChannelRoom(t, h.db, "space-e2", "dm", "public")

		w := getVoice(t, r, "/voice/e2ee/key?roomId="+roomID)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
		}
		code, _ := respFields(t, w)
		if code != string(errors.CHANNEL_NOT_FOUND) {
			t.Fatalf("expected code CHANNEL_NOT_FOUND, got %s", code)
		}
		assertNoKeyMaterial(t, w)
	})

	t.Run("频道已软删 → 404 CHANNEL_NOT_FOUND", func(t *testing.T) {
		h, r := newVoiceE2EERouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-e3")
		chID, roomID := seedVoiceChannelRoom(t, h.db, "space-e3", "voice", "public")
		if err := h.db.Delete(&model.Channel{}, "id = ?", chID).Error; err != nil {
			t.Fatalf("soft delete channel: %v", err)
		}

		w := getVoice(t, r, "/voice/e2ee/key?roomId="+roomID)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
		}
		code, _ := respFields(t, w)
		if code != string(errors.CHANNEL_NOT_FOUND) {
			t.Fatalf("expected code CHANNEL_NOT_FOUND, got %s", code)
		}
		assertNoKeyMaterial(t, w)
	})

	t.Run("有权限成员 → 200 且 server 模式返回 64 位 hex key", func(t *testing.T) {
		h, r := newVoiceE2EERouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-e4")
		chID, roomID := seedVoiceChannelRoom(t, h.db, "space-e4", "voice", "role-specific")
		if err := h.db.Create(&model.ChannelRolePermission{
			ID: "crp-e4", ChannelID: chID, Role: middleware.RoleMember, CanView: true, CanWrite: true,
		}).Error; err != nil {
			t.Fatalf("create channel role permission: %v", err)
		}

		w := getVoice(t, r, "/voice/e2ee/key?roomId="+roomID)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
		}
		_, data := respFields(t, w)
		if mode, _ := data["mode"].(string); mode != E2EEModeServer {
			t.Fatalf("expected mode %s, got %v", E2EEModeServer, data["mode"])
		}
		key, _ := data["key"].(string)
		if len(key) != 64 {
			t.Fatalf("expected 64-char hex key, got %q", key)
		}
		if _, err := hex.DecodeString(key); err != nil {
			t.Fatalf("expected hex key, got %q: %v", key, err)
		}
		if n := countE2EEKeys(t, h, roomID); n != 1 {
			t.Fatalf("authorized request must persist exactly one key row, got %d", n)
		}
	})

	t.Run("client 模式（passphrase 自持密钥）+ 有权限 → 200 只回 magic 无 key", func(t *testing.T) {
		h, r := newVoiceE2EERouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-e5")
		chID, roomID := seedVoiceChannelRoom(t, h.db, "space-e5", "voice", "role-specific")
		if err := h.db.Create(&model.ChannelRolePermission{
			ID: "crp-e5", ChannelID: chID, Role: middleware.RoleMember, CanView: true, CanWrite: true,
		}).Error; err != nil {
			t.Fatalf("create channel role permission: %v", err)
		}
		blob := makeMagicBlob(t)
		if err := h.service.SetE2EEPassphrase(roomID, &blob); err != nil {
			t.Fatalf("set e2ee passphrase: %v", err)
		}

		w := getVoice(t, r, "/voice/e2ee/key?roomId="+roomID)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
		}
		_, data := respFields(t, w)
		if mode, _ := data["mode"].(string); mode != E2EEModePassphrase {
			t.Fatalf("expected mode %s, got %v", E2EEModePassphrase, data["mode"])
		}
		if magic, _ := data["magic"].(string); magic != blob {
			t.Fatalf("expected magic %q, got %v", blob, data["magic"])
		}
		if _, ok := data["key"]; ok {
			t.Fatalf("passphrase mode must never return key, body=%s", w.Body.String())
		}
	})

	t.Run("client 模式（passphrase 自持密钥）+ 无频道权限 → 200 且不含真实 key 材料", func(t *testing.T) {
		h, r := newVoiceE2EERouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-e6")
		_, roomID := seedVoiceChannelRoom(t, h.db, "space-e6", "voice", "admin-only")
		blob := makeMagicBlob(t)
		if err := h.service.SetE2EEPassphrase(roomID, &blob); err != nil {
			t.Fatalf("set e2ee passphrase: %v", err)
		}

		// client 模式不依赖服务端下发密钥：无频道可见性也不得被 403 锁死（否则正常功能被误伤）。
		w := getVoice(t, r, "/voice/e2ee/key?roomId="+roomID)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for client mode without channel access, got %d body=%s", w.Code, w.Body.String())
		}
		_, data := respFields(t, w)
		if mode, _ := data["mode"].(string); mode != E2EEModePassphrase {
			t.Fatalf("expected mode %s, got %v", E2EEModePassphrase, data["mode"])
		}
		if _, ok := data["key"]; ok {
			t.Fatalf("client mode response must not carry real key material, body=%s", w.Body.String())
		}
		if strings.Contains(w.Body.String(), `"secret"`) || strings.Contains(w.Body.String(), `"e2eeKey"`) {
			t.Fatalf("client mode response must not carry key fields, body=%s", w.Body.String())
		}
		// 服务端模式下密钥才会落库；client 模式不应产生 e2ee_keys 行。
		if n := countE2EEKeys(t, h, roomID); n != 0 {
			t.Fatalf("client mode must not snapshot server key, got %d rows", n)
		}
	})
}

func TestGetParticipantsRequiresChannelAccess(t *testing.T) {
	t.Run("role-specific 无权限 → 403", func(t *testing.T) {
		h, r := newVoiceE2EERouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-p1")
		_, roomID := seedVoiceChannelRoom(t, h.db, "space-p1", "voice", "role-specific")

		w := getVoice(t, r, "/voice/participants/"+roomID)
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
		}
		code, _ := respFields(t, w)
		if code != string(errors.CHANNEL_ACCESS_DENIED) {
			t.Fatalf("expected code CHANNEL_ACCESS_DENIED, got %s", code)
		}
	})

	t.Run("DM 房间非参与者 → 404", func(t *testing.T) {
		h, r := newVoiceE2EERouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-p2")
		_, roomID := seedVoiceChannelRoom(t, h.db, "space-p2", "dm", "public")

		w := getVoice(t, r, "/voice/participants/"+roomID)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
		}
		code, _ := respFields(t, w)
		if code != string(errors.CHANNEL_NOT_FOUND) {
			t.Fatalf("expected code CHANNEL_NOT_FOUND, got %s", code)
		}
	})

	t.Run("有权限成员 → 200 且 data 为参与者数组", func(t *testing.T) {
		h, r := newVoiceE2EERouter(t, middleware.RoleMember)
		seedVoiceSpaceAndMember(t, h.db, "space-p3")
		chID, roomID := seedVoiceChannelRoom(t, h.db, "space-p3", "voice", "role-specific")
		if err := h.db.Create(&model.ChannelRolePermission{
			ID: "crp-p3", ChannelID: chID, Role: middleware.RoleMember, CanView: true, CanWrite: true,
		}).Error; err != nil {
			t.Fatalf("create channel role permission: %v", err)
		}

		w := getVoice(t, r, "/voice/participants/"+roomID)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			Code string        `json:"code"`
			Data []interface{} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode participants response %q: %v", w.Body.String(), err)
		}
		if resp.Code != "OK" {
			t.Fatalf("expected code OK, got %s", resp.Code)
		}
		if resp.Data == nil {
			t.Fatalf("expected participants array in data, body=%s", w.Body.String())
		}
	})
}
