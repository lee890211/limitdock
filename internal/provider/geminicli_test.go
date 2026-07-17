package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"limitdock/internal/readmodel"
)

// Fixture mirrors a real free-tier retrieveUserQuota response (2026-07):
// unavailable pro models come back as remainingFraction 0 with an epoch
// resetTime and must not render as an exhausted 0% row.
func TestGeminiQuotaSkipsEpochSentinelBucketsAndPoolsLowestWins(t *testing.T) {
	data := map[string]any{
		"buckets": []any{
			map[string]any{"resetTime": "2026-07-17T16:28:25Z", "tokenType": "REQUESTS", "modelId": "gemini-2.5-flash", "remainingFraction": 1.0},
			map[string]any{"resetTime": "2026-07-17T16:28:25Z", "tokenType": "REQUESTS", "modelId": "gemini-2.5-flash-lite", "remainingFraction": 1.0},
			map[string]any{"resetTime": "1970-01-01T00:00:00Z", "tokenType": "REQUESTS", "modelId": "gemini-2.5-pro", "remainingFraction": 0.0},
			map[string]any{"resetTime": "2026-07-17T16:28:25Z", "tokenType": "REQUESTS", "modelId": "gemini-3-flash-preview", "remainingFraction": 0.4},
			map[string]any{"resetTime": "1970-01-01T00:00:00Z", "tokenType": "REQUESTS", "modelId": "gemini-3-pro-preview", "remainingFraction": 0.0},
		},
	}
	snap := geminiQuotaToSnapshot(data)
	if snap == nil {
		t.Fatal("expected snapshot")
	}
	if _, exists := snap.Metrics["quota_model_gemini_pro"]; exists {
		t.Fatalf("epoch-sentinel pro bucket must be skipped: %#v", snap.Metrics)
	}
	flash, ok := snap.Metrics["quota_model_gemini_flash"]
	if !ok || flash.Remaining == nil || *flash.Remaining != 40 {
		t.Fatalf("pooled flash should keep the lowest remaining (40), got %#v", flash)
	}
	if _, ok := snap.Metrics["quota_model_gemini_flash_lite"]; !ok {
		t.Fatalf("flash-lite row missing: %#v", snap.Metrics)
	}
	if _, ok := snap.Resets["quota_model_gemini_flash_reset"]; !ok {
		t.Fatalf("flash reset should be kept: %#v", snap.Resets)
	}
}

// A genuine exhaustion (remaining 0 with a real future reset) must still show.
func TestGeminiQuotaKeepsGenuineExhaustionWithRealReset(t *testing.T) {
	data := map[string]any{
		"buckets": []any{
			map[string]any{"resetTime": "2026-07-18T02:00:00Z", "modelId": "gemini-2.5-pro", "remainingFraction": 0.0},
		},
	}
	snap := geminiQuotaToSnapshot(data)
	if snap == nil {
		t.Fatal("expected snapshot")
	}
	pro, ok := snap.Metrics["quota_model_gemini_pro"]
	if !ok || pro.Remaining == nil || *pro.Remaining != 0 {
		t.Fatalf("genuinely exhausted pro should render 0%%: %#v", snap.Metrics)
	}
	if _, ok := snap.Resets["quota_model_gemini_pro_reset"]; !ok {
		t.Fatalf("real reset must be kept: %#v", snap.Resets)
	}
}

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

func TestGeminiCLIReaderExpiredTokenNoRefreshSurfacesNeedsAuth(t *testing.T) {
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
	snap := model.Snapshots["gemini_cli-local"]
	if snap == nil {
		t.Fatalf("expected needs_auth snapshot for expired token with no refresh_token: %#v", model.Snapshots)
	}
	if snap.Status != readmodel.StatusNeedsAuth {
		t.Fatalf("expected status %q, got %q", readmodel.StatusNeedsAuth, snap.Status)
	}
	if snap.Message == "" {
		t.Fatalf("expected a non-empty message explaining the needs_auth status")
	}
}

func TestGeminiCLIReaderHappyPathWithArrayQuotas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "loadCodeAssist") {
			json.NewEncoder(w).Encode(map[string]any{"cloudaicompanionProject": "gen-lang-client-test"})
			return
		}
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
		if strings.Contains(r.URL.Path, "loadCodeAssist") {
			json.NewEncoder(w).Encode(map[string]any{})
			return
		}
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
	if err == nil {
		t.Fatalf("expected error on quota API failure, got model=%#v", model)
	}
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected error to wrap *HTTPStatusError, got %v (%T)", err, err)
	}
	if httpErr.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", httpErr.StatusCode)
	}
}

func TestGeminiReaderRefreshesExpiredTokenAndPersists(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "ya29.refreshed-token"})
	}))
	defer tokenServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "loadCodeAssist") {
			json.NewEncoder(w).Encode(map[string]any{"cloudaicompanionProject": "gen-lang-client-test"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"email": "user@gmail.com",
			"quotas": []any{
				map[string]any{"model": "gemini-1.5-flash", "remaining": 0.9, "resetTime": "2026-05-18T00:00:00Z"},
			},
		})
	}))
	defer apiServer.Close()

	dir := t.TempDir()
	path := writeGeminiCredsRaw(t, dir, map[string]any{
		"access_token":  "ya29.old-token",
		"refresh_token": "1//old-refresh-token",
		"client_id":     "test-client-id",
		"client_secret": "test-client-secret",
		"expiry_date":   time.Now().Add(-10 * time.Minute).UnixMilli(),
		// Stale and independent of expiry_date - reproduces the on-disk shape
		// that used to cause a refresh loop before persistGeminiAccessToken
		// synced both fields.
		"expiry":       time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
		"custom_field": "keep-me",
	})

	r := GeminiCLIReader{
		CredentialsPath: path,
		BaseURL:         apiServer.URL,
		TokenURL:        tokenServer.URL,
	}
	model, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	snap := model.Snapshots["gemini_cli-user@gmail.com"]
	if snap == nil {
		t.Fatalf("expected quota snapshot after refresh: %#v", model.Snapshots)
	}

	persisted := readRawGeminiCreds(t, path)
	if persisted["access_token"] != "ya29.refreshed-token" {
		t.Fatalf("expected refreshed access_token persisted, got %v", persisted["access_token"])
	}
	if persisted["custom_field"] != "keep-me" {
		t.Fatalf("expected unknown field preserved, got %#v", persisted)
	}
	expiryDateMs, ok := persisted["expiry_date"].(float64)
	if !ok {
		t.Fatalf("expected numeric expiry_date, got %#v", persisted["expiry_date"])
	}
	expiryStr, _ := persisted["expiry"].(string)
	expiryTime, err := time.Parse(time.RFC3339, expiryStr)
	if err != nil {
		t.Fatalf("expiry not RFC3339: %v (%q)", err, expiryStr)
	}
	now := time.Now()
	if !time.UnixMilli(int64(expiryDateMs)).After(now) || !expiryTime.After(now) {
		t.Fatalf("expected both expiry fields in the future: expiry_date=%v expiry=%v", time.UnixMilli(int64(expiryDateMs)), expiryTime)
	}
	if diff := time.UnixMilli(int64(expiryDateMs)).Sub(expiryTime); diff < -2*time.Second || diff > 2*time.Second {
		t.Fatalf("expiry_date and expiry not consistent: expiry_date=%v expiry=%v", time.UnixMilli(int64(expiryDateMs)), expiryTime)
	}
}

func TestPersistGeminiAccessTokenSyncsBothExpiryFields(t *testing.T) {
	dir := t.TempDir()
	path := writeGeminiCredsRaw(t, dir, map[string]any{
		"access_token": "ya29.old-token",
		"expiry_date":  time.Now().Add(-10 * time.Minute).UnixMilli(),
		"expiry":       time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
	})

	if err := persistGeminiAccessToken(path, "ya29.new-token"); err != nil {
		t.Fatalf("persist: %v", err)
	}

	persisted := readRawGeminiCreds(t, path)
	if persisted["access_token"] != "ya29.new-token" {
		t.Fatalf("expected access_token updated, got %v", persisted["access_token"])
	}
	expiryDateMs, ok := persisted["expiry_date"].(float64)
	if !ok {
		t.Fatalf("expected numeric expiry_date, got %#v", persisted["expiry_date"])
	}
	expiryStr, ok := persisted["expiry"].(string)
	if !ok {
		t.Fatalf("expected string expiry, got %#v", persisted["expiry"])
	}
	expiryTime, err := time.Parse(time.RFC3339, expiryStr)
	if err != nil {
		t.Fatalf("expiry not RFC3339: %v", err)
	}
	now := time.Now()
	expiryDate := time.UnixMilli(int64(expiryDateMs))
	if !expiryDate.After(now) || !expiryTime.After(now) {
		t.Fatalf("expected both expiry fields in the future: expiry_date=%v expiry=%v", expiryDate, expiryTime)
	}
	if diff := expiryDate.Sub(expiryTime); diff < -2*time.Second || diff > 2*time.Second {
		t.Fatalf("expiry fields not consistent: expiry_date=%v expiry=%v", expiryDate, expiryTime)
	}
}

func TestPersistGeminiAccessTokenPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := writeGeminiCredsRaw(t, dir, map[string]any{
		"access_token":  "ya29.old-token",
		"refresh_token": "1//refresh",
		"expiry_date":   time.Now().Add(time.Hour).UnixMilli(),
		"custom_field":  "keep-me",
		"nested":        map[string]any{"a": 1.0},
	})

	if err := persistGeminiAccessToken(path, "ya29.new-token"); err != nil {
		t.Fatalf("persist: %v", err)
	}

	persisted := readRawGeminiCreds(t, path)
	if persisted["refresh_token"] != "1//refresh" {
		t.Fatalf("expected refresh_token preserved, got %v", persisted["refresh_token"])
	}
	if persisted["custom_field"] != "keep-me" {
		t.Fatalf("expected custom_field preserved, got %v", persisted["custom_field"])
	}
	nested, ok := persisted["nested"].(map[string]any)
	if !ok || nested["a"] != 1.0 {
		t.Fatalf("expected nested field preserved, got %#v", persisted["nested"])
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

// writeGeminiCredsRaw writes an arbitrary field set (including fields not
// modeled by geminiCredentials) and returns the file path.
func writeGeminiCredsRaw(t *testing.T, dir string, fields map[string]any) string {
	t.Helper()
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	path := filepath.Join(dir, "oauth_credentials.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	return path
}

func readRawGeminiCreds(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading persisted creds: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal persisted creds: %v", err)
	}
	return out
}
