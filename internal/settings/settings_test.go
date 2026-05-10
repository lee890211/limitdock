package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSettingsExampleShape(t *testing.T) {
	path := filepath.Join("..", "..", "settings.example.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load example: %v", err)
	}
	if cfg.DockMode != "reserved" || cfg.DockEdge != "bottom" {
		t.Fatalf("unexpected dock defaults: %#v", cfg)
	}
	if cfg.GaugeMaxBands != 4 || cfg.RefreshSeconds != 30 {
		t.Fatalf("unexpected gauge/refresh defaults: %#v", cfg)
	}
	if cfg.Theme != "light" {
		t.Fatalf("unexpected theme default: %#v", cfg)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	cfg := Defaults()
	cfg.DockMode = "overlay"
	cfg.DockEdge = "right"
	cfg.Theme = "night"
	cfg.HiddenQuotaBands["codex"] = map[string]bool{"rate_limit_primary": true}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.DockMode != "overlay" || loaded.DockEdge != "right" || loaded.Theme != "night" {
		t.Fatalf("round trip lost dock fields: %#v", loaded)
	}
	if !loaded.HiddenQuotaBands["codex"]["rate_limit_primary"] {
		t.Fatalf("round trip lost hidden band")
	}
	if b, err := os.ReadFile(path); err != nil || len(b) == 0 || b[len(b)-1] != '\n' {
		t.Fatalf("settings should be written as newline-terminated UTF-8 JSON")
	}
}
