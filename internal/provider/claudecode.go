package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"limitdock/internal/readmodel"
)

const (
	claudeCodeAPIBase  = "https://api.anthropic.com"
	claudeCodeUsagePath = "/api/oauth/usage"
	claudeCodeBetaHeader = "oauth-2025-04-20"
	claudeCodeUserAgent  = "claude-code/2.1.0"
	claudeCodeTimeout    = 10 * time.Second
)

// ClaudeCodeReader fetches quota directly from the Anthropic OAuth usage API.
// Credentials are auto-discovered from CLAUDE_CODE_OAUTH_TOKEN env var,
// CLAUDE_CONFIG_DIR env var, or ~/.claude/.credentials.json.
type ClaudeCodeReader struct {
	Log             Logger
	CredentialsPath string // overrides auto-discovery; empty = auto
	BaseURL         string // overrides API base URL; empty = production
}

func (r ClaudeCodeReader) Name() string { return "claude-code" }

type claudeCredentialsFile struct {
	ClaudeAiOauth *claudeOAuthToken `json:"claudeAiOauth"`
}

type claudeOAuthToken struct {
	AccessToken string `json:"accessToken"`
	ExpiresAt   int64  `json:"expiresAt"` // unix milliseconds
}

type claudeUsageResponse struct {
	FiveHour        *claudeUsageBucket `json:"five_hour"`
	SevenDay        *claudeUsageBucket `json:"seven_day"`
	SevenDaySonnet  *claudeUsageBucket `json:"seven_day_sonnet"`
	SevenDayOpus    *claudeUsageBucket `json:"seven_day_opus"`
	SevenDayCowork  *claudeUsageBucket `json:"seven_day_cowork"`
}

type claudeUsageBucket struct {
	Utilization float64  `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

func (r ClaudeCodeReader) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	token, err := r.resolveToken()
	if err != nil {
		if r.Log != nil {
			r.Log.Printf("Claude Code reader skipped: %v", err)
		}
		return emptyReadModel(), nil
	}

	usage, err := r.fetchUsage(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("claude usage API: %w", err)
	}

	snap := claudeUsageToSnapshot(usage)
	if snap == nil || len(snap.Metrics) == 0 {
		return emptyReadModel(), nil
	}
	return &readmodel.ReadModel{
		Snapshots: map[string]*readmodel.Snapshot{"claude-code": snap},
	}, nil
}

func (r ClaudeCodeReader) resolveToken() (string, error) {
	if token := strings.TrimSpace(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")); token != "" {
		return token, nil
	}
	path := r.CredentialsPath
	if path == "" {
		path = discoverClaudeCredentialsPath()
	}
	if path == "" {
		return "", fmt.Errorf("credentials file not found")
	}
	return readClaudeAccessToken(path)
}

func discoverClaudeCredentialsPath() string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		p := filepath.Join(dir, ".credentials.json")
		if claudeFileExists(p) {
			return p
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	p := filepath.Join(home, ".claude", ".credentials.json")
	if claudeFileExists(p) {
		return p
	}
	return ""
}

func readClaudeAccessToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	var creds claudeCredentialsFile
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", fmt.Errorf("parsing credentials: %w", err)
	}
	if creds.ClaudeAiOauth == nil {
		return "", fmt.Errorf("no claudeAiOauth in credentials")
	}
	token := strings.TrimSpace(creds.ClaudeAiOauth.AccessToken)
	if token == "" {
		return "", fmt.Errorf("empty access token")
	}
	if creds.ClaudeAiOauth.ExpiresAt > 0 {
		expiresAt := time.UnixMilli(creds.ClaudeAiOauth.ExpiresAt)
		if time.Now().Add(30 * time.Second).After(expiresAt) {
			return "", fmt.Errorf("access token expired at %s", expiresAt.Format(time.RFC3339))
		}
	}
	return token, nil
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
		return nil, fmt.Errorf("HTTP %s", resp.Status)
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
		AccountID:  "claude-code",
		Status:     "ok",
		Metrics:    metrics,
		Resets:     resets,
		Raw:        map[string]any{"source": "claude-code-api"},
	}
}

func claudeFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
