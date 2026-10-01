package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/internal/config"
	"ridgericetalk/internal/database"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/tests/testutil"
)

// TestRegisterSharedAdminRoutesDoesNotPanic is a regression test for M5:
// setting RRT_ADMIN_PORT_SHARED=true made the server panic during startup
// ("handlers are already registered for path '/api/v1/health'") because
// registerSharedAdminRoutes re-registered routes that registerRoutes had
// already mounted on the main engine.
func TestRegisterSharedAdminRoutesDoesNotPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		Env:             "development",
		Port:            8080,
		AdminPort:       9090,
		AdminPortShared: true,
		LocalDataPath:   t.TempDir(),
	}
	log := testutil.TestLogger()
	hub := realtime.NewHub(log)
	engine := gin.New()

	app := &App{
		cfg:        cfg,
		log:        log,
		db:         &database.DB{DB: testutil.MustSetupTestDB()},
		hub:        hub,
		mainEngine: engine,
	}

	healthHandler := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}

	// Mimic what registerRoutes already mounts on the main engine before the
	// shared-admin block runs (health + the full admin API surface).
	mainV1 := engine.Group("/api/v1")
	mainV1.GET("/health", healthHandler)
	mainV1.HEAD("/health", healthHandler)
	app.registerAdminRoutes(mainV1)

	mainLegacy := engine.Group("/api")
	mainLegacy.GET("/health", healthHandler)
	mainLegacy.HEAD("/health", healthHandler)
	app.registerAdminRoutes(mainLegacy)

	// Enabling the shared admin port must not panic on duplicate registration.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("registerSharedAdminRoutes panicked: %v", r)
			}
		}()
		app.registerSharedAdminRoutes(healthHandler)
	}()

	// The main health endpoint must still be served.
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/v1/health status = %d, want %d", rec.Code, http.StatusOK)
	}

	// The admin-side health alias unique to shared mode must be mounted.
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/admin/health status = %d, want %d", rec.Code, http.StatusOK)
	}
}
