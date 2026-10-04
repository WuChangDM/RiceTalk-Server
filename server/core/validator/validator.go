package validator

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	// Username allows alphanumeric, underscore, and Chinese characters
	usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9_\p{Han}]+$`)
)

// ValidateEmail checks if the email is valid
func ValidateEmail(email string) bool {
	if len(email) < 5 || len(email) > 255 {
		return false
	}
	return emailRegex.MatchString(email)
}

// ValidateUsername checks if the username is valid
func ValidateUsername(username string) bool {
	if len(username) < 2 || len(username) > 32 {
		return false
	}
	return usernameRegex.MatchString(username)
}

// ValidatePassword checks password strength per security design doc:
// minimum 12 chars, at least 3 character types (upper/lower/digit/special)
func ValidatePassword(password string) (valid bool, reason string) {
	if len(password) < 12 {
		return false, "password must be at least 12 characters"
	}
	if len(password) > 128 {
		return false, "password must not exceed 128 characters"
	}
	// Count character types
	hasUpper := false
	hasLower := false
	hasDigit := false
	hasSpecial := false
	for _, r := range password {
		if r >= 'a' && r <= 'z' {
			hasLower = true
		} else if r >= 'A' && r <= 'Z' {
			hasUpper = true
		} else if r >= '0' && r <= '9' {
			hasDigit = true
		} else {
			hasSpecial = true
		}
	}
	charTypes := 0
	if hasUpper {
		charTypes++
	}
	if hasLower {
		charTypes++
	}
	if hasDigit {
		charTypes++
	}
	if hasSpecial {
		charTypes++
	}
	if charTypes < 3 {
		return false, "password must contain at least 3 character types (uppercase, lowercase, digits, special characters)"
	}
	return true, ""
}

// ValidateChannelName checks if a channel name is valid
func ValidateChannelName(name string) bool {
	name = strings.TrimSpace(name)
	length := utf8.RuneCountInString(name)
	return length >= 1 && length <= 64
}

// ValidateMessageContent checks message content
func ValidateMessageContent(content string) (valid bool, reason string) {
	content = strings.TrimSpace(content)
	length := utf8.RuneCountInString(content)
	if length == 0 {
		return false, "message cannot be empty"
	}
	if length > 2000 {
		return false, "message too long (max 2000 characters)"
	}
	return true, ""
}

// SanitizeString trims and limits string length
func SanitizeString(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxLen {
		runes := []rune(s)
		s = string(runes[:maxLen])
	}
	return s
}
