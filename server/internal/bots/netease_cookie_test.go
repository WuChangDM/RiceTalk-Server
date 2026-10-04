package bots

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"ridgericetalk/internal/config"
	"ridgericetalk/tests/testutil"
)

// FIX-20261003-01 N17：RRT_NETEASE_COOKIE 运维注入登录 cookie。
// 匿名请求被上游风控（-462，要求 music.163.com 人机验证）时，携带有效
// 登录 cookie（MUSIC_U）即可消除。本组用例验证注入值确实随代理请求到达
// 上游（httptest 记录请求头），覆盖三种取值形态与空值不注入。

func newCookieCaptureService(t *testing.T, captured *syncValue) *Service {
	t.Helper()
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	svc := NewService(db, cfg, nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.set(r.Header.Get("Cookie"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200}`))
	}))
	t.Cleanup(upstream.Close)
	t.Setenv("RRT_NETEASE_API_ENDPOINT", upstream.URL)
	return svc
}

// syncValue is a tiny thread-safe string holder for the capture handler.
type syncValue struct {
	mu sync.Mutex
	v  string
}

func (s *syncValue) set(v string) {
	s.mu.Lock()
	s.v = v
	s.mu.Unlock()
}

func (s *syncValue) get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.v
}

func TestNeteaseEnvCookieInjectedIntoProxyRequests(t *testing.T) {
	t.Run("bare MUSIC_U value is wrapped into a cookie pair", func(t *testing.T) {
		captured := &syncValue{}
		svc := newCookieCaptureService(t, captured)
		t.Setenv("RRT_NETEASE_COOKIE", "abcdefghijklmnopqrstuvwxyz012345")

		if _, err := svc.NeteaseProxy("/search?keywords=test"); err != nil {
			t.Fatalf("NeteaseProxy failed: %v", err)
		}
		if got := captured.get(); got != "MUSIC_U=abcdefghijklmnopqrstuvwxyz012345" {
			t.Fatalf("Cookie header = %q, want wrapped MUSIC_U pair", got)
		}
	})

	t.Run("full cookie string passes through verbatim", func(t *testing.T) {
		captured := &syncValue{}
		svc := newCookieCaptureService(t, captured)
		t.Setenv("RRT_NETEASE_COOKIE", "MUSIC_U=abc123; os=pc; appver=8.9.75")

		if _, err := svc.NeteaseProxy("/lyric?id=186016"); err != nil {
			t.Fatalf("NeteaseProxy failed: %v", err)
		}
		if got := captured.get(); got != "MUSIC_U=abc123; os=pc; appver=8.9.75" {
			t.Fatalf("Cookie header = %q, want verbatim full string", got)
		}
	})

	t.Run("MUSIC_U pair form is also passed through", func(t *testing.T) {
		captured := &syncValue{}
		svc := newCookieCaptureService(t, captured)
		t.Setenv("RRT_NETEASE_COOKIE", "MUSIC_U=token456")

		if _, err := svc.NeteaseProxy("/song/url?id=1"); err != nil {
			t.Fatalf("NeteaseProxy failed: %v", err)
		}
		if got := captured.get(); got != "MUSIC_U=token456" {
			t.Fatalf("Cookie header = %q, want MUSIC_U=token456", got)
		}
	})

	t.Run("empty env injects nothing", func(t *testing.T) {
		captured := &syncValue{}
		svc := newCookieCaptureService(t, captured)
		t.Setenv("RRT_NETEASE_COOKIE", "")

		if _, err := svc.NeteaseProxy("/search?keywords=x"); err != nil {
			t.Fatalf("NeteaseProxy failed: %v", err)
		}
		if got := captured.get(); got != "" {
			t.Fatalf("Cookie header = %q, want empty", got)
		}
	})

	t.Run("cookie reaches upstream on POST proxy path too", func(t *testing.T) {
		captured := &syncValue{}
		svc := newCookieCaptureService(t, captured)
		t.Setenv("RRT_NETEASE_COOKIE", "MUSIC_U=postcookie")

		if _, err := svc.NeteaseProxyPost("/logout", map[string]string{"k": "v"}); err != nil {
			t.Fatalf("NeteaseProxyPost failed: %v", err)
		}
		if got := captured.get(); got != "MUSIC_U=postcookie" {
			t.Fatalf("Cookie header = %q, want MUSIC_U=postcookie", got)
		}
	})

	t.Run("helper normalizes whitespace-only env to empty", func(t *testing.T) {
		t.Setenv("RRT_NETEASE_COOKIE", "   ")
		if got := neteaseEnvCookie(); got != "" {
			t.Fatalf("neteaseEnvCookie() = %q, want empty", got)
		}
	})
}
