package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"limitdock/internal/readmodel"
)

const (
	geminiCLITimeout = 8 * time.Second
	geminiQuotaURL   = "https://aistudio.google.com/api/v1alpha/quota"
)

var geminiLogOnce sync.Map

type GeminiCLIReader struct {
	Log             Logger
	CredentialsPath string // overrides auto-discovery; empty = auto
	BaseURL         string // overrides API base; empty = production
}

func (r GeminiCLIReader) Name() string             { return "gemini-cli" }
func (r GeminiCLIReader) FallbackProviderID() string { return "gemini_cli" }

type geminiCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	ExpiryDate   int64  `json:"expiry_date"` // unix milliseconds (Node.js format)
	Expiry       string `json:"expiry"`      // RFC3339 (Go gcloud format)
}

func (r GeminiCLIReader) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	token, err := r.resolveToken()
	if err != nil {
		logGeminiOnce(r.Log, "skip", "Gemini CLI reader skipped: %v", err)
		return emptyReadModel(), nil
	}

	data, err := r.fetchQuota(ctx, token)
	if err != nil {
		logGeminiOnce(r.Log, "api-err", "Gemini CLI reader skipped: quota API unavailable: %v", err)
		return emptyReadModel(), nil
	}

	snap := geminiQuotaToSnapshot(data)
	if snap == nil || len(snap.Metrics) == 0 {
		logGeminiOnce(r.Log, "no-quota", "Gemini CLI reader: credentials found but no quota rows in response")
		return emptyReadModel(), nil
	}
	logGeminiOnce(r.Log, "success", "Gemini CLI reader captured quota rows.")
	return &readmodel.ReadModel{
		Snapshots: map[string]*readmodel.Snapshot{
			snapshotKey("gemini_cli", snap.AccountID): snap,
		},
	}, nil
}

func (r GeminiCLIReader) resolveToken() (string, error) {
	path := r.CredentialsPath
	if path == "" {
		path = discoverGeminiCredentialsPath()
	}
	if path == "" {
		return "", fmt.Errorf("~/.gemini/oauth_credentials.json not found")
	}
	return readGeminiAccessToken(path)
}

func discoverGeminiCredentialsPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	candidates := []string{
		filepath.Join(home, ".gemini", "oauth_creds.json"),
		filepath.Join(home, ".gemini", "oauth_credentials.json"),
		filepath.Join(home, ".gemini", "credentials.json"),
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		candidates = append(candidates,
			filepath.Join(appData, "gemini", "oauth_credentials.json"),
			filepath.Join(appData, "gemini", "credentials.json"),
		)
	}
	for _, p := range candidates {
		if geminiFileExists(p) {
			return p
		}
	}
	return ""
}

func readGeminiAccessToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	var creds geminiCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", fmt.Errorf("parsing credentials: %w", err)
	}
	// Check expiry and refresh if needed
	expired := false
	var expiredAt time.Time
	if creds.ExpiryDate > 0 {
		expiresAt := time.UnixMilli(creds.ExpiryDate)
		if time.Now().Add(30 * time.Second).After(expiresAt) {
			expired = true
			expiredAt = expiresAt
		}
	}
	if !expired && creds.Expiry != "" {
		if exp, err := time.Parse(time.RFC3339, creds.Expiry); err == nil {
			if time.Now().Add(30 * time.Second).After(exp) {
				expired = true
				expiredAt = exp
			}
		}
	}
	if expired {
		if refreshed, err := refreshGeminiToken(creds); err == nil {
			return refreshed, nil
		}
		return "", fmt.Errorf("access token expired at %s", expiredAt.Format(time.RFC3339))
	}
	token := strings.TrimSpace(creds.AccessToken)
	if token == "" {
		return "", fmt.Errorf("no access_token in credentials file")
	}
	return token, nil
}

func refreshGeminiToken(creds geminiCredentials) (string, error) {
	if creds.RefreshToken == "" || creds.ClientID == "" {
		return "", fmt.Errorf("missing refresh_token or client_id")
	}
	vals := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {creds.RefreshToken},
		"client_id":     {creds.ClientID},
	}
	if creds.ClientSecret != "" {
		vals.Set("client_secret", creds.ClientSecret)
	}
	ctx, cancel := context.WithTimeout(context.Background(), geminiCLITimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://oauth2.googleapis.com/token",
		strings.NewReader(vals.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("token refresh: %s", out.Error)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("no access_token in refresh response")
	}
	return out.AccessToken, nil
}

func (r GeminiCLIReader) fetchQuota(ctx context.Context, token string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, geminiCLITimeout)
	defer cancel()

	base := strings.TrimRight(r.BaseURL, "/")
	if base == "" {
		base = "https://aistudio.google.com"
	}
	url := base + "/api/v1alpha/quota"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return out, nil
}

// geminiQuotaToSnapshot converts an AI Studio quota API response to a Snapshot.
// Expected response shape (one of several tried formats):
//
//	{
//	  "email": "user@gmail.com",
//	  "quotas": [
//	    {"model": "gemini-1.5-flash", "remaining": 0.87, "resetTime": "..."},
//	    {"model": "gemini-1.5-pro",   "remaining": 0.40, "resetTime": "..."}
//	  ]
//	}
//
// OR a flat format like {"quota_flash": 0.87, "quota_pro": 0.40, ...}.
func geminiQuotaToSnapshot(data map[string]any) *readmodel.Snapshot {
	if data == nil {
		return nil
	}
	accountID := firstString(data, "email", "userId", "user_id", "sub", "id")
	if accountID == "" {
		accountID = "local"
	}
	metrics := map[string]readmodel.Metric{}
	resets := map[string]any{}

	// Try array of per-model quota objects
	if quotas, ok := data["quotas"].([]any); ok {
		for _, q := range quotas {
			qm, ok := q.(map[string]any)
			if !ok {
				continue
			}
			modelName := firstString(qm, "model", "name", "model_id", "modelId")
			remaining, ok := firstNumber(qm, "remaining", "remaining_fraction", "remainingFraction", "fraction")
			if !ok || modelName == "" {
				continue
			}
			pct := normalizeRemainingPercent(remaining)
			key := "quota_model_" + slug(geminiModelPoolLabel(modelName))
			metrics[key] = readmodel.Metric{
				Remaining: floatPtr(pct),
				Unit:      "%",
				Window:    firstString(qm, "window", "period", "resetPeriod"),
			}
			if reset := firstString(qm, "resetTime", "reset_time", "resetsAt", "resets_at"); reset != "" {
				resets[key+"_reset"] = reset
			}
		}
	}

	// Try flat quota fields (quota_flash, quota_pro, quota, etc.)
	for _, pair := range []struct{ key, field string }{
		{"quota_flash", "quota_flash"},
		{"quota_pro", "quota_pro"},
		{"quota", "quota"},
		{"quota_model_flash", "flash_quota"},
		{"quota_model_pro", "pro_quota"},
	} {
		if _, exists := metrics[pair.key]; exists {
			continue
		}
		if v, ok := firstNumber(data, pair.field); ok {
			pct := normalizeRemainingPercent(v)
			metrics[pair.key] = readmodel.Metric{
				Remaining: floatPtr(pct),
				Unit:      "%",
			}
		}
	}

	// Also accept model-specific flat fields like "gemini_flash_remaining"
	for rawKey, rawVal := range data {
		k := strings.ToLower(strings.TrimSpace(rawKey))
		if !strings.Contains(k, "quota") && !strings.Contains(k, "remaining") && !strings.Contains(k, "limit") {
			continue
		}
		v, ok := numberAny(rawVal)
		if !ok {
			continue
		}
		var metricKey string
		switch {
		case strings.Contains(k, "flash"):
			metricKey = "quota_model_flash"
		case strings.Contains(k, "pro"):
			metricKey = "quota_model_pro"
		case k == "quota" || k == "quota_remaining":
			metricKey = "quota"
		}
		if metricKey == "" || metrics[metricKey].Remaining != nil {
			continue
		}
		pct := normalizeRemainingPercent(v)
		metrics[metricKey] = readmodel.Metric{
			Remaining: floatPtr(pct),
			Unit:      "%",
		}
	}

	if len(metrics) == 0 {
		return nil
	}
	return &readmodel.Snapshot{
		ProviderID: "gemini_cli",
		AccountID:  accountID,
		Status:     "ok",
		Metrics:    metrics,
		Resets:     resets,
		Raw:        map[string]any{"source": "gemini-cli-api"},
	}
}

func geminiModelPoolLabel(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(n, "flash") && strings.Contains(n, "lite"):
		return "gemini-flash-lite"
	case strings.Contains(n, "flash"):
		return "gemini-flash"
	case strings.Contains(n, "pro") && strings.Contains(n, "ultra"):
		return "gemini-ultra"
	case strings.Contains(n, "pro"):
		return "gemini-pro"
	case strings.Contains(n, "ultra"):
		return "gemini-ultra"
	}
	return name
}

func geminiFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func logGeminiOnce(log Logger, key string, format string, args ...any) {
	if log == nil {
		return
	}
	if _, loaded := geminiLogOnce.LoadOrStore(key, true); loaded {
		return
	}
	log.Printf(format, args...)
}
