package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"limitdock/internal/readmodel"
)

const maxCodexSessionFiles = 160

var codexLogOnce sync.Map

type CodexReader struct {
	Root string
	Log  Logger
}

type codexSessionFile struct {
	Path    string
	ModTime time.Time
}

func (r CodexReader) Name() string {
	return "codex"
}

func (r CodexReader) FallbackProviderID() string {
	return "codex"
}

func (r CodexReader) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	root := strings.TrimSpace(r.Root)
	if root == "" {
		root = defaultCodexRoot()
	}
	files := codexSessionFiles(root)
	for _, file := range files {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		limits, err := latestCodexRateLimits(file.Path)
		if err != nil || limits == nil {
			continue
		}
		snap := codexSnapshot(limits)
		if snap == nil || len(snap.Metrics) == 0 {
			continue
		}
		return &readmodel.ReadModel{
			Snapshots: map[string]*readmodel.Snapshot{
				snapshotKey("codex", snap.AccountID): snap,
			},
		}, nil
	}
	logCodexOnce(r.Log, "missing", "Codex fallback reader skipped: no local Codex rate-limit rows found.")
	return emptyReadModel(), nil
}

func defaultCodexRoot() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".codex")
}

func codexSessionFiles(root string) []codexSessionFile {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	sessions := filepath.Join(root, "sessions")
	if info, err := os.Stat(sessions); err != nil || !info.IsDir() {
		return nil
	}
	out := []codexSessionFile{}
	_ = filepath.WalkDir(sessions, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() || strings.ToLower(filepath.Ext(path)) != ".jsonl" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, codexSessionFile{Path: path, ModTime: info.ModTime()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		return out[i].ModTime.After(out[j].ModTime)
	})
	if len(out) > maxCodexSessionFiles {
		out = out[:maxCodexSessionFiles]
	}
	return out
}

func latestCodexRateLimits(path string) (map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	var latest map[string]any
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, `"rate_limits"`) && !strings.Contains(line, `"rateLimits"`) {
			continue
		}
		var raw any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		found := []map[string]any{}
		collectCodexRateLimits(raw, &found)
		for _, item := range found {
			if item != nil {
				latest = item
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return latest, nil
}

func collectCodexRateLimits(v any, out *[]map[string]any) {
	switch x := v.(type) {
	case map[string]any:
		if limits := objectAny(x, "rate_limits", "rateLimits"); limits != nil {
			*out = append(*out, limits)
		}
		for _, child := range x {
			collectCodexRateLimits(child, out)
		}
	case []any:
		for _, child := range x {
			collectCodexRateLimits(child, out)
		}
	}
}

func codexSnapshot(limits map[string]any) *readmodel.Snapshot {
	if limits == nil {
		return nil
	}
	limitID := firstString(limits, "limit_id", "limitId", "limit_name", "limitName")
	accountID := firstString(limits, "account_id", "accountId", "email", "user")
	if accountID == "" {
		accountID = "cli"
	}
	metrics := map[string]readmodel.Metric{}
	resets := map[string]any{}
	attrs := map[string]any{"source": "codex-local"}
	if plan := firstString(limits, "plan_type", "planType"); plan != "" {
		attrs["plan_type"] = plan
	}
	if name := firstString(limits, "limit_name", "limitName"); name != "" {
		attrs[codexRateLimitNameAttr(limitID)] = name
	}

	for _, bucketName := range []string{"primary", "secondary"} {
		bucket := objectAny(limits, bucketName)
		if bucket == nil {
			continue
		}
		used, ok := firstNumber(bucket, "used_percent", "usedPercent", "used", "percent")
		if !ok {
			continue
		}
		key := codexRateLimitMetricKey(limitID, bucketName)
		metrics[key] = readmodel.Metric{
			Used:   floatPtr(used),
			Unit:   "%",
			Window: codexRateLimitWindow(bucket),
		}
		if reset, ok := codexResetValue(bucket); ok {
			resets[key+"_reset"] = reset
		}
	}

	if len(metrics) == 0 {
		return nil
	}
	return &readmodel.Snapshot{
		ProviderID: "codex",
		AccountID:  accountID,
		Status:     "ok",
		Metrics:    metrics,
		Resets:     resets,
		Attributes: attrs,
		Raw:        map[string]any{"source": "codex-local"},
	}
}

func codexRateLimitMetricKey(limitID, bucket string) string {
	bucket = strings.ToLower(strings.TrimSpace(bucket))
	if bucket != "secondary" {
		bucket = "primary"
	}
	id := codexSlug(limitID)
	if id == "" || id == "codex" {
		return "rate_limit_" + bucket
	}
	return "rate_limit_" + id + "_" + bucket
}

func codexRateLimitNameAttr(limitID string) string {
	id := codexSlug(limitID)
	if id == "" || id == "codex" {
		id = "primary"
	}
	return "rate_limit_" + id + "_name"
}

func codexRateLimitWindow(bucket map[string]any) any {
	if window := firstString(bucket, "window", "period"); window != "" {
		return window
	}
	mins, ok := firstNumber(bucket, "window_minutes", "windowMinutes")
	if !ok || mins <= 0 {
		return nil
	}
	if mins == float64(int64(mins)) {
		return strconv.FormatInt(int64(mins), 10) + "m"
	}
	return fmt.Sprintf("%.1fm", mins)
}

func codexResetValue(bucket map[string]any) (string, bool) {
	for _, key := range []string{"resets_at", "resetsAt", "reset_at", "resetAt"} {
		raw := bucket[key]
		if text, ok := raw.(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text), true
		}
		if ts, ok := numberAny(raw); ok {
			if ts > 1e12 {
				ts = ts / 1000
			}
			return time.Unix(int64(ts), 0).UTC().Format(time.RFC3339), true
		}
	}
	return "", false
}

func numberAny(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func codexSlug(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.NewReplacer("-", "_", ".", "_", "/", "_", " ", "_").Replace(s)
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	return strings.Trim(s, "_")
}

func logCodexOnce(log Logger, key string, format string, args ...any) {
	if log == nil {
		return
	}
	if _, loaded := codexLogOnce.LoadOrStore(key, true); loaded {
		return
	}
	log.Printf(format, args...)
}
