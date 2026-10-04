package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/internal/config"
)

func TestEffectiveAdminPort(t *testing.T) {
	t.Run("dedicated admin port", func(t *testing.T) {
		cfg := &config.Config{Port: 8080, AdminPort: 9090, AdminPortShared: false}
		if got := effectiveAdminPort(cfg); got != 9090 {
			t.Errorf("effectiveAdminPort = %d, want 9090", got)
		}
	})

	t.Run("shared admin port falls back to API port in dev", func(t *testing.T) {
		cfg := &config.Config{Port: 8080, AdminPort: 9090, AdminPortShared: true, Env: "development"}
		if got := effectiveAdminPort(cfg); got != 8080 {
			t.Errorf("effectiveAdminPort (dev) = %d, want 8080", got)
		}
	})

	t.Run("shared admin port ignored in production", func(t *testing.T) {
		cfg := &config.Config{Port: 8080, AdminPort: 9090, AdminPortShared: true, Env: "production"}
		if got := effectiveAdminPort(cfg); got != 9090 {
			t.Errorf("effectiveAdminPort (production) = %d, want 9090 (admin_port_shared must be ignored in production)", got)
		}
	})

	t.Run("dedicated admin port used in production regardless of flag", func(t *testing.T) {
		cfg := &config.Config{Port: 8080, AdminPort: 9090, AdminPortShared: false, Env: "production"}
		if got := effectiveAdminPort(cfg); got != 9090 {
			t.Errorf("effectiveAdminPort (production, no shared) = %d, want 9090", got)
		}
	})
}

func TestWhiteboardStaticRouteUsesConfiguredLocalDataPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	localDataPath := t.TempDir()
	whiteboardDir := filepath.Join(localDataPath, "whiteboard")
	if err := os.MkdirAll(whiteboardDir, 0o755); err != nil {
		t.Fatalf("create whiteboard dir: %v", err)
	}

	const fileName = "wb_route_test.png"
	want := []byte("\x89PNG\r\n\x1a\nconfigured-local-data-path")
	if err := os.WriteFile(filepath.Join(whiteboardDir, fileName), want, 0o644); err != nil {
		t.Fatalf("write thumbnail: %v", err)
	}

	engine := gin.New()
	app := &App{
		cfg:        &config.Config{LocalDataPath: localDataPath},
		mainEngine: engine,
	}
	app.registerAvatarRoutes()

	req := httptest.NewRequest(http.MethodGet, "/storage/whiteboard/"+fileName, nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("thumbnail status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !bytes.Equal(recorder.Body.Bytes(), want) {
		t.Fatalf("thumbnail body = %q, want %q", recorder.Body.Bytes(), want)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", contentType)
	}
}
