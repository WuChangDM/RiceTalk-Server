package user

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/tests/testutil"
)

func setupTestRouterAndUser(t *testing.T, db *gorm.DB, cfg *config.Config, hub *realtime.Hub) (*gin.Engine, *model.User) {
	user := createTestUser(db, "presenceuser", "presence@example.com")

	handler := NewHandler(db, cfg, hub)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", user.ID)
		c.Next()
	})
	r.PUT("/api/users/:id/status", handler.UpdateStatus)
	return r, user
}

func TestHandlerUpdateStatusBroadcastsPresenceUpdate(t *testing.T) {
	log := testutil.TestLogger()
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret-key-for-unit-tests-only"

	hub := realtime.NewHub(log)
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	r, user := setupTestRouterAndUser(t, db, cfg, hub)

	// Create a WebSocket server endpoint using the same hub.
	r.GET("/ws", func(c *gin.Context) {
		c.Set("user_id", user.ID)
		hub.HandleWebSocket(c)
	})
	server := httptest.NewServer(r)
	defer server.Close()

	// Connect observer WebSocket.
	wsURL := strings.Replace(server.URL, "http", "ws", 1) + "/ws"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer ws.Close()

	// Drain welcome message.
	ws.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _, err = ws.ReadMessage()
	require.NoError(t, err)

	// Call HTTP API to update status.
	body, _ := json.Marshal(map[string]string{
		"status":       "away",
		"customStatus": "In a meeting",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/users/"+user.ID+"/status", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Wait for broadcast.
	ws.SetReadDeadline(time.Now().Add(1 * time.Second))
	_, data, err := ws.ReadMessage()
	require.NoError(t, err)

	var msg map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &msg))
	assert.Equal(t, "presence_update", msg["type"])
	payload := msg["payload"].(map[string]interface{})
	assert.Equal(t, user.ID, payload["userId"])
	assert.Equal(t, "away", payload["status"])
	assert.Equal(t, "In a meeting", payload["customStatus"])
}

func TestHandlerUpdateStatusInvisibleBroadcastsOffline(t *testing.T) {
	log := testutil.TestLogger()
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret-key-for-unit-tests-only"

	hub := realtime.NewHub(log)
	go hub.Run()
	time.Sleep(50 * time.Millisecond)

	r, user := setupTestRouterAndUser(t, db, cfg, hub)

	r.GET("/ws", func(c *gin.Context) {
		c.Set("user_id", user.ID)
		hub.HandleWebSocket(c)
	})
	server := httptest.NewServer(r)
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http", "ws", 1) + "/ws"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer ws.Close()

	ws.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _, err = ws.ReadMessage()
	require.NoError(t, err)

	body, _ := json.Marshal(map[string]string{
		"status":       "invisible",
		"customStatus": "Hidden",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/users/"+user.ID+"/status", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	ws.SetReadDeadline(time.Now().Add(1 * time.Second))
	_, data, err := ws.ReadMessage()
	require.NoError(t, err)

	var msg map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &msg))
	payload := msg["payload"].(map[string]interface{})
	assert.Equal(t, "offline", payload["status"])
}

func TestHandlerUpdateStatusForbiddenForOthers(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret-key-for-unit-tests-only"

	userA := createTestUser(db, "usera", "a@example.com")
	createTestUser(db, "userb", "b@example.com")

	handler := NewHandler(db, cfg, nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", userA.ID)
		c.Next()
	})
	r.PUT("/api/users/:id/status", handler.UpdateStatus)

	body, _ := json.Marshal(map[string]string{"status": "online"})
	req := httptest.NewRequest(http.MethodPut, "/api/users/"+userA.ID+"_other/status", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, errors.ErrForbidden.Status, w.Code)
}
