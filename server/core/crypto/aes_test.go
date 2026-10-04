package crypto

import (
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	secret := "test-secret-key"
	cases := []struct {
		name      string
		plaintext string
	}{
		{"empty", ""},
		{"short", "hello"},
		{"unicode", "你好，世界！"},
		{"special chars", "p@ssw0rd!#$%^&*()"},
		{"long", strings.Repeat("a", 4096)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ciphertext, err := EncryptAES(tc.plaintext, secret)
			if err != nil {
				t.Fatalf("EncryptAES failed: %v", err)
			}
			if ciphertext == tc.plaintext && tc.plaintext != "" {
				t.Errorf("ciphertext equals plaintext; encryption did not occur")
			}
			decrypted, err := DecryptAES(ciphertext, secret)
			if err != nil {
				t.Fatalf("DecryptAES failed: %v", err)
			}
			if decrypted != tc.plaintext {
				t.Errorf("round-trip mismatch: got %q, want %q", decrypted, tc.plaintext)
			}
		})
	}
}

func TestEncryptProducesDifferentCiphertexts(t *testing.T) {
	secret := "same-secret"
	plaintext := "identical-input"
	c1, err := EncryptAES(plaintext, secret)
	if err != nil {
		t.Fatalf("first EncryptAES failed: %v", err)
	}
	c2, err := EncryptAES(plaintext, secret)
	if err != nil {
		t.Fatalf("second EncryptAES failed: %v", err)
	}
	if c1 == c2 {
		t.Errorf("expected different ciphertexts due to random nonce, got identical")
	}
}

func TestDecryptWithWrongKey(t *testing.T) {
	plaintext := "sensitive-data"
	correctSecret := "correct-key"
	wrongSecret := "wrong-key"

	ciphertext, err := EncryptAES(plaintext, correctSecret)
	if err != nil {
		t.Fatalf("EncryptAES failed: %v", err)
	}

	if _, err := DecryptAES(ciphertext, wrongSecret); err == nil {
		t.Errorf("expected decryption error with wrong key, got nil")
	}
}

func TestDecryptInvalidBase64(t *testing.T) {
	if _, err := DecryptAES("!!!not-base64!!!", "any-secret"); err == nil {
		t.Errorf("expected base64 decode error, got nil")
	}
}

func TestDecryptCiphertextTooShort(t *testing.T) {
	// A valid base64 string that decodes to fewer bytes than the nonce size.
	// nonceSize for AES-GCM is 12 bytes; 1 byte encodes to "AA==".
	if _, err := DecryptAES("AA==", "any-secret"); err == nil {
		t.Errorf("expected 'ciphertext too short' error, got nil")
	}
}
