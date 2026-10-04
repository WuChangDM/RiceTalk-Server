package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// setupAdminHandler builds a handler + router + owner access token for
// exercising admin HTTP endpoints end-to-end.
func setupAdminHandler(t *testing.T) (*Handler, *gin.Engine, *model.User, string) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"

	owner := &model.User{
		ID: idgen.NextString(), Username: "owner", Email: "owner@example.com",
		PasswordHash: "x", Role: middleware.RoleOwner, IsActive: true,
	}
	if err := db.Create(owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	token, _, err := middleware.GenerateTokenPair(owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	handler := NewHandler(db, cfg, nil, middleware.NewOwnerBreakGlassProtector())
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))
	return handler, router, owner, token
}

func TestHandlerGetConfig(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/config", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["code"] != "OK" {
		t.Errorf("expected code OK, got %v", resp["code"])
	}
}

func TestNewHandlerWithServiceReusesProvidedService(t *testing.T) {
	service := &Service{}
	handler := NewHandlerWithService(nil, nil, nil, nil, nil, service)
	if handler.service != service {
		t.Fatal("handler did not retain the provided admin service")
	}
}

func TestHandlerUpdateConfig(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	body, _ := json.Marshal(map[string]interface{}{
		"serverName": "Updated Server",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/admin/config", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

func TestHandlerUpdateConfigRejectsNegativeFileSize(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	bigSize := -1
	body, _ := json.Marshal(map[string]interface{}{
		"maxFileSize": bigSize,
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/admin/config", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for negative maxFileSize, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestHandlerGetUsers(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/users?page=1&pageSize=10", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("expected data object")
	}
	if _, ok := data["summary"]; !ok {
		t.Errorf("expected summary in users response")
	}
}

func TestHandlerGetStorage(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/storage", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

func TestHandlerGetModules(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/modules", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	items, _ := data["items"].([]interface{})
	if len(items) == 0 {
		t.Errorf("expected at least one module in response")
	}
}

func TestHandlerModuleAction(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	body, _ := json.Marshal(map[string]string{"action": "disable"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/modules/voice/actions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

func TestHandlerModuleActionInvalidBody(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	// Invalid JSON body
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/modules/voice/actions", bytes.NewReader([]byte("not-json")))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for invalid body, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestHandlerGetRuntime(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/runtime", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

func TestHandlerGetSystemUsage(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/system/usage", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

// TestHandlerCheckUpdate verifies the endpoint is explicit about being
// unimplemented instead of fabricating an "up to date" answer (P1-3).
func TestHandlerCheckUpdate(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/update/check", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data["implemented"] != false {
		t.Errorf("expected implemented=false, got %v", data["implemented"])
	}
	if data["latest_version"] != "" {
		t.Errorf("expected empty latest_version (no release channel), got %v", data["latest_version"])
	}
	if data["changelog"] == "当前已是最新版本" {
		t.Errorf("changelog must not claim the server is up to date")
	}
}

func TestHandlerGetAuditLogs(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/audit-logs?page=1&pageSize=10", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

func TestHandlerGetSecurityAuditLogs(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/security-audit-logs?page=1&pageSize=10", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

func TestHandlerExportAuditLogs(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/audit-logs/export", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if ct != "text/csv; charset=utf-8" {
		t.Errorf("expected Content-Type text/csv; charset=utf-8, got %q", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if cd == "" {
		t.Errorf("expected Content-Disposition header set")
	}
	// BOM prefix for Excel UTF-8 compatibility
	if !bytes.HasPrefix(w.Body.Bytes(), []byte{0xEF, 0xBB, 0xBF}) {
		t.Errorf("expected UTF-8 BOM at start of CSV export")
	}
}

func TestHandlerDeleteUserCannotDeleteOwner(t *testing.T) {
	_, router, owner, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", "/api/admin/users/"+owner.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	// Cannot delete owner
	if !bytes.Contains(w.Body.Bytes(), []byte("ADMIN_CANNOT_DELETE_OWNER")) {
		t.Errorf("expected ADMIN_CANNOT_DELETE_OWNER error, got: %s", w.Body.String())
	}
}

func TestHandlerDeleteUserNotFound(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", "/api/admin/users/nonexistent", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status %d for non-existent user, got %d, body: %s",
			http.StatusNotFound, w.Code, w.Body.String())
	}
}

func TestHandlerUpdateUserStatus(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	// Missing body → bad request
	req, _ := http.NewRequest("PATCH", "/api/admin/users/some-uid/status", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for missing isActive, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestHandlerResetUserPassword(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	// Empty password → bad request
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/users/some-uid/reset-password", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for missing password, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestHandlerAdminListChannels(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/channels", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

func TestHandlerAdminCreateChannelInvalidType(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	body, _ := json.Marshal(map[string]string{"name": "ch", "type": "invalid"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/channels", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for invalid channel type, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestHandlerAdminCreateChannelSuccess(t *testing.T) {
	handler, router, _, token := setupAdminHandler(t)

	// CreateChannel requires a Space to exist (single-space model). Seed one
	// using the handler's underlying DB before invoking the endpoint.
	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: "owner"}
	if err := handler.db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"name": "newch", "type": "text"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/channels", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

func TestHandlerAdminUpdateChannelInvalidVisibility(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	body, _ := json.Marshal(map[string]string{"visibility": "bogus"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/api/admin/channels/some-id", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for invalid visibility, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestHandlerTransferChannelOwnershipMissingField(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/channels/some-id/transfer", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for missing new_owner_id, got %d", http.StatusBadRequest, w.Code)
	}
}

// TestHandlerAdminUpdateChannelSuccess covers the success path of
// AdminUpdateChannel — needs a real channel to update.
func TestHandlerAdminUpdateChannelSuccess(t *testing.T) {
	handler, router, _, token := setupAdminHandler(t)

	// Seed a space + channel so the update has a target.
	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: "owner"}
	if err := handler.db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	ch := &model.Channel{
		ID: idgen.NextString(), SpaceID: space.ID, Name: "ToUpdate", Type: "text",
	}
	if err := handler.db.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"name":       "Renamed",
		"visibility": "admin-only",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/api/admin/channels/"+ch.ID, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	// Verify the channel was updated.
	var updated model.Channel
	if err := handler.db.First(&updated, "id = ?", ch.ID).Error; err != nil {
		t.Fatalf("query channel: %v", err)
	}
	if updated.Name != "Renamed" {
		t.Errorf("expected name Renamed, got %s", updated.Name)
	}
	if updated.Visibility != "admin-only" {
		t.Errorf("expected visibility admin-only, got %s", updated.Visibility)
	}
}

// TestHandlerAdminUpdateChannelIsPrivate covers the IsPrivate → visibility
// conversion branch.
func TestHandlerAdminUpdateChannelIsPrivate(t *testing.T) {
	handler, router, _, token := setupAdminHandler(t)

	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: "owner"}
	if err := handler.db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	ch := &model.Channel{
		ID: idgen.NextString(), SpaceID: space.ID, Name: "Priv", Type: "text",
	}
	if err := handler.db.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	isPrivate := true
	body, _ := json.Marshal(map[string]interface{}{"isPrivate": isPrivate})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/api/admin/channels/"+ch.ID, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var updated model.Channel
	if err := handler.db.First(&updated, "id = ?", ch.ID).Error; err != nil {
		t.Fatalf("query channel: %v", err)
	}
	if updated.Visibility != "admin-only" {
		t.Errorf("expected visibility admin-only for isPrivate=true, got %s", updated.Visibility)
	}
}

// TestHandlerAdminUpdateChannelPosition covers the Position update branch.
func TestHandlerAdminUpdateChannelPosition(t *testing.T) {
	handler, router, _, token := setupAdminHandler(t)

	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: "owner"}
	if err := handler.db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	ch := &model.Channel{
		ID: idgen.NextString(), SpaceID: space.ID, Name: "Pos", Type: "text",
	}
	if err := handler.db.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	pos := 5
	body, _ := json.Marshal(map[string]interface{}{"position": pos})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/api/admin/channels/"+ch.ID, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var updated model.Channel
	if err := handler.db.First(&updated, "id = ?", ch.ID).Error; err != nil {
		t.Fatalf("query channel: %v", err)
	}
	if updated.Position != 5 {
		t.Errorf("expected position 5, got %d", updated.Position)
	}
}

// P1-1 回归：admin 频道路径此前不接收 audioQuality，音频质量选择器会被
// 静默丢弃。改用 admin 接口后必须真正落库到 channels.voice_quality。
func TestHandlerAdminUpdateChannelAudioQuality(t *testing.T) {
	handler, router, _, token := setupAdminHandler(t)

	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: "owner"}
	if err := handler.db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	ch := &model.Channel{
		ID: idgen.NextString(), SpaceID: space.ID, Name: "Voice", Type: "voice",
		VoiceQuality: "standard",
	}
	if err := handler.db.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	body, _ := json.Marshal(map[string]interface{}{"audioQuality": "ultra"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/api/admin/channels/"+ch.ID, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var updated model.Channel
	if err := handler.db.First(&updated, "id = ?", ch.ID).Error; err != nil {
		t.Fatalf("query channel: %v", err)
	}
	if updated.VoiceQuality != "ultra" {
		t.Errorf("expected voiceQuality ultra, got %q", updated.VoiceQuality)
	}
}

// P1-1 回归：非法 audioQuality 必须被 admin 接口拒绝（取值域与
// channel/handler.go 的 UpdateChannel 一致）。
func TestHandlerAdminUpdateChannelInvalidAudioQuality(t *testing.T) {
	_, router, _, token := setupAdminHandler(t)

	body, _ := json.Marshal(map[string]interface{}{"audioQuality": "lossless"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/api/admin/channels/some-id", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for invalid audioQuality, got %d: %s",
			http.StatusBadRequest, w.Code, w.Body.String())
	}
}

// P1-1 回归：admin 创建语音频道时 audioQuality 必须落库。
func TestHandlerAdminCreateChannelAudioQuality(t *testing.T) {
	handler, router, _, token := setupAdminHandler(t)

	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: "owner"}
	if err := handler.db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"name": "voicech", "type": "voice", "audioQuality": "high",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/channels", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var created model.Channel
	if err := handler.db.First(&created, "name = ?", "voicech").Error; err != nil {
		t.Fatalf("query channel: %v", err)
	}
	if created.VoiceQuality != "high" {
		t.Errorf("expected voiceQuality high, got %q", created.VoiceQuality)
	}
}

// TestHandlerAdminDeleteChannel covers AdminDeleteChannel on the success path.
//
// 历史背景：channel.Service.DeleteChannel 曾用不存在的列 bind_channel_id 删除
// ScreenShareSession，导致删除频道事务必然失败、接口恒返回 500。旧版本这个用例
// 把这个 500 断言成预期行为，等于把 bug 写成了规格；列名修正为 channel_id 后，
// 用例改为断言删除成功（200 + 记录真被删除），失败路径改由「重复删除同一频道
// 必须报 CHANNEL_NOT_FOUND」覆盖。
func TestHandlerAdminDeleteChannel(t *testing.T) {
	handler, router, _, token := setupAdminHandler(t)

	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: "owner"}
	if err := handler.db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	ch := &model.Channel{
		ID: idgen.NextString(), SpaceID: space.ID, Name: "ToDelete", Type: "text",
	}
	if err := handler.db.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", "/api/admin/channels/"+ch.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	// 频道是软删除，用 Unscoped 取出并确认 DeletedAt 已置位。
	var stored model.Channel
	if err := handler.db.Unscoped().First(&stored, "id = ?", ch.ID).Error; err != nil {
		t.Fatalf("reload channel: %v", err)
	}
	if !stored.DeletedAt.Valid {
		t.Errorf("channel %s still active after delete", ch.ID)
	}

	// 失败路径：再次删除同一频道必须返回 404 CHANNEL_NOT_FOUND，
	// 而不是 panic 或 500。
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/api/admin/channels/"+ch.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status %d for repeated delete, got %d: %s",
			http.StatusNotFound, w.Code, w.Body.String())
	}
}

// P0-4 回归：基本配置的端口字段此前被 handler 静默忽略（body 未映射），
// 修复后必须真正落库到 AdminConfig。
func TestHandlerUpdateConfigPorts(t *testing.T) {
	handler, router, _, token := setupAdminHandler(t)

	body, _ := json.Marshal(map[string]interface{}{
		"apiPort":     8123,
		"livekitPort": 7980,
		"vpnPort":     42424,
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/admin/config", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var stored model.AdminConfig
	if err := handler.db.First(&stored).Error; err != nil {
		t.Fatalf("load config: %v", err)
	}
	if stored.APIPort != 8123 {
		t.Errorf("APIPort = %d, want 8123", stored.APIPort)
	}
	if stored.LiveKitPort != 7980 {
		t.Errorf("LiveKitPort = %d, want 7980", stored.LiveKitPort)
	}
	if stored.VPNPort != 42424 {
		t.Errorf("VPNPort = %d, want 42424", stored.VPNPort)
	}
}

// P0-4 回归：非法端口（越界）应被拒绝，不得静默写入。
func TestHandlerUpdateConfigRejectsInvalidPort(t *testing.T) {
	handler, router, _, token := setupAdminHandler(t)

	body, _ := json.Marshal(map[string]interface{}{"apiPort": 70000})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/admin/config", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d for out-of-range apiPort, got %d: %s",
			http.StatusBadRequest, w.Code, w.Body.String())
	}

	// 不应创建/写入任何端口变更。
	var stored model.AdminConfig
	if err := handler.db.First(&stored).Error; err == nil && stored.APIPort == 70000 {
		t.Errorf("invalid apiPort was persisted: %d", stored.APIPort)
	}
}
