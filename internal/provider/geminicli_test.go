package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGeminiCLIReaderSkipsWhenNoCredentials(t *testing.T) {
	r := GeminiCLIReader{CredentialsPath: filepath.Join(t.TempDir(), "does-not-exist.json")}
	model, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(model.Snapshots) != 0 {
		t.Fatalf("expected empty model, got %#v", model.Snapshots)
	}
}

func TestGeminiCLIReaderSkipsExpiredToken(t *testing.T) {
	dir := t.TempDir()
	creds := geminiCredentials{
		AccessToken: "ya29.old-token",
		ExpiryDate:  time.Now().Add(-10 * time.Minute).UnixMilli(),
	}
	writeGeminiCreds(t, dir, creds)

	r := GeminiCLIReader{CredentialsPath: filepath.Join(dir, "oauth_credentials.json")}
	model, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(model.Snapshots) != 0 {
		t.Fatalf("expected empty model for expired token: %#v", model.Snapshots)
	}
}

func TestGeminiCLIReaderHappyPathWithArrayQuotas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"email": "user@gmail.com",
			"quotas": []any{
				map[string]any{"model": "gemini-1.5-flash", "remaining": 0.87, "window": "1d", "resetTime": "2026-05-18T00:00:00Z"},
				map[string]any{"model": "gemini-1.5-pro", "remaining": 0.40, "window": "1d", "resetTime": "2026-05-18T00:00:00Z"},
			},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	writeGeminiCreds(t, dir, geminiCredentials{
		AccessToken: "ya29.valid-token",
		ExpiryDate:  time.Now().Add(time.Hour).UnixMilli(),
	})

	r := GeminiCLIReader{
		CredentialsPath: filepath.Join(dir, "oauth_credentials.json"),
		BaseURL:         server.URL,
	}
	model, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	snap := model.Snapshots["gemini_cli-user@gmail.com"]
	if snap == nil {
		t.Fatalf("expected gemini_cli snapshot: %#v", model.Snapshots)
	}
	if snap.ProviderID != "gemini_cli" {
		t.Fatalf("wrong provider ID: %q", snap.ProviderID)
	}
	flashMetric := snap.Metrics["quota_model_gemini_flash"]
	if flashMetric.Remaining == nil || *flashMetric.Remaining != 87 {
		t.Fatalf("expected flash remaining=87, got %#v", flashMetric)
	}
	proMetric := snap.Metrics["quota_model_gemini_pro"]
	if proMetric.Remaining == nil || *proMetric.Remaining != 40 {
		t.Fatalf("expected pro remaining=40, got %#v", proMetric)
	}
}

func TestGeminiCLIReaderHappyPathWithFlatQuotas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"quota_flash": 0.75,
			"quota_pro":   0.50,
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	writeGeminiCreds(t, dir, geminiCredentials{
		AccessToken: "ya29.valid-token",
		ExpiryDate:  time.Now().Add(time.Hour).UnixMilli(),
	})

	r := GeminiCLIReader{
		CredentialsPath: filepath.Join(dir, "oauth_credentials.json"),
		BaseURL:         server.URL,
	}
	model, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	snap := model.Snapshots["gemini_cli-local"]
	if snap == nil {
		t.Fatalf("expected gemini_cli snapshot: %#v", model.Snapshots)
	}
	if snap.Metrics["quota_flash"].Remaining == nil {
		t.Fatalf("expected quota_flash metric: %#v", snap.Metrics)
	}
}

func TestGeminiCLIReaderSkipsOnAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	dir := t.TempDir()
	writeGeminiCreds(t, dir, geminiCredentials{
		AccessToken: "ya29.valid-token",
		ExpiryDate:  time.Now().Add(time.Hour).UnixMilli(),
	})

	r := GeminiCLIReader{
		CredentialsPath: filepath.Join(dir, "oauth_credentials.json"),
		BaseURL:         server.URL,
	}
	model, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(model.Snapshots) != 0 {
		t.Fatalf("expected empty model on API error: %#v", model.Snapshots)
	}
}

func writeGeminiCreds(t *testing.T, dir string, creds geminiCredentials) {
	t.Helper()
	data, err := json.Marshal(creds)
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "oauth_credentials.json"), data, 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
}
