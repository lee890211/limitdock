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
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"limitdock/internal/quota"
	"limitdock/internal/readmodel"
	"limitdock/internal/settings"

	_ "modernc.org/sqlite"
)

func TestCursorReaderSkipsWhenNoDBPath(t *testing.T) {
	r := CursorReader{DBPath: filepath.Join(t.TempDir(), "does-not-exist.vscdb")}
	model, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(model.Snapshots) != 0 {
		t.Fatalf("expected empty model, got %#v", model.Snapshots)
	}
}

func TestCursorReaderSkipsWhenNoToken(t *testing.T) {
	db := createCursorDB(t)
	r := CursorReader{DBPath: db}
	model, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(model.Snapshots) != 0 {
		t.Fatalf("expected empty model: %#v", model.Snapshots)
	}
}

func TestCursorReaderHappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			end := time.Now().UTC().Add(12 * 24 * time.Hour)
			start := end.Add(-30 * 24 * time.Hour)
			json.NewEncoder(w).Encode(map[string]any{
				"planUsage": map[string]any{
					"totalPercentUsed": 10.0,
				},
				"billingCycleStart": fmt.Sprintf("%d", start.UnixMilli()),
				"billingCycleEnd":   fmt.Sprintf("%d", end.UnixMilli()),
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"premium_requests_remaining": 450,
			"premium_requests_total":     500,
			"billing_cycle_end":          "2026-06-01T00:00:00.000Z",
		})
	}))
	defer server.Close()

	db := createCursorDB(t)
	insertCursorItem(t, db, "cursorAuth/accessToken", "test-token")
	insertCursorItem(t, db, "cursorAuth/cachedEmail", "user@example.com")

	r := CursorReader{DBPath: db, BaseURL: server.URL}
	model, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	snap := model.Snapshots["cursor-user@example.com"]
	if snap == nil {
		t.Fatalf("expected cursor snapshot: %#v", model.Snapshots)
	}
	if snap.ProviderID != "cursor" {
		t.Fatalf("wrong provider ID: %q", snap.ProviderID)
	}
	metric := snap.Metrics["plan_percent_used"]
	if metric.Used == nil {
		t.Fatalf("expected plan_percent_used: %#v", snap.Metrics)
	}
	// (500-450)/500 * 100 = 10%
	if *metric.Used != 10.0 {
		t.Fatalf("expected used=10.0, got %v", *metric.Used)
	}
	if snap.Resets["billing_cycle_end"] == "" {
		t.Fatalf("expected billing_cycle_end reset: %#v", snap.Resets)
	}

	cards := quota.Cards(model, settings.Defaults())
	if len(cards) != 1 || len(cards[0].Bands) != 1 {
		t.Fatalf("expected rendered Cursor quota band, got %#v", cards)
	}
	// 90% remaining
	if cards[0].Main != "90%" {
		t.Fatalf("expected 90%% remaining, got %q", cards[0].Main)
	}
	if cards[0].Bands[0].Reset == "" {
		t.Fatalf("expected billing cycle reset text, got %#v", cards[0].Bands[0])
	}
	if cards[0].Bands[0].Window != "~30d" {
		t.Fatalf("expected ~30d billing window, got %q", cards[0].Bands[0].Window)
	}
}

func TestCursorUsageToSnapshotBillingCycleEndMillisecondsString(t *testing.T) {
	end := time.Now().UTC().Add(8 * 24 * time.Hour)
	start := end.Add(-30 * 24 * time.Hour)
	snap := cursorUsageToSnapshot(map[string]any{
		"billingCycleStart": fmt.Sprintf("%d", start.UnixMilli()),
		"billingCycleEnd":   fmt.Sprintf("%d", end.UnixMilli()),
		"planUsage": map[string]any{
			"totalPercentUsed": 38.4,
		},
	}, "user@example.com")
	if snap == nil {
		t.Fatal("expected snapshot")
	}
	card := quota.SnapshotToCard("cursor", snap, settings.Defaults())
	if len(card.Bands) != 1 {
		t.Fatalf("expected one band, got %#v", card.Bands)
	}
	if card.Main != "61.6%" {
		t.Fatalf("expected 61.6%% remaining, got %q", card.Main)
	}
	if card.Bands[0].Reset == "" {
		t.Fatalf("billing_cycle_end should format reset text, resets=%#v", snap.Resets)
	}
}

func TestCursorUsagePlanUsageWinsOverPremiumRequests(t *testing.T) {
	snap := cursorUsageToSnapshot(map[string]any{
		"planUsage": map[string]any{
			"totalPercentUsed": 25.0,
		},
		"premium_requests_total":     500,
		"premium_requests_remaining": 450,
	}, "user@example.com")
	metric := snap.Metrics["plan_percent_used"]
	if metric.Used == nil || *metric.Used != 25.0 {
		t.Fatalf("planUsage should win, got %#v", metric)
	}
}

func TestCursorUsagePlanPercentFromSpendCents(t *testing.T) {
	snap := cursorUsageToSnapshot(map[string]any{
		"planUsage": map[string]any{
			"limit":     40000,
			"remaining": 16778,
		},
	}, "user@example.com")
	metric := snap.Metrics["plan_percent_used"]
	if metric.Used == nil {
		t.Fatalf("expected computed used percent, got %#v", snap.Metrics)
	}
	want := 100.0 * (40000 - 16778) / 40000
	if math.Abs(*metric.Used-want) > 0.01 {
		t.Fatalf("used=%v want=%v", *metric.Used, want)
	}
}

// Cursor auth is fine here (no 401); the usage API itself is unavailable, so
// Read must propagate the error rather than swallowing it into an empty model.
func TestCursorReaderReturnsErrorOnAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	db := createCursorDB(t)
	insertCursorItem(t, db, "cursorAuth/accessToken", "test-token")

	r := CursorReader{DBPath: db, BaseURL: server.URL}
	model, err := r.Read(context.Background())
	if err == nil {
		t.Fatalf("expected error on API failure, got model=%#v", model)
	}
	if model != nil {
		t.Fatalf("expected nil model on API error, got %#v", model)
	}
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected *HTTPStatusError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", httpErr.StatusCode)
	}
}

// cursorTestJWT builds a minimal (unsigned) JWT with only an exp claim, which
// is all jwtExpiryUnix reads.
func cursorTestJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return header + "." + payload + ".sig"
}

func TestCursorReaderCachesRefreshedTokenAcrossPolls(t *testing.T) {
	resetCursorRefreshCache()
	t.Cleanup(resetCursorRefreshCache)

	var refreshCalls atomic.Int32
	var usageCalls atomic.Int32
	refreshedToken := cursorTestJWT(t, time.Now().Add(1*time.Hour))

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": refreshedToken})
	})
	mux.HandleFunc("/aiserver.v1.DashboardService/GetCurrentPeriodUsage", func(w http.ResponseWriter, r *http.Request) {
		usageCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"planUsage": map[string]any{"totalPercentUsed": 5.0},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	db := createCursorDB(t)
	insertCursorItem(t, db, "cursorAuth/accessToken", cursorTestJWT(t, time.Now().Add(1*time.Minute)))
	insertCursorItem(t, db, "cursorAuth/refreshToken", "refresh-token-1")
	insertCursorItem(t, db, "cursorAuth/cachedEmail", "user@example.com")

	r := CursorReader{DBPath: db, BaseURL: server.URL}

	model1, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if snap := model1.Snapshots["cursor-user@example.com"]; snap == nil || len(snap.Metrics) == 0 {
		t.Fatalf("expected quota snapshot on first read: %#v", model1.Snapshots)
	}

	model2, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if snap := model2.Snapshots["cursor-user@example.com"]; snap == nil || len(snap.Metrics) == 0 {
		t.Fatalf("expected quota snapshot on second read: %#v", model2.Snapshots)
	}

	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 refresh call across both polls, got %d", got)
	}
	if got := usageCalls.Load(); got != 2 {
		t.Fatalf("expected 2 successful usage fetches, got %d", got)
	}
}

func TestCursorReaderDropsCacheWhenDBTokenChanges(t *testing.T) {
	resetCursorRefreshCache()
	t.Cleanup(resetCursorRefreshCache)

	var refreshCalls atomic.Int32
	var lastUsageAuth string

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": cursorTestJWT(t, time.Now().Add(1*time.Hour))})
	})
	mux.HandleFunc("/aiserver.v1.DashboardService/GetCurrentPeriodUsage", func(w http.ResponseWriter, r *http.Request) {
		lastUsageAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"planUsage": map[string]any{"totalPercentUsed": 5.0},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	db := createCursorDB(t)
	insertCursorItem(t, db, "cursorAuth/accessToken", cursorTestJWT(t, time.Now().Add(1*time.Minute)))
	insertCursorItem(t, db, "cursorAuth/refreshToken", "refresh-token-1")
	insertCursorItem(t, db, "cursorAuth/cachedEmail", "user@example.com")

	r := CursorReader{DBPath: db, BaseURL: server.URL}
	if _, err := r.Read(context.Background()); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("expected 1 refresh call after first read, got %d", got)
	}

	// Cursor app rotates the DB-stored token to a new, already-valid token.
	tokenB := cursorTestJWT(t, time.Now().Add(2*time.Hour))
	insertCursorItem(t, db, "cursorAuth/accessToken", tokenB)

	if _, err := r.Read(context.Background()); err != nil {
		t.Fatalf("second read: %v", err)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("expected still 1 refresh call (new DB token doesn't need refresh), got %d", got)
	}
	if lastUsageAuth != "Bearer "+tokenB {
		t.Fatalf("expected usage call to use the new DB token, got %q", lastUsageAuth)
	}
}

func TestCursorReaderNeedsAuthWhenRefreshFails(t *testing.T) {
	resetCursorRefreshCache()
	t.Cleanup(resetCursorRefreshCache)

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	db := createCursorDB(t)
	insertCursorItem(t, db, "cursorAuth/accessToken", cursorTestJWT(t, time.Now().Add(-1*time.Hour)))
	insertCursorItem(t, db, "cursorAuth/refreshToken", "refresh-token-1")
	insertCursorItem(t, db, "cursorAuth/cachedEmail", "user@example.com")

	r := CursorReader{DBPath: db, BaseURL: server.URL}
	model, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	snap := model.Snapshots["cursor-user@example.com"]
	if snap == nil {
		t.Fatalf("expected needs_auth snapshot: %#v", model.Snapshots)
	}
	if snap.Status != readmodel.StatusNeedsAuth {
		t.Fatalf("expected status %q, got %q", readmodel.StatusNeedsAuth, snap.Status)
	}
	if snap.ProviderID != "cursor" {
		t.Fatalf("wrong provider id: %q", snap.ProviderID)
	}
	if len(snap.Metrics) != 0 {
		t.Fatalf("expected no metrics on needs_auth snapshot, got %#v", snap.Metrics)
	}
}

func createCursorDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return path
}

func insertCursorItem(t *testing.T, dbPath, key, value string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT OR REPLACE INTO ItemTable (key, value) VALUES (?, ?)`, key, value); err != nil {
		t.Fatalf("insert: %v", err)
	}
}
