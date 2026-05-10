package provider

import (
	"context"
	"errors"
	"testing"

	"limitdock/internal/readmodel"
)

type fixedReader struct {
	name  string
	model *readmodel.ReadModel
	err   error
}

type fixedFallbackReader struct {
	fixedReader
	providerID string
}

func (r fixedReader) Name() string {
	return r.name
}

func (r fixedReader) Read(context.Context) (*readmodel.ReadModel, error) {
	return r.model, r.err
}

func (r fixedFallbackReader) FallbackProviderID() string {
	return r.providerID
}

func TestAggregatorMergesReaderSnapshots(t *testing.T) {
	model, err := Aggregator{Readers: []Reader{
		fixedReader{name: "openusage", model: &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{
			"codex-cli": {ProviderID: "codex", Metrics: map[string]readmodel.Metric{"rate_limit_primary": {}}},
		}}},
		fixedReader{name: "antigravity", model: &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{
			"antigravity-local": {ProviderID: "antigravity", Metrics: map[string]readmodel.Metric{"quota_model_gemini": {}}},
		}}},
	}}.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(model.Snapshots) != 2 || model.Snapshots["codex-cli"] == nil || model.Snapshots["antigravity-local"] == nil {
		t.Fatalf("unexpected merged snapshots: %#v", model.Snapshots)
	}
}

func TestAggregatorReturnsErrorWhenAllReadersFailOrEmpty(t *testing.T) {
	_, err := Aggregator{Readers: []Reader{
		fixedReader{name: "openusage", err: errors.New("socket missing")},
		fixedReader{name: "antigravity", model: &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{}}},
	}}.Read(context.Background())
	if err == nil {
		t.Fatalf("expected first reader error when no snapshots are available")
	}
}

func TestAggregatorKeepsFirstDuplicateSnapshot(t *testing.T) {
	model, err := Aggregator{Readers: []Reader{
		fixedReader{name: "openusage", model: &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{
			"shared": {ProviderID: "openusage"},
		}}},
		fixedReader{name: "custom", model: &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{
			"shared": {ProviderID: "custom"},
		}}},
	}}.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := model.Snapshots["shared"].ProviderID; got != "openusage" {
		t.Fatalf("expected first duplicate to win, got %q", got)
	}
}

func TestAggregatorSkipsFallbackWhenProviderAlreadyHasQuota(t *testing.T) {
	model, err := Aggregator{Readers: []Reader{
		fixedReader{name: "openusage", model: &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{
			"codex-openusage": {ProviderID: "codex", Metrics: map[string]readmodel.Metric{
				"rate_limit_primary": {Remaining: floatPtrForTest(80), Unit: "%"},
			}},
		}}},
		fixedFallbackReader{
			providerID: "codex",
			fixedReader: fixedReader{name: "codex", model: &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{
				"codex-cli": {ProviderID: "codex", Metrics: map[string]readmodel.Metric{
					"rate_limit_primary": {Remaining: floatPtrForTest(40), Unit: "%"},
				}},
			}}},
		},
	}}.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if model.Snapshots["codex-openusage"] == nil {
		t.Fatalf("expected OpenUsage snapshot to remain")
	}
	if model.Snapshots["codex-cli"] != nil {
		t.Fatalf("expected Codex fallback snapshot to be skipped")
	}
}

func TestAggregatorUsesFallbackWhenProviderHasNoQuota(t *testing.T) {
	model, err := Aggregator{Readers: []Reader{
		fixedReader{name: "openusage", model: &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{
			"codex-openusage": {ProviderID: "codex", Metrics: map[string]readmodel.Metric{}},
		}}},
		fixedFallbackReader{
			providerID: "codex",
			fixedReader: fixedReader{name: "codex", model: &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{
				"codex-cli": {ProviderID: "codex", Metrics: map[string]readmodel.Metric{
					"rate_limit_primary": {Remaining: floatPtrForTest(40), Unit: "%"},
				}},
			}}},
		},
	}}.Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if model.Snapshots["codex-cli"] == nil {
		t.Fatalf("expected Codex fallback snapshot when OpenUsage has no quota")
	}
}

func floatPtrForTest(v float64) *float64 {
	return &v
}
