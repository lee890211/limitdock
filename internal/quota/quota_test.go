package quota

import (
	"encoding/json"
	"strings"
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

func TestCardsIncludesNeedsAuthCardWithZeroBands(t *testing.T) {
	model := &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{
		"claude-code": {
			ProviderID: "claude_code",
			Status:     readmodel.StatusNeedsAuth,
			Message:    "access token expired",
			Metrics:    map[string]readmodel.Metric{},
		},
	}}
	cards := Cards(model, settings.Defaults())
	if len(cards) != 1 {
		t.Fatalf("needs_auth snapshot should render a card, got %#v", cards)
	}
	card := cards[0]
	if card.Status != readmodel.StatusNeedsAuth || card.Level != "critical" || card.Main != "Sign in" {
		t.Fatalf("unexpected needs_auth card: %#v", card)
	}
	if card.Detail != "sign-in required" {
		t.Fatalf("unexpected needs_auth detail: %q", card.Detail)
	}
	if card.Message != "access token expired" {
		t.Fatalf("snapshot message should carry through: %q", card.Message)
	}
}

func TestCardsIncludesStaleCardAndKeepsBands(t *testing.T) {
	model := &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{
		"claude-code": {
			ProviderID: "claude_code",
			Status:     readmodel.StatusStale,
			Metrics: map[string]readmodel.Metric{
				"usage_five_hour": metric(t, `{"unit":"%","used":40,"window":"5h"}`),
			},
		},
	}}
	cards := Cards(model, settings.Defaults())
	if len(cards) != 1 {
		t.Fatalf("stale snapshot should render a card, got %#v", cards)
	}
	card := cards[0]
	if card.Main != "60%" || card.Level != "warn" || card.Status != readmodel.StatusStale {
		t.Fatalf("stale card should keep last-known percent at warn level: %#v", card)
	}
	if !strings.HasPrefix(card.Detail, "stale") {
		t.Fatalf("stale marker should lead detail: %q", card.Detail)
	}
}

func TestCardsStaleKeepsCriticalLevelFromThreshold(t *testing.T) {
	snap := &readmodel.Snapshot{
		ProviderID: "claude_code",
		Status:     readmodel.StatusStale,
		Metrics: map[string]readmodel.Metric{
			"usage_five_hour": metric(t, `{"unit":"%","used":99,"window":"5h"}`),
		},
	}
	card := SnapshotToCard("claude-code", snap, settings.Defaults())
	if card.Level != "critical" {
		t.Fatalf("stale must not downgrade critical: %#v", card)
	}
}

func TestCardsErrorStatusRendersErrorCard(t *testing.T) {
	model := &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{
		"claude-code": {
			ProviderID: "claude_code",
			Status:     readmodel.StatusError,
			Message:    "usage API: HTTP 500",
			Metrics:    map[string]readmodel.Metric{},
		},
	}}
	cards := Cards(model, settings.Defaults())
	if len(cards) != 1 || cards[0].Main != "Error" || cards[0].Level != "critical" {
		t.Fatalf("error snapshot should render an error card: %#v", cards)
	}
}

func TestCardsExcludesOkZeroBandSnapshotUnchanged(t *testing.T) {
	model := &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{
		"claude-code": {
			ProviderID: "claude_code",
			Status:     readmodel.StatusOK,
			Metrics:    map[string]readmodel.Metric{},
		},
	}}
	if cards := Cards(model, settings.Defaults()); len(cards) != 0 {
		t.Fatalf("ok snapshot without bands must stay hidden: %#v", cards)
	}
}

func TestDisplayQuotaMetricKeyShowsAllCodexRateLimitRows(t *testing.T) {
	snap := &readmodel.Snapshot{ProviderID: "codex"}
	for _, key := range []string{
		"rate_limit_primary",
		"rate_limit_secondary",
		"rate_limit_code_review_primary",
		"rate_limit_gpt_5_3_codex_spark_primary",
		"rate_limit_bengalfox_secondary",
	} {
		if !DisplayQuotaMetricKey(snap, key) {
			t.Fatalf("codex rate_limit row %q should display", key)
		}
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
