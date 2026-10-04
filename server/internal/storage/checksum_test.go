package storage

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// ChecksumWriter must produce the same digest as CalculateChecksum: the upload
// paths hash while streaming, everything else hashes a reader.
func TestChecksumWriterMatchesCalculateChecksum(t *testing.T) {
	payload := []byte("ridge-rice-talk checksum")

	want, err := CalculateChecksum(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("CalculateChecksum: %v", err)
	}

	sum := NewChecksumWriter()
	// Write in two chunks to prove the hash spans calls, like io.MultiWriter does.
	if _, err := io.Copy(sum, io.MultiReader(bytes.NewReader(payload[:5]), bytes.NewReader(payload[5:]))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := sum.Sum(); got != want {
		t.Errorf("ChecksumWriter.Sum() = %q, want %q", got, want)
	}
	if len(want) != 64 || strings.ToLower(want) != want {
		t.Errorf("expected 64-char lowercase hex digest, got %q", want)
	}
}

// CalculateChecksum must stay stable across calls (deterministic algorithm).
func TestCalculateChecksumDeterministic(t *testing.T) {
	a, _ := CalculateChecksum(bytes.NewReader([]byte("abc")))
	b, _ := CalculateChecksum(bytes.NewReader([]byte("abc")))
	if a != b {
		t.Errorf("checksum is not deterministic: %s != %s", a, b)
	}
	const abcSHA256 = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if a != abcSHA256 {
		t.Errorf("expected known SHA-256 of \"abc\", got %s", a)
	}
}
