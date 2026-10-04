package httpbind

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newContextWithBody 构造一个携带指定请求体的 gin.Context
func newContextWithBody(t *testing.T, method, path, body string) *gin.Context {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	return c
}

func TestBindJSONAllowEmpty_EmptyBody(t *testing.T) {
	c := newContextWithBody(t, "POST", "/api/v1/test", "")
	var body struct {
		Pinned *bool `json:"pinned"`
	}
	err := BindJSONAllowEmpty(c, &body)
	if err != nil {
		t.Fatalf("expected nil error for empty body, got: %v", err)
	}
	if body.Pinned != nil {
		t.Fatalf("expected nil Pinned for empty body, got: %v", *body.Pinned)
	}
}

func TestBindJSONAllowEmpty_ValidJSON(t *testing.T) {
	c := newContextWithBody(t, "POST", "/api/v1/test", `{"pinned": false}`)
	var body struct {
		Pinned *bool `json:"pinned"`
	}
	err := BindJSONAllowEmpty(c, &body)
	if err != nil {
		t.Fatalf("expected nil error for valid JSON, got: %v", err)
	}
	if body.Pinned == nil {
		t.Fatal("expected Pinned to be set, got nil")
	}
	if *body.Pinned != false {
		t.Fatalf("expected Pinned=false, got: %v", *body.Pinned)
	}
}

func TestBindJSONAllowEmpty_InvalidJSON(t *testing.T) {
	c := newContextWithBody(t, "POST", "/api/v1/test", `{invalid json`)
	var body struct {
		Pinned *bool `json:"pinned"`
	}
	err := BindJSONAllowEmpty(c, &body)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestBindJSONAllowEmpty_TypeMismatch(t *testing.T) {
	c := newContextWithBody(t, "POST", "/api/v1/test", `{"pinned": "not-a-bool"}`)
	var body struct {
		Pinned *bool `json:"pinned"`
	}
	err := BindJSONAllowEmpty(c, &body)
	if err == nil {
		t.Fatal("expected error for type mismatch, got nil")
	}
}

func TestBindJSONAllowEmpty_EmptyStringBody(t *testing.T) {
	// 仅含空白字符的请求体也应被解码为"空"
	c := newContextWithBody(t, "POST", "/api/v1/test", "   \n  ")
	var body struct {
		LastMessageID string `json:"lastMessageId"`
	}
	err := BindJSONAllowEmpty(c, &body)
	if err != nil {
		t.Fatalf("expected nil error for whitespace-only body, got: %v", err)
	}
	if body.LastMessageID != "" {
		t.Fatalf("expected empty LastMessageID, got: %q", body.LastMessageID)
	}
}

func TestBindJSONAllowEmpty_PartialJSON(t *testing.T) {
	// 不完整的 JSON（如缺少闭合括号）应返回错误，而非 io.EOF
	c := newContextWithBody(t, "POST", "/api/v1/test", `{"pinned": true`)
	var body struct {
		Pinned *bool `json:"pinned"`
	}
	err := BindJSONAllowEmpty(c, &body)
	if err == nil {
		t.Fatal("expected error for partial JSON, got nil")
	}
}
