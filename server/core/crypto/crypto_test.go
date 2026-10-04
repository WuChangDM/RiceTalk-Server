package crypto

import "testing"

func TestHashPasswordAndCheck(t *testing.T) {
	password := "MyStrongPassword123!"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}
	if hash == "" {
		t.Errorf("hash is empty")
	}
	if hash == password {
		t.Errorf("hash equals plaintext")
	}
	if !CheckPassword(password, hash) {
		t.Errorf("CheckPassword returned false for correct password")
	}
}

func TestCheckPasswordWrongPassword(t *testing.T) {
	hash, err := HashPassword("correct-password")
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}
	if CheckPassword("wrong-password", hash) {
		t.Errorf("CheckPassword returned true for wrong password")
	}
}

func TestHashPasswordUniqueSalts(t *testing.T) {
	password := "same-password"
	h1, err := HashPassword(password)
	if err != nil {
		t.Fatalf("first HashPassword failed: %v", err)
	}
	h2, err := HashPassword(password)
	if err != nil {
		t.Fatalf("second HashPassword failed: %v", err)
	}
	if h1 == h2 {
		t.Errorf("expected different hashes due to random salt, got identical")
	}
}

func TestSetBcryptCost(t *testing.T) {
	original := bcryptCost
	defer func() { bcryptCost = original }()

	// Valid cost should update
	SetBcryptCost(4)
	if bcryptCost != 4 {
		t.Errorf("expected bcryptCost=4, got %d", bcryptCost)
	}

	// Invalid cost (too low) should be ignored
	SetBcryptCost(3)
	if bcryptCost != 4 {
		t.Errorf("expected bcryptCost=4 (ignored invalid), got %d", bcryptCost)
	}

	// Invalid cost (too high) should be ignored
	SetBcryptCost(32)
	if bcryptCost != 4 {
		t.Errorf("expected bcryptCost=4 (ignored invalid), got %d", bcryptCost)
	}
}

func TestRandomToken(t *testing.T) {
	token, err := RandomToken(32)
	if err != nil {
		t.Fatalf("RandomToken failed: %v", err)
	}
	if len(token) != 64 { // 32 bytes hex = 64 chars
		t.Errorf("expected token length 64, got %d", len(token))
	}

	// Two tokens should differ
	t2, _ := RandomToken(32)
	if token == t2 {
		t.Errorf("expected different random tokens, got identical")
	}
}

func TestRandomTokenDefaultSize(t *testing.T) {
	token, err := RandomToken(0) // should use TokenBytes default
	if err != nil {
		t.Fatalf("RandomToken(0) failed: %v", err)
	}
	if len(token) != TokenBytes*2 {
		t.Errorf("expected default token length %d, got %d", TokenBytes*2, len(token))
	}
}

func TestRandomHex(t *testing.T) {
	hex, err := RandomHex(16)
	if err != nil {
		t.Fatalf("RandomHex failed: %v", err)
	}
	if len(hex) != 32 { // 16 bytes hex = 32 chars
		t.Errorf("expected hex length 32, got %d", len(hex))
	}
}
