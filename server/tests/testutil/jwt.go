package testutil

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"ridgericetalk/internal/config"
)

// MockJWTToken generates a valid JWT access token for testing.
func MockJWTToken(userID, username, email, role string) string {
	cfg := config.DefaultConfig()
	cfg.JWTSecret = "test-secret-key-for-unit-tests-only"

	claims := jwt.MapClaims{
		"user_id":  userID,
		"username": username,
		"email":    email,
		"role":     role,
		"type":     "access",
		"exp":      time.Now().Add(time.Hour).Unix(),
		"iat":      time.Now().Unix(),
		"iss":      "ridgericetalk",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := token.SignedString([]byte(cfg.JWTSecret))
	if err != nil {
		panic(fmt.Sprintf("failed to sign mock JWT token: %v", err))
	}
	return s
}
