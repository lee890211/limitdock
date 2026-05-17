package provider

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"limitdock/internal/readmodel"

	_ "modernc.org/sqlite"
)

const (
	cursorTimeout = 8 * time.Second
	// Connect RPC (preferred; matches Cursor desktop / crossusage).
	cursorConnectUsageURL = "https://api2.cursor.sh/aiserver.v1.DashboardService/GetCurrentPeriodUsage"
	// Legacy REST fallback when Connect is unavailable.
	cursorRESTUsageURL = "https://www.cursor.com/api/usage"
	// Public credentials embedded in the Cursor desktop app binary.
	cursorOAuthClientID = "KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB"
	cursorOAuthTokenURL   = "https://api2.cursor.sh/oauth/token"
)

var errCursorUnauthorized = errors.New("cursor: 401 unauthorized")

var cursorLogOnce sync.Map

type CursorReader struct {
	Log     Logger
	DBPath  string // overrides auto-discovery; empty = auto
	BaseURL string // overrides API base; empty = production
}

func (r CursorReader) Name() string              { return "cursor" }
func (r CursorReader) FallbackProviderID() string { return "cursor" }

func (r CursorReader) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	token, refreshToken, accountID, err := r.resolveAuth()
	if err != nil {
		logCursorOnce(r.Log, "skip", "Cursor reader skipped: %v", err)
		return emptyReadModel(), nil
	}

	if cursorTokenNeedsRefresh(token) && refreshToken != "" {
		if newToken, rerr := refreshCursorToken(ctx, refreshToken); rerr == nil {
			token = newToken
		}
	}

	data, err := r.fetchUsage(ctx, token)
	if errors.Is(err, errCursorUnauthorized) && refreshToken != "" {
		if newToken, rerr := refreshCursorToken(ctx, refreshToken); rerr == nil {
			token = newToken
			data, err = r.fetchUsage(ctx, newToken)
		}
	}
	if err != nil {
		logCursorOnce(r.Log, "api-err", "Cursor reader skipped: usage API unavailable: %v", err)
		return emptyReadModel(), nil
	}

	snap := cursorUsageToSnapshot(data, accountID)
	if snap == nil || len(snap.Metrics) == 0 {
		logCursorOnce(r.Log, "no-quota", "Cursor reader: no quota rows in response")
		return emptyReadModel(), nil
	}
	logCursorOnce(r.Log, "success", "Cursor reader captured quota rows.")
	return &readmodel.ReadModel{
		Snapshots: map[string]*readmodel.Snapshot{
			snapshotKey("cursor", snap.AccountID): snap,
		},
	}, nil
}

func (r CursorReader) resolveAuth() (token, refreshToken, accountID string, err error) {
	dbPath := r.DBPath
	if dbPath == "" {
		dbPath = defaultCursorDBPath()
	}
	if dbPath == "" {
		return "", "", "", fmt.Errorf("Cursor state.vscdb not found")
	}
	return readCursorAuth(dbPath)
}

func defaultCursorDBPath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return ""
	}
	p := filepath.Join(appData, "Cursor", "User", "globalStorage", "state.vscdb")
	if info, err := os.Stat(p); err == nil && !info.IsDir() {
		return p
	}
	return ""
}

func readCursorAuth(dbPath string) (token, refreshToken, accountID string, err error) {
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(dbPath))
	if err != nil {
		return "", "", "", fmt.Errorf("opening cursor db: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	rows, err := db.Query(`SELECT key, value FROM ItemTable WHERE key IN ('cursorAuth/accessToken','cursorAuth/refreshToken','cursorAuth/cachedEmail','cursorAuth/userId')`)
	if err != nil {
		return "", "", "", fmt.Errorf("querying cursor db: %w", err)
	}
	defer rows.Close()

	values := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			continue
		}
		values[k] = strings.TrimSpace(v)
	}
	if err := rows.Err(); err != nil {
		return "", "", "", err
	}

	token = values["cursorAuth/accessToken"]
	if token == "" {
		return "", "", "", fmt.Errorf("no access token in Cursor state.vscdb")
	}
	refreshToken = values["cursorAuth/refreshToken"]
	accountID = values["cursorAuth/cachedEmail"]
	if accountID == "" {
		accountID = values["cursorAuth/userId"]
	}
	if accountID == "" {
		accountID = "local"
	}
	return token, refreshToken, accountID, nil
}

func refreshCursorToken(ctx context.Context, refreshToken string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cursorTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     cursorOAuthClientID,
		"refresh_token": refreshToken,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cursorOAuthTokenURL,
		strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
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

func (r CursorReader) fetchUsage(ctx context.Context, token string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, cursorTimeout)
	defer cancel()

	if data, err := r.fetchConnectUsage(ctx, token); err == nil && data != nil {
		return data, nil
	} else if errors.Is(err, errCursorUnauthorized) {
		return nil, err
	}

	base := strings.TrimRight(r.BaseURL, "/")
	if base == "" {
		base = "https://www.cursor.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/usage", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errCursorUnauthorized
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return out, nil
}

func (r CursorReader) fetchConnectUsage(ctx context.Context, token string) (map[string]any, error) {
	url := cursorConnectUsageURL
	if base := strings.TrimRight(r.BaseURL, "/"); base != "" {
		url = base + "/aiserver.v1.DashboardService/GetCurrentPeriodUsage"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("User-Agent", "Cursor/1.0.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errCursorUnauthorized
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("connect usage HTTP %s", resp.Status)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding connect usage: %w", err)
	}
	return out, nil
}

func cursorTokenNeedsRefresh(token string) bool {
	exp, ok := jwtExpiryUnix(token)
	if !ok {
		return false
	}
	return time.Now().Add(5 * time.Minute).After(time.Unix(exp, 0))
}

func jwtExpiryUnix(token string) (int64, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return 0, false
	}
	payload := parts[1]
	if m := len(payload) % 4; m != 0 {
		payload += strings.Repeat("=", 4-m)
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(payload)
		if err != nil {
			return 0, false
		}
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Exp <= 0 {
		return 0, false
	}
	return int64(claims.Exp), true
}

// cursorUsageToSnapshot converts a Cursor usage API response to a Snapshot.
// Expected response:
//
//	{
//	  "premium_requests_remaining": 450,
//	  "premium_requests_total":     500,
//	  "billing_cycle_end": "2024-12-31T23:59:59.999Z"
//	}
//
// Computes plan_percent_used = (total - remaining) / total * 100.
func cursorUsageToSnapshot(data map[string]any, accountID string) *readmodel.Snapshot {
	if data == nil {
		return nil
	}
	if accountID == "" {
		accountID = firstString(data, "email", "userId", "user_id", "sub")
		if accountID == "" {
			accountID = "local"
		}
	}

	metrics := map[string]readmodel.Metric{}
	resets := map[string]any{}

	if pu := objectAny(data, "planUsage"); pu != nil {
		if v, ok := firstNumber(pu, "totalPercentUsed", "total_percent_used"); ok {
			metrics["plan_percent_used"] = readmodel.Metric{
				Used:   floatPtr(v),
				Unit:   "%",
				Window: "billing-cycle",
			}
		}
	}
	if end, ok := firstNumber(data, "billingCycleEnd", "billing_cycle_end"); ok && end > 0 {
		if end > 1e12 {
			end = end / 1000
		}
		resets["billing_cycle_end"] = time.Unix(int64(end), 0).UTC().Format(time.RFC3339)
	}

	// Compute plan_percent_used from premium_requests fields
	if total, ok := firstNumber(data, "premium_requests_total", "gpt4_requests_total",
		"total_requests", "monthly_limit"); ok && total > 0 {
		remaining, _ := firstNumber(data, "premium_requests_remaining", "gpt4_requests_remaining", "requests_remaining")
		used := 100.0 * (total - remaining) / total
		metrics["plan_percent_used"] = readmodel.Metric{
			Used:   floatPtr(used),
			Unit:   "%",
			Window: "billing-cycle",
		}
	}

	// Direct plan_percent_used field
	if _, exists := metrics["plan_percent_used"]; !exists {
		if v, ok := firstNumber(data, "plan_percent_used", "usage_percent", "used_percent"); ok {
			metrics["plan_percent_used"] = readmodel.Metric{
				Used:   floatPtr(v),
				Unit:   "%",
				Window: "billing-cycle",
			}
		}
	}

	if len(metrics) == 0 {
		return nil
	}

	// Billing cycle end → reset key for plan_percent_used
	if end := firstString(data, "billing_cycle_end", "billingCycleEnd", "cycle_end", "period_end"); end != "" {
		resets["billing_cycle_end"] = end
	}

	return &readmodel.Snapshot{
		ProviderID: "cursor",
		AccountID:  accountID,
		Status:     "ok",
		Metrics:    metrics,
		Resets:     resets,
		Raw:        map[string]any{"source": "cursor-api"},
	}
}

func logCursorOnce(log Logger, key string, format string, args ...any) {
	if log == nil {
		return
	}
	if _, loaded := cursorLogOnce.LoadOrStore(key, true); loaded {
		return
	}
	log.Printf(format, args...)
}
