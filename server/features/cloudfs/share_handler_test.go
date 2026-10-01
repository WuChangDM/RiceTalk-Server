package cloudfs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// DES-2026-0912-05 分享链接：HTTP 层安全测试
// ─────────────────────────────────────────────────────────────────────────────

// shareEnv 是一套分享链接测试夹具：一个 Handler、一个测试引擎（公开路由 +
// 身份注入的鉴权分享路由）、一个空间与两个文件（html / png，用于 MIME 白名单用例）。
type shareEnv struct {
	t        *testing.T
	handler  *Handler
	router   *gin.Engine
	db       *gorm.DB
	space    model.Space
	htmlFile *model.SharedFileEntry
	pngFile  *model.SharedFileEntry
	ipSeq    int
}

const (
	shareTestHTMLContent = "<html><body>stored xss primitive</body></html>"
	shareTestPNGContent  = "\x89PNG\r\n\x1a\n-fake-png"
)

func newShareEnv(t *testing.T) *shareEnv {
	t.Helper()
	db := testutil.MustSetupTestDB()
	dataPath := t.TempDir()
	cfg := &config.Config{LocalDataPath: dataPath, PublicAddress: "https://rt.example"}
	h := NewHandler(db, cfg, nil)

	space := model.Space{ID: idgen.NextString(), Name: "share-space", OwnerID: "user-1"}
	if err := db.Create(&space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	db.Create(&model.Membership{ID: idgen.NextString(), SpaceID: space.ID, UserID: "user-1", Role: "MEMBER"})
	db.Create(&model.Membership{ID: idgen.NextString(), SpaceID: space.ID, UserID: "admin-1", Role: "ADMIN"})

	baseDir := filepath.Join(dataPath, "cloudfs")
	html := seedStoredFile(t, db, baseDir, space.ID, "", "notes.html", "/notes.html", "text/html", shareTestHTMLContent)
	png := seedStoredFile(t, db, baseDir, space.ID, "", "shot.png", "/shot.png", "image/png", shareTestPNGContent)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := r.Group("/api/v1")
	// 鉴权路由：测试里直接注入身份（与既有 setupTestHandler 的做法一致）。
	authed := api.Group("/cloudfs")
	authed.Use(func(c *gin.Context) {
		c.Set("user_id", "user-1")
		c.Set("role", "MEMBER")
		c.Set("space_id", space.ID)
	})
	authed.POST("/share", h.CreateShare)
	authed.GET("/shares", h.ListShares)
	authed.DELETE("/share/:shareId", h.RevokeShare)
	// 公开路由：与 production 完全相同的挂法，且不注入任何身份。
	h.RegisterShareRoutes(api.Group("/share"))

	return &shareEnv{t: t, handler: h, router: r, db: db, space: space, htmlFile: html, pngFile: png}
}

// do 发起请求。默认每个请求换一个源 IP，避免用例之间共用限流桶。
func (e *shareEnv) do(method, target string, body interface{}) *httptest.ResponseRecorder {
	e.ipSeq++
	return e.doFrom(method, target, body, fmt.Sprintf("198.51.100.%d", e.ipSeq%200+1))
}

// doFrom 是指定源 IP 的请求（用于限流 / 爆破防护用例）。
func (e *shareEnv) doFrom(method, target string, body interface{}, ip string) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(payload)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

// createShare 直接经服务层建分享（要的是令牌，不需要走 HTTP）。
func (e *shareEnv) createShare(opts ShareOptions) (*model.CloudFileShare, string) {
	e.t.Helper()
	share, token, err := e.handler.service.CreateShare(e.space.ID, "user-1", opts)
	if err != nil {
		e.t.Fatalf("CreateShare: %v", err)
	}
	return share, token
}

func shareSuccessData(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Code string                 `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	if resp.Code != "OK" {
		t.Fatalf("response code = %q, want OK; body=%s", resp.Code, w.Body.String())
	}
	return resp.Data
}

func shareErrorFields(t *testing.T, w *httptest.ResponseRecorder) (code, message, chinese string) {
	t.Helper()
	var resp struct {
		Code           string `json:"code"`
		Message        string `json:"message"`
		ChineseMessage string `json:"chinese_message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v (body=%s)", err, w.Body.String())
	}
	return resp.Code, resp.Message, resp.ChineseMessage
}

// TestShareRoutesPublicButCloudFSRoutesStillRequireAuth 是设计的目标本身：
// 三条分享路由必须免鉴权可达，而其余接口必须仍然要求鉴权。
//
// 这里用**生产挂法**（RegisterRoutes + RegisterShareRoutes）而不是注入身份的
// 测试路由，否则路由表写错也测不出来。
func TestShareRoutesPublicButCloudFSRoutesStillRequireAuth(t *testing.T) {
	db := testutil.MustSetupTestDB()
	dataPath := t.TempDir()
	cfg := &config.Config{LocalDataPath: dataPath, PublicAddress: "https://rt.example"}
	h := NewHandler(db, cfg, nil)

	space := model.Space{ID: idgen.NextString(), Name: "space", OwnerID: "user-1"}
	db.Create(&space)
	db.Create(&model.Membership{ID: idgen.NextString(), SpaceID: space.ID, UserID: "user-1", Role: "MEMBER"})
	entry := seedStoredFile(t, db, filepath.Join(dataPath, "cloudfs"), space.ID, "", "a.txt", "/a.txt", "text/plain", "hello")
	share, token, err := h.service.CreateShare(space.ID, "user-1", ShareOptions{FileID: entry.ID})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	apiV1 := r.Group("/api/v1")
	h.RegisterRoutes(apiV1)                      // 既有 cloudfs 鉴权组
	h.RegisterShareRoutes(apiV1.Group("/share")) // 与 internal/server/routes.go 相同的挂法

	unauthenticated := func(method, target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, target, nil))
		return w
	}

	// 免鉴权面：元信息与下载都必须能直接访问（否则公开链接就是个摆设）。
	if w := unauthenticated("GET", "/api/v1/share/"+token); w.Code != http.StatusOK {
		t.Fatalf("public metadata must be reachable without auth, got %d: %s", w.Code, w.Body.String())
	}
	if w := unauthenticated("POST", "/api/v1/share/"+token+"/verify"); w.Code != http.StatusOK {
		t.Fatalf("public verify must be reachable without auth, got %d: %s", w.Code, w.Body.String())
	}
	w := unauthenticated("GET", "/api/v1/share/"+token+"/download")
	if w.Code != http.StatusOK {
		t.Fatalf("public download must be reachable without auth, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "hello" {
		t.Fatalf("served body = %q, want the stored content", w.Body.String())
	}

	// 鉴权面：新增的分享管理路由必须和既有 cloudfs 路由一样被 AuthRequired 挡住。
	for _, req := range []struct{ method, target string }{
		{"GET", "/api/v1/cloudfs/list?path=/"},
		{"GET", "/api/v1/cloudfs/download?path=/a.txt"},
		{"GET", "/api/v1/cloudfs/preview?path=/a.txt"},
		{"POST", "/api/v1/cloudfs/share"},
		{"GET", "/api/v1/cloudfs/shares"},
		{"DELETE", "/api/v1/cloudfs/share/" + share.ID},
	} {
		w := unauthenticated(req.method, req.target)
		if w.Code == http.StatusOK {
			t.Errorf("%s %s must not be reachable without auth, got 200", req.method, req.target)
		}
	}
}

// TestShareMetaReportsStateWithoutLeakingInternals 覆盖设计 §7「元信息不得泄漏」。
func TestShareMetaReportsStateWithoutLeakingInternals(t *testing.T) {
	env := newShareEnv(t)
	share, token := env.createShare(ShareOptions{FileID: env.htmlFile.ID, Password: "pw-123456"})

	w := env.do("GET", "/api/v1/share/"+token, nil)
	data := shareSuccessData(t, w)

	if data["fileName"] != "notes.html" {
		t.Fatalf("fileName = %v", data["fileName"])
	}
	if data["requiresPassword"] != true {
		t.Fatalf("requiresPassword = %v, want true", data["requiresPassword"])
	}
	if data["available"] != true || data["expired"] != false || data["revoked"] != false {
		t.Fatalf("unexpected state flags: %v", data)
	}
	if data["maxDownloads"] != float64(0) {
		t.Fatalf("maxDownloads = %v, want 0 (unlimited)", data["maxDownloads"])
	}

	for _, forbidden := range []string{"tokenHash", "passwordHash", "ownerId", "physicalName", "storageKey", "filePath", "spaceId", "createdAt"} {
		if _, ok := data[forbidden]; ok {
			t.Errorf("metadata must not expose %q", forbidden)
		}
	}
	body := w.Body.String()
	for _, secret := range []string{token, share.TokenHash, "pw-123456", share.PasswordHash} {
		if strings.Contains(body, secret) {
			t.Errorf("metadata response leaks secret material: %q", secret)
		}
	}

	// 链接失效后不再披露文件元信息。
	if err := env.db.Model(&model.CloudFileShare{}).Where("id = ?", share.ID).
		Update("expires_at", time.Now().UTC().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("expire share: %v", err)
	}
	data = shareSuccessData(t, env.do("GET", "/api/v1/share/"+token, nil))
	if data["expired"] != true || data["available"] != false {
		t.Fatalf("expired share state = %v", data)
	}
	if _, ok := data["fileName"]; ok {
		t.Error("an expired share must not keep disclosing the file name")
	}
}

// TestShareResponseSecurityHeaders 覆盖设计 §7 的响应头要求。
func TestShareResponseSecurityHeaders(t *testing.T) {
	env := newShareEnv(t)
	_, token := env.createShare(ShareOptions{FileID: env.htmlFile.ID})

	assertHeaders := func(name string, w *httptest.ResponseRecorder) {
		t.Helper()
		for header, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"Cache-Control":          "no-store",
			"X-Robots-Tag":           "noindex, nofollow",
			"Referrer-Policy":        "no-referrer",
		} {
			if got := w.Header().Get(header); got != want {
				t.Errorf("%s: header %s = %q, want %q", name, header, got, want)
			}
		}
	}

	assertHeaders("metadata", env.do("GET", "/api/v1/share/"+token, nil))
	assertHeaders("verify", env.do("POST", "/api/v1/share/"+token+"/verify", nil))

	w := env.do("GET", "/api/v1/share/"+token+"/download", nil)
	assertHeaders("download", w)

	// HTML 是存储型 XSS 原语：必须 attachment，且不能沿用 text/html 作为 Content-Type。
	disposition := w.Header().Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "attachment") || !strings.Contains(disposition, "notes.html") {
		t.Fatalf("Content-Disposition = %q, want attachment with the file name", disposition)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream for HTML", ct)
	}
}

// TestShareDownloadMimeWhitelistMatchesPreview 与 GET /cloudfs/preview 的口径一致：
// 白名单内 inline，其余一律 attachment。
func TestShareDownloadMimeWhitelistMatchesPreview(t *testing.T) {
	env := newShareEnv(t)

	_, pngToken := env.createShare(ShareOptions{FileID: env.pngFile.ID})
	w := env.do("GET", "/api/v1/share/"+pngToken+"/download", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("png download status = %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("png Content-Type = %q, want image/png (whitelisted inline)", got)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline") {
		t.Fatalf("png Content-Disposition = %q, want inline", got)
	}
	if w.Body.String() != shareTestPNGContent {
		t.Fatalf("png body = %q", w.Body.String())
	}

	// 白名单判定函数与预览接口共用，直接交叉验证一次防止两边口径漂移。
	if isInlinePreviewAllowed("image/svg+xml") || isInlinePreviewAllowed("text/html") {
		t.Fatal("SVG/HTML must never be inline (storage XSS primitive)")
	}
	if !isInlinePreviewAllowed("image/png") || !isInlinePreviewAllowed("text/plain") {
		t.Fatal("png/plain text must be inline-able")
	}
}

// TestShareDownloadCountsAndRefusesAfterLimit 校验次数上限在 HTTP 层生效。
func TestShareDownloadCountsAndRefusesAfterLimit(t *testing.T) {
	env := newShareEnv(t)
	share, token := env.createShare(ShareOptions{FileID: env.pngFile.ID, MaxDownloads: 1})

	if w := env.do("GET", "/api/v1/share/"+token+"/download", nil); w.Code != http.StatusOK {
		t.Fatalf("first download status = %d: %s", w.Code, w.Body.String())
	}

	w := env.do("GET", "/api/v1/share/"+token+"/download", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("second download status = %d, want 403", w.Code)
	}
	if code, _, _ := shareErrorFields(t, w); code != string(errors.SHARE_DOWNLOAD_LIMITED) {
		t.Fatalf("error code = %s, want %s", code, errors.SHARE_DOWNLOAD_LIMITED)
	}

	var after model.CloudFileShare
	if err := env.db.First(&after, "id = ?", share.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.DownloadCount != 1 {
		t.Fatalf("download_count = %d, want 1 (no oversell over HTTP)", after.DownloadCount)
	}
	if after.LastAccessAt == nil {
		t.Fatal("last_access_at must be stamped")
	}
}

// TestShareDownloadDeniedWhenExpiredOrRevoked 过期/撤销的 HTTP 状态与错误码。
func TestShareDownloadDeniedWhenExpiredOrRevoked(t *testing.T) {
	env := newShareEnv(t)

	expired, expiredToken := env.createShare(ShareOptions{FileID: env.pngFile.ID})
	if err := env.db.Model(&model.CloudFileShare{}).Where("id = ?", expired.ID).
		Update("expires_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatalf("expire: %v", err)
	}
	w := env.do("GET", "/api/v1/share/"+expiredToken+"/download", nil)
	if w.Code != http.StatusGone {
		t.Fatalf("expired download status = %d, want 410", w.Code)
	}
	if code, _, _ := shareErrorFields(t, w); code != string(errors.SHARE_EXPIRED) {
		t.Fatalf("error code = %s, want %s", code, errors.SHARE_EXPIRED)
	}

	revoked, revokedToken := env.createShare(ShareOptions{FileID: env.pngFile.ID})
	if _, err := env.handler.service.RevokeShare(env.space.ID, "user-1", revoked.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	w = env.do("GET", "/api/v1/share/"+revokedToken+"/download", nil)
	if w.Code != http.StatusGone {
		t.Fatalf("revoked download status = %d, want 410", w.Code)
	}
	if code, _, _ := shareErrorFields(t, w); code != string(errors.SHARE_REVOKED) {
		t.Fatalf("error code = %s, want %s", code, errors.SHARE_REVOKED)
	}

	// 不存在的 token → 404（不泄漏其它分享的存在性）。
	w = env.do("GET", "/api/v1/share/does-not-exist/download", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown token status = %d, want 404", w.Code)
	}
}

// TestSharePasswordFlowAndUnifiedVerifyError 覆盖设计 §5：
// 「密码错误」与「分享不存在」必须完全不可区分，且下载必须凭一次性凭据。
func TestSharePasswordFlowAndUnifiedVerifyError(t *testing.T) {
	env := newShareEnv(t)
	const password = "correct-horse"
	_, token := env.createShare(ShareOptions{FileID: env.pngFile.ID, Password: password})

	// 有密码的分享：没有凭据不能下载。
	w := env.do("GET", "/api/v1/share/"+token+"/download", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("download without a ticket status = %d, want 401", w.Code)
	}
	if code, _, _ := shareErrorFields(t, w); code != string(errors.SHARE_TICKET_INVALID) {
		t.Fatalf("error code = %s, want %s", code, errors.SHARE_TICKET_INVALID)
	}

	wrongPassword := env.do("POST", "/api/v1/share/"+token+"/verify", map[string]interface{}{"password": "wrong"})
	unknownShare := env.do("POST", "/api/v1/share/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/verify", map[string]interface{}{"password": "wrong"})

	if wrongPassword.Code != http.StatusUnauthorized || unknownShare.Code != http.StatusUnauthorized {
		t.Fatalf("verify statuses = %d / %d, want both 401", wrongPassword.Code, unknownShare.Code)
	}
	wpCode, wpMsg, wpCN := shareErrorFields(t, wrongPassword)
	usCode, usMsg, usCN := shareErrorFields(t, unknownShare)
	if wpCode != usCode || wpMsg != usMsg || wpCN != usCN {
		t.Fatalf("wrong password and unknown share must be indistinguishable:\n got %s/%s/%s\n got %s/%s/%s",
			wpCode, wpMsg, wpCN, usCode, usMsg, usCN)
	}
	if wpCode != string(errors.SHARE_VERIFY_FAILED) {
		t.Fatalf("error code = %s, want %s", wpCode, errors.SHARE_VERIFY_FAILED)
	}

	// 正确密码 → 一次性凭据。
	w = env.do("POST", "/api/v1/share/"+token+"/verify", map[string]interface{}{"password": password})
	data := shareSuccessData(t, w)
	ticket, _ := data["ticket"].(string)
	if ticket == "" {
		t.Fatalf("verify must return a ticket, got %v", data)
	}
	if data["expiresIn"] != float64(int(ShareTicketTTL.Seconds())) {
		t.Fatalf("expiresIn = %v, want %d", data["expiresIn"], int(ShareTicketTTL.Seconds()))
	}

	// 凭据只能用一次，但下载成功。
	if w := env.do("GET", "/api/v1/share/"+token+"/download?ticket="+ticket, nil); w.Code != http.StatusOK {
		t.Fatalf("download with ticket status = %d: %s", w.Code, w.Body.String())
	}
	if w := env.do("GET", "/api/v1/share/"+token+"/download?ticket="+ticket, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("a re-used ticket must be rejected, got %d", w.Code)
	}

	// 无密码分享：不需要凭据也能下载。
	_, openToken := env.createShare(ShareOptions{FileID: env.pngFile.ID})
	if w := env.do("GET", "/api/v1/share/"+openToken+"/download", nil); w.Code != http.StatusOK {
		t.Fatalf("password-less share must download without a ticket, got %d", w.Code)
	}
}

// TestShareVerifyUnifiedErrorForExpiredAndRevokedShare 失效链接的校验失败也必须
// 与密码错误同形，不能借校验接口区分「存在但已过期」。
func TestShareVerifyUnifiedErrorForExpiredAndRevokedShare(t *testing.T) {
	env := newShareEnv(t)
	share, token := env.createShare(ShareOptions{FileID: env.pngFile.ID, Password: "pw"})
	if err := env.db.Model(&model.CloudFileShare{}).Where("id = ?", share.ID).
		Update("expires_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatalf("expire: %v", err)
	}

	w := env.do("POST", "/api/v1/share/"+token+"/verify", map[string]interface{}{"password": "pw"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("verify on an expired share status = %d, want 401", w.Code)
	}
	if code, _, _ := shareErrorFields(t, w); code != string(errors.SHARE_VERIFY_FAILED) {
		t.Fatalf("error code = %s, want %s", code, errors.SHARE_VERIFY_FAILED)
	}
}

// TestShareVerifyBruteForceLockout 复用 middleware.BruteForceProtector：
// 账户维度 5 次失败后锁定，连正确密码也一并拒绝。
func TestShareVerifyBruteForceLockout(t *testing.T) {
	env := newShareEnv(t)
	const password = "brand-new-password"
	_, token := env.createShare(ShareOptions{FileID: env.pngFile.ID, Password: password})

	// 每次换源 IP，确保触发的确实是「按分享」的账户维度锁定。
	for i := 0; i < 5; i++ {
		w := env.doFrom("POST", "/api/v1/share/"+token+"/verify",
			map[string]interface{}{"password": "nope"}, fmt.Sprintf("203.0.113.%d", i+1))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status = %d, want 401", i+1, w.Code)
		}
	}

	w := env.doFrom("POST", "/api/v1/share/"+token+"/verify",
		map[string]interface{}{"password": password}, "203.0.113.200")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("after 5 failures the share must be locked, got %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("a 429 must carry Retry-After")
	}
}

// TestShareRateLimitIsIndependentPerIPAndPerShare 覆盖设计 §3/§5 的独立限流。
//
// 两个维度分开验证，避免互相掩盖：
//   - 按分享：10 个不同 IP 各打该分享 1 次（IP 桶远未耗尽）→ 第 11 次被拒，
//     证明「按分享」这一维真实存在；
//   - 按 IP：单一 IP 连续打同一个分享 → 到 burst 为止，证明投放不会无限量打到库里。
func TestShareRateLimitIsIndependentPerIPAndPerShare(t *testing.T) {
	env := newShareEnv(t)
	_, tokenA := env.createShare(ShareOptions{FileID: env.pngFile.ID})
	_, tokenB := env.createShare(ShareOptions{FileID: env.htmlFile.ID})
	_, tokenC := env.createShare(ShareOptions{FileID: env.pngFile.ID})

	t.Run("per share", func(t *testing.T) {
		for i := 0; i < shareRateLimitBurst; i++ {
			ip := fmt.Sprintf("192.0.2.%d", i+1)
			w := env.doFrom("GET", "/api/v1/share/"+tokenA, nil, ip)
			if w.Code != http.StatusOK {
				t.Fatalf("request %d from %s status = %d, want 200: %s", i+1, ip, w.Code, w.Body.String())
			}
		}
		// 第 11 个 IP 首次访问该分享也要被拒：桶是按分享记的。
		w := env.doFrom("GET", "/api/v1/share/"+tokenA, nil, "192.0.2.250")
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("per-share bucket must be exhausted, got %d: %s", w.Code, w.Body.String())
		}
		// 同一个 IP 访问**另一个**分享仍然正常：限流不是全局熔断。
		if w := env.doFrom("GET", "/api/v1/share/"+tokenB, nil, "192.0.2.250"); w.Code != http.StatusOK {
			t.Fatalf("a different share must not inherit the exhausted bucket, got %d", w.Code)
		}
	})

	t.Run("per ip", func(t *testing.T) {
		const ip = "192.0.2.99"
		ok, limited := 0, 0
		for i := 0; i < 15; i++ {
			w := env.doFrom("GET", "/api/v1/share/"+tokenC, nil, ip)
			switch w.Code {
			case http.StatusOK:
				ok++
			case http.StatusTooManyRequests:
				limited++
			default:
				t.Fatalf("unexpected status %d: %s", w.Code, w.Body.String())
			}
		}
		if limited == 0 {
			t.Fatal("flooding one share from one IP must eventually be rate limited")
		}
		if ok != shareRateLimitBurst {
			t.Fatalf("%d requests passed before the limit, want exactly burst=%d", ok, shareRateLimitBurst)
		}
	})
}

// TestCreateListRevokeShareOverHTTP 鉴权路由（创建/列出/撤销）。
func TestCreateListRevokeShareOverHTTP(t *testing.T) {
	env := newShareEnv(t)

	w := env.do("POST", "/api/v1/cloudfs/share", map[string]interface{}{
		"fileId":        env.pngFile.ID,
		"expiresInDays": 3,
		"password":      "hunter2",
		"maxDownloads":  2,
	})
	data := shareSuccessData(t, w)
	token, _ := data["token"].(string)
	shareID, _ := data["id"].(string)
	if token == "" || shareID == "" {
		t.Fatalf("create response missing token/id: %v", data)
	}
	if data["requiresPassword"] != true || data["maxDownloads"] != float64(2) {
		t.Fatalf("create response = %v", data)
	}
	if url, _ := data["url"].(string); !strings.HasPrefix(url, "https://rt.example/api/v1/share/") {
		t.Fatalf("url = %q", url)
	}
	if prefix, _ := data["tokenPrefix"].(string); prefix != token[:8] {
		t.Fatalf("tokenPrefix = %v, want %q", data["tokenPrefix"], token[:8])
	}

	// 列表：只返回非敏感字段。
	data = shareSuccessData(t, env.do("GET", "/api/v1/cloudfs/shares?fileId="+env.pngFile.ID, nil))
	items, _ := data["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("list returned %d items, want 1: %v", len(items), data)
	}
	item, _ := items[0].(map[string]interface{})
	if item["fileName"] != "shot.png" {
		t.Fatalf("list item fileName = %v", item["fileName"])
	}
	for _, forbidden := range []string{"token", "tokenHash", "passwordHash", "ownerId", "spaceId"} {
		if _, ok := item[forbidden]; ok {
			t.Errorf("list item must not expose %q", forbidden)
		}
	}

	// 撤销后公开链接立即失效。
	if w := env.do("DELETE", "/api/v1/cloudfs/share/"+shareID, nil); w.Code != http.StatusOK {
		t.Fatalf("revoke status = %d: %s", w.Code, w.Body.String())
	}
	data = shareSuccessData(t, env.do("GET", "/api/v1/share/"+token, nil))
	if data["revoked"] != true || data["available"] != false {
		t.Fatalf("after revoke: %v", data)
	}
	// 该分享带密码，因此下载会先卡在「凭据」这一关（401）—— 未通过密码校验的
	// 调用方不应从下载接口读到分享的状态。无密码分享的撤销后下载在
	// TestShareDownloadDeniedWhenExpiredOrRevoked 中覆盖（410）。
	w = env.do("GET", "/api/v1/share/"+token+"/download", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("download after revoke status = %d, want 401", w.Code)
	}
	if code, _, _ := shareErrorFields(t, w); code != string(errors.SHARE_TICKET_INVALID) {
		t.Fatalf("error code = %s, want %s", code, errors.SHARE_TICKET_INVALID)
	}

	// 创建参数越界 → 400。
	w = env.do("POST", "/api/v1/cloudfs/share", map[string]interface{}{
		"fileId": env.pngFile.ID, "expiresInDays": ShareMaxTTLDays + 1,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("ttl above cap status = %d, want 400", w.Code)
	}
	if code, _, _ := shareErrorFields(t, w); code != string(errors.SHARE_TTL_INVALID) {
		t.Fatalf("error code = %s, want %s", code, errors.SHARE_TTL_INVALID)
	}

	// 越权：拿别的空间的文件 ID 创建分享。
	w = env.do("POST", "/api/v1/cloudfs/share", map[string]interface{}{"fileId": "file_from_another_space"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign file status = %d, want 404", w.Code)
	}

	// 非成员（换掉注入的身份）创建分享必须被拒。
	denied := gin.New()
	deniedAPI := denied.Group("/api/v1")
	deniedCF := deniedAPI.Group("/cloudfs")
	deniedCF.Use(func(c *gin.Context) {
		c.Set("user_id", "outsider")
		c.Set("role", "MEMBER")
		c.Set("space_id", env.space.ID)
	})
	deniedCF.POST("/share", env.handler.CreateShare)
	body, _ := json.Marshal(map[string]interface{}{"fileId": env.pngFile.ID})
	req := httptest.NewRequest("POST", "/api/v1/cloudfs/share", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	denied.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-member create status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if code, _, _ := shareErrorFields(t, rec); code != string(errors.CLOUDFS_PERMISSION_DENIED) {
		t.Fatalf("error code = %s, want %s", code, errors.CLOUDFS_PERMISSION_DENIED)
	}
}

// TestShareAuditEventsWrittenFromHandlers 设计 §8：密码校验失败与拒绝访问必须落审计。
func TestShareAuditEventsWrittenFromHandlers(t *testing.T) {
	env := newShareEnv(t)
	share, token := env.createShare(ShareOptions{FileID: env.pngFile.ID, Password: "pw", MaxDownloads: 1})

	env.do("POST", "/api/v1/share/"+token+"/verify", map[string]interface{}{"password": "bad"})
	env.do("GET", "/api/v1/share/"+token+"/download", nil) // 缺凭据 → 拒绝
	env.do("GET", "/api/v1/share/"+token+"/download", nil)

	var failures int64
	if err := env.db.Model(&model.SecurityAuditLog{}).
		Where("resource_id = ? AND action = ?", share.ID, ShareAuditPasswordFailed).
		Count(&failures).Error; err != nil {
		t.Fatalf("count password failures: %v", err)
	}
	if failures != 1 {
		t.Fatalf("password failure audit rows = %d, want 1", failures)
	}

	var denied int64
	if err := env.db.Model(&model.SecurityAuditLog{}).
		Where("resource_id = ? AND action = ?", share.ID, ShareAuditDownloadDenied).
		Count(&denied).Error; err != nil {
		t.Fatalf("count denied downloads: %v", err)
	}
	if denied != 2 {
		t.Fatalf("denied download audit rows = %d, want 2", denied)
	}

	// 成功下载写审计（第 1 次必记）。
	if err := env.db.Model(&model.CloudFileShare{}).Where("id = ?", share.ID).
		Update("password_hash", "").Error; err != nil {
		t.Fatalf("drop password: %v", err)
	}
	if w := env.do("GET", "/api/v1/share/"+token+"/download", nil); w.Code != http.StatusOK {
		t.Fatalf("download status = %d: %s", w.Code, w.Body.String())
	}
	var downloaded int64
	if err := env.db.Model(&model.SecurityAuditLog{}).
		Where("resource_id = ? AND action = ?", share.ID, ShareAuditDownloaded).
		Count(&downloaded).Error; err != nil {
		t.Fatalf("count downloads: %v", err)
	}
	if downloaded != 1 {
		t.Fatalf("download audit rows = %d, want 1", downloaded)
	}
}
