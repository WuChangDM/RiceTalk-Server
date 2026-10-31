package files

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"ridgericetalk/internal/config"
	"ridgericetalk/middleware"
	"ridgericetalk/tests/testutil"
)

// uploadOnce 打一次真实的 multipart 上传，返回响应体（JSON）。
func uploadOnce(t *testing.T, cfg *config.Config, host string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	handler := NewHandler(db, newFakeStorage(), cfg)

	router := gin.New()
	router.POST("/files/upload", func(c *gin.Context) {
		setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
		c.Next()
	}, handler.Upload)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "test.png")
	if err != nil {
		t.Fatalf("CreateFormFile failed: %v", err)
	}
	if _, err := part.Write(minimalPNG); err != nil {
		t.Fatalf("write part failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close failed: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "/files/upload", body)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Host = host

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("上传失败 status=%d body=%s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

// TestUploadResponseURLIncludesPort 覆盖 task-100 的三态：
// baseURL 为空 / 无端口 / 带端口，响应里的 url 必须是「带端口、可直接外网访问」的绝对地址。
func TestUploadResponseURLIncludesPort(t *testing.T) {
	cases := []struct {
		name          string
		publicAddress string
		host          string
		wantPrefix    string
	}{
		{
			name:          "baseURL 为空 → 用请求 Host 补全",
			publicAddress: "",
			host:          "t46.sjcmc.cn:21103",
			wantPrefix:    "http://t46.sjcmc.cn:21103/api/v1/files/uploads/",
		},
		{
			name:          "baseURL 无端口 → 用请求 Host 补全（t46 线上形态）",
			publicAddress: "http://t46.sjcmc.cn",
			host:          "t46.sjcmc.cn:21103",
			wantPrefix:    "http://t46.sjcmc.cn:21103/api/v1/files/uploads/",
		},
		{
			name:          "baseURL 带端口 → 以配置为准",
			publicAddress: "http://cfg.example:9000",
			host:          "t46.sjcmc.cn:21103",
			wantPrefix:    "http://cfg.example:9000/api/v1/files/uploads/",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.PublicAddress = tc.publicAddress
			body := uploadOnce(t, cfg, tc.host)

			var resp struct {
				Code string `json:"code"`
				Data struct {
					URL      string `json:"url"`
					Filename string `json:"filename"`
					Size     int64  `json:"size"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(body), &resp); err != nil {
				t.Fatalf("解析响应失败：%v body=%s", err, body)
			}
			if resp.Code != "OK" {
				t.Fatalf("响应 code=%q，期望 OK body=%s", resp.Code, body)
			}
			if resp.Data.URL[:len(tc.wantPrefix)] != tc.wantPrefix {
				t.Fatalf("url=%q，期望以 %q 开头", resp.Data.URL, tc.wantPrefix)
			}
			if resp.Data.Filename != "test.png" {
				t.Fatalf("filename=%q，期望 test.png", resp.Data.Filename)
			}
		})
	}
}

// TestUploadResponseURLUsesForwardedProto 单独覆盖反代场景：Host 无端口但 scheme 为 https。
func TestUploadResponseURLUsesForwardedProto(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.MustSetupTestDB()
	cfg := config.DefaultConfig()
	cfg.PublicAddress = "http://t46.sjcmc.cn" // 缺端口
	handler := NewHandler(db, newFakeStorage(), cfg)

	router := gin.New()
	router.POST("/files/upload", func(c *gin.Context) {
		setupAuthContext(c, "user-1", "testuser", middleware.RoleMember)
		c.Next()
	}, handler.Upload)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "test.png")
	part.Write(minimalPNG)
	writer.Close()

	req, _ := http.NewRequest(http.MethodPost, "/files/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Host = "talk.example.com"

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("上传失败 status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	const want = "https://talk.example.com/api/v1/files/"
	if len(resp.Data.URL) < len(want) || resp.Data.URL[:len(want)] != want {
		t.Fatalf("url=%q，期望以 %q 开头", resp.Data.URL, want)
	}
}
