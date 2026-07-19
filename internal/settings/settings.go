package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"limitdock/internal/fsutil"
)

type Settings struct {
	AutoHide         bool                       `json:"autoHide"`
	DockMode         string                     `json:"dockMode"`
	DockEdge         string                     `json:"dockEdge"`
	Theme            string                     `json:"theme"`
	OverlayOpacity   int                        `json:"overlayOpacity"`
	StartWithWindows bool                       `json:"startWithWindows"`
	HiddenQuotaBands map[string]map[string]bool `json:"hiddenQuotaBands"`
	GaugeMaxBands    int                        `json:"gaugeMaxBands"`
	GaugeWarnPercent int                        `json:"gaugeWarnPercent"`
	GaugeCritPercent int                        `json:"gaugeCritPercent"`
	RefreshSeconds   int                        `json:"refreshSeconds"`
}

func Defaults() Settings {
	return Settings{
		AutoHide:         false,
		DockMode:         "reserved",
		DockEdge:         "bottom",
		Theme:            "light",
		OverlayOpacity:   100,
		StartWithWindows: false,
		HiddenQuotaBands: map[string]map[string]bool{},
		GaugeMaxBands:    4,
		GaugeWarnPercent: 72,
		GaugeCritPercent: 90,
		RefreshSeconds:   30,
	}
}

func Load(path string) (Settings, error) {
	cfg := Defaults()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Defaults(), err
	}
	cfg.Normalize()
	return cfg, nil
}

func Save(path string, cfg Settings) error {
	cfg.Normalize()
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	// Atomic write: a crash mid-save must not truncate settings.json, which
	// Load would silently replace with defaults.
	return fsutil.AtomicWriteFile(path, b, 0o644)
}

func (s *Settings) Normalize() {
	d := Defaults()
	s.DockMode = strings.ToLower(strings.TrimSpace(s.DockMode))
	if s.DockMode != "reserved" && s.DockMode != "overlay" {
		s.DockMode = d.DockMode
	}
	s.DockEdge = strings.ToLower(strings.TrimSpace(s.DockEdge))
	switch s.DockEdge {
	case "top", "bottom", "left", "right":
	default:
		s.DockEdge = d.DockEdge
	}
	s.Theme = strings.ToLower(strings.TrimSpace(s.Theme))
	switch s.Theme {
	case "light", "night":
	default:
		s.Theme = d.Theme
	}
	if s.OverlayOpacity == 0 {
		s.OverlayOpacity = d.OverlayOpacity
	}
	if s.OverlayOpacity < 35 {
		s.OverlayOpacity = 35
	}
	if s.OverlayOpacity > 100 {
		s.OverlayOpacity = 100
	}
	if s.HiddenQuotaBands == nil {
		s.HiddenQuotaBands = map[string]map[string]bool{}
	}
	for k, v := range s.HiddenQuotaBands {
		if v == nil {
			s.HiddenQuotaBands[k] = map[string]bool{}
		}
	}
	if s.GaugeMaxBands < 1 {
		s.GaugeMaxBands = d.GaugeMaxBands
	}
	if s.GaugeMaxBands > 4 {
		s.GaugeMaxBands = 4
	}
	if s.GaugeWarnPercent <= 0 {
		s.GaugeWarnPercent = d.GaugeWarnPercent
	}
	if s.GaugeCritPercent <= 0 {
		s.GaugeCritPercent = d.GaugeCritPercent
	}
	if s.GaugeCritPercent < s.GaugeWarnPercent {
		s.GaugeCritPercent = s.GaugeWarnPercent
	}
	if s.RefreshSeconds < 5 {
		s.RefreshSeconds = d.RefreshSeconds
	}
}

func HiddenSet(s Settings, snapshotKey string) map[string]bool {
	key := strings.TrimSpace(snapshotKey)
	if key == "" {
		key = "__default"
	}
	if s.HiddenQuotaBands == nil {
		s.HiddenQuotaBands = map[string]map[string]bool{}
	}
	if s.HiddenQuotaBands[key] == nil {
		s.HiddenQuotaBands[key] = map[string]bool{}
	}
	return s.HiddenQuotaBands[key]
}
