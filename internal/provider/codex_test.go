package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
