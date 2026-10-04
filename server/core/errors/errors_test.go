package errors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name       string
		code       ErrorCode
		message    string
		wantStatus int
	}{
		{"bad request", SYSTEM_BAD_REQUEST, "invalid input", http.StatusBadRequest},
		{"not found", SYSTEM_NOT_FOUND, "missing", http.StatusNotFound},
		{"unauthorized", AUTH_UNAUTHORIZED, "login required", http.StatusUnauthorized},
		{"forbidden", AUTH_FORBIDDEN, "no permission", http.StatusForbidden},
		{"internal", SYSTEM_INTERNAL_ERROR, "oops", http.StatusInternalServerError},
		{"unknown code", ErrorCode("UNKNOWN_CODE"), "unknown", http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New(tt.code, tt.message)
			if err.Code != tt.code {
				t.Errorf("Code = %v, want %v", err.Code, tt.code)
			}
			if err.Message != tt.message {
				t.Errorf("Message = %v, want %v", err.Message, tt.message)
			}
			if err.Status != tt.wantStatus {
				t.Errorf("Status = %v, want %v", err.Status, tt.wantStatus)
			}
		})
	}
}

func TestAppError_Error(t *testing.T) {
	err := New(SYSTEM_BAD_REQUEST, "bad input")
	want := "[SYSTEM_BAD_REQUEST] bad input"
	if err.Error() != want {
		t.Errorf("Error() = %v, want %v", err.Error(), want)
	}
}

func TestWithDetails(t *testing.T) {
	err := New(SYSTEM_BAD_REQUEST, "bad input").WithDetails("field: name")
	if err.Details != "field: name" {
		t.Errorf("Details = %v, want %v", err.Details, "field: name")
	}
}

func TestSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	Success(c, map[string]string{"key": "value"})

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.Code != "OK" {
		t.Errorf("Code = %v, want OK", resp.Code)
	}
	if resp.Message != "success" {
		t.Errorf("Message = %v, want success", resp.Message)
	}
	if resp.Data == nil {
		t.Error("Data should not be nil")
	}
	if resp.Meta == nil {
		t.Error("Meta should not be nil")
	}
	if resp.Meta.RequestID == "" {
		t.Error("Meta.RequestID should not be empty")
	}
	if resp.Meta.Timestamp == "" {
		t.Error("Meta.Timestamp should not be empty")
	}
}

func TestFromError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	t.Run("AppError", func(t *testing.T) {
		resp := FromError(c, New(SYSTEM_NOT_FOUND, "not here"))
		if resp.Code != string(SYSTEM_NOT_FOUND) {
			t.Errorf("Code = %v, want %v", resp.Code, SYSTEM_NOT_FOUND)
		}
		if resp.Meta == nil || resp.Meta.RequestID == "" {
			t.Error("Meta.RequestID should not be empty")
		}
	})

	t.Run("generic error", func(t *testing.T) {
		resp := FromError(c, http.ErrAbortHandler)
		if resp.Code != string(SYSTEM_INTERNAL_ERROR) {
			t.Errorf("Code = %v, want %v", resp.Code, SYSTEM_INTERNAL_ERROR)
		}
		if resp.Meta == nil || resp.Meta.RequestID == "" {
			t.Error("Meta.RequestID should not be empty")
		}
	})
}

func TestPredefinedErrors(t *testing.T) {
	predefined := map[string]*AppError{
		"ErrInternal":     ErrInternal,
		"ErrBadRequest":   ErrBadRequest,
		"ErrNotFound":     ErrNotFound,
		"ErrUnauthorized": ErrUnauthorized,
		"ErrForbidden":    ErrForbidden,
		"ErrRateLimited":  ErrRateLimited,
	}

	for name, err := range predefined {
		if err == nil {
			t.Errorf("%s should not be nil", name)
		}
		if err.Status == 0 {
			t.Errorf("%s.Status should not be 0", name)
		}
	}
}
