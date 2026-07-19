// Package claudeauth resolves a usable Claude OAuth access token from, in
// priority order: the CLAUDE_CODE_OAUTH_TOKEN env var, LimitDock's own
// DPAPI-encrypted token store, or the Claude Code CLI's credentials file.
// Expired tokens are refreshed in place; because Anthropic rotates refresh
// tokens on use, a refresh is only considered successful once the rotated
// pair has been persisted back to its source.
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
// refreshes must stand down long enough for the window to clear.
var cooldownLadder = []time.Duration{2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute}

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
		// The LimitDock store existed but is unusable; a healthy CLI file may
		// still serve, so keep going and only report the store failure when
		// nothing else exists.
		m.logf("Claude token store unusable: %v", storeErr)
		tok, cliErr := m.resolveCLIFile(ctx)
		if cliErr == nil {
			return tok, nil
		}
		if errors.Is(cliErr, ErrNoSource) {
			return Token{}, storeErr
		}
		return Token{}, cliErr
	}
	return m.resolveCLIFile(ctx)
}

// Disconnect removes the LimitDock-owned token.
func (m Manager) Disconnect() error {
	return m.store().Delete(storeTokenName)
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
	if strings.TrimSpace(creds.refreshToken) == "" {
		return Token{}, authErrorf("Claude Code access token expired at %s and no refresh token is available; run claude login or connect Claude", expiresAt.Format(time.RFC3339))
	}
	res, err := m.refresh(ctx, creds.refreshToken)
	if err != nil {
		return Token{}, err
	}
	// Anthropic rotates refresh tokens: if the rotated pair cannot be written
	// back for the CLI to keep using, the refresh must count as a failure.
	if err := writeBackCLICredentials(path, creds.raw, res, m.now()); err != nil {
		return Token{}, authErrorf("refreshed Claude token but failed to update %s: %v; run claude login if the CLI stops authenticating", path, err)
	}
	return Token{
		AccessToken: res.AccessToken,
		ExpiresAt:   m.now().Add(time.Duration(res.ExpiresIn) * time.Second),
		Source:      "cli",
	}, nil
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
		until := gate.recordRateLimit(m.tokenURL(), m.now(), parseRetryAfter(resp.Header.Get("Retry-After")))
		m.logf("Claude token endpoint rate limited (HTTP 429); pausing token refresh until %s", until.Local().Format("15:04:05"))
		return nil, fmt.Errorf("token endpoint: HTTP %s: %w", resp.Status, ErrRateLimited)
	}
	// Any non-429 response proves the rate limit is not blocking this client.
	gate.clear(m.tokenURL())
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

// parseRetryAfter reads a seconds-form Retry-After header; the endpoint
// currently sends none, so 0 (use the cooldown ladder) is the common case.
func parseRetryAfter(header string) time.Duration {
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
