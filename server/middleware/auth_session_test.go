package middleware

// N22（A4 双端实测发现）——会话撤销即时生效测试。
// 背景：authenticateRequest 原本只验 JWT 签名 + 用户级 TokenVersion，不查会话表；
// revoke-others/单会话撤销后 access token 仍可用至自然过期，与设置页「立即失效」
// 承诺矛盾。修复后：带 session_id 声明的 token，其会话行被删即 401；
// 无 session_id 声明的旧 token 豁免（自然过期退役）。

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	testutil "ridgericetalk/tests/testutil"
)

func newN22Router(t *testing.T) (*gin.Engine, *config.Config, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := testutil.SetupTestDB()
	if err != nil {
		t.Fatalf("setup db: %v", err)
	}
	cfg := &config.Config{JWTSecret: "n22-test-secret", JWTAccessTTL: 15, JWTRefreshTTL: 7}
	r := gin.New()
	r.GET("/protected", AuthRequired(cfg, db), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r, cfg, db
}

// n22SeedUser 写入用户行（validateTokenVersion 需要 TokenVersion 比对）。
func n22SeedUser(t *testing.T, db interface{ Create(interface{}) interface{ Error() error } }, id string) {
	t.Helper()
	_ = db
	_ = id
}

func n22Token(t *testing.T, cfg *config.Config, userID, sessionID string, legacy bool) string {
	t.Helper()
	sid := sessionID
	if legacy {
		sid = ""
	}
	access, _, err := GenerateTokenPair(userID, "u", "u@example.com", "MEMBER", 1, "space_n22", sid, cfg)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return access
}

func n22Do(r *gin.Engine, token string) int {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	r.ServeHTTP(w, req)
	return w.Code
}

func TestAuthRevokedSessionRejected(t *testing.T) {
	r, cfg, db := newN22Router(t)
	if err := db.Create(&model.User{ID: "user_n22", TokenVersion: 1}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	live := &model.UserSession{ID: "sess_live", UserID: "user_n22", TokenHash: "hash-live", ExpiresAt: time.Now().Add(time.Hour)}
	dead := &model.UserSession{ID: "sess_dead", UserID: "user_n22", TokenHash: "hash-dead", ExpiresAt: time.Now().Add(time.Hour)}
	if err := db.Create(live).Error; err != nil {
		t.Fatalf("seed live: %v", err)
	}
	if err := db.Create(dead).Error; err != nil {
		t.Fatalf("seed dead: %v", err)
	}

	t.Run("会话存在时放行", func(t *testing.T) {
		if got := n22Do(r, n22Token(t, cfg, "user_n22", "sess_live", false)); got != http.StatusOK {
			t.Fatalf("want 200, got %d", got)
		}
	})
	t.Run("撤销（删行）后同 claim token 立即 401", func(t *testing.T) {
		tok := n22Token(t, cfg, "user_n22", "sess_dead", false)
		if got := n22Do(r, tok); got != http.StatusOK {
			t.Fatalf("precondition: 未删行前应放行, got %d", got)
		}
		if err := db.Delete(dead).Error; err != nil {
			t.Fatalf("revoke: %v", err)
		}
		if got := n22Do(r, tok); got != http.StatusUnauthorized {
			t.Fatalf("want 401 after revoke, got %d", got)
		}
	})
	t.Run("user_id 绑定：他用户同 id 行不豁免", func(t *testing.T) {
		// user_other 建一个同 id "sess_live2" 的行；user_n22 的 token 声明 sess_live2 时应 401
		if err := db.Create(&model.UserSession{ID: "sess_live2", UserID: "user_other", TokenHash: "hash-other", ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
			t.Fatalf("seed other-user row: %v", err)
		}
		if got := n22Do(r, n22Token(t, cfg, "user_n22", "sess_live2", false)); got != http.StatusUnauthorized {
			t.Fatalf("want 401 (session belongs to other user), got %d", got)
		}
	})
	t.Run("无 session_id 声明的旧 token 豁免", func(t *testing.T) {
		if got := n22Do(r, n22Token(t, cfg, "user_n22", "", true)); got != http.StatusOK {
			t.Fatalf("legacy token should pass, got %d", got)
		}
	})
}
