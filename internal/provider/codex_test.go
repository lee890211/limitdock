package provider

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"limitdock/internal/quota"
	"limitdock/internal/readmodel"
	"limitdock/internal/settings"
)

func TestCodexReaderReadsSessionRateLimits(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "2026", "05", "10")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := `{"payload":{"rate_limits":{"limit_id":"codex_bengalfox","limit_name":"GPT-5.3-Codex-Spark","primary":{"used_percent":27,"window_minutes":300,"resets_at":1778407279},"secondary":{"used_percent":35,"window_minutes":10080,"resets_at":1778916833},"plan_type":"prolite"}}}`
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	model, err := CodexReader{Root: root}.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	snap := model.Snapshots["codex-cli"]
	if snap == nil {
		t.Fatalf("expected codex-cli snapshot: %#v", model.Snapshots)
	}
	if snap.ProviderID != "codex" || snap.Metrics["rate_limit_codex_bengalfox_primary"].Used == nil {
		t.Fatalf("unexpected snapshot: %#v", snap)
	}
	cards := quota.Cards(model, settings.Defaults())
	if len(cards) != 1 || len(cards[0].Bands) != 2 {
		t.Fatalf("expected rendered Codex quota rows, got %#v", cards)
	}
}

func TestCodexReaderPrefersLogRateLimitsOverOlderSession(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "2026", "05", "16")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sessionLine := `{"timestamp":"2026-05-16T11:17:03.242Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","primary":{"used_percent":6,"window_minutes":300,"resets_at":1778935199},"secondary":{"used_percent":1,"window_minutes":10080,"resets_at":1779521999},"plan_type":"prolite"}}}`
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(sessionLine+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(root, "logs_2.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE logs (
		ts INTEGER NOT NULL,
		ts_nanos INTEGER NOT NULL,
		target TEXT NOT NULL,
		feedback_log_body TEXT
	)`)
	if err != nil {
		t.Fatalf("create logs: %v", err)
	}
	// Relative to time.Now() rather than a fixed date: the log query only looks
	// back codexLogLookback (7d), so a hardcoded past date eventually falls
	// outside that window and gets silently excluded by the SQL filter.
	at := time.Now().UTC().Add(-1 * time.Hour)
	body := `session_loop{}: websocket event: {"type":"codex.rate_limits","plan_type":"prolite","rate_limits":{"allowed":true,"limit_reached":false,"primary":{"used_percent":38,"window_minutes":300,"reset_at":1778935199},"secondary":{"used_percent":6,"window_minutes":10080,"reset_at":1779521999}}}`
	_, err = db.Exec(`INSERT INTO logs (ts, ts_nanos, target, feedback_log_body) VALUES (?, ?, ?, ?)`, at.Unix(), int64(at.Nanosecond()), "codex_api::endpoint::responses_websocket", body)
	if err != nil {
		t.Fatalf("insert log: %v", err)
	}

	model, err := CodexReader{Root: root}.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	cards := quota.Cards(model, settings.Defaults())
	if len(cards) != 1 || cards[0].Main != "62%" {
		t.Fatalf("expected latest log quota to render 62%% remaining, got %#v", cards)
	}
}

func TestCodexReaderPicksNewestSessionFileWhenNoTimestamps(t *testing.T) {
	// Regression: session files with no timestamps must be disambiguated by
	// file mod time, so the newest file wins. Mod times are forced to an
	// exact tie (via os.Chtimes) rather than relying on write-order timing,
	// since real filesystems (coarse mtime resolution, bulk-restored files)
	// can easily produce identical timestamps; the lexically later
	// sessions/YYYY/MM/DD path must still win deterministically.
	root := t.TempDir()
	old := filepath.Join(root, "sessions", "2026", "05", "10")
	new := filepath.Join(root, "sessions", "2026", "05", "16")
	for _, dir := range []string{old, new} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	oldLine := `{"payload":{"rate_limits":{"limit_id":"codex","primary":{"used_percent":90,"window_minutes":300},"secondary":{"used_percent":10,"window_minutes":10080}}}}`
	newLine := `{"payload":{"rate_limits":{"limit_id":"codex","primary":{"used_percent":20,"window_minutes":300},"secondary":{"used_percent":5,"window_minutes":10080}}}}`
	oldPath := filepath.Join(old, "session.jsonl")
	newPath := filepath.Join(new, "session.jsonl")
	if err := os.WriteFile(oldPath, []byte(oldLine+"\n"), 0o644); err != nil {
		t.Fatalf("write old: %v", err)
	}
	if err := os.WriteFile(newPath, []byte(newLine+"\n"), 0o644); err != nil {
		t.Fatalf("write new: %v", err)
	}
	tied := time.Now()
	if err := os.Chtimes(oldPath, tied, tied); err != nil {
		t.Fatalf("chtimes old: %v", err)
	}
	if err := os.Chtimes(newPath, tied, tied); err != nil {
		t.Fatalf("chtimes new: %v", err)
	}

	model, err := CodexReader{Root: root}.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	snap := model.Snapshots["codex-cli"]
	if snap == nil {
		t.Fatalf("expected codex-cli snapshot: %#v", model.Snapshots)
	}
	metric := snap.Metrics["rate_limit_primary"]
	if metric.Used == nil || *metric.Used != 20 {
		t.Fatalf("expected primary used=20 (newest file), got %#v", metric)
	}
}

// Fixture mirrors a real free-tier wham/usage response captured 2026-07-17:
// *_window key style, null secondary_window, epoch-seconds reset_at,
// limit_window_seconds, and top-level plan_type.
func TestCodexWhamToSnapshotLiveFreeTierShape(t *testing.T) {
	data := map[string]any{
		"user_id":   "user-REDACTED",
		"email":     "user@example.com",
		"plan_type": "free",
		"rate_limit": map[string]any{
			"allowed":       false,
			"limit_reached": true,
			"primary_window": map[string]any{
				"used_percent":         float64(100),
				"limit_window_seconds": float64(2592000),
				"reset_after_seconds":  float64(392588),
				"reset_at":             float64(1784612470),
			},
			"secondary_window": nil,
		},
		"code_review_rate_limit": nil,
		"additional_rate_limits": nil,
	}
	snap := codexWhamToSnapshot(data)
	if snap == nil {
		t.Fatal("expected snapshot from live-shape wham response")
	}
	m, ok := snap.Metrics["rate_limit_primary"]
	if !ok || m.Used == nil || *m.Used != 100 {
		t.Fatalf("primary metric = %#v", m)
	}
	if got := readmodel.String(m.Window); got != "43200m" {
		t.Fatalf("window = %q, want 43200m (limit_window_seconds/60)", got)
	}
	if _, ok := snap.Metrics["rate_limit_secondary"]; ok {
		t.Fatalf("null secondary_window must not produce a metric: %#v", snap.Metrics)
	}
	if _, ok := snap.Resets["rate_limit_primary_reset"]; !ok {
		t.Fatalf("epoch reset_at should be stored: %#v", snap.Resets)
	}
	if snap.Attributes["source"] != "codex-wham" {
		t.Fatalf("wham snapshot must be labeled codex-wham, got %#v", snap.Attributes)
	}
	if snap.Attributes["plan_type"] != "free" {
		t.Fatalf("plan_type should carry through: %#v", snap.Attributes)
	}
}

func TestCodexWhamToSnapshotBothKeyStyles(t *testing.T) {
	// primary/secondary (no _window suffix) — actual wham API style.
	data := map[string]any{
		"rate_limit": map[string]any{
			"primary":   map[string]any{"used_percent": float64(30), "window_minutes": float64(300), "resets_at": "2026-05-17T23:00:00Z"},
			"secondary": map[string]any{"used_percent": float64(10), "window_minutes": float64(10080), "resets_at": "2026-05-24T00:00:00Z"},
		},
	}
	snap := codexWhamToSnapshot(data)
	if snap == nil {
		t.Fatal("expected snapshot, got nil")
	}
	prim := snap.Metrics["rate_limit_primary"]
	if prim.Used == nil || *prim.Used != 30 {
		t.Fatalf("primary used: %#v", prim)
	}
	sec := snap.Metrics["rate_limit_secondary"]
	if sec.Used == nil || *sec.Used != 10 {
		t.Fatalf("secondary used: %#v", sec)
	}
	// Verify window strings so quota.FormatWindowLabel produces 5h / 7d.
	if sec.Window != "10080m" {
		t.Fatalf("expected secondary window '10080m', got %v", sec.Window)
	}

	// primary_window / secondary_window suffix style.
	data2 := map[string]any{
		"rate_limit": map[string]any{
			"primary_window":   map[string]any{"used_percent": float64(50), "window_minutes": float64(300)},
			"secondary_window": map[string]any{"used_percent": float64(20), "window_minutes": float64(10080)},
		},
	}
	snap2 := codexWhamToSnapshot(data2)
	if snap2 == nil || snap2.Metrics["rate_limit_primary"].Used == nil {
		t.Fatalf("suffix style: expected snapshot, got %#v", snap2)
	}

	// Wham API uses limit_window_seconds (604800 = 7d), not window_minutes.
	data3 := map[string]any{
		"rate_limit": map[string]any{
			"primary_window": map[string]any{
				"used_percent":         float64(100),
				"limit_window_seconds": float64(604800),
				"reset_at":             float64(1779542577),
			},
		},
	}
	snap3 := codexWhamToSnapshot(data3)
	if snap3 == nil {
		t.Fatal("expected wham snapshot with limit_window_seconds")
	}
	if got := snap3.Metrics["rate_limit_primary"].Window; got != "10080m" {
		t.Fatalf("expected primary window 10080m, got %v", got)
	}
}

func TestMergeCodexSnapsKeepsSecondaryAttributes(t *testing.T) {
	used := 27.0
	primary := &readmodel.Snapshot{
		ProviderID: "codex",
		AccountID:  "acct",
		Status:     readmodel.StatusOK,
		Metrics:    map[string]readmodel.Metric{"rate_limit_codex_primary": {Used: &used, Unit: "%", Window: "5h"}},
		Attributes: map[string]any{"source": "codex-wham"},
	}
	secondary := &readmodel.Snapshot{
		ProviderID: "codex",
		AccountID:  "acct",
		Status:     readmodel.StatusOK,
		Metrics:    map[string]readmodel.Metric{"rate_limit_codex_bengalfox_primary": {Used: &used, Unit: "%", Window: "5h"}},
		Attributes: map[string]any{"rate_limit_codex_bengalfox_name": "GPT-5.3-Codex-Spark", "source": "codex-local"},
	}

	merged := mergeCodexSnaps(primary, secondary)
	if merged.Attributes["rate_limit_codex_bengalfox_name"] != "GPT-5.3-Codex-Spark" {
		t.Fatalf("secondary display-name attribute lost in merge: %#v", merged.Attributes)
	}
	if merged.Attributes["source"] != "codex-wham" {
		t.Fatalf("primary must win on attribute conflicts: %#v", merged.Attributes)
	}
	if len(merged.Metrics) != 2 {
		t.Fatalf("expected metric union, got %#v", merged.Metrics)
	}
}

func TestCodexReaderReturnsEmptyWithoutRateLimits(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(`{"payload":{"message":"hello"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	model, err := CodexReader{Root: root}.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(model.Snapshots) != 0 {
		t.Fatalf("expected empty read model, got %#v", model.Snapshots)
	}
}

// codexTestJWT builds a JWT with the given exp claim and a throwaway
// signature, sufficient for jwtExpiryUnix (which only parses the payload).
func codexTestJWT(exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp)))
	return header + "." + payload + ".sig"
}

func writeCodexAuthFixture(t *testing.T, root, accessToken string) string {
	t.Helper()
	authJSON := fmt.Sprintf(`{
		"auth_mode": "chatgpt",
		"OPENAI_API_KEY": "sk-test",
		"tokens": {
			"id_token": "old-id-token",
			"access_token": %q,
			"refresh_token": "old-refresh-token",
			"account_id": "acct-123"
		},
		"last_refresh": "2026-05-28T15:50:45.689999554Z"
	}`, accessToken)
	path := filepath.Join(root, "auth.json")
	if err := os.WriteFile(path, []byte(authJSON), 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
	return path
}

func TestCodexReaderSetsAccountIDHeaderOnWhamRequest(t *testing.T) {
	root := t.TempDir()
	writeCodexAuthFixture(t, root, "plain-access-token")

	var gotAccountID, gotAuth string
	whamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAccountID = req.Header.Get("chatgpt-account-id")
		gotAuth = req.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rate_limit":{"primary":{"used_percent":42,"window_minutes":300}}}`))
	}))
	defer whamServer.Close()

	model, err := CodexReader{Root: root, BaseURL: whamServer.URL}.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if gotAccountID != "acct-123" {
		t.Fatalf("expected chatgpt-account-id header 'acct-123', got %q", gotAccountID)
	}
	if gotAuth != "Bearer plain-access-token" {
		t.Fatalf("expected bearer access token, got %q", gotAuth)
	}
	snap := model.Snapshots["codex-cli"]
	if snap == nil || snap.Metrics["rate_limit_primary"].Used == nil || *snap.Metrics["rate_limit_primary"].Used != 42 {
		t.Fatalf("unexpected snapshot: %#v", model.Snapshots)
	}
}

func TestCodexReaderRefreshesExpiredTokenAndWritesBack(t *testing.T) {
	root := t.TempDir()
	expiredJWT := codexTestJWT(time.Now().Add(-1 * time.Hour).Unix())
	authPath := writeCodexAuthFixture(t, root, expiredJWT)

	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access-token","id_token":"new-id-token","refresh_token":"new-refresh-token"}`))
	}))
	defer refreshServer.Close()

	var gotAuth string
	whamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rate_limit":{"primary":{"used_percent":15,"window_minutes":300}}}`))
	}))
	defer whamServer.Close()

	model, err := CodexReader{Root: root, BaseURL: whamServer.URL, TokenURL: refreshServer.URL}.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if gotAuth != "Bearer new-access-token" {
		t.Fatalf("expected wham call with refreshed token, got %q", gotAuth)
	}
	snap := model.Snapshots["codex-cli"]
	if snap == nil || snap.Metrics["rate_limit_primary"].Used == nil {
		t.Fatalf("expected snapshot with quota, got %#v", model.Snapshots)
	}

	raw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read back auth.json: %v", err)
	}
	var updated map[string]any
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatalf("parse updated auth.json: %v", err)
	}
	tokens, _ := updated["tokens"].(map[string]any)
	if tokens == nil {
		t.Fatalf("expected tokens object, got %#v", updated)
	}
	if tokens["access_token"] != "new-access-token" || tokens["id_token"] != "new-id-token" || tokens["refresh_token"] != "new-refresh-token" {
		t.Fatalf("expected rotated tokens written back, got %#v", tokens)
	}
	if tokens["account_id"] != "acct-123" {
		t.Fatalf("expected account_id preserved, got %#v", tokens["account_id"])
	}
	if updated["auth_mode"] != "chatgpt" || updated["OPENAI_API_KEY"] != "sk-test" {
		t.Fatalf("expected auth_mode/OPENAI_API_KEY preserved, got %#v", updated)
	}
	if updated["last_refresh"] == "2026-05-28T15:50:45.689999554Z" {
		t.Fatalf("expected last_refresh to be updated")
	}
}

func TestCodexReaderNeedsAuthWhenRefreshFailsAndNoLocalData(t *testing.T) {
	root := t.TempDir()
	expiredJWT := codexTestJWT(time.Now().Add(-1 * time.Hour).Unix())
	writeCodexAuthFixture(t, root, expiredJWT)

	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer refreshServer.Close()

	model, err := CodexReader{Root: root, TokenURL: refreshServer.URL}.Read(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	snap := model.Snapshots["codex-cli"]
	if snap == nil {
		t.Fatalf("expected needs_auth status snapshot, got %#v", model.Snapshots)
	}
	if snap.Status != readmodel.StatusNeedsAuth {
		t.Fatalf("expected status %q, got %q", readmodel.StatusNeedsAuth, snap.Status)
	}
	if snap.Message == "" {
		t.Fatalf("expected a non-empty status message")
	}
}

func TestCodexReaderWriteBackFailureDoesNotUseNewToken(t *testing.T) {
	// writeCodexAuth must fail (and write nothing) when its destination
	// directory doesn't exist, so refreshCodexAuth's caller knows not to use
	// the rotated token it would otherwise have returned.
	badPath := filepath.Join(t.TempDir(), "missing-subdir", "auth.json")
	err := writeCodexAuth(badPath, map[string]any{
		"tokens": map[string]any{"access_token": "new-token"},
	})
	if err == nil {
		t.Fatal("expected error writing auth.json into a nonexistent directory")
	}
	if _, statErr := os.Stat(badPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected no file written, stat err = %v", statErr)
	}
}
