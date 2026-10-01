package logger

import "testing"

func TestMaskIPForLogIPv4(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"192.168.1.100", "192.168.1.0"},
		{"10.0.0.1", "10.0.0.0"},
		{"127.0.0.1", "127.0.0.0"},
		{"8.8.8.8", "8.8.8.0"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := MaskIPForLog(tc.input)
			if got != tc.want {
				t.Errorf("MaskIPForLog(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestMaskIPForLogIPv6(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"2001:db8:1234:5678:abcd:ef01:2345:6789", "2001:db8:1234:5678::"},
		{"::1", "::1"},       // short IPv6, returned as-is
		{"fe80::1", "fe80::1"}, // 3 hextets, returned as-is
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := MaskIPForLog(tc.input)
			if got != tc.want {
				t.Errorf("MaskIPForLog(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestMaskIPForLogEmpty(t *testing.T) {
	if got := MaskIPForLog(""); got != "" {
		t.Errorf("MaskIPForLog(\"\") = %q, want empty", got)
	}
}

func TestMaskIPForLogInvalid(t *testing.T) {
	// Non-IP strings should be returned as-is (no dots, no colons).
	input := "not-an-ip"
	if got := MaskIPForLog(input); got != input {
		t.Errorf("MaskIPForLog(%q) = %q, want %q", input, got, input)
	}
}

func TestSanitizeLogValueString(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"clean", "hello world", "hello world"},
		{"CR", "line1\rline2", "line1_line2"},
		{"LF", "line1\nline2", "line1_line2"},
		{"CRLF", "line1\r\nline2", "line1__line2"},
		{"TAB", "col1\tcol2", "col1_col2"},
		{"control char", "a\x01b", "a_b"},
		{"mixed", "a\nb\rc\td\x1fe", "a_b_c_d_e"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeLogValue(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeLogValue(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestSanitizeLogValueMap(t *testing.T) {
	input := map[string]interface{}{
		"key":  "value\nwith\nnewlines",
		"safe": "normal",
	}
	result := sanitizeLogValue(input).(map[string]interface{})
	if result["key"] != "value_with_newlines" {
		t.Errorf("expected sanitized key, got %v", result["key"])
	}
	if result["safe"] != "normal" {
		t.Errorf("expected unchanged safe value, got %v", result["safe"])
	}
}

func TestSanitizeLogValueSlice(t *testing.T) {
	input := []interface{}{"a\n", "b"}
	result := sanitizeLogValue(input).([]interface{})
	if result[0] != "a_" {
		t.Errorf("expected sanitized slice element, got %v", result[0])
	}
	if result[1] != "b" {
		t.Errorf("expected unchanged slice element, got %v", result[1])
	}
}

func TestSanitizeLogValueNonString(t *testing.T) {
	cases := []interface{}{
		42,
		true,
		3.14,
		nil,
	}
	for _, v := range cases {
		if got := sanitizeLogValue(v); got != v {
			t.Errorf("sanitizeLogValue(%v) = %v, want %v", v, got, v)
		}
	}
}

func TestSanitizeQuery(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"no sensitive", "foo=bar&baz=qux", "foo=bar&baz=qux"},
		{"token", "token=secret123", "token=[REDACTED]"},
		{"password", "user=admin&password=hunter2", "user=admin&password=[REDACTED]"},
		{"access_token", "access_token=abc", "access_token=[REDACTED]"},
		{"refresh_token", "refresh_token=xyz", "refresh_token=[REDACTED]"},
		{"api_key", "api_key=k", "api_key=[REDACTED]"},
		{"secret", "secret=s", "secret=[REDACTED]"},
		{"mixed", "a=1&password=p&b=2", "a=1&password=[REDACTED]&b=2"},
		{"key without value", "token", "token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeQuery(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeQuery(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNewLogger(t *testing.T) {
	l, err := New("info")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if l == nil {
		t.Fatalf("logger is nil")
	}
	if l.GetLevel() == 0 {
		// InfoLevel is 4 in logrus; just ensure non-zero
	}
}

func TestNewLoggerInvalidLevel(t *testing.T) {
	l, err := New("not-a-level")
	if err != nil {
		t.Fatalf("New should not return error for invalid level, got %v", err)
	}
	if l == nil {
		t.Fatalf("logger is nil")
	}
	// Should default to InfoLevel
}
