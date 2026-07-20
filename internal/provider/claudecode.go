package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"limitdock/internal/claudeauth"
	"limitdock/internal/readmodel"
)

// claudeUsageCooldown remembers, per API base, the Retry-After window from a
// usage-API 429 so subsequent polls skip straight to the header probe instead
// of re-hitting an endpoint that already told us to wait.
var claudeUsageCooldown = struct {
	sync.Mutex
	until map[string]time.Time
}{until: map[string]time.Time{}}

func claudeUsageCoolingDown(base string, now time.Time) bool {
	claudeUsageCooldown.Lock()
	defer claudeUsageCooldown.Unlock()
	return now.Before(claudeUsageCooldown.until[base])
}

func markClaudeUsageCooldown(base string, retryAfter time.Duration, now time.Time) {
	if retryAfter <= 0 {
		// Measured live 2026-07-19: the endpoint locks for 5 minutes.
		retryAfter = 5 * time.Minute
	}
	claudeUsageCooldown.Lock()
	claudeUsageCooldown.until[base] = now.Add(retryAfter)
	claudeUsageCooldown.Unlock()
}

const (
	claudeCodeAPIBase    = "https://api.anthropic.com"
	claudeCodeUsagePath  = "/api/oauth/usage"
	claudeCodeBetaHeader = "oauth-2025-04-20"
	// claudeCodeUserAgent must stay in sync with the token-endpoint client;
	// both mirror the official CLI (see claudeauth.UserAgent).
	claudeCodeUserAgent = claudeauth.UserAgent
	claudeCodeTimeout   = 10 * time.Second
	claudeSnapshotKey   = "claude-code"

	// Header-probe fallback: the usage API budget is tiny (~4-5 calls per
	// 5-minute window, measured live 2026-07-19) and shared with the Claude
	// Code CLI itself, while /v1/messages carries anthropic-ratelimit-unified-*
	// headers on both 2xx and rate_limit_error 429 responses (exhausted
	// budget still reports utilization, measured live 2026-07-20). A 1-token
	// probe keeps 5h/7d quota fresh when the usage API is rate limited, and
	// is the only quota source for user:inference-only tokens (claude
	// setup-token), which the usage API rejects with 403.
	claudeMessagesPath  = "/v1/messages"
	claudeAPIVersion    = "2023-06-01"
	claudeProbeModel    = "claude-haiku-4-5-20251001"
	claudeProbeSystem   = "You are Claude Code, Anthropic's official CLI for Claude."
	claudeHeader5hUtil  = "anthropic-ratelimit-unified-5h-utilization"
	claudeHeader5hReset = "anthropic-ratelimit-unified-5h-reset"
	claudeHeader7dUtil  = "anthropic-ratelimit-unified-7d-utilization"
	claudeHeader7dReset = "anthropic-ratelimit-unified-7d-reset"
)

// ClaudeCodeReader fetches quota directly from the Anthropic OAuth usage API.
// Tokens come from CLAUDE_CODE_OAUTH_TOKEN, LimitDock's own store (Connect
// flow), or the Claude Code CLI credentials file; expired tokens are refreshed
// (and the rotated pair persisted) by claudeauth.
type ClaudeCodeReader struct {
	Log             Logger
	CredentialsPath string // overrides CLI credentials auto-discovery; empty = auto
	CredStoreDir    string // LimitDock-owned token store directory; empty = disabled
	TokenURL        string // OAuth token endpoint override; empty = production
	BaseURL         string // overrides usage API base URL; empty = production
}

func (r ClaudeCodeReader) Name() string { return "claude-code" }

func (r ClaudeCodeReader) manager() claudeauth.Manager {
	return claudeauth.Manager{
		Log:             r.Log,
		CredentialsPath: r.CredentialsPath,
		StoreDir:        r.CredStoreDir,
		TokenURL:        r.TokenURL,
	}
}

type claudeUsageResponse struct {
	FiveHour       *claudeUsageBucket `json:"five_hour"`
	SevenDay       *claudeUsageBucket `json:"seven_day"`
	SevenDaySonnet *claudeUsageBucket `json:"seven_day_sonnet"`
	SevenDayOpus   *claudeUsageBucket `json:"seven_day_opus"`
	SevenDayCowork *claudeUsageBucket `json:"seven_day_cowork"`
}

type claudeUsageBucket struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    *string `json:"resets_at"`
}

func (r ClaudeCodeReader) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	tok, err := r.manager().Resolve(ctx)
	if err != nil {
		if errors.Is(err, claudeauth.ErrNoSource) {
			if r.Log != nil {
				r.Log.Printf("Claude Code reader skipped: %v", err)
			}
			return emptyReadModel(), nil
		}
		var authErr *claudeauth.AuthError
		if errors.As(err, &authErr) {
			if r.Log != nil {
				r.Log.Printf("Claude Code needs sign-in: %v", err)
			}
			return statusReadModel(claudeSnapshotKey, "claude_code", claudeSnapshotKey, readmodel.StatusNeedsAuth, authErr.Reason), nil
		}
		return nil, fmt.Errorf("claude auth: %w", err)
	}

	var usage *claudeUsageResponse
	if claudeUsageCoolingDown(r.apiBase(), time.Now()) {
		err = &HTTPStatusError{StatusCode: http.StatusTooManyRequests, Status: "429 usage API cooling down (Retry-After)"}
	} else {
		usage, err = r.fetchUsage(ctx, tok.AccessToken)
	}
	if err != nil {
		var httpErr *HTTPStatusError
		if errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode == http.StatusForbidden) {
			// 429: ride out the usage API's 5-minute penalty window on the
			// header probe instead of going stale. 403: inference-only
			// token — headers are the only available quota source.
			if snap := r.headerProbeSnapshot(ctx, tok); snap != nil {
				return &readmodel.ReadModel{
					Snapshots: map[string]*readmodel.Snapshot{claudeSnapshotKey: snap},
				}, nil
			}
		}
		if errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden) {
			msg := fmt.Sprintf("usage API rejected the %s token (%s)", tok.Source, httpErr.Error())
			if httpErr.StatusCode == http.StatusForbidden {
				msg += "; the token may lack the user:profile scope — use Connect Claude to sign in"
			}
			if r.Log != nil {
				r.Log.Printf("Claude Code needs sign-in: %s", msg)
			}
			return statusReadModel(claudeSnapshotKey, "claude_code", claudeSnapshotKey, readmodel.StatusNeedsAuth, msg), nil
		}
		return nil, fmt.Errorf("claude usage API: %w", err)
	}

	snap := claudeUsageToSnapshot(usage)
	if snap == nil || len(snap.Metrics) == 0 {
		return emptyReadModel(), nil
	}
	if tok.AccountEmail != "" {
		snap.AccountID = tok.AccountEmail
	}
	return &readmodel.ReadModel{
		Snapshots: map[string]*readmodel.Snapshot{claudeSnapshotKey: snap},
	}, nil
}

func (r ClaudeCodeReader) apiBase() string {
	if r.BaseURL != "" {
		return r.BaseURL
	}
	return claudeCodeAPIBase
}

func (r ClaudeCodeReader) fetchUsage(ctx context.Context, token string) (*claudeUsageResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, claudeCodeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.apiBase()+claudeCodeUsagePath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-beta", claudeCodeBetaHeader)
	req.Header.Set("User-Agent", claudeCodeUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusTooManyRequests {
			markClaudeUsageCooldown(r.apiBase(), claudeauth.ParseRetryAfter(resp.Header.Get("Retry-After")), time.Now())
		}
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}
	var usage claudeUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&usage); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &usage, nil
}

// headerProbeSnapshot fetches quota via the unified rate-limit headers on a
// minimal inference call; nil (with a log line) on any failure so callers can
// fall through to their normal error handling.
func (r ClaudeCodeReader) headerProbeSnapshot(ctx context.Context, tok claudeauth.Token) *readmodel.Snapshot {
	snap, err := r.fetchHeaderQuota(ctx, tok.AccessToken)
	if err != nil {
		if r.Log != nil {
			r.Log.Printf("Claude header quota probe failed: %v", err)
		}
		return nil
	}
	if r.Log != nil {
		r.Log.Printf("Claude quota served from rate-limit headers (usage API unavailable).")
	}
	if tok.AccountEmail != "" {
		snap.AccountID = tok.AccountEmail
	}
	return snap
}

func (r ClaudeCodeReader) fetchHeaderQuota(ctx context.Context, token string) (*readmodel.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, claudeCodeTimeout)
	defer cancel()

	body := fmt.Sprintf(`{"model":%q,"max_tokens":1,"system":%q,"messages":[{"role":"user","content":"."}]}`,
		claudeProbeModel, claudeProbeSystem)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.apiBase()+claudeMessagesPath, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", claudeAPIVersion)
	req.Header.Set("anthropic-beta", claudeCodeBetaHeader)
	req.Header.Set("User-Agent", claudeCodeUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))

	// Unified rate-limit headers arrive on 2xx and on rate_limit_error (HTTP
	// 429) when the 5h/7d budget is exhausted — measured live 2026-07-20.
	// Prefer headers whenever present so an exhausted window still surfaces
	// as 0% remaining; only fall through to the status code when they are
	// absent (auth rejection, edge challenge pages, etc.).
	snap, hdrErr := claudeHeadersToSnapshot(resp.Header)
	if hdrErr == nil {
		return snap, nil
	}
	if resp.StatusCode >= 300 {
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}
	return nil, hdrErr
}

// claudeHeadersToSnapshot maps unified rate-limit headers (utilization as a
// 0-1 fraction, resets as epoch seconds) onto the same metric keys the usage
// API produces. Callers may pass headers from any status; missing headers
// yield an error.
func claudeHeadersToSnapshot(h http.Header) (*readmodel.Snapshot, error) {
	metrics := map[string]readmodel.Metric{}
	resets := map[string]any{}
	add := func(key, window, utilHeader, resetHeader string) {
		raw := strings.TrimSpace(h.Get(utilHeader))
		if raw == "" {
			return
		}
		frac, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return
		}
		used := frac * 100
		metrics[key] = readmodel.Metric{Used: &used, Unit: "%", Window: window}
		if rs := strings.TrimSpace(h.Get(resetHeader)); rs != "" {
			if epoch, err := strconv.ParseInt(rs, 10, 64); err == nil && epoch > 0 {
				resets[key+"_reset"] = time.Unix(epoch, 0).UTC().Format(time.RFC3339)
			}
		}
	}
	add("usage_five_hour", "5h", claudeHeader5hUtil, claudeHeader5hReset)
	add("usage_seven_day", "7d", claudeHeader7dUtil, claudeHeader7dReset)
	if len(metrics) == 0 {
		return nil, fmt.Errorf("response carried no unified rate-limit headers")
	}
	return &readmodel.Snapshot{
		ProviderID: "claude_code",
		AccountID:  claudeSnapshotKey,
		Status:     "ok",
		Metrics:    metrics,
		Resets:     resets,
		Raw:        map[string]any{"source": "claude-code-headers"},
	}, nil
}

func claudeUsageToSnapshot(usage *claudeUsageResponse) *readmodel.Snapshot {
	if usage == nil {
		return nil
	}
	metrics := map[string]readmodel.Metric{}
	resets := map[string]any{}

	addBucket := func(key, window string, bucket *claudeUsageBucket) {
		if bucket == nil {
			return
		}
		used := bucket.Utilization
		metrics[key] = readmodel.Metric{
			Used:   &used,
			Unit:   "%",
			Window: window,
		}
		if bucket.ResetsAt != nil && strings.TrimSpace(*bucket.ResetsAt) != "" {
			resets[key+"_reset"] = *bucket.ResetsAt
		}
	}

	addBucket("usage_five_hour", "5h", usage.FiveHour)
	addBucket("usage_seven_day", "7d", usage.SevenDay)
	addBucket("usage_seven_day_sonnet", "7d", usage.SevenDaySonnet)
	addBucket("usage_seven_day_opus", "7d", usage.SevenDayOpus)
	addBucket("usage_seven_day_cowork", "7d", usage.SevenDayCowork)

	if len(metrics) == 0 {
		return nil
	}
	return &readmodel.Snapshot{
		ProviderID: "claude_code",
		AccountID:  claudeSnapshotKey,
		Status:     "ok",
		Metrics:    metrics,
		Resets:     resets,
		Raw:        map[string]any{"source": "claude-code-api"},
	}
}
