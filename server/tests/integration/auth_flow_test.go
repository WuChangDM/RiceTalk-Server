package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/auth"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/user"
	"ridgericetalk/tests/testutil"
)

func init() {
	_ = idgen.Init(1, 1)
}

func TestAuthFlow(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret-key-for-unit-tests-only"
	cfg.HIBPEnabled = false // disable HIBP in integration tests to avoid network dependency

	r := testutil.NewTestGinEngine()
	api := r.Group("/api/v1")

	authHandler := auth.NewHandler(db, cfg)
	authHandler.RegisterRoutes(api)

	userHandler := user.NewHandler(db, cfg, nil)
	userHandler.RegisterRoutes(api)

	server := httptest.NewServer(r)
	defer server.Close()

	// Step 1: Register via httptest.NewRecorder
	var registerResp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			AccessToken string `json:"accessToken"`
			User        struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"user"`
		} `json:"data"`
	}

	t.Run("register with recorder", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"email":       "flow@example.com",
			"password":    "Password123!",
			"displayName": "Flow User",
			"securityQuestions": []map[string]string{
				{"question": "您父亲的生日是哪一天？", "answer": "1970-01-01"},
				{"question": "您出生城市是哪里？", "answer": "上海"},
			},
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/v1/auth/register", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("register expected status 200, got %d: %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &registerResp); err != nil {
			t.Fatalf("failed to decode register response: %v", err)
		}
		if registerResp.Data.AccessToken == "" {
			t.Fatal("expected access token in register response")
		}
		// 身份模型（666ec93）：登录身份为邮箱，username 已退役为系统生成的内部标识，
		// 展示名统一走 displayName。
		if registerResp.Data.User.DisplayName != "Flow User" {
			t.Errorf("expected displayName Flow User, got %s", registerResp.Data.User.DisplayName)
		}
		// Ensure next JWT has a different iat to avoid refresh token collision
		time.Sleep(1 * time.Second)
	})

	// Step 2: Login via httptest.NewRecorder
	var loginResp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			AccessToken string `json:"accessToken"`
			User        struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"user"`
		} `json:"data"`
	}

	t.Run("login with recorder", func(t *testing.T) {
		// 登录身份为邮箱（L4）；username 字段会按“旧客户端把邮箱放在 username”的
		// 兜底路径解析，因此必须传 email。
		body, _ := json.Marshal(map[string]string{
			"email":    "flow@example.com",
			"password": "Password123!",
		})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("login expected status 200, got %d: %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &loginResp); err != nil {
			t.Fatalf("failed to decode login response: %v", err)
		}
		if loginResp.Data.AccessToken == "" {
			t.Fatal("expected access token in login response")
		}
		if loginResp.Data.User.DisplayName != "Flow User" {
			t.Errorf("expected displayName Flow User, got %s", loginResp.Data.User.DisplayName)
		}
	})

	token := loginResp.Data.AccessToken
	userID := loginResp.Data.User.ID

	// Step 3: Get me via httptest.NewServer
	t.Run("get me with server", func(t *testing.T) {
		req, _ := http.NewRequest("GET", server.URL+"/api/v1/auth/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("get me request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get me expected status 200, got %d", resp.StatusCode)
		}

		var meResp struct {
			Code string `json:"code"`
			Data struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&meResp); err != nil {
			t.Fatalf("failed to decode me response: %v", err)
		}
		if meResp.Data.ID != userID {
			t.Errorf("expected user ID %s, got %s", userID, meResp.Data.ID)
		}
		if meResp.Data.DisplayName != "Flow User" {
			t.Errorf("expected displayName Flow User, got %s", meResp.Data.DisplayName)
		}
	})

	// Step 4: Update profile via httptest.NewServer
	t.Run("update profile with server", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{
			"displayName": "Updated Name",
		})
		req, _ := http.NewRequest("PATCH", server.URL+"/api/v1/users/me", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("update profile request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("update profile expected status 200, got %d", resp.StatusCode)
		}

		var updateResp struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&updateResp); err != nil {
			t.Fatalf("failed to decode update response: %v", err)
		}
		if updateResp.Code != "OK" {
			t.Errorf("expected code OK, got %s", updateResp.Code)
		}
	})
}
