package provider

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	cursorOAuthTokenURL = "https://api2.cursor.sh/oauth/token"
)

var errCursorUnauthorized = errors.New("cursor: 401 unauthorized")

var cursorLogOnce sync.Map

// cursorRefreshCache holds the last token we refreshed, keyed by the
// DB-stored access token it was refreshed from. This lets repeated polls
// within the same access-token generation reuse a refreshed token instead of
// paying a refresh HTTP call every time.
var cursorRefreshCache = struct {
	sync.Mutex
	dbToken        string
	refreshedToken string
}{}

type CursorReader struct {
	Log     Logger
	DBPath  string // overrides auto-discovery; empty = auto
	BaseURL string // overrides API base; empty = production
}

func (r CursorReader) Name() string               { return "cursor" }
func (r CursorReader) FallbackProviderID() string { return "cursor" }

func (r CursorReader) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	dbToken, refreshToken, accountID, err := r.resolveAuth()
	if err != nil {
		logCursorOnce(r.Log, "skip", "Cursor reader skipped: %v", err)
		return emptyReadModel(), nil
	}
	key := snapshotKey("cursor", accountID)

	token := cursorCachedToken(dbToken)
	if cursorTokenNeedsRefresh(token) && refreshToken != "" {
		newToken, rerr := refreshCursorToken(ctx, r.BaseURL, refreshToken)
		if rerr != nil {
			reason := "token refresh failed: " + rerr.Error()
			logCursorOnce(r.Log, "needs-auth", "Cursor reader needs re-auth: %s", reason)
			return statusReadModel(key, "cursor", accountID, readmodel.StatusNeedsAuth, reason), nil
		}
		token = newToken
		cursorCacheRefreshedToken(dbToken, newToken)
	}

	data, err := r.fetchUsage(ctx, token)
	if errors.Is(err, errCursorUnauthorized) {
		if refreshToken == "" {
			reason := "access token expired; no refresh token available"
			logCursorOnce(r.Log, "needs-auth", "Cursor reader needs re-auth: %s", reason)
			return statusReadModel(key, "cursor", accountID, readmodel.StatusNeedsAuth, reason), nil
		}
		newToken, rerr := refreshCursorToken(ctx, r.BaseURL, refreshToken)
		if rerr != nil {
			reason := "token refresh failed: " + rerr.Error()
			logCursorOnce(r.Log, "needs-auth", "Cursor reader needs re-auth: %s", reason)
			return statusReadModel(key, "cursor", accountID, readmodel.StatusNeedsAuth, reason), nil
		}
		cursorCacheRefreshedToken(dbToken, newToken)
		data, err = r.fetchUsage(ctx, newToken)
		if errors.Is(err, errCursorUnauthorized) {
			reason := "still unauthorized after token refresh"
			logCursorOnce(r.Log, "needs-auth", "Cursor reader needs re-auth: %s", reason)
			return statusReadModel(key, "cursor", accountID, readmodel.StatusNeedsAuth, reason), nil
		}
	}
	if err != nil {
		logCursorOnce(r.Log, "api-err", "Cursor reader: usage API unavailable: %v", err)
		return nil, err
	}

	snap := cursorUsageToSnapshot(data, accountID)
	if snap == nil || len(snap.Metrics) == 0 {
		logCursorOnce(r.Log, "no-quota", "Cursor reader: no quota rows in response")
		return emptyReadModel(), nil
	}
	logCursorOnce(r.Log, "success", "Cursor reader captured quota rows.")
	return &readmodel.ReadModel{
		Snapshots: map[string]*readmodel.Snapshot{
			key: snap,
		},
	}, nil
}

// cursorCachedToken returns the cached refreshed token when dbToken (the
// current DB-stored access token) matches the token the cache was built from
// and that refreshed token still has more than 5 minutes of life left.
// Otherwise it drops any stale entry and returns dbToken so the caller falls
// back to a fresh refresh (or uses dbToken as-is if it doesn't need one).
func cursorCachedToken(dbToken string) string {
	cursorRefreshCache.Lock()
	defer cursorRefreshCache.Unlock()
	if cursorRefreshCache.dbToken != dbToken {
		cursorRefreshCache.dbToken = ""
		cursorRefreshCache.refreshedToken = ""
		return dbToken
	}
	if cursorRefreshCache.refreshedToken == "" || cursorTokenNeedsRefresh(cursorRefreshCache.refreshedToken) {
		return dbToken
	}
	return cursorRefreshCache.refreshedToken
}

// cursorCacheRefreshedToken records a freshly refreshed token, keyed by the
// DB-stored token it was refreshed from.
func cursorCacheRefreshedToken(dbToken, refreshedToken string) {
	cursorRefreshCache.Lock()
	defer cursorRefreshCache.Unlock()
	cursorRefreshCache.dbToken = dbToken
	cursorRefreshCache.refreshedToken = refreshedToken
}

// resetCursorRefreshCache clears the in-memory refreshed-token cache. Used by tests.
func resetCursorRefreshCache() {
	cursorRefreshCache.Lock()
	defer cursorRefreshCache.Unlock()
	cursorRefreshCache.dbToken = ""
	cursorRefreshCache.refreshedToken = ""
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

func refreshCursorToken(ctx context.Context, baseURL, refreshToken string) (string, error) {
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
	url := cursorOAuthTokenURL
	if base := strings.TrimRight(baseURL, "/"); base != "" {
		url = base + "/oauth/token"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}
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
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
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
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
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
// Computes plan_percent_used as consumption % (remaining = 100 - used).
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

	var cycleStart, cycleEnd time.Time
	if t, ok := cursorTimeFromMap(data, "billingCycleStart", "billing_cycle_start"); ok {
		cycleStart = t
	}
	if t, ok := cursorTimeFromMap(data, "billingCycleEnd", "billing_cycle_end", "cycle_end", "period_end"); ok {
		cycleEnd = t
	}
	window := cursorCycleWindowLabel(cycleStart, cycleEnd)

	if pu := objectAny(data, "planUsage"); pu != nil {
		if t, ok := cursorTimeFromMap(pu, "billingCycleEnd", "billing_cycle_end"); ok && cycleEnd.IsZero() {
			cycleEnd = t
		}
		if t, ok := cursorTimeFromMap(pu, "billingCycleStart", "billing_cycle_start"); ok && cycleStart.IsZero() {
			cycleStart = t
		}
		window = cursorCycleWindowLabel(cycleStart, cycleEnd)

		usedPct, ok := cursorPlanPercentUsed(pu)
		if ok {
			metrics["plan_percent_used"] = readmodel.Metric{
				Used:   floatPtr(usedPct),
				Unit:   "%",
				Window: window,
			}
		}
	}

	if _, exists := metrics["plan_percent_used"]; !exists {
		if total, ok := firstNumber(data, "premium_requests_total", "gpt4_requests_total",
			"total_requests", "monthly_limit"); ok && total > 0 {
			remaining, _ := firstNumber(data, "premium_requests_remaining", "gpt4_requests_remaining", "requests_remaining")
			used := 100.0 * (total - remaining) / total
			metrics["plan_percent_used"] = readmodel.Metric{
				Used:   floatPtr(used),
				Unit:   "%",
				Window: window,
			}
		}
	}

	if _, exists := metrics["plan_percent_used"]; !exists {
		if v, ok := firstNumber(data, "plan_percent_used", "usage_percent", "used_percent"); ok {
			metrics["plan_percent_used"] = readmodel.Metric{
				Used:   floatPtr(v),
				Unit:   "%",
				Window: window,
			}
		}
	}

	if len(metrics) == 0 {
		return nil
	}

	if !cycleEnd.IsZero() {
		resets["billing_cycle_end"] = cycleEnd.UTC().Format(time.RFC3339)
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

func cursorPlanPercentUsed(planUsage map[string]any) (float64, bool) {
	if v, ok := firstNumber(planUsage, "totalPercentUsed", "total_percent_used"); ok {
		return v, true
	}
	limit, ok := firstNumber(planUsage, "limit", "includedAmountCents", "included_amount_cents")
	if !ok || limit <= 0 {
		return 0, false
	}
	remaining, _ := firstNumber(planUsage, "remaining", "remainingCents", "remaining_cents")
	return 100.0 * (limit - remaining) / limit, true
}

func cursorTimeFromMap(m map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if m == nil {
			break
		}
		if t, ok := parseCursorTime(m[key]); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

func parseCursorTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case nil:
		return time.Time{}, false
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z07:00"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC(), true
			}
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || f <= 0 {
			return time.Time{}, false
		}
		return cursorUnixToTime(f), true
	case float64:
		if x <= 0 {
			return time.Time{}, false
		}
		return cursorUnixToTime(x), true
	case float32:
		if x <= 0 {
			return time.Time{}, false
		}
		return cursorUnixToTime(float64(x)), true
	case int:
		if x <= 0 {
			return time.Time{}, false
		}
		return cursorUnixToTime(float64(x)), true
	case int64:
		if x <= 0 {
			return time.Time{}, false
		}
		return cursorUnixToTime(float64(x)), true
	case json.Number:
		f, err := x.Float64()
		if err != nil || f <= 0 {
			return time.Time{}, false
		}
		return cursorUnixToTime(f), true
	default:
		return time.Time{}, false
	}
}

func cursorUnixToTime(f float64) time.Time {
	if f >= 1e12 {
		sec := int64(f / 1000)
		nsec := int64(math.Mod(f, 1000)) * int64(time.Millisecond)
		return time.Unix(sec, nsec).UTC()
	}
	return time.Unix(int64(f), 0).UTC()
}

func cursorCycleWindowLabel(start, end time.Time) string {
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return "billing-cycle"
	}
	days := end.Sub(start).Hours() / 24
	switch {
	case days >= 27 && days <= 33:
		return "~30d"
	case days >= 6 && days <= 8:
		return "~7d"
	case days >= 0.9 && days <= 1.1:
		return "~1d"
	case days >= 1:
		return fmt.Sprintf("~%.0fd", math.Round(days))
	default:
		return "billing-cycle"
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
