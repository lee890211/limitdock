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
	if cfg.OverlayOpacity != 100 {
		t.Fatalf("unexpected overlay opacity default: %#v", cfg)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	cfg := Defaults()
	cfg.DockMode = "overlay"
	cfg.DockEdge = "right"
	cfg.Theme = "night"
	cfg.OverlayOpacity = 72
	cfg.HiddenQuotaBands["codex"] = map[string]bool{"rate_limit_primary": true}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.DockMode != "overlay" || loaded.DockEdge != "right" || loaded.Theme != "night" || loaded.OverlayOpacity != 72 {
		t.Fatalf("round trip lost dock fields: %#v", loaded)
	}
	if !loaded.HiddenQuotaBands["codex"]["rate_limit_primary"] {
		t.Fatalf("round trip lost hidden band")
	}
	if b, err := os.ReadFile(path); err != nil || len(b) == 0 || b[len(b)-1] != '\n' {
		t.Fatalf("settings should be written as newline-terminated UTF-8 JSON")
	}
}

func TestNormalizeOverlayOpacity(t *testing.T) {
	cfg := Defaults()
	cfg.OverlayOpacity = 10
	cfg.Normalize()
	if cfg.OverlayOpacity != 35 {
		t.Fatalf("low opacity should clamp to 35, got %d", cfg.OverlayOpacity)
	}
	cfg.OverlayOpacity = 150
	cfg.Normalize()
	if cfg.OverlayOpacity != 100 {
		t.Fatalf("high opacity should clamp to 100, got %d", cfg.OverlayOpacity)
	}
	cfg.OverlayOpacity = 0
	cfg.Normalize()
	if cfg.OverlayOpacity != 100 {
		t.Fatalf("missing opacity should default to 100, got %d", cfg.OverlayOpacity)
	}
}
