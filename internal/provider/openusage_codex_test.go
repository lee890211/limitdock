package provider

import (
	"testing"

	"limitdock/internal/readmodel"
)

func TestNeedsCodexSupplementWhenPrimaryLacksQuota(t *testing.T) {
	snaps := map[string]*readmodel.Snapshot{
		"codex-cli": {ProviderID: "codex", Metrics: map[string]readmodel.Metric{"rpm": {}}},
	}
	if !needsCodexSupplement(snaps) {
		t.Fatalf("expected supplement when codex quota metrics are absent")
	}
}

func TestMergeCodexSnapshotsPrefersFallbackQuota(t *testing.T) {
	primary := map[string]*readmodel.Snapshot{
		"codex-cli": {ProviderID: "codex", Metrics: map[string]readmodel.Metric{}},
	}
	fallback := map[string]*readmodel.Snapshot{
		"codex-cli": {ProviderID: "codex", Metrics: map[string]readmodel.Metric{"rate_limit_primary": {}}},
	}
	merged := mergeCodexSnapshots(primary, fallback, nil)
	if codexQuotaMetricCount(merged["codex-cli"]) != 1 {
		t.Fatalf("expected fallback quota snapshot, got %#v", merged["codex-cli"])
	}
}
