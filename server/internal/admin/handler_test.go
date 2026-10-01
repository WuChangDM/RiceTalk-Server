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

func init() {
	_ = idgen.Init(1, 1)
}

func TestHandlerUpdateUserRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"

	owner := &model.User{ID: idgen.NextString(), Username: "owner", Email: "owner@example.com", PasswordHash: "x", Role: middleware.RoleOwner}
	member := &model.User{ID: idgen.NextString(), Username: "member", Email: "member@example.com", PasswordHash: "x", Role: middleware.RoleMember}
	for _, u := range []*model.User{owner, member} {
		if err := db.Create(u).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}

	accessToken, _, err := middleware.GenerateTokenPair(owner.ID, owner.Username, owner.Email, owner.Role, owner.TokenVersion, "", "", cfg)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	handler := NewHandler(db, cfg, nil, middleware.NewOwnerBreakGlassProtector())
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	body, _ := json.Marshal(map[string]string{"role": middleware.RoleAdmin})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/admin/users/"+member.ID+"/role", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated model.User
	if err := db.First(&updated, "id = ?", member.ID).Error; err != nil {
		t.Fatalf("find user: %v", err)
	}
	if updated.Role != middleware.RoleAdmin {
		t.Errorf("role = %q, want ADMIN", updated.Role)
	}
}
