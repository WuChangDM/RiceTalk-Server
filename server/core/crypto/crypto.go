package crypto

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const (
	// DefaultBcryptCost is the default cost parameter for bcrypt hashing
	DefaultBcryptCost = 12
	// TokenBytes is the number of random bytes for token generation
	TokenBytes = 32
)

// bcryptCost holds the active bcrypt cost; it can be changed once at startup.
var bcryptCost = DefaultBcryptCost

// SetBcryptCost updates the bcrypt cost used by HashPassword.
func SetBcryptCost(cost int) {
	if cost >= 4 && cost <= 31 {
		bcryptCost = cost
	}
}

// HashPassword hashes a plaintext password using bcrypt
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(bytes), nil
}

// CheckPassword verifies a password against a bcrypt hash
func CheckPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// RandomToken generates a cryptographically secure random hex token
func RandomToken(n int) (string, error) {
	if n <= 0 {
		n = TokenBytes
	}
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random token: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// RandomHex generates a hex string of n bytes
func RandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random hex: %w", err)
	}
	return hex.EncodeToString(b), nil
}
