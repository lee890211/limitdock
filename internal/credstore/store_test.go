package credstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type payload struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

func TestStoreRoundTrip(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "credentials"))
	in := payload{AccessToken: "sk-ant-oat01-super-secret-token", RefreshToken: "sk-ant-ort01-refresh"}
	if err := s.Save("claude", in); err != nil {
		t.Fatalf("save: %v", err)
	}
	var out payload
	if err := s.Load("claude", &out); err != nil {
		t.Fatalf("load: %v", err)
	}
	if out != in {
		t.Fatalf("round trip mismatch: %#v", out)
	}
}

func TestStoreFileDoesNotContainPlaintext(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	s := New(dir)
	secret := "sk-ant-oat01-super-secret-token-value"
	if err := s.Save("claude", payload{AccessToken: secret}); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "claude.bin"))
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("stored blob contains plaintext secret")
	}
}

func TestStoreLoadMissingReturnsErrNotFound(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "credentials"))
	var out payload
	if err := s.Load("claude", &out); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStoreLoadCorruptedBlobFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	s := New(dir)
	if err := s.Save("claude", payload{AccessToken: "secret"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	path := filepath.Join(dir, "claude.bin")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	raw[len(raw)/2] ^= 0xFF
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	var out payload
	if err := s.Load("claude", &out); err == nil {
		t.Fatal("expected error for corrupted blob")
	}
}

func TestStoreDeleteIsIdempotent(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "credentials"))
	if err := s.Delete("claude"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if err := s.Save("claude", payload{AccessToken: "x"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.Delete("claude"); err != nil {
		t.Fatalf("delete existing: %v", err)
	}
	var out payload
	if err := s.Load("claude", &out); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}
