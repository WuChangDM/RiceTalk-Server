package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// H2 频道权限接口（GET/PUT /api/admin/channels/:channelId/permissions）此前
// 没有任何测试：整条「管理页勾选角色 → 落库 → 回读」的链路无人看守。
// 本文件覆盖默认值、回读一致性、nil 规范化、404、损坏 JSON 回落与鉴权。

// setupChannelPermsHandler 建一个 owner + 一个 member，返回 owner / member 两个
// 有效 token，用于区分「有权」与「无权」两条路径。
//
// 路由上挂了 AuthRequiredWithState + RequireAdmin —— 与生产路径一致
// （internal/server/routes.go 的 registerAdminRoutes）。handler.RegisterRoutes
// 本身不挂鉴权中间件，只有调用方挂，所以鉴权断言必须自己装配。
func setupChannelPermsHandler(t *testing.T) (*Handler, *gin.Engine, string, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"

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

	ownerToken, _, err := middleware.GenerateTokenPair(
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

	handler := NewHandler(db, cfg, nil, middleware.NewOwnerBreakGlassProtector())
	router := gin.New()
	group := router.Group("/api")
	group.Use(middleware.AuthRequiredWithState(cfg, db, nil))
	group.Use(middleware.RequireAdmin())
	handler.RegisterRoutes(group)
	return handler, router, ownerToken, memberToken
}

// seedPermChannel 建一个 space + channel；permissions 为空表示「无覆盖」。
func seedPermChannel(t *testing.T, db *gorm.DB, name string, permissions string) *model.Channel {
	t.Helper()
	space := &model.Space{ID: idgen.NextString(), Name: "S", OwnerID: "owner"}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	ch := &model.Channel{
		ID: idgen.NextString(), SpaceID: space.ID, Name: name, Type: "text",
		Permissions: permissions,
	}
	if err := db.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	return ch
}

func doPermsRequest(t *testing.T, router *gin.Engine, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == nil {
		req, _ = http.NewRequest(method, path, nil)
	} else {
		req, _ = http.NewRequest(method, path, bytes.NewReader(body))
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func decodePermsEnvelope(t *testing.T, w *httptest.ResponseRecorder) (string, map[string]interface{}) {
	t.Helper()
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", w.Body.String(), err)
	}
	code, _ := resp["code"].(string)
	data, _ := resp["data"].(map[string]interface{})
	return code, data
}

func TestGetChannelPermissionsDefaultsWithoutOverrides(t *testing.T) {
	handler, router, ownerToken, _ := setupChannelPermsHandler(t)
	ch := seedPermChannel(t, handler.db, "general", "")

	w := doPermsRequest(t, router, http.MethodGet, "/api/admin/channels/"+ch.ID+"/permissions", ownerToken, nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	code, data := decodePermsEnvelope(t, w)
	if code != "OK" {
		t.Fatalf("code = %q, want OK", code)
	}
	// 三个角色列表必须是 []，不是 null —— 前端直接 list.includes(role)。
	for _, key := range []string{"read_roles", "write_roles", "visible_roles"} {
		if data[key] == nil {
			t.Errorf("%s = null, want [] (前端会直接调 .includes)", key)
		}
	}
	if data["is_public"] != true {
		t.Errorf("is_public = %v, want true (默认公开)", data["is_public"])
	}
}

func TestUpdateChannelPermissionsRoundTripAndPersist(t *testing.T) {
	handler, router, ownerToken, _ := setupChannelPermsHandler(t)
	ch := seedPermChannel(t, handler.db, "secret", "")

	payload := map[string]interface{}{
		"read_roles":    []string{"ADMIN", "OWNER"},
		"write_roles":   []string{"ADMIN"},
		"visible_roles": []string{"OWNER"},
		"is_public":     false,
	}
	body, _ := json.Marshal(payload)
	w := doPermsRequest(t, router, http.MethodPut, "/api/admin/channels/"+ch.ID+"/permissions", ownerToken, body)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if _, data := decodePermsEnvelope(t, w); data["is_public"] != false {
		t.Errorf("PUT 响应 is_public = %v, want false", data["is_public"])
	}

	// 落库：channels.permissions 列必须是刚提交的 JSON
	var stored model.Channel
	if err := handler.db.Select("id, permissions").First(&stored, "id = ?", ch.ID).Error; err != nil {
		t.Fatalf("reload channel: %v", err)
	}
	var persisted ChannelPermissions
	if err := json.Unmarshal([]byte(stored.Permissions), &persisted); err != nil {
		t.Fatalf("persisted permissions is not valid JSON (%q): %v", stored.Permissions, err)
	}
	if len(persisted.ReadRoles) != 2 || persisted.ReadRoles[0] != "ADMIN" {
		t.Errorf("persisted read_roles = %v, want [ADMIN OWNER]", persisted.ReadRoles)
	}
	if persisted.IsPublic {
		t.Error("persisted is_public = true, want false")
	}

	// 回读一致：管理页保存后会立刻 GET 一次做校验
	w = doPermsRequest(t, router, http.MethodGet, "/api/admin/channels/"+ch.ID+"/permissions", ownerToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", w.Code)
	}
	_, readBack := decodePermsEnvelope(t, w)
	if readBack["is_public"] != false {
		t.Errorf("read-back is_public = %v, want false", readBack["is_public"])
	}
	roles, _ := readBack["visible_roles"].([]interface{})
	if len(roles) != 1 || roles[0] != "OWNER" {
		t.Errorf("read-back visible_roles = %v, want [OWNER]", readBack["visible_roles"])
	}

	// 审计：权限变更必须留痕
	var auditCount int64
	if err := handler.db.Model(&model.AuditLog{}).
		Where("action = ?", "admin_update_channel_permissions").Count(&auditCount).Error; err != nil {
		t.Fatalf("count audit logs: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("audit log count = %d, want 1", auditCount)
	}
}

// 前端只提交被勾选的部分字段时（body 里没有 role 列表），响应必须是 [] 而不是
// null —— 否则前端回显时会因为 null.includes 直接崩。
func TestUpdateChannelPermissionsNormalizesMissingRoleLists(t *testing.T) {
	handler, router, ownerToken, _ := setupChannelPermsHandler(t)
	ch := seedPermChannel(t, handler.db, "no-roles", "")

	body, _ := json.Marshal(map[string]interface{}{"is_public": false})
	w := doPermsRequest(t, router, http.MethodPut, "/api/admin/channels/"+ch.ID+"/permissions", ownerToken, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	_, data := decodePermsEnvelope(t, w)
	for _, key := range []string{"read_roles", "write_roles", "visible_roles"} {
		if data[key] == nil {
			t.Errorf("%s = null, want []", key)
		}
	}

	// 空列表语义 = 不限制；回读也必须还是空列表
	w = doPermsRequest(t, router, http.MethodGet, "/api/admin/channels/"+ch.ID+"/permissions", ownerToken, nil)
	_, readBack := decodePermsEnvelope(t, w)
	if readBack["read_roles"] == nil {
		t.Error("read-back read_roles = null, want []")
	}
}

func TestChannelPermissionsNotFound(t *testing.T) {
	_, router, ownerToken, _ := setupChannelPermsHandler(t)

	w := doPermsRequest(t, router, http.MethodGet, "/api/admin/channels/missing-id/permissions", ownerToken, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET missing channel status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
	if code, _ := decodePermsEnvelope(t, w); code != "CHANNEL_NOT_FOUND" {
		t.Errorf("GET missing channel code = %q, want CHANNEL_NOT_FOUND", code)
	}

	body, _ := json.Marshal(map[string]interface{}{"is_public": true})
	w = doPermsRequest(t, router, http.MethodPut, "/api/admin/channels/missing-id/permissions", ownerToken, body)
	if w.Code != http.StatusNotFound {
		t.Errorf("PUT missing channel status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
	if code, _ := decodePermsEnvelope(t, w); code != "CHANNEL_NOT_FOUND" {
		t.Errorf("PUT missing channel code = %q, want CHANNEL_NOT_FOUND", code)
	}
}

// permissions 列里的历史脏数据（人工改库、旧版本写入失败等）不能让管理页 500；
// 回落到默认权限，界面仍可用。
func TestGetChannelPermissionsCorruptJSONFallsBackToDefaults(t *testing.T) {
	handler, router, ownerToken, _ := setupChannelPermsHandler(t)
	ch := seedPermChannel(t, handler.db, "corrupt", "{not-json")

	w := doPermsRequest(t, router, http.MethodGet, "/api/admin/channels/"+ch.ID+"/permissions", ownerToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (脏数据应回落默认值); body=%s", w.Code, w.Body.String())
	}
	_, data := decodePermsEnvelope(t, w)
	if data["is_public"] != true {
		t.Errorf("is_public = %v, want true (默认值)", data["is_public"])
	}
	if data["read_roles"] == nil {
		t.Error("read_roles = null, want []")
	}
}

func TestChannelPermissionsRequireAdminRole(t *testing.T) {
	handler, router, ownerToken, memberToken := setupChannelPermsHandler(t)
	ch := seedPermChannel(t, handler.db, "guarded", "")
	body, _ := json.Marshal(map[string]interface{}{"is_public": false})

	for _, tc := range []struct {
		name   string
		method string
		body   []byte
	}{
		{"GET", http.MethodGet, nil},
		{"PUT", http.MethodPut, body},
	} {
		t.Run(tc.name+" 普通成员被拒", func(t *testing.T) {
			w := doPermsRequest(t, router, tc.method, "/api/admin/channels/"+ch.ID+"/permissions", memberToken, tc.body)
			if w.Code != http.StatusForbidden {
				t.Errorf("member status = %d, want 403; body=%s", w.Code, w.Body.String())
			}
		})
		t.Run(tc.name+" 未登录被拒", func(t *testing.T) {
			w := doPermsRequest(t, router, tc.method, "/api/admin/channels/"+ch.ID+"/permissions", "", tc.body)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("anonymous status = %d, want 401; body=%s", w.Code, w.Body.String())
			}
		})
	}

	// 被拒的 PUT 不能改动数据
	var stored model.Channel
	if err := handler.db.Select("id, permissions").First(&stored, "id = ?", ch.ID).Error; err != nil {
		t.Fatalf("reload channel: %v", err)
	}
	if stored.Permissions != "" {
		t.Errorf("member PUT 修改了权限: %q", stored.Permissions)
	}
	_ = ownerToken
}
