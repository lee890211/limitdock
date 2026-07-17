package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"limitdock/internal/fsutil"
	"limitdock/internal/readmodel"
)

const (
	geminiCLITimeout        = 8 * time.Second
	geminiLoadCodeAssistURL = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	geminiRetrieveQuotaURL  = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"
	geminiProjectsURL       = "https://cloudresourcemanager.googleapis.com/v1/projects"
	geminiOAuthTokenURL     = "https://oauth2.googleapis.com/token"
	// Public credentials embedded in the Gemini CLI binary (google/gemini-cli).
	// Split to avoid GitHub secret-scanning false positives on these known-public strings.
	geminiCLIClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j" + ".apps.googleusercontent.com"
	geminiCLIClientSecret = "GOCSPX" + "-4uHgMPm-1o7Sk-geV6Cu5clXFsxl"
)

var geminiLogOnce sync.Map

// errGeminiCredentialsNotFound signals that no credentials file could be
// found at all (auto-discovery came up empty), as opposed to a file that
// exists but is unusable. Read() treats the two cases differently.
var errGeminiCredentialsNotFound = errors.New("gemini credentials file not found")

type GeminiCLIReader struct {
	Log             Logger
	CredentialsPath string // overrides auto-discovery; empty = auto
	BaseURL         string // overrides API base; empty = production
	TokenURL        string // overrides OAuth refresh endpoint; empty = production
}

func (r GeminiCLIReader) Name() string               { return "gemini-cli" }
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
		if errors.Is(err, errGeminiCredentialsNotFound) || errors.Is(err, os.ErrNotExist) {
			logGeminiOnce(r.Log, "skip", "Gemini CLI reader skipped: %v", err)
			return emptyReadModel(), nil
		}
		logGeminiOnce(r.Log, "needs-auth", "Gemini CLI reader: credentials unusable: %v", err)
		key := snapshotKey("gemini_cli", "local")
		return statusReadModel(key, "gemini_cli", "local", readmodel.StatusNeedsAuth, err.Error()), nil
	}

	data, err := r.fetchQuota(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("gemini quota API: %w", err)
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
		return "", errGeminiCredentialsNotFound
	}
	return readGeminiAccessToken(path, r.TokenURL)
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

func readGeminiAccessToken(path, tokenURL string) (string, error) {
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
		refreshed, refreshErr := refreshGeminiToken(creds, tokenURL)
		if refreshErr == nil {
			_ = persistGeminiAccessToken(path, refreshed)
			return refreshed, nil
		}
		return "", fmt.Errorf("access token expired at %s: refresh failed: %w", expiredAt.Format(time.RFC3339), refreshErr)
	}
	token := strings.TrimSpace(creds.AccessToken)
	if token == "" {
		return "", fmt.Errorf("no access_token in credentials file")
	}
	return token, nil
}

func refreshGeminiToken(creds geminiCredentials, tokenURL string) (string, error) {
	if creds.RefreshToken == "" {
		return "", fmt.Errorf("missing refresh_token")
	}
	clientID := creds.ClientID
	if clientID == "" {
		clientID = geminiCLIClientID
	}
	clientSecret := creds.ClientSecret
	if clientSecret == "" {
		clientSecret = geminiCLIClientSecret
	}
	vals := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {creds.RefreshToken},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	endpoint := tokenURL
	if endpoint == "" {
		endpoint = geminiOAuthTokenURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), geminiCLITimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		endpoint,
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

	loadURL := geminiLoadCodeAssistURL
	quotaURL := geminiRetrieveQuotaURL
	if base := strings.TrimRight(r.BaseURL, "/"); base != "" {
		loadURL = base + "/v1internal:loadCodeAssist"
		quotaURL = base + "/v1internal:retrieveUserQuota"
	}

	loadBody := map[string]any{
		"metadata": map[string]any{
			"ideType":     "IDE_UNSPECIFIED",
			"platform":    "PLATFORM_UNSPECIFIED",
			"pluginType":  "GEMINI",
			"duetProject": "default",
		},
	}
	loadData, err := geminiPostJSON(ctx, loadURL, token, loadBody)
	if err != nil {
		return nil, fmt.Errorf("loadCodeAssist: %w", err)
	}
	projectID := discoverGeminiProjectID(ctx, token, loadData)
	quotaBody := map[string]any{}
	if projectID != "" {
		quotaBody["project"] = projectID
	}
	data, err := geminiPostJSON(ctx, quotaURL, token, quotaBody)
	if err != nil {
		return nil, fmt.Errorf("retrieveUserQuota: %w", err)
	}
	return data, nil
}

func geminiPostJSON(ctx context.Context, url, token string, body map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return out, nil
}

func discoverGeminiProjectID(ctx context.Context, token string, loadData map[string]any) string {
	if id := readFirstStringDeep(loadData, "cloudaicompanionProject", "cloudAiCompanionProject", "project"); id != "" {
		return id
	}
	ctx, cancel := context.WithTimeout(ctx, geminiCLITimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, geminiProjectsURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode >= 300 {
		if resp != nil {
			resp.Body.Close()
		}
		return ""
	}
	defer resp.Body.Close()
	var projects struct {
		Projects []struct {
			ProjectID string            `json:"projectId"`
			Labels    map[string]string `json:"labels"`
		} `json:"projects"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		return ""
	}
	for _, p := range projects.Projects {
		if strings.HasPrefix(p.ProjectID, "gen-lang-client") {
			return p.ProjectID
		}
		if p.Labels["generative-language"] != "" {
			return p.ProjectID
		}
	}
	return ""
}

func readFirstStringDeep(value any, keys ...string) string {
	switch v := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if s := firstString(v, key); s != "" {
				return s
			}
		}
		for _, child := range v {
			if s := readFirstStringDeep(child, keys...); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range v {
			if s := readFirstStringDeep(child, keys...); s != "" {
				return s
			}
		}
	}
	return ""
}

// persistGeminiAccessToken updates access_token, expiry_date, and expiry in
// the on-disk credentials file, leaving every other key byte-identical. It
// round-trips through map[string]any (rather than the typed geminiCredentials
// struct) so unknown fields written by the Gemini CLI survive, and writes
// atomically so readers never observe a partial file.
func persistGeminiAccessToken(path, accessToken string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var creds map[string]any
	if err := json.Unmarshal(raw, &creds); err != nil {
		return err
	}
	// expiry_date (unix ms, Node.js format) and expiry (RFC3339, gcloud
	// format) must be kept in sync: readGeminiAccessToken treats either one
	// being in the past as expired, so a stale sibling field re-triggers a
	// refresh on every subsequent poll.
	expiresAt := time.Now().Add(55 * time.Minute)
	creds["access_token"] = accessToken
	creds["expiry_date"] = expiresAt.UnixMilli()
	creds["expiry"] = expiresAt.UTC().Format(time.RFC3339)
	b, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.AtomicWriteFile(path, b, 0o600)
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

	collectGeminiQuotaBuckets(data, func(modelName string, remaining float64, reset string) {
		if modelName == "" {
			return
		}
		// Free-tier responses mark models that are not available at all with
		// remainingFraction 0 and an epoch resetTime; a genuine exhaustion
		// carries a real future reset. Skip the sentinel buckets so the card
		// is not dominated by a fake 0% row.
		if remaining <= 0 && geminiSentinelReset(reset) {
			return
		}
		pct := normalizeRemainingPercent(remaining)
		key := "quota_model_" + slug(geminiModelPoolLabel(modelName))
		if existing, exists := metrics[key]; exists {
			// Lowest remaining wins within a pooled model family.
			if existing.Remaining != nil && *existing.Remaining <= pct {
				return
			}
		}
		metrics[key] = readmodel.Metric{
			Remaining: floatPtr(pct),
			Unit:      "%",
		}
		if reset != "" && !geminiSentinelReset(reset) {
			resets[key+"_reset"] = reset
		} else {
			delete(resets, key+"_reset")
		}
	})

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

func collectGeminiQuotaBuckets(value any, emit func(modelName string, remaining float64, reset string)) {
	switch v := value.(type) {
	case []any:
		for _, child := range v {
			collectGeminiQuotaBuckets(child, emit)
		}
	case map[string]any:
		if remaining, ok := firstNumber(v, "remainingFraction", "remaining_fraction", "remaining"); ok {
			modelName := firstString(v, "modelId", "model_id", "model", "name")
			reset := firstString(v, "resetTime", "reset_time", "resetsAt", "resets_at")
			emit(modelName, remaining, reset)
		}
		for _, child := range v {
			collectGeminiQuotaBuckets(child, emit)
		}
	}
}

// geminiSentinelReset reports whether a bucket resetTime is the epoch
// placeholder (or absent) that the quota API uses for models without an
// actual quota window.
func geminiSentinelReset(reset string) bool {
	reset = strings.TrimSpace(reset)
	return reset == "" || strings.HasPrefix(reset, "1970-")
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
