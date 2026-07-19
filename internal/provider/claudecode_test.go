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

	"limitdock/internal/quota"
	"limitdock/internal/readmodel"
	"limitdock/internal/settings"
)

func TestClaudeCodeReaderUsesEnvToken(t *testing.T) {
	srv := newClaudeUsageServer(t, `{
		"five_hour":{"utilization":69.0,"resets_at":"2026-05-16T20:20:00Z"},
		"seven_day":{"utilization":6.0,"resets_at":"2026-05-22T04:00:00Z"}
	}`)
	defer srv.Close()
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-token")

	model, err := ClaudeCodeReader{BaseURL: srv.URL}.Read(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	snap := model.Snapshots["claude-code"]
	if snap == nil {
		t.Fatalf("missing claude-code snapshot: %#v", model.Snapshots)
	}
	if snap.ProviderID != "claude_code" {
		t.Errorf("provider_id = %q, want claude_code", snap.ProviderID)
	}

	m5h, ok := snap.Metrics["usage_five_hour"]
	if !ok || m5h.Used == nil || *m5h.Used != 69.0 {
		t.Errorf("usage_five_hour = %#v, want Used=69.0", m5h)
	}
	if m5h.Unit != "%" || m5h.Window != "5h" {
		t.Errorf("usage_five_hour unit/window = %q/%v, want %%/5h", m5h.Unit, m5h.Window)
	}

	m7d, ok := snap.Metrics["usage_seven_day"]
	if !ok || m7d.Used == nil || *m7d.Used != 6.0 {
		t.Errorf("usage_seven_day = %#v, want Used=6.0", m7d)
	}
}

func TestClaudeCodeReaderUsesCredentialsFile(t *testing.T) {
	srv := newClaudeUsageServer(t, `{
		"five_hour":{"utilization":42.0,"resets_at":"2026-05-16T20:00:00Z"},
		"seven_day":{"utilization":10.0,"resets_at":"2026-05-22T04:00:00Z"}
	}`)
	defer srv.Close()
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "") // ensure env token doesn't shadow file

	credsPath := writeClaudeCredentials(t, "file-token")
	model, err := ClaudeCodeReader{BaseURL: srv.URL, CredentialsPath: credsPath}.Read(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	snap := model.Snapshots["claude-code"]
	if snap == nil {
		t.Fatalf("missing claude-code snapshot")
	}
	if m, ok := snap.Metrics["usage_five_hour"]; !ok || m.Used == nil || *m.Used != 42.0 {
		t.Errorf("usage_five_hour = %#v, want 42.0", m)
	}
}

func TestClaudeCodeReaderNilBucketsSkipped(t *testing.T) {
	srv := newClaudeUsageServer(t, `{
		"five_hour":{"utilization":50.0,"resets_at":"2026-05-16T20:00:00Z"},
		"seven_day":null,
		"seven_day_sonnet":null,
		"seven_day_opus":null,
		"seven_day_cowork":null
	}`)
	defer srv.Close()
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-token")

	model, err := ClaudeCodeReader{BaseURL: srv.URL}.Read(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	snap := model.Snapshots["claude-code"]
	if snap == nil {
		t.Fatalf("missing claude-code snapshot")
	}
	if len(snap.Metrics) != 1 {
		t.Errorf("expected 1 metric, got %d: %v", len(snap.Metrics), snap.Metrics)
	}
	if _, ok := snap.Metrics["usage_five_hour"]; !ok {
		t.Errorf("missing usage_five_hour metric")
	}
}

func TestClaudeCodeReaderNoCredentials(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // empty dir — no credentials.json

	model, err := ClaudeCodeReader{CredentialsPath: "/nonexistent/.credentials.json"}.Read(context.Background())
	if err != nil {
		t.Fatalf("expected silent skip, got error: %v", err)
	}
	if len(model.Snapshots) != 0 {
		t.Errorf("expected empty model, got %v", model.Snapshots)
	}
}

func TestClaudeCodeReaderRendersQuotaCards(t *testing.T) {
	srv := newClaudeUsageServer(t, `{
		"five_hour":{"utilization":69.0,"resets_at":"2026-05-16T20:20:00Z"},
		"seven_day":{"utilization":6.0,"resets_at":"2026-05-22T04:00:00Z"}
	}`)
	defer srv.Close()
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-token")

	model, err := ClaudeCodeReader{BaseURL: srv.URL}.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	cards := quota.Cards(model, settings.Defaults())
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards[0].ProviderID != "claude_code" {
		t.Errorf("card provider = %q, want claude_code", cards[0].ProviderID)
	}
	if len(cards[0].Bands) == 0 {
		t.Errorf("expected quota bands, got none")
	}
	// 5h band: used=69% → remaining=31%
	band5h := cards[0].Bands[0]
	if band5h.Key != "usage_five_hour" {
		t.Errorf("first band key = %q, want usage_five_hour", band5h.Key)
	}
	if band5h.Percent == nil || *band5h.Percent < 30 || *band5h.Percent > 32 {
		t.Errorf("5h remaining = %v, want ~31%%", band5h.Percent)
	}
}

func TestClaudeCodeReaderResetsStored(t *testing.T) {
	srv := newClaudeUsageServer(t, `{
		"five_hour":{"utilization":50.0,"resets_at":"2026-05-16T20:00:00Z"},
		"seven_day":{"utilization":20.0,"resets_at":null}
	}`)
	defer srv.Close()
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-token")

	model, err := ClaudeCodeReader{BaseURL: srv.URL}.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	snap := model.Snapshots["claude-code"]
	if snap == nil {
		t.Fatalf("missing snapshot")
	}
	if _, ok := snap.Resets["usage_five_hour_reset"]; !ok {
		t.Errorf("missing reset key for usage_five_hour")
	}
	if _, ok := snap.Resets["usage_seven_day_reset"]; ok {
		t.Errorf("reset key should not exist for null resets_at")
	}
}

func TestClaudeCodeReaderExpiredCredentialsTriggersRefresh(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	var usageAuth string
	usageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		usageAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"five_hour":{"utilization":12.0,"resets_at":"2026-08-01T00:00:00Z"}}`))
	}))
	defer usageSrv.Close()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"refreshed-token","refresh_token":"rotated-refresh","expires_in":28800}`))
	}))
	defer tokenSrv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	creds := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  "expired-token",
			"refreshToken": "old-refresh",
			"expiresAt":    int64(1000), // long past
		},
	}
	data, _ := json.Marshal(creds)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}

	reader := ClaudeCodeReader{BaseURL: usageSrv.URL, CredentialsPath: path, TokenURL: tokenSrv.URL, CredStoreDir: t.TempDir()}
	model, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if model.Snapshots["claude-code"] == nil {
		t.Fatalf("missing snapshot after refresh: %#v", model.Snapshots)
	}
	if usageAuth != "Bearer refreshed-token" {
		t.Fatalf("usage call used %q, want refreshed token", usageAuth)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back creds: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(after, &doc); err != nil {
		t.Fatalf("parse back creds: %v", err)
	}
	oauth, _ := doc["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "refreshed-token" || oauth["refreshToken"] != "rotated-refresh" {
		t.Fatalf("rotated pair not written back: %#v", oauth)
	}
}

func TestClaudeCodeReaderRefreshRejectionSurfacesNeedsAuth(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer tokenSrv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	creds := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  "expired-token",
			"refreshToken": "dead-refresh",
			"expiresAt":    int64(1000),
		},
	}
	data, _ := json.Marshal(creds)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}

	reader := ClaudeCodeReader{CredentialsPath: path, TokenURL: tokenSrv.URL, CredStoreDir: t.TempDir()}
	model, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("needs_auth should not be an error: %v", err)
	}
	snap := model.Snapshots["claude-code"]
	if snap == nil || snap.Status != readmodel.StatusNeedsAuth {
		t.Fatalf("expected needs_auth snapshot, got %#v", snap)
	}
}

func TestClaudeCodeReaderUsage403SurfacesNeedsAuth(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "inference-only-token")
	usageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer usageSrv.Close()

	model, err := ClaudeCodeReader{BaseURL: usageSrv.URL, CredStoreDir: t.TempDir()}.Read(context.Background())
	if err != nil {
		t.Fatalf("403 should map to needs_auth, not error: %v", err)
	}
	snap := model.Snapshots["claude-code"]
	if snap == nil || snap.Status != readmodel.StatusNeedsAuth {
		t.Fatalf("expected needs_auth snapshot, got %#v", snap)
	}
	if !strings.Contains(snap.Message, "user:profile") {
		t.Fatalf("403 message should mention scope: %q", snap.Message)
	}
}

func TestClaudeCodeReaderUsage429ReturnsTypedError(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-token")
	usageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer usageSrv.Close()

	_, err := ClaudeCodeReader{BaseURL: usageSrv.URL, CredStoreDir: t.TempDir()}.Read(context.Background())
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected HTTPStatusError 429, got %v", err)
	}
}

func TestClaudeCodeReaderMandatoryHeaders(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-token")
	var ua, beta string
	usageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		beta = r.Header.Get("anthropic-beta")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"five_hour":{"utilization":1.0}}`))
	}))
	defer usageSrv.Close()

	if _, err := (ClaudeCodeReader{BaseURL: usageSrv.URL, CredStoreDir: t.TempDir()}).Read(context.Background()); err != nil {
		t.Fatalf("read: %v", err)
	}
	if ua != claudeCodeUserAgent {
		t.Fatalf("User-Agent = %q, want %q", ua, claudeCodeUserAgent)
	}
	if beta != claudeCodeBetaHeader {
		t.Fatalf("anthropic-beta = %q, want %q", beta, claudeCodeBetaHeader)
	}
}

// helpers

// newClaudeHybridServer serves the usage API with usageStatus and the
// messages probe endpoint with probeStatus (attaching unified rate-limit
// headers on 2xx).
func newClaudeHybridServer(t *testing.T, usageStatus, probeStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case claudeCodeUsagePath:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(usageStatus)
			w.Write([]byte(`{}`))
		case claudeMessagesPath:
			if probeStatus < 300 {
				w.Header().Set(claudeHeader5hUtil, "0.4")
				w.Header().Set(claudeHeader5hReset, "1784470200")
				w.Header().Set(claudeHeader7dUtil, "0.26")
				w.Header().Set(claudeHeader7dReset, "1784865600")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(probeStatus)
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestClaudeCodeReaderFallsBackToHeadersOnUsage429(t *testing.T) {
	srv := newClaudeHybridServer(t, http.StatusTooManyRequests, http.StatusOK)
	defer srv.Close()
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-token")

	model, err := ClaudeCodeReader{BaseURL: srv.URL}.Read(context.Background())
	if err != nil {
		t.Fatalf("header fallback should succeed: %v", err)
	}
	snap := model.Snapshots["claude-code"]
	if snap == nil || snap.Status != "ok" {
		t.Fatalf("expected ok snapshot from headers, got %#v", snap)
	}
	if m := snap.Metrics["usage_five_hour"]; m.Used == nil || *m.Used != 40.0 || m.Window != "5h" {
		t.Fatalf("usage_five_hour = %#v, want Used=40 Window=5h", m)
	}
	if m := snap.Metrics["usage_seven_day"]; m.Used == nil || *m.Used != 26.0 {
		t.Fatalf("usage_seven_day = %#v, want Used=26", m)
	}
	if rs, ok := snap.Resets["usage_five_hour_reset"].(string); !ok || !strings.HasPrefix(rs, "2026-") {
		t.Fatalf("usage_five_hour_reset = %#v, want RFC3339 string", snap.Resets["usage_five_hour_reset"])
	}
	if snap.Raw["source"] != "claude-code-headers" {
		t.Fatalf("snapshot should mark header source: %#v", snap.Raw)
	}
}

func TestClaudeCodeReaderFallsBackToHeadersOnUsage403(t *testing.T) {
	// user:inference-only tokens (claude setup-token) get 403 from the usage
	// API but can still read quota via inference headers.
	srv := newClaudeHybridServer(t, http.StatusForbidden, http.StatusOK)
	defer srv.Close()
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "setup-token")

	model, err := ClaudeCodeReader{BaseURL: srv.URL}.Read(context.Background())
	if err != nil {
		t.Fatalf("header fallback should succeed: %v", err)
	}
	snap := model.Snapshots["claude-code"]
	if snap == nil || snap.Status != "ok" || len(snap.Metrics) != 2 {
		t.Fatalf("expected quota snapshot for setup-token, got %#v", snap)
	}
}

func TestClaudeCodeReaderProbeFailureKeepsExistingBehavior(t *testing.T) {
	// Probe also rate limited → the reader surfaces the usage 429 so the
	// cache backoff ladder engages.
	srv := newClaudeHybridServer(t, http.StatusTooManyRequests, http.StatusTooManyRequests)
	defer srv.Close()
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-token")

	_, err := ClaudeCodeReader{BaseURL: srv.URL}.Read(context.Background())
	if err == nil || !IsRateLimitError(err) {
		t.Fatalf("expected rate-limit error, got %v", err)
	}

	// Probe fails for a 403 usage rejection → needs_auth like before.
	srv2 := newClaudeHybridServer(t, http.StatusForbidden, http.StatusForbidden)
	defer srv2.Close()
	model, err := ClaudeCodeReader{BaseURL: srv2.URL}.Read(context.Background())
	if err != nil {
		t.Fatalf("403 path should return status model: %v", err)
	}
	snap := model.Snapshots["claude-code"]
	if snap == nil || snap.Status != readmodel.StatusNeedsAuth {
		t.Fatalf("expected needs_auth when both sources reject, got %#v", snap)
	}
}

func newClaudeUsageServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	// Validate the body is valid JSON
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("invalid JSON in test body: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != claudeCodeUsagePath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
}

func writeClaudeCredentials(t *testing.T, token string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	creds := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken": token,
			"expiresAt":   int64(9999999999999), // far future
		},
	}
	data, err := json.Marshal(creds)
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	return path
}
