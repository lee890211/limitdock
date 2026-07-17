package native

import (
	"bytes"
	"testing"
)

func TestProtectUnprotectRoundTrip(t *testing.T) {
	entropy := []byte("LimitDock:test-entropy")
	for _, plaintext := range [][]byte{
		[]byte("x"),
		[]byte(`{"accessToken":"secret-value","refreshToken":"another"}`),
		bytes.Repeat([]byte("payload"), 4096),
	} {
		blob, err := ProtectData(plaintext, entropy)
		if err != nil {
			t.Fatalf("ProtectData(%d bytes): %v", len(plaintext), err)
		}
		if len(plaintext) >= 16 && bytes.Contains(blob, plaintext) {
			t.Fatal("ciphertext contains plaintext")
		}
		got, err := UnprotectData(blob, entropy)
		if err != nil {
			t.Fatalf("UnprotectData: %v", err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(plaintext))
		}
	}
}

func TestUnprotectFailsWithWrongEntropy(t *testing.T) {
	blob, err := ProtectData([]byte("secret"), []byte("entropy-a"))
	if err != nil {
		t.Fatalf("ProtectData: %v", err)
	}
	if _, err := UnprotectData(blob, []byte("entropy-b")); err == nil {
		t.Fatal("expected error with mismatched entropy")
	}
}

func TestUnprotectFailsOnCorruptedBlob(t *testing.T) {
	blob, err := ProtectData([]byte("secret"), nil)
	if err != nil {
		t.Fatalf("ProtectData: %v", err)
	}
	blob[len(blob)/2] ^= 0xFF
	if _, err := UnprotectData(blob, nil); err == nil {
		t.Fatal("expected error for corrupted blob")
	}
}
