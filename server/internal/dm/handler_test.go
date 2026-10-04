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

func init() {
	_ = idgen.Init(1, 1)
}

func TestDMChannelFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret"
	msgSvc := message.NewService(db)
	handler := NewHandler(db, cfg, msgSvc, realtime.NewHub(nil))

	// Create two users and a space.
	u1 := &model.User{ID: idgen.NextString(), Username: "alice", Email: "a@example.com", PasswordHash: "x", Role: middleware.RoleMember}
	u2 := &model.User{ID: idgen.NextString(), Username: "bob", Email: "b@example.com", PasswordHash: "x", Role: middleware.RoleMember}
	for _, u := range []*model.User{u1, u2} {
		if err := db.Create(u).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	if err := db.Create(&model.Space{ID: idgen.NextString(), Name: "Test", OwnerID: u1.ID}).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}

	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))

	// Helper to auth as u1.
	accessToken, _, err := middleware.GenerateTokenPair(u1.ID, u1.Username, u1.Email, u1.Role, u1.TokenVersion, "", "", cfg)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	token := accessToken

	// Create DM channel with u2.
	body, _ := json.Marshal(map[string]string{"recipientId": u2.ID})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/dm/channels", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create DM expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var createResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &createResp)
	data := createResp["data"].(map[string]interface{})
	channelID := data["id"].(string)
	if data["type"] != "DM" {
		t.Errorf("channel type = %v, want DM", data["type"])
	}

	// List DM channels.
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/dm/channels", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list DM expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &listResp)
	listData := listResp["data"].([]interface{})
	if len(listData) != 1 {
		t.Errorf("DM count = %d, want 1", len(listData))
	}

	// Get messages (empty).
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/dm/channels/"+channelID+"/messages", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get messages expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
