package provider

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"limitdock/internal/quota"
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
	at := time.Date(2026, 5, 16, 12, 2, 53, 850000000, time.UTC)
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
	// file mod time, so the newest file wins.
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
	if err := os.WriteFile(filepath.Join(old, "session.jsonl"), []byte(oldLine+"\n"), 0o644); err != nil {
		t.Fatalf("write old: %v", err)
	}
	if err := os.WriteFile(filepath.Join(new, "session.jsonl"), []byte(newLine+"\n"), 0o644); err != nil {
		t.Fatalf("write new: %v", err)
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
