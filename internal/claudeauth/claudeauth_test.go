package claudeauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"limitdock/internal/credstore"
)

func rateLimitBody() map[string]any {
	return map[string]any{"error": map[string]any{"type": "rate_limit_error", "message": "Rate limited. Please try again later."}}
}

func clearAuthEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "no-config"))
}

func writeCLIFile(t *testing.T, oauth map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	doc := map[string]any{
		"claudeAiOauth": oauth,
		"designOauth": map[string]any{
			"accessToken": "design-token",
			"expiresAt":   1783454492935,
		},
		"unknownTopLevel": "keep-me",
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal cli file: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write cli file: %v", err)
	}
	return path
}

func expiredOAuth() map[string]any {
	return map[string]any{
		"accessToken":      "old-access",
		"refreshToken":     "old-refresh",
		"expiresAt":        time.Now().Add(-time.Hour).UnixMilli(),
		"scopes":           []string{"user:inference", "user:profile"},
		"subscriptionType": "max",
		"rateLimitTier":    "default_claude_ai",
	}
}

// seedExpiredStore returns a StoreDir holding an expired Connect token with a
// refresh token, the only credential shape that still triggers a background
// refresh.
func seedExpiredStore(t *testing.T) string {
	t.Helper()
	storeDir := t.TempDir()
	if err := credstore.New(storeDir).Save("claude", storedToken{
		AccessToken:  "stored-old",
		RefreshToken: "stored-refresh",
		ExpiresAt:    time.Now().Add(-time.Minute).UnixMilli(),
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	return storeDir
}

func missingCLIPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "missing.json")
}

type tokenServer struct {
	*httptest.Server
	hits     atomic.Int64
	lastForm url.Values
}

func newTokenServer(t *testing.T, status int, response map[string]any, onRequest func()) *tokenServer {
	t.Helper()
	ts := &tokenServer{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.hits.Add(1)
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("unexpected Content-Type %q", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		ts.lastForm = r.PostForm
		if onRequest != nil {
			onRequest()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func rotatedResponse() map[string]any {
	return map[string]any{
		"access_token":  "new-access",
		"refresh_token": "new-refresh",
		"expires_in":    28800,
		"scope":         "user:profile user:inference",
		"account":       map[string]any{"email_address": "user@example.com"},
	}
}

func TestResolveEnvVarWins(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-env-token")
	m := Manager{StoreDir: t.TempDir(), CredentialsPath: filepath.Join(t.TempDir(), "missing.json")}
	tok, err := m.Resolve(context.Background())
	if err != nil || tok.AccessToken != "sk-ant-oat01-env-token" || tok.Source != "env" {
		t.Fatalf("env token should win: %#v err=%v", tok, err)
	}
}

func TestSaveSetupTokenResolvesWithoutRefresh(t *testing.T) {
	clearAuthEnv(t)
	// TokenURL deliberately unreachable: a setup-token must never trigger a
	// token-endpoint call.
	m := Manager{
		StoreDir:        t.TempDir(),
		CredentialsPath: filepath.Join(t.TempDir(), "missing.json"),
		TokenURL:        "http://127.0.0.1:0/unreachable",
	}
	if err := m.SaveSetupToken("  sk-ant-oat01-setup  "); err != nil {
		t.Fatalf("save: %v", err)
	}
	tok, err := m.Resolve(context.Background())
	if err != nil || tok.AccessToken != "sk-ant-oat01-setup" || tok.Source != "store" {
		t.Fatalf("setup-token should resolve from the store untouched: %#v err=%v", tok, err)
	}
}

func TestSaveSetupTokenRejectsNonToken(t *testing.T) {
	m := Manager{StoreDir: t.TempDir()}
	if err := m.SaveSetupToken("abc12#xyz89"); err == nil {
		t.Fatal("a pasted authorize code must be rejected, not stored")
	}
	if err := m.SaveSetupToken("sk-ant-api03-console-key"); err == nil {
		t.Fatal("a Console API key must be rejected, not stored")
	}
}

func TestResolveUsesValidCLIToken(t *testing.T) {
	clearAuthEnv(t)
	oauth := expiredOAuth()
	oauth["accessToken"] = "valid-access"
	oauth["expiresAt"] = time.Now().Add(time.Hour).UnixMilli()
	path := writeCLIFile(t, oauth)
	m := Manager{StoreDir: t.TempDir(), CredentialsPath: path}
	tok, err := m.Resolve(context.Background())
	if err != nil || tok.AccessToken != "valid-access" || tok.Source != "cli" {
		t.Fatalf("valid cli token should be used directly: %#v err=%v", tok, err)
	}
}

func TestResolveExpiredCLITokenIsReadOnlyAuthError(t *testing.T) {
	clearAuthEnv(t)
	path := writeCLIFile(t, expiredOAuth())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	ts := newTokenServer(t, http.StatusOK, rotatedResponse(), nil)
	m := Manager{StoreDir: t.TempDir(), CredentialsPath: path, TokenURL: ts.URL}

	_, err = m.Resolve(context.Background())
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expired CLI token must be AuthError, got %v", err)
	}
	if !strings.Contains(authErr.Reason, "setup-token") {
		t.Fatalf("guidance should point at the setup-token route, got %q", authErr.Reason)
	}
	if ts.hits.Load() != 0 {
		t.Fatalf("the CLI lineage must never be refreshed by LimitDock, got %d POSTs", ts.hits.Load())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read fixture: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("the CLI credentials file must stay untouched")
	}
}

func TestResolveRefreshRejectionIsAuthError(t *testing.T) {
	clearAuthEnv(t)
	ts := newTokenServer(t, http.StatusBadRequest, map[string]any{"error": "invalid_grant"}, nil)
	m := Manager{StoreDir: seedExpiredStore(t), CredentialsPath: missingCLIPath(t), TokenURL: ts.URL}
	_, err := m.Resolve(context.Background())
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("400 should be AuthError, got %v", err)
	}
}

func TestResolveRefreshServerErrorIsTransient(t *testing.T) {
	clearAuthEnv(t)
	ts := newTokenServer(t, http.StatusInternalServerError, map[string]any{}, nil)
	m := Manager{StoreDir: seedExpiredStore(t), CredentialsPath: missingCLIPath(t), TokenURL: ts.URL}
	_, err := m.Resolve(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var authErr *AuthError
	if errors.As(err, &authErr) {
		t.Fatalf("500 must not be AuthError: %v", err)
	}
}

func TestResolveExpiredWithoutRefreshTokenIsAuthError(t *testing.T) {
	clearAuthEnv(t)
	oauth := expiredOAuth()
	delete(oauth, "refreshToken")
	path := writeCLIFile(t, oauth)
	m := Manager{StoreDir: t.TempDir(), CredentialsPath: path}
	_, err := m.Resolve(context.Background())
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %v", err)
	}
}

func TestResolveNoSource(t *testing.T) {
	clearAuthEnv(t)
	m := Manager{StoreDir: t.TempDir(), CredentialsPath: filepath.Join(t.TempDir(), "missing.json")}
	_, err := m.Resolve(context.Background())
	if !errors.Is(err, ErrNoSource) {
		t.Fatalf("expected ErrNoSource, got %v", err)
	}
}

func TestResolveStoreRefreshPersistsRotatedPair(t *testing.T) {
	clearAuthEnv(t)
	storeDir := t.TempDir()
	store := credstore.New(storeDir)
	if err := store.Save("claude", storedToken{
		AccessToken:  "stored-old",
		RefreshToken: "stored-refresh",
		ExpiresAt:    time.Now().Add(-time.Minute).UnixMilli(),
		AccountEmail: "user@example.com",
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	ts := newTokenServer(t, http.StatusOK, rotatedResponse(), nil)
	m := Manager{StoreDir: storeDir, CredentialsPath: filepath.Join(t.TempDir(), "missing.json"), TokenURL: ts.URL}

	tok, err := m.Resolve(context.Background())
	if err != nil || tok.AccessToken != "new-access" || tok.Source != "store" {
		t.Fatalf("store refresh failed: %#v err=%v", tok, err)
	}
	var st storedToken
	if err := store.Load("claude", &st); err != nil {
		t.Fatalf("reload store: %v", err)
	}
	if st.AccessToken != "new-access" || st.RefreshToken != "new-refresh" {
		t.Fatalf("rotated pair not persisted: %#v", st)
	}

	// Second resolve uses the persisted (now valid) token without HTTP.
	tok2, err := m.Resolve(context.Background())
	if err != nil || tok2.AccessToken != "new-access" {
		t.Fatalf("second resolve: %#v err=%v", tok2, err)
	}
	if ts.hits.Load() != 1 {
		t.Fatalf("expected exactly one refresh call, got %d", ts.hits.Load())
	}
}

func TestExchangeCodeStateMismatchMakesNoHTTPCall(t *testing.T) {
	clearAuthEnv(t)
	ts := newTokenServer(t, http.StatusOK, rotatedResponse(), nil)
	m := Manager{StoreDir: t.TempDir(), TokenURL: ts.URL}
	ch := PKCEChallenge{Verifier: "v", Challenge: "c", State: "expected-state"}
	if _, err := m.ExchangeCode(context.Background(), "somecode#wrong-state", ch); err == nil {
		t.Fatal("expected state mismatch error")
	}
	if ts.hits.Load() != 0 {
		t.Fatalf("token endpoint must not be called on state mismatch, got %d hits", ts.hits.Load())
	}
}

func TestExchangeCodePersistsTokenAndResolves(t *testing.T) {
	clearAuthEnv(t)
	storeDir := t.TempDir()
	ts := newTokenServer(t, http.StatusOK, rotatedResponse(), nil)
	m := Manager{StoreDir: storeDir, CredentialsPath: filepath.Join(t.TempDir(), "missing.json"), TokenURL: ts.URL}
	ch, err := NewPKCEChallenge()
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	res, err := m.ExchangeCode(context.Background(), "authcode#"+ch.State, ch)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.AccountEmail != "user@example.com" {
		t.Fatalf("account email missing: %#v", res)
	}
	if got := ts.lastForm.Get("grant_type"); got != "authorization_code" {
		t.Fatalf("grant_type = %q", got)
	}
	if got := ts.lastForm.Get("code_verifier"); got != ch.Verifier {
		t.Fatalf("code_verifier = %q", got)
	}
	if got := ts.lastForm.Get("code"); got != "authcode" {
		t.Fatalf("code = %q", got)
	}
	if email, ok := m.StoredAccountEmail(); !ok || email != "user@example.com" {
		t.Fatalf("StoredAccountEmail = %q %v", email, ok)
	}
	tok, err := m.Resolve(context.Background())
	if err != nil || tok.Source != "store" || tok.AccessToken != "new-access" {
		t.Fatalf("resolve after connect: %#v err=%v", tok, err)
	}
}

func TestRefreshRateLimitCooldownStopsFollowupPosts(t *testing.T) {
	clearAuthEnv(t)
	ts := newTokenServer(t, http.StatusTooManyRequests, rateLimitBody(), nil)
	now := time.Now()
	m := Manager{StoreDir: seedExpiredStore(t), CredentialsPath: missingCLIPath(t), TokenURL: ts.URL, Now: func() time.Time { return now }}

	if _, err := m.Resolve(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limited, got %v", err)
	}
	if ts.hits.Load() != 1 {
		t.Fatalf("expected 1 POST, got %d", ts.hits.Load())
	}

	// While cooling down, resolves fail fast without any network call.
	for i := 0; i < 5; i++ {
		if _, err := m.Resolve(context.Background()); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("expected rate limited during cooldown, got %v", err)
		}
	}
	if ts.hits.Load() != 1 {
		t.Fatalf("cooldown must block POSTs, got %d", ts.hits.Load())
	}

	// Past the first cooldown rung (2m) one retry goes out; the second 429
	// escalates to the 5m rung, which still blocks 3m later.
	now = now.Add(2*time.Minute + time.Second)
	m.Resolve(context.Background())
	if ts.hits.Load() != 2 {
		t.Fatalf("expected retry after cooldown, got %d", ts.hits.Load())
	}
	now = now.Add(3 * time.Minute)
	m.Resolve(context.Background())
	if ts.hits.Load() != 2 {
		t.Fatalf("escalated cooldown must still block, got %d", ts.hits.Load())
	}
}

func TestResolveStoreRateLimitReportsStoreErrorNotCLI(t *testing.T) {
	clearAuthEnv(t)
	// Store refresh 429s and the CLI file is expired too: exactly one POST may
	// go out, and the transient store error (not the CLI file's AuthError)
	// must surface so backoff keeps working.
	path := writeCLIFile(t, expiredOAuth())
	ts := newTokenServer(t, http.StatusTooManyRequests, rateLimitBody(), nil)
	m := Manager{StoreDir: seedExpiredStore(t), CredentialsPath: path, TokenURL: ts.URL}

	_, err := m.Resolve(context.Background())
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limited, got %v", err)
	}
	if ts.hits.Load() != 1 {
		t.Fatalf("expected exactly one POST, got %d", ts.hits.Load())
	}
}

func TestResolveStoreRateLimitBridgesToValidCLIToken(t *testing.T) {
	clearAuthEnv(t)
	// Store refresh 429s but the CLI file still holds a valid token: quota
	// polling should ride the CLI token instead of failing.
	oauth := expiredOAuth()
	oauth["accessToken"] = "cli-valid"
	oauth["expiresAt"] = time.Now().Add(time.Hour).UnixMilli()
	path := writeCLIFile(t, oauth)
	ts := newTokenServer(t, http.StatusTooManyRequests, rateLimitBody(), nil)
	m := Manager{StoreDir: seedExpiredStore(t), CredentialsPath: path, TokenURL: ts.URL}

	tok, err := m.Resolve(context.Background())
	if err != nil || tok.AccessToken != "cli-valid" || tok.Source != "cli" {
		t.Fatalf("valid CLI token should bridge a store outage: %#v err=%v", tok, err)
	}
}

func TestRefreshBreakerTripsAfterConsecutive429s(t *testing.T) {
	clearAuthEnv(t)
	var hits atomic.Int64
	var succeed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if !succeed.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(rateLimitBody())
			return
		}
		json.NewEncoder(w).Encode(rotatedResponse())
	}))
	t.Cleanup(srv.Close)
	now := time.Now()
	m := Manager{StoreDir: seedExpiredStore(t), CredentialsPath: missingCLIPath(t), TokenURL: srv.URL, Now: func() time.Time { return now }}

	// Three POSTs ride the cooldown ladder (2m, 5m rungs between them).
	m.Resolve(context.Background())
	now = now.Add(2*time.Minute + time.Second)
	m.Resolve(context.Background())
	now = now.Add(5*time.Minute + time.Second)
	m.Resolve(context.Background())
	if hits.Load() != 3 {
		t.Fatalf("setup: expected 3 POSTs, got %d", hits.Load())
	}

	// Past every cooldown rung, the breaker still refuses a fourth attempt
	// and reports a needs-auth state pointing at the setup-token route.
	now = now.Add(time.Hour)
	_, err := m.Resolve(context.Background())
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("breaker must convert to AuthError, got %v", err)
	}
	if !strings.Contains(authErr.Reason, "setup-token") {
		t.Fatalf("breaker guidance should mention setup-token, got %q", authErr.Reason)
	}
	if hits.Load() != 3 {
		t.Fatalf("breaker must stop POSTs, got %d", hits.Load())
	}

	// A user-initiated exchange still goes through; success resets the
	// breaker and stores a fresh token.
	succeed.Store(true)
	ch := PKCEChallenge{Verifier: "v", Challenge: "c", State: "s"}
	if _, err := m.ExchangeCode(context.Background(), "code#s", ch); err != nil {
		t.Fatalf("exchange must bypass the breaker: %v", err)
	}
	tok, err := m.Resolve(context.Background())
	if err != nil || tok.AccessToken != "new-access" {
		t.Fatalf("resolve after exchange: %#v err=%v", tok, err)
	}
}

func TestExchangeCodeBypassesCooldownAndClearsIt(t *testing.T) {
	clearAuthEnv(t)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(rateLimitBody())
			return
		}
		json.NewEncoder(w).Encode(rotatedResponse())
	}))
	t.Cleanup(srv.Close)
	m := Manager{StoreDir: seedExpiredStore(t), CredentialsPath: missingCLIPath(t), TokenURL: srv.URL}

	// Arm the cooldown via a failed background refresh.
	if _, err := m.Resolve(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limited, got %v", err)
	}
	// A user-initiated exchange still goes out and succeeds.
	ch := PKCEChallenge{Verifier: "v", Challenge: "c", State: "s"}
	if _, err := m.ExchangeCode(context.Background(), "code#s", ch); err != nil {
		t.Fatalf("exchange during cooldown must go through: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("expected exchange POST, got %d hits", hits.Load())
	}
	// The success cleared the cooldown and stored a usable token.
	tok, err := m.Resolve(context.Background())
	if err != nil || tok.Source != "store" || tok.AccessToken != "new-access" {
		t.Fatalf("resolve after exchange: %#v err=%v", tok, err)
	}
}

func TestRateLimitHonorsRetryAfterHeader(t *testing.T) {
	clearAuthEnv(t)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(rateLimitBody())
	}))
	t.Cleanup(srv.Close)
	now := time.Now()
	m := Manager{StoreDir: seedExpiredStore(t), CredentialsPath: missingCLIPath(t), TokenURL: srv.URL, Now: func() time.Time { return now }}

	if _, err := m.Resolve(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limited, got %v", err)
	}
	// Retry-After (10m) outranks the 2m ladder rung.
	now = now.Add(5 * time.Minute)
	m.Resolve(context.Background())
	if hits.Load() != 1 {
		t.Fatalf("Retry-After must extend the cooldown, got %d hits", hits.Load())
	}
	now = now.Add(6 * time.Minute)
	m.Resolve(context.Background())
	if hits.Load() != 2 {
		t.Fatalf("expected retry after Retry-After elapsed, got %d hits", hits.Load())
	}
}

func TestServerErrorDoesNotResetCooldownStrikes(t *testing.T) {
	clearAuthEnv(t)
	var hits atomic.Int64
	// Sequence: 429 (strike 1) → 500 (must NOT reset strikes) → 429 (strike
	// 2 → 5m rung). If the 500 cleared the ladder, the final 429 would only
	// arm the 2m rung and the last resolve below would POST again.
	statuses := []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusTooManyRequests}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(hits.Add(1))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statuses[min(n, len(statuses))-1])
		json.NewEncoder(w).Encode(rateLimitBody())
	}))
	t.Cleanup(srv.Close)
	now := time.Now()
	m := Manager{StoreDir: seedExpiredStore(t), CredentialsPath: missingCLIPath(t), TokenURL: srv.URL, Now: func() time.Time { return now }}

	m.Resolve(context.Background()) // 429 → strike 1, 2m cooldown
	now = now.Add(2*time.Minute + time.Second)
	m.Resolve(context.Background()) // 500 → transient error, strikes preserved
	now = now.Add(time.Second)
	m.Resolve(context.Background()) // 429 → strike 2, 5m rung
	if hits.Load() != 3 {
		t.Fatalf("setup: expected 3 POSTs, got %d", hits.Load())
	}
	// 3 minutes later we are inside the 5m rung; no POST may go out.
	now = now.Add(3 * time.Minute)
	m.Resolve(context.Background())
	if hits.Load() != 3 {
		t.Fatalf("500 must not reset the strike ladder: got %d POSTs", hits.Load())
	}
}

func TestRefreshRejectionSurfacesNestedErrorDetail(t *testing.T) {
	clearAuthEnv(t)
	ts := newTokenServer(t, http.StatusBadRequest, map[string]any{
		"error": map[string]any{"type": "invalid_request_error", "message": "refresh token revoked"},
	}, nil)
	m := Manager{StoreDir: seedExpiredStore(t), CredentialsPath: missingCLIPath(t), TokenURL: ts.URL}

	_, err := m.Resolve(context.Background())
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %v", err)
	}
	if !strings.Contains(authErr.Reason, "invalid_request_error") || !strings.Contains(authErr.Reason, "refresh token revoked") {
		t.Fatalf("nested error detail missing from %q", authErr.Reason)
	}
}

func TestParsePastedCodeVariants(t *testing.T) {
	if code, state, err := ParsePastedCode(" abc#xyz "); err != nil || code != "abc" || state != "xyz" {
		t.Fatalf("abc#xyz: %q %q %v", code, state, err)
	}
	if code, state, err := ParsePastedCode("abconly"); err != nil || code != "abconly" || state != "" {
		t.Fatalf("code only: %q %q %v", code, state, err)
	}
	if _, _, err := ParsePastedCode("#xyz"); err == nil {
		t.Fatal("missing code should error")
	}
	if _, _, err := ParsePastedCode("   "); err == nil {
		t.Fatal("empty paste should error")
	}
}

func TestBuildAuthorizeURLParams(t *testing.T) {
	m := Manager{}
	ch, err := NewPKCEChallenge()
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	u, err := url.Parse(m.BuildAuthorizeURL(ch))
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	q := u.Query()
	if u.Host != "claude.ai" || u.Path != "/oauth/authorize" {
		t.Fatalf("unexpected authorize endpoint: %s", u)
	}
	for key, want := range map[string]string{
		"code":                  "true",
		"client_id":             ClaudeOAuthClientID,
		"response_type":         "code",
		"redirect_uri":          redirectURI,
		"scope":                 oauthScopes,
		"code_challenge":        ch.Challenge,
		"code_challenge_method": "S256",
		"state":                 ch.State,
	} {
		if got := q.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}
