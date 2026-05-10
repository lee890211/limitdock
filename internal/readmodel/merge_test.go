package readmodel

import "testing"

func TestNeedsCodexSupplementWhenPrimaryLacksQuota(t *testing.T) {
	snaps := map[string]*Snapshot{
		"codex-cli": {ProviderID: "codex", Metrics: map[string]Metric{"rpm": {}}},
	}
	if !NeedsCodexSupplement(snaps) {
		t.Fatalf("expected supplement when codex quota metrics are absent")
	}
}

func TestMergeSnapshotsPrefersFallbackQuota(t *testing.T) {
	primary := map[string]*Snapshot{
		"codex-cli": {ProviderID: "codex", Metrics: map[string]Metric{}},
	}
	fallback := map[string]*Snapshot{
		"codex-cli": {ProviderID: "codex", Metrics: map[string]Metric{"rate_limit_primary": {}}},
	}
	merged := MergeSnapshots(primary, fallback, nil)
	if CodexQuotaMetricCount(merged["codex-cli"]) != 1 {
		t.Fatalf("expected fallback quota snapshot, got %#v", merged["codex-cli"])
	}
}
