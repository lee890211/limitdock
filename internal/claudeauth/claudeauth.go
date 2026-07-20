// Package claudeauth resolves a usable Claude OAuth access token from, in
// priority order: the CLAUDE_CODE_OAUTH_TOKEN env var, LimitDock's own
// DPAPI-encrypted token store (setup-token or Connect flow), or the Claude
// Code CLI's credentials file. Only store tokens are refreshed (Anthropic
// rotates refresh tokens on use, so a refresh counts as successful once the
// rotated pair is persisted); the CLI file is strictly read-only — refreshing
// it raced the CLI's own writer over one rotating lineage, and background
// refresh attempts with a dead credential are what escalate the token
// endpoint's per-client rate limiting (observed 2026-07-19/20).
package claudeauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"limitdock/internal/credstore"
)

const (
	// ClaudeOAuthClientID is the public OAuth client id of the official
	// Claude Code CLI (not a secret); LimitDock uses the same client so
	// user-approved tokens carry the user:profile scope the usage API needs.
	ClaudeOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	defaultTokenURL = "https://platform.claude.com/v1/oauth/token"
	authorizeURL    = "https://claude.ai/oauth/authorize"
	redirectURI     = "https://platform.claude.com/oauth/code/callback"
	oauthScopes     = "user:profile user:inference"

	// UserAgent mirrors the Claude Code CLI; Anthropic rate-limits unknown
	// clients far more aggressively. Shared with the usage API reader.
	UserAgent = "claude-code/2.1.0"

	storeTokenName = "claude"
	requestTimeout = 15 * time.Second
	expirySkew     = 30 * time.Second
)

// cooldownLadder spaces out token-endpoint retries after consecutive 429s.
// The endpoint rate-limits per IP with no Retry-After header, and a saturated
// limit also blocks the user-initiated Connect code exchange, so background
// refreshes must stand down long enough for the window to clear. Capped at
// 15m (matching the provider cache backoff ceiling) so recovery is detected
// within a practical delay.
var cooldownLadder = []time.Duration{2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute}

// maxRefreshStrikes is the circuit breaker: after this many consecutive 429s
// the background refresh stops entirely instead of retrying on the ladder
// forever — sustained retries with a rejected credential are what taught the
// endpoint to throttle this client in the first place. User-initiated
// ExchangeCode bypasses the breaker, and any non-429/non-5xx response (e.g. a
// successful exchange) resets it.
const maxRefreshStrikes = 3

// tokenEndpointGate holds per-endpoint 429 cooldown state. It is process-wide
// because Manager values are constructed fresh for every call; keying by URL
// keeps production and test endpoints independent.
type tokenEndpointGate struct {
	mu      sync.Mutex
	entries map[string]*gateEntry
}

type gateEntry struct {
	until   time.Time
	strikes int
}

var gate = &tokenEndpointGate{entries: map[string]*gateEntry{}}

func (g *tokenEndpointGate) blockedUntil(url string, now time.Time) (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.entries[url]
	if e == nil || !now.Before(e.until) {
		return time.Time{}, false
	}
	return e.until, true
}

func (g *tokenEndpointGate) recordRateLimit(url string, now time.Time, retryAfter time.Duration) time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.entries[url]
	if e == nil {
		e = &gateEntry{}
		g.entries[url] = e
	}
	d := cooldownLadder[min(e.strikes, len(cooldownLadder)-1)]
	if retryAfter > d {
		d = retryAfter
	}
	e.strikes++
	e.until = now.Add(d)
	return e.until
}

func (g *tokenEndpointGate) clear(url string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.entries, url)
}

func (g *tokenEndpointGate) strikeCount(url string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.entries[url]; e != nil {
		return e.strikes
	}
	return 0
}

// ErrNoSource means no credential source exists at all; callers should stay
// silent (provider not configured) rather than surface an auth problem.
var ErrNoSource = errors.New("claudeauth: no credential source found")

// ErrRateLimited wraps a transient 429 from the OAuth token endpoint so the
// provider cache layer can apply its backoff ladder.
var ErrRateLimited = errors.New("claudeauth: token endpoint rate limited")

// AuthError marks failures that need user re-authentication (Connect or
// claude login) rather than a retry.
type AuthError struct {
	Reason string
}

func (e *AuthError) Error() string { return e.Reason }

func authErrorf(format string, args ...any) error {
	return &AuthError{Reason: fmt.Sprintf(format, args...)}
}

type Logger interface {
	Printf(format string, args ...any)
}

type Token struct {
	AccessToken  string
	ExpiresAt    time.Time
	Source       string // "env", "store", or "cli"
	AccountEmail string
}

type Manager struct {
	Log             Logger
	CredentialsPath string // Claude Code CLI credentials file override; "" = auto-discover
	StoreDir        string // LimitDock-owned credential store directory
	TokenURL        string // OAuth token endpoint override; "" = production
	HTTPClient      *http.Client
	Now             func() time.Time
}

type storedToken struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"` // unix milliseconds
	AccountEmail string `json:"accountEmail,omitempty"`
	Scopes       string `json:"scopes,omitempty"`
}

// Resolve returns a usable access token, refreshing and persisting rotated
// credentials when needed.
func (m Manager) Resolve(ctx context.Context) (Token, error) {
	if tok := strings.TrimSpace(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")); tok != "" {
		return Token{AccessToken: tok, Source: "env"}, nil
	}

	tok, storeErr := m.resolveStore(ctx)
	if storeErr == nil {
		return tok, nil
	}
	if !errors.Is(storeErr, credstore.ErrNotFound) {
		// The LimitDock store existed but is unusable; a CLI file with a
		// still-valid token may bridge the gap. When it cannot, the store
		// failure is the one to report — the store is the primary source and
		// its error carries the actionable guidance.
		m.logf("Claude token store unusable: %v", storeErr)
		if tok, cliErr := m.resolveCLIFile(ctx); cliErr == nil {
			return tok, nil
		}
		return Token{}, storeErr
	}
	return m.resolveCLIFile(ctx)
}

// SaveSetupToken stores a long-lived token minted by `claude setup-token` as
// the LimitDock-owned credential. Such tokens carry no refresh token and no
// known expiry (ExpiresAt 0 is treated as non-expiring by resolveStore), so
// the reader never touches the rate-limited token endpoint for them; quota
// comes via the header probe because the usage API rejects the token's
// user:inference-only scope with 403.
func (m Manager) SaveSetupToken(token string) error {
	token = strings.TrimSpace(token)
	// sk-ant-oat is the setup-token (OAuth access token) shape specifically;
	// a plain sk-ant- prefix would also accept Console API keys
	// (sk-ant-api...), which the OAuth usage endpoints reject.
	if !strings.HasPrefix(token, "sk-ant-oat") {
		return fmt.Errorf("that does not look like a Claude setup-token (expected it to start with sk-ant-oat)")
	}
	return m.store().Save(storeTokenName, storedToken{AccessToken: token})
}

// StoredAccountEmail reports the account email of the LimitDock-owned token,
// if one is connected.
func (m Manager) StoredAccountEmail() (string, bool) {
	var st storedToken
	if err := m.store().Load(storeTokenName, &st); err != nil {
		return "", false
	}
	return st.AccountEmail, st.AccessToken != "" || st.RefreshToken != ""
}

func (m Manager) resolveStore(ctx context.Context) (Token, error) {
	if strings.TrimSpace(m.StoreDir) == "" {
		return Token{}, credstore.ErrNotFound
	}
	var st storedToken
	if err := m.store().Load(storeTokenName, &st); err != nil {
		if errors.Is(err, credstore.ErrNotFound) {
			return Token{}, err
		}
		return Token{}, authErrorf("stored Claude token unreadable: %v; reconnect Claude", err)
	}
	if strings.TrimSpace(st.AccessToken) == "" && strings.TrimSpace(st.RefreshToken) == "" {
		return Token{}, credstore.ErrNotFound
	}
	expiresAt := time.UnixMilli(st.ExpiresAt)
	if st.AccessToken != "" && (st.ExpiresAt <= 0 || m.now().Add(expirySkew).Before(expiresAt)) {
		return Token{AccessToken: st.AccessToken, ExpiresAt: expiresAt, Source: "store", AccountEmail: st.AccountEmail}, nil
	}
	if strings.TrimSpace(st.RefreshToken) == "" {
		return Token{}, authErrorf("LimitDock token expired at %s and no refresh token is stored; reconnect Claude", expiresAt.Format(time.RFC3339))
	}
	res, err := m.refresh(ctx, st.RefreshToken)
	if err != nil {
		return Token{}, err
	}
	st.AccessToken = res.AccessToken
	if res.RefreshToken != "" {
		st.RefreshToken = res.RefreshToken
	}
	st.ExpiresAt = m.now().Add(time.Duration(res.ExpiresIn) * time.Second).UnixMilli()
	if res.Account.EmailAddress != "" {
		st.AccountEmail = res.Account.EmailAddress
	}
	if err := m.store().Save(storeTokenName, st); err != nil {
		return Token{}, authErrorf("refreshed Claude token but failed to persist it: %v; reconnect Claude", err)
	}
	return Token{AccessToken: st.AccessToken, ExpiresAt: time.UnixMilli(st.ExpiresAt), Source: "store", AccountEmail: st.AccountEmail}, nil
}

// resolveCLIFile serves the Claude Code CLI's own token strictly read-only:
// it is used only while still valid, never refreshed and never written back.
// LimitDock refreshing this file rotated a refresh-token lineage the CLI also
// rotates (a reuse-detection lockout risk), and retrying the refresh with an
// expired credential is what escalates the token endpoint's rate limiting.
// The CLI rewrites the file whenever the user actually runs claude.
func (m Manager) resolveCLIFile(ctx context.Context) (Token, error) {
	path := m.credentialsPath()
	if path == "" || !fileExists(path) {
		return Token{}, ErrNoSource
	}
	creds, err := readCLICredentials(path)
	if err != nil {
		return Token{}, err
	}
	expiresAt := time.UnixMilli(creds.expiresAt)
	if creds.accessToken != "" && (creds.expiresAt <= 0 || m.now().Add(expirySkew).Before(expiresAt)) {
		return Token{AccessToken: creds.accessToken, ExpiresAt: expiresAt, Source: "cli"}, nil
	}
	if creds.accessToken == "" {
		return Token{}, authErrorf("Claude Code CLI credentials at %s carry no access token; register a setup-token in Settings, or use Connect Claude", path)
	}
	return Token{}, authErrorf("Claude Code CLI token expired at %s; run claude in a terminal (it refreshes on use), register a setup-token in Settings, or use Connect Claude", expiresAt.Local().Format("2006-01-02 15:04"))
}

func (m Manager) credentialsPath() string {
	if m.CredentialsPath != "" {
		return m.CredentialsPath
	}
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		p := filepath.Join(dir, ".credentials.json")
		if fileExists(p) {
			return p
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	p := filepath.Join(home, ".claude", ".credentials.json")
	if fileExists(p) {
		return p
	}
	return ""
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Account      struct {
		EmailAddress string `json:"email_address"`
	} `json:"account"`
	// The endpoint emits both OAuth-style ("error": "invalid_grant",
	// "error_description": ...) and Anthropic-style ("error": {"type": ...,
	// "message": ...}) failure bodies, so "error" must accept either shape.
	ErrorRaw  json.RawMessage `json:"error"`
	ErrorDesc string          `json:"error_description"`
}

// errorDetail flattens whichever error body shape the endpoint produced.
func (r *tokenResponse) errorDetail() string {
	parts := []string{}
	if len(r.ErrorRaw) > 0 {
		var code string
		if json.Unmarshal(r.ErrorRaw, &code) == nil {
			parts = append(parts, code)
		} else {
			var obj struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}
			if json.Unmarshal(r.ErrorRaw, &obj) == nil {
				parts = append(parts, obj.Type, obj.Message)
			}
		}
	}
	parts = append(parts, r.ErrorDesc)
	out := []string{}
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

func (m Manager) refresh(ctx context.Context, refreshToken string) (*tokenResponse, error) {
	// Circuit breaker: after maxRefreshStrikes consecutive 429s this is no
	// longer a transient blip — stop retrying (retry loops with a rejected
	// credential are what get this client throttled) and surface a needs-auth
	// state that points at the refresh-free setup-token route instead.
	if strikes := gate.strikeCount(m.tokenURL()); strikes >= maxRefreshStrikes {
		return nil, authErrorf("Anthropic keeps rate limiting sign-in refresh (HTTP 429 x%d); paused further attempts to avoid a lockout — register a setup-token in Settings, or use Connect Claude", strikes)
	}
	// Background refreshes must not touch a rate-limited endpoint: repeated
	// POSTs keep the per-IP limit saturated, which also blocks the
	// user-initiated Connect code exchange on the same endpoint.
	if until, blocked := gate.blockedUntil(m.tokenURL(), m.now()); blocked {
		return nil, fmt.Errorf("token endpoint cooling down until %s after HTTP 429: %w", until.Local().Format("15:04:05"), ErrRateLimited)
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {ClaudeOAuthClientID},
	}
	res, err := m.postTokenForm(ctx, form)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// postTokenForm POSTs to the OAuth token endpoint. 4xx responses become
// AuthError (re-authentication required); network errors and 5xx/429 are
// returned as transient errors.
func (m Manager) postTokenForm(ctx context.Context, form url.Values) (*tokenResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := m.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("token endpoint: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		until := gate.recordRateLimit(m.tokenURL(), m.now(), ParseRetryAfter(resp.Header.Get("Retry-After")))
		hint := ""
		if ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); strings.Contains(ct, "html") {
			// A 429 with an HTML body is an edge/bot challenge, not the
			// endpoint's usual JSON throttle response — keep that visible.
			hint = "; the response was an HTML page, Anthropic may be challenging this client"
		}
		m.logf("Claude token endpoint rate limited (HTTP 429%s); pausing token refresh until %s", hint, until.Local().Format("15:04:05"))
		return nil, fmt.Errorf("token endpoint: HTTP %s%s: %w", resp.Status, hint, ErrRateLimited)
	}
	// A non-429, non-5xx response proves the rate limit is not blocking this
	// client. A 5xx says nothing about the limiter, so it must not reset the
	// strike ladder (alternating 429/500 would otherwise never escalate).
	if resp.StatusCode < 500 {
		gate.clear(m.tokenURL())
	}
	if ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); strings.Contains(ct, "html") {
		return nil, fmt.Errorf("token endpoint returned an unexpected page (HTTP %d); Anthropic may be challenging this client, try again later", resp.StatusCode)
	}
	var res tokenResponse
	if len(body) > 0 {
		if err := json.Unmarshal(body, &res); err != nil && resp.StatusCode < 300 {
			return nil, fmt.Errorf("token endpoint: decoding response: %w", err)
		}
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("token endpoint: HTTP %s", resp.Status)
	}
	if resp.StatusCode >= 400 {
		detail := res.errorDetail()
		if detail == "" {
			detail = "HTTP " + resp.Status
		}
		return nil, authErrorf("Claude sign-in is no longer valid (%s); reconnect Claude or run claude login", detail)
	}
	if strings.TrimSpace(res.AccessToken) == "" {
		return nil, fmt.Errorf("token endpoint: response missing access_token")
	}
	return &res, nil
}

// ParseRetryAfter reads a seconds-form Retry-After header; the token endpoint
// currently sends none, so 0 (use the cooldown ladder) is the common case.
// Exported for the usage-API reader, which receives Retry-After on 429.
func ParseRetryAfter(header string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

func (m Manager) store() credstore.Store { return credstore.New(m.StoreDir) }

func (m Manager) tokenURL() string {
	if m.TokenURL != "" {
		return m.TokenURL
	}
	return defaultTokenURL
}

func (m Manager) httpClient() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return http.DefaultClient
}

func (m Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m Manager) logf(format string, args ...any) {
	if m.Log != nil {
		m.Log.Printf(format, args...)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
