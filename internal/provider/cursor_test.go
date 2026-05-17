package provider

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"limitdock/internal/quota"
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
			json.NewEncoder(w).Encode(map[string]any{
				"planUsage": map[string]any{
					"totalPercentUsed": 10.0,
				},
				"billingCycleEnd": 1780272000,
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
}

func TestCursorReaderSkipsOnAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	db := createCursorDB(t)
	insertCursorItem(t, db, "cursorAuth/accessToken", "test-token")

	r := CursorReader{DBPath: db, BaseURL: server.URL}
	model, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(model.Snapshots) != 0 {
		t.Fatalf("expected empty model on API error: %#v", model.Snapshots)
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
