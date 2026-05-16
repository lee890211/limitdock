package openusage

import (
	"context"
	"strconv"
	"strings"

	"limitdock/internal/readmodel"
)

type Reader struct {
	Client readmodel.Client
}

func (r Reader) Name() string {
	return "openusage"
}

func (r Reader) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	model, _, err := r.Client.Read(ctx, map[string]any{})
	if err != nil {
		return model, err
	}
	normalizeClaudeCodeModel(model)
	return model, nil
}

// normalizeClaudeCodeModel synthesizes a usage_five_hour quota metric for
// claude_code snapshots when OpenUsage only exposes block_progress_pct in
// attributes rather than structured quota metrics.
func normalizeClaudeCodeModel(model *readmodel.ReadModel) {
	if model == nil {
		return
	}
	for _, snap := range model.Snapshots {
		normalizeClaudeCodeSnapshot(snap)
	}
}

func normalizeClaudeCodeSnapshot(snap *readmodel.Snapshot) {
	if snap == nil || strings.ToLower(snap.ProviderID) != "claude_code" {
		return
	}
	for key := range snap.Metrics {
		k := strings.ToLower(strings.TrimSpace(key))
		if k == "usage_five_hour" || strings.HasPrefix(k, "usage_seven_day") ||
			k == "quota" || strings.HasPrefix(k, "quota_") || strings.HasPrefix(k, "rate_limit_") {
			return
		}
	}
	pctStr := readmodel.AttrString(snap.Attributes, "block_progress_pct")
	if pctStr == "" {
		return
	}
	pct, err := strconv.ParseFloat(pctStr, 64)
	if err != nil || pct < 0 || pct > 100 {
		return
	}
	remaining := 100.0 - pct
	if snap.Metrics == nil {
		snap.Metrics = map[string]readmodel.Metric{}
	}
	snap.Metrics["usage_five_hour"] = readmodel.Metric{
		Remaining: &remaining,
		Unit:      "%",
		Window:    "5h",
	}
	if snap.Resets == nil {
		snap.Resets = map[string]any{}
	}
	const resetKey = "usage_five_hour_reset"
	if _, ok := snap.Resets[resetKey]; !ok {
		if v, ok := snap.Resets["billing_block"]; ok {
			snap.Resets[resetKey] = v
		} else if blockEnd := readmodel.AttrString(snap.Attributes, "block_end"); blockEnd != "" {
			snap.Resets[resetKey] = blockEnd
		}
	}
}
