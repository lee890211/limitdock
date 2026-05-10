package quota

import (
	"encoding/json"
	"testing"
	"time"

	"limitdock/internal/readmodel"
	"limitdock/internal/settings"
)

func metric(t *testing.T, js string) readmodel.Metric {
	t.Helper()
	var m readmodel.Metric
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatalf("metric json: %v", err)
	}
	return m
}

func TestRemainingPercentForms(t *testing.T) {
	if got := RemainingPercent(metric(t, `{"unit":"%","used":80}`), "plan_percent_used"); got == nil || *got != 20 {
		t.Fatalf("unit percent used should become remaining 20, got %v", got)
	}
	if got := RemainingPercent(metric(t, `{"limit":200,"remaining":50}`), "quota"); got == nil || *got != 25 {
		t.Fatalf("remaining/limit should become 25, got %v", got)
	}
	if got := RemainingPercent(metric(t, `{"limit":200,"used":50}`), "quota"); got == nil || *got != 75 {
		t.Fatalf("used/limit should become 75, got %v", got)
	}
}

func TestGeminiSpecificRowsSuppressAggregate(t *testing.T) {
	snap := &readmodel.Snapshot{
		ProviderID: "gemini_cli",
		Metrics: map[string]readmodel.Metric{
			"quota":             metric(t, `{"unit":"%","remaining":60}`),
			"quota_model_flash": metric(t, `{"unit":"%","remaining":10,"window":"daily"}`),
		},
	}
	card := SnapshotToCard("gemini", snap, settings.Defaults())
	if len(card.AllBands) != 1 {
		t.Fatalf("expected only model row, got %#v", card.AllBands)
	}
	if card.AllBands[0].Key != "quota_model_flash" || card.Main != "10%" {
		t.Fatalf("unexpected model row: %#v", card)
	}
}

func TestCursorPlanPercentUsed(t *testing.T) {
	reset := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	snap := &readmodel.Snapshot{
		ProviderID: "cursor",
		Metrics: map[string]readmodel.Metric{
			"plan_percent_used": metric(t, `{"unit":"%","used":82,"window":"billing-cycle"}`),
		},
		Resets: map[string]any{"billing_cycle_end": reset},
	}
	card := SnapshotToCard("cursor", snap, settings.Defaults())
	if card.Main != "18%" || len(card.Bands) != 1 || card.Bands[0].Caption == "" {
		t.Fatalf("unexpected cursor card: %#v", card)
	}
	if card.Bands[0].Reset == "" {
		t.Fatalf("cursor billing_cycle_end reset should be shown: %#v", card.Bands[0])
	}
}

func TestResetShortTextUsesLegacyResetCandidates(t *testing.T) {
	reset := time.Now().UTC().Add(6 * time.Hour).Format(time.RFC3339)
	snap := &readmodel.Snapshot{
		ProviderID: "gemini_cli",
		Metrics: map[string]readmodel.Metric{
			"quota_flash": metric(t, `{"unit":"%","remaining":42,"window":"daily"}`),
		},
		Resets: map[string]any{"quota_flash_reset": reset},
	}
	card := SnapshotToCard("gemini", snap, settings.Defaults())
	if len(card.Bands) != 1 || card.Bands[0].Reset == "" {
		t.Fatalf("gemini quota_flash_reset should be shown: %#v", card.Bands)
	}
}

func TestHiddenBandsKeepAllBandsForPicker(t *testing.T) {
	cfg := settings.Defaults()
	cfg.HiddenQuotaBands["codex"] = map[string]bool{"rate_limit_primary": true}
	snap := &readmodel.Snapshot{
		ProviderID: "codex",
		Metrics: map[string]readmodel.Metric{
			"rate_limit_primary":   metric(t, `{"unit":"%","remaining":30}`),
			"rate_limit_secondary": metric(t, `{"unit":"%","remaining":70}`),
		},
	}
	card := SnapshotToCard("codex", snap, cfg)
	if len(card.Bands) != 1 || card.Bands[0].Key != "rate_limit_secondary" {
		t.Fatalf("hidden row should be suppressed from visible bands: %#v", card.Bands)
	}
	if len(card.AllBands) != 2 {
		t.Fatalf("all bands should remain available for picker: %#v", card.AllBands)
	}
}

func TestFormatWindowLabelConvertsLargeMinuteWindows(t *testing.T) {
	tests := map[string]string{
		"300m":     "5h",
		"10080m":   "7d",
		"PT300M":   "5h",
		"PT10080M": "7d",
	}
	for input, want := range tests {
		if got := FormatWindowLabel(input); got != want {
			t.Fatalf("FormatWindowLabel(%q) = %q, want %q", input, got, want)
		}
	}
}
