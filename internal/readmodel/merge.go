package readmodel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Logger interface {
	Printf(format string, args ...any)
}

func FetchMerged(ctx context.Context, client Client, openUsageSettingsPath string, log Logger) (*ReadModel, error) {
	primary, _, err := client.Read(ctx, map[string]any{})
	if err != nil {
		return nil, err
	}
	if !NeedsCodexSupplement(primary.Snapshots) {
		return primary, nil
	}
	tw := OpenUsageTimeWindow(openUsageSettingsPath)
	supplement, _, err := client.Read(ctx, map[string]any{
		"time_window": tw,
		"accounts": []map[string]string{
			{"account_id": "codex-cli", "provider_id": "codex"},
		},
	})
	if err != nil {
		if log != nil {
			log.Printf("Codex read-model merge skipped: %v", err)
		}
		return primary, nil
	}
	primary.Snapshots = MergeSnapshots(primary.Snapshots, supplement.Snapshots, log)
	return primary, nil
}

func NeedsCodexSupplement(snapshots map[string]*Snapshot) bool {
	if len(snapshots) == 0 {
		return true
	}
	for _, snap := range snapshots {
		if snap == nil || strings.ToLower(strings.TrimSpace(snap.ProviderID)) != "codex" {
			continue
		}
		if CodexQuotaMetricCount(snap) > 0 {
			return false
		}
	}
	return true
}

func CodexQuotaMetricCount(s *Snapshot) int {
	if s == nil {
		return 0
	}
	n := 0
	for key := range s.Metrics {
		k := strings.ToLower(strings.TrimSpace(key))
		if k == "quota" || k == "quota_pro" || k == "quota_flash" || k == "usage_five_hour" || strings.HasPrefix(k, "usage_seven_day") || strings.HasPrefix(k, "rate_limit_") {
			n++
		}
	}
	return n
}

func MergeSnapshots(primary, supplement map[string]*Snapshot, log Logger) map[string]*Snapshot {
	out := map[string]*Snapshot{}
	for k, v := range primary {
		if k == "__invalid" {
			continue
		}
		if _, exists := out[k]; exists {
			if log != nil {
				log.Printf("Merge snapshot skip duplicate %s", k)
			}
			continue
		}
		out[k] = v
	}
	for k, snap := range supplement {
		if k == "__invalid" || snap == nil || snap.ProviderID != "codex" {
			continue
		}
		existing, exists := out[k]
		if exists && existing.ProviderID != "codex" {
			continue
		}
		primaryMetricCount, primaryQuotaCount := -1, 0
		if exists && existing != nil {
			primaryMetricCount = len(existing.Metrics)
			primaryQuotaCount = CodexQuotaMetricCount(existing)
		}
		fallbackMetricCount := len(snap.Metrics)
		fallbackQuotaCount := CodexQuotaMetricCount(snap)
		shouldPrefer := fallbackMetricCount > 0 && (!exists || primaryMetricCount <= 0 || (primaryQuotaCount <= 0 && fallbackQuotaCount > 0))
		if shouldPrefer || !exists {
			out[k] = snap
		}
	}
	sorted := make([]string, 0, len(out))
	for k := range out {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	ordered := map[string]*Snapshot{}
	for _, k := range sorted {
		ordered[k] = out[k]
	}
	return ordered
}

func OpenUsageSettingsPath() string {
	if appData := os.Getenv("APPDATA"); appData != "" {
		return filepath.Join(appData, "openusage", "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "AppData", "Roaming", "openusage", "settings.json")
}

func OpenUsageTimeWindow(path string) string {
	if path == "" {
		path = OpenUsageSettingsPath()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "30d"
	}
	var cfg struct {
		Data struct {
			TimeWindow string `json:"time_window"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "30d"
	}
	tw := strings.ToLower(strings.TrimSpace(cfg.Data.TimeWindow))
	if tw == "" {
		return "30d"
	}
	return tw
}
