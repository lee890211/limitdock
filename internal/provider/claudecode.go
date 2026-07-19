package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"limitdock/internal/claudeauth"
	"limitdock/internal/readmodel"
)

const (
	claudeCodeAPIBase    = "https://api.anthropic.com"
	claudeCodeUsagePath  = "/api/oauth/usage"
	claudeCodeBetaHeader = "oauth-2025-04-20"
	// claudeCodeUserAgent must stay in sync with the token-endpoint client;
	// both mirror the official CLI (see claudeauth.UserAgent).
	claudeCodeUserAgent = claudeauth.UserAgent
	claudeCodeTimeout   = 10 * time.Second
	claudeSnapshotKey   = "claude-code"
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

	usage, err := r.fetchUsage(ctx, tok.AccessToken)
	if err != nil {
		var httpErr *HTTPStatusError
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

func (r ClaudeCodeReader) fetchUsage(ctx context.Context, token string) (*claudeUsageResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, claudeCodeTimeout)
	defer cancel()

	base := r.BaseURL
	if base == "" {
		base = claudeCodeAPIBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+claudeCodeUsagePath, nil)
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
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}
	var usage claudeUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&usage); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &usage, nil
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
