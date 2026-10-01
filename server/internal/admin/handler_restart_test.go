package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// newRestartTestHandler builds a Handler backed by an in-memory SQLite DB and a
// fresh Break-Glass protector for restart-service tests.
func newRestartTestHandler(t *testing.T) (*Handler, *middleware.OwnerBreakGlassProtector) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := &config.Config{ServerName: "test", Port: 8080}
	bgp := middleware.NewOwnerBreakGlassProtector()
	h := NewHandlerWithState(db, cfg, nil, bgp, nil)
	return h, bgp
}

// newRestartContext builds a gin test context with the given role/userID and
// JSON body, mimicking what AuthRequired + RequireAdmin would have set up.
func newRestartContext(role, userID string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("role", role)
	c.Set("user_id", userID)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/service/restart", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	return c, w
}

// TestRestartService_NonOwnerForbidden verifies that a non-Owner (ADMIN) caller
// is rejected with 403 even though RequireAdmin would have admitted them.
func TestRestartService_NonOwnerForbidden(t *testing.T) {
	h, _ := newRestartTestHandler(t)
	body, _ := json.Marshal(map[string]string{"service": "ridgericetalk"})

	c, w := newRestartContext(middleware.RoleAdmin, "admin-1", body)
	h.RestartService(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

// TestRestartService_BreakGlassLockedOut verifies that an Owner who has been
// locked out by the Break-Glass protector (3 consecutive failures) is rejected.
// The existing checkBreakGlass helper returns AUTH_ACCOUNT_LOCKED (HTTP 429
// Too Many Requests), matching the codebase-wide Break-Glass contract.
func TestRestartService_BreakGlassLockedOut(t *testing.T) {
	h, bgp := newRestartTestHandler(t)
	// Trigger the Break-Glass lockout by recording 3 failures.
	bgp.RecordFailure("owner-1")
	bgp.RecordFailure("owner-1")
	bgp.RecordFailure("owner-1")

	body, _ := json.Marshal(map[string]string{"service": "ridgericetalk"})
	c, w := newRestartContext(middleware.RoleOwner, "owner-1", body)
	h.RestartService(c)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d (Break-Glass lockout)", w.Code, http.StatusTooManyRequests)
	}
}

// TestRestartService_Success verifies the happy path: an Owner with a valid
// service name and an unlocked Break-Glass state receives 200 with
// success:true, the restart function is invoked, and an audit log is recorded.
func TestRestartService_Success(t *testing.T) {
	h, _ := newRestartTestHandler(t)

	called := make(chan string, 1)
	h.restartFunc = func(ctx context.Context, service string) error {
		select {
		case called <- service:
		default:
		}
		return nil
	}

	body, _ := json.Marshal(map[string]string{"service": "ridgericetalk"})
	c, w := newRestartContext(middleware.RoleOwner, "owner-1", body)
	h.RestartService(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Verify response payload: errors.Success wraps data in Response.Data.
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil || data["success"] != true {
		t.Errorf("response data.success = %v, want true", data)
	}

	// Verify the restart function was actually invoked asynchronously.
	select {
	case svc := <-called:
		if svc != "ridgericetalk" {
			t.Errorf("restart called with service %q, want %q", svc, "ridgericetalk")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restart function was not called")
	}

	// Verify an audit log entry was recorded.
	var logs []model.AuditLog
	if err := h.db.Where("action = ? AND user_id = ?", "restart_service", "owner-1").Find(&logs).Error; err != nil {
		t.Fatalf("query audit logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 audit log, got %d", len(logs))
	}
	if logs[0].Resource != "service" {
		t.Errorf("audit log resource = %q, want %q", logs[0].Resource, "service")
	}
	if logs[0].Details != "ridgericetalk" {
		t.Errorf("audit log details = %q, want %q", logs[0].Details, "ridgericetalk")
	}
	if !logs[0].Success {
		t.Errorf("audit log success = false, want true")
	}
}

// TestRestartService_InvalidServiceName verifies that a service name not on the
// whitelist is rejected with 400 and that the restart function is never called.
func TestRestartService_InvalidServiceName(t *testing.T) {
	h, _ := newRestartTestHandler(t)

	invoked := false
	h.restartFunc = func(ctx context.Context, service string) error {
		invoked = true
		return nil
	}

	body, _ := json.Marshal(map[string]string{"service": "rm -rf"})
	c, w := newRestartContext(middleware.RoleOwner, "owner-1", body)
	h.RestartService(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if invoked {
		t.Error("restart function should not be called for an invalid service name")
	}
}
