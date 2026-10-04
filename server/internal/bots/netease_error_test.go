package bots

import (
	"strings"
	"testing"

	apperrors "ridgericetalk/core/errors"
)

func TestNeteaseUpstreamErrorVerificationRequired(t *testing.T) {
	body := []byte(`{
		"code": -462,
		"verifyType": 40,
		"verifyId": 1007602,
		"verifyUrl": "https://st.music.163.com/encrypt-pages",
		"verifyToken": "must-not-leak",
		"params": {"sign": "must-not-leak"}
	}`)

	err := neteaseUpstreamError(400, body)
	appErr, ok := err.(*apperrors.AppError)
	if !ok {
		t.Fatalf("error type = %T, want *errors.AppError", err)
	}
	if appErr.Code != apperrors.BOT_NETEASE_VERIFICATION_REQUIRED {
		t.Fatalf("code = %s, want %s", appErr.Code, apperrors.BOT_NETEASE_VERIFICATION_REQUIRED)
	}
	if appErr.Status != 409 {
		t.Fatalf("status = %d, want 409", appErr.Status)
	}
	for _, expected := range []string{`"upstreamCode":-462`, `"verifyType":40`, `"verifyId":1007602`, `"verifyUrl":"https://st.music.163.com/encrypt-pages"`} {
		if !strings.Contains(appErr.Details, expected) {
			t.Errorf("details %q missing %q", appErr.Details, expected)
		}
	}
	if strings.Contains(appErr.Details, "must-not-leak") || strings.Contains(appErr.Details, "verifyToken") {
		t.Fatalf("sensitive verification data leaked: %s", appErr.Details)
	}
}

func TestNeteaseUpstreamErrorGeneric(t *testing.T) {
	err := neteaseUpstreamError(400, []byte(`{"code":400,"message":"bad request"}`))
	appErr, ok := err.(*apperrors.AppError)
	if !ok {
		t.Fatalf("error type = %T, want *errors.AppError", err)
	}
	if appErr.Code != apperrors.BOT_NETEASE_API_ERROR {
		t.Fatalf("code = %s, want %s", appErr.Code, apperrors.BOT_NETEASE_API_ERROR)
	}
}
