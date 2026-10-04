package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// GET /api/admin/tts/status 与 POST /api/admin/tts/reinit 此前无测试。
// 这两条接口的存在意义是让运维在模型缺失/损坏后能从管理页看到原因并重初始化，
// 因此「失败必须明确失败」比「成功」更值得锁住。

func setupTTSHandler(t *testing.T) (*Handler, *gin.Engine, string, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"
	cfg.LocalDataPath = t.TempDir()

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

func doTTSRequest(t *testing.T, router *gin.Engine, method, path, token string, body []byte) *httptest.ResponseRecorder {
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

func decodeTTSEnvelope(t *testing.T, w *httptest.ResponseRecorder) (string, map[string]interface{}) {
	t.Helper()
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", w.Body.String(), err)
	}
	code, _ := resp["code"].(string)
	data, _ := resp["data"].(map[string]interface{})
	return code, data
}

// 状态接口必须始终给出 ready / model / error 三个字段（类型稳定），
// 管理页据此显示「未初始化 + 原因」，而不是显示空白。
func TestGetTTSStatusReturnsStableShape(t *testing.T) {
	_, router, ownerToken, _ := setupTTSHandler(t)

	w := doTTSRequest(t, router, http.MethodGet, "/api/admin/tts/status", ownerToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	code, data := decodeTTSEnvelope(t, w)
	if code != "OK" {
		t.Fatalf("code = %q, want OK", code)
	}
	for _, key := range []string{"ready", "model", "error"} {
		if _, ok := data[key]; !ok {
			t.Errorf("data 缺少字段 %q: %v", key, data)
		}
	}
	if _, ok := data["ready"].(bool); !ok {
		t.Errorf("ready 类型 = %T, want bool", data["ready"])
	}
	if _, ok := data["model"].(string); !ok {
		t.Errorf("model 类型 = %T, want string", data["model"])
	}
	if _, ok := data["error"].(string); !ok {
		t.Errorf("error 类型 = %T, want string", data["error"])
	}
}

// 模型目录不存在时必须报错（500 + 原因），绝不能返回 200 谎报「已重新初始化」。
func TestReinitTTSWithMissingModelDirReportsFailure(t *testing.T) {
	_, router, ownerToken, _ := setupTTSHandler(t)

	body, _ := json.Marshal(map[string]string{"modelDir": t.TempDir()}) // 空目录，没有 model.onnx
	w := doTTSRequest(t, router, http.MethodPost, "/api/admin/tts/reinit", ownerToken, body)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500（模型缺失不能谎报成功）; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// 失败必须是失败信封，而不是 {success:true}
	if _, ok := resp["data"]; ok {
		t.Errorf("失败响应不应带 data（会看起来像成功）: %s", w.Body.String())
	}
	if message, _ := resp["message"].(string); message == "success" {
		t.Errorf("message = %q, 不能是 success", message)
	}
	// 具体原因在 details（ErrInternal.WithDetails），运维据此定位是模型缺失还是加载失败
	details, _ := resp["details"].(string)
	if !strings.Contains(details, "TTS reinit failed") {
		t.Errorf("details = %q, want 含 'TTS reinit failed'", details)
	}
}

func TestTTSRoutesRequireAdminRole(t *testing.T) {
	_, router, _, memberToken := setupTTSHandler(t)
	body, _ := json.Marshal(map[string]string{"modelDir": t.TempDir()})

	for _, tc := range []struct {
		name, method, path string
		body               []byte
	}{
		{"status", http.MethodGet, "/api/admin/tts/status", nil},
		{"reinit", http.MethodPost, "/api/admin/tts/reinit", body},
	} {
		t.Run(tc.name+" 普通成员被拒", func(t *testing.T) {
			w := doTTSRequest(t, router, tc.method, tc.path, memberToken, tc.body)
			if w.Code != http.StatusForbidden {
				t.Errorf("member status = %d, want 403; body=%s", w.Code, w.Body.String())
			}
		})
		t.Run(tc.name+" 未登录被拒", func(t *testing.T) {
			w := doTTSRequest(t, router, tc.method, tc.path, "", tc.body)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("anonymous status = %d, want 401; body=%s", w.Code, w.Body.String())
			}
		})
	}
}
