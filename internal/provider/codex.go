package provider

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"limitdock/internal/readmodel"

	_ "modernc.org/sqlite"
)

const (
	maxCodexSessionFiles = 160
	codexLogLookback     = 7 * 24 * time.Hour
	codexLogQueryLimit   = 64
)

var codexLogOnce sync.Map

type CodexReader struct {
	Root string
	Log  Logger
}

type codexSessionFile struct {
	Path    string
	ModTime time.Time
}

type codexRateLimitEvent struct {
	Limits map[string]any
	At     time.Time
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

	whamSnap := r.tryWham(ctx, root)

	// Also scan local JSONL/sqlite to fill in any windows the wham API omits
	// (wham often returns only the primary 5h window; local events carry the 7d secondary).
	event, err := latestCodexRateLimitEvent(ctx, root)
	if err != nil {
		logCodexOnce(r.Log, "read", "Codex fallback reader had trouble reading local rate-limit rows: %v", err)
	}
	var localSnap *readmodel.Snapshot
	if event != nil {
		localSnap = codexSnapshot(event.Limits)
	}

	snap := mergeCodexSnaps(whamSnap, localSnap)
	if snap == nil || len(snap.Metrics) == 0 {
		logCodexOnce(r.Log, "missing", "Codex reader skipped: no local rate-limit rows found and wham API unavailable.")
		return emptyReadModel(), nil
	}
	return &readmodel.ReadModel{
		Snapshots: map[string]*readmodel.Snapshot{
			snapshotKey("codex", snap.AccountID): snap,
		},
	}, nil
}

// mergeCodexSnaps combines two Codex snapshots. primary wins for any metric key
// present in both; secondary fills in keys that primary lacks.
func mergeCodexSnaps(primary, secondary *readmodel.Snapshot) *readmodel.Snapshot {
	if primary == nil || len(primary.Metrics) == 0 {
		return secondary
	}
	if secondary == nil || len(secondary.Metrics) == 0 {
		return primary
	}
	merged := &readmodel.Snapshot{
		ProviderID: primary.ProviderID,
		AccountID:  primary.AccountID,
		Status:     primary.Status,
		Metrics:    make(map[string]readmodel.Metric, len(primary.Metrics)+len(secondary.Metrics)),
		Resets:     map[string]any{},
		Raw:        primary.Raw,
		Attributes: primary.Attributes,
	}
	for k, v := range secondary.Metrics {
		merged.Metrics[k] = v
	}
	for k, v := range primary.Metrics {
		if existing, ok := merged.Metrics[k]; ok {
			merged.Metrics[k] = preferCodexMetric(existing, v)
			continue
		}
		merged.Metrics[k] = v
	}
	for k, v := range secondary.Resets {
		merged.Resets[k] = v
	}
	for k, v := range primary.Resets {
		merged.Resets[k] = v
	}
	return merged
}

func preferCodexMetric(existing, incoming readmodel.Metric) readmodel.Metric {
	ew := strings.TrimSpace(readmodel.String(existing.Window))
	iw := strings.TrimSpace(readmodel.String(incoming.Window))
	switch {
	case ew == "" && iw != "":
		return incoming
	case ew != "" && iw == "":
		return existing
	case incoming.Used != nil && existing.Used == nil:
		return incoming
	case existing.Used != nil && incoming.Used == nil:
		return existing
	default:
		return incoming
	}
}

func (r CodexReader) tryWham(ctx context.Context, root string) *readmodel.Snapshot {
	token, err := readCodexAuthToken(root)
	if err != nil {
		return nil
	}
	data, err := fetchCodexWhamUsage(ctx, token)
	if err != nil {
		return nil
	}
	return codexWhamToSnapshot(data)
}

func readCodexAuthToken(root string) (string, error) {
	authPath := filepath.Join(root, "auth.json")
	b, err := os.ReadFile(authPath)
	if err != nil {
		return "", err
	}
	var auth struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(b, &auth); err != nil {
		return "", err
	}
	token := strings.TrimSpace(auth.Tokens.AccessToken)
	if token == "" {
		return "", fmt.Errorf("no access_token in codex auth.json")
	}
	return token, nil
}

func fetchCodexWhamUsage(ctx context.Context, token string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://chatgpt.com/backend-api/wham/usage", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("wham: HTTP %s", resp.Status)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("wham: decoding response: %w", err)
	}
	return out, nil
}

// codexWhamToSnapshot converts wham API response to a Snapshot.
// Accepted shapes (rate_limit key may be "rate_limit" or "rateLimit"):
//
//	{"rate_limit": {"primary_window": {...}, "secondary_window": {...}}}
//	{"rate_limit": {"primary": {...}, "secondary": {...}}}
func codexWhamToSnapshot(data map[string]any) *readmodel.Snapshot {
	rl := objectAny(data, "rate_limit", "rateLimit")
	if rl == nil {
		return nil
	}
	limits := map[string]any{"limit_id": "codex"}
	for _, name := range []string{"primary", "secondary"} {
		win := objectAny(rl, name+"_window", name+"Window", name)
		if win == nil {
			continue
		}
		limits[name] = win
	}
	snap := codexSnapshot(limits)
	if snap != nil {
		snap.Raw = map[string]any{"source": "codex-wham"}
	}
	return snap
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

func latestCodexRateLimitEvent(ctx context.Context, root string) (*codexRateLimitEvent, error) {
	var latest *codexRateLimitEvent
	var firstErr error
	if event, err := latestCodexLogRateLimitEvent(ctx, root, time.Now().UTC()); err != nil {
		firstErr = err
	} else {
		latest = newerCodexRateLimitEvent(latest, event)
	}
	for _, file := range codexSessionFiles(root) {
		select {
		case <-ctx.Done():
			return latest, ctx.Err()
		default:
		}
		event, err := latestCodexSessionRateLimitEvent(file.Path)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// Use the file's modification time as a fallback when the event has no
		// embedded timestamp. Without this, iterating newest-first session files
		// with zero timestamps always replaces the result with data from older files.
		if event != nil && event.At.IsZero() && !file.ModTime.IsZero() {
			event.At = file.ModTime
		}
		latest = newerCodexRateLimitEvent(latest, event)
	}
	return latest, firstErr
}

func latestCodexLogRateLimitEvent(ctx context.Context, root string, now time.Time) (*codexRateLimitEvent, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, nil
	}
	dbPath := filepath.Join(root, "logs_2.sqlite")
	if info, err := os.Stat(dbPath); err != nil || info.IsDir() {
		return nil, nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer cancel()
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(dbPath))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(queryCtx, `
SELECT ts, ts_nanos, feedback_log_body
FROM logs
WHERE ts >= ?
  AND target = 'codex_api::endpoint::responses_websocket'
  AND feedback_log_body LIKE '%websocket event: {"type":"codex.rate_limits"%'
ORDER BY ts DESC, ts_nanos DESC
LIMIT ?`, now.Add(-codexLogLookback).Unix(), codexLogQueryLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var latest *codexRateLimitEvent
	for rows.Next() {
		var sec, nanos int64
		var body sql.NullString
		if err := rows.Scan(&sec, &nanos, &body); err != nil {
			return latest, err
		}
		if !body.Valid {
			continue
		}
		at := time.Unix(sec, nanos).UTC()
		for _, event := range codexRateLimitEventsFromText(body.String, at) {
			latest = newerCodexRateLimitEvent(latest, event)
		}
		if latest != nil {
			return latest, rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return latest, err
	}
	return latest, nil
}

func sqliteReadOnlyDSN(path string) string {
	return "file:" + filepath.ToSlash(path) + "?mode=ro&cache=private"
}

func latestCodexSessionRateLimitEvent(path string) (*codexRateLimitEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	var latest *codexRateLimitEvent
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, `"rate_limits"`) && !strings.Contains(line, `"rateLimits"`) {
			continue
		}
		var raw any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		at := codexEventTime(raw)
		found := []map[string]any{}
		collectCodexRateLimits(raw, &found)
		for _, item := range found {
			if item != nil {
				latest = &codexRateLimitEvent{Limits: normalizeCodexRateLimits(item, nil), At: at}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return latest, nil
}

func codexRateLimitEventsFromText(text string, at time.Time) []*codexRateLimitEvent {
	const marker = `{"type":"codex.rate_limits"`
	events := []*codexRateLimitEvent{}
	for {
		idx := strings.Index(text, marker)
		if idx < 0 {
			return events
		}
		obj, ok := extractJSONObject(text[idx:])
		if !ok {
			text = text[idx+len(marker):]
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(obj), &raw); err == nil {
			if limits := objectAny(raw, "rate_limits", "rateLimits"); limits != nil {
				events = append(events, &codexRateLimitEvent{
					Limits: normalizeCodexRateLimits(limits, raw),
					At:     at,
				})
			}
		}
		text = text[idx+len(obj):]
	}
}

func extractJSONObject(text string) (string, bool) {
	depth := 0
	inString := false
	escape := false
	for i, r := range text {
		if inString {
			if escape {
				escape = false
				continue
			}
			switch r {
			case '\\':
				escape = true
			case '"':
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[:i+1], true
			}
		}
	}
	return "", false
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

func normalizeCodexRateLimits(limits map[string]any, event map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range limits {
		out[key] = value
	}
	if firstString(out, "limit_id", "limitId", "limit_name", "limitName") == "" {
		out["limit_id"] = "codex"
	}
	if event != nil {
		if firstString(out, "plan_type", "planType") == "" {
			if plan := firstString(event, "plan_type", "planType"); plan != "" {
				out["plan_type"] = plan
			}
		}
	}
	return out
}

func codexEventTime(raw any) time.Time {
	m, ok := raw.(map[string]any)
	if !ok {
		return time.Time{}
	}
	if ts := firstString(m, "timestamp", "time"); ts != "" {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			return t.UTC()
		}
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func newerCodexRateLimitEvent(a, b *codexRateLimitEvent) *codexRateLimitEvent {
	if b == nil {
		return a
	}
	if a == nil {
		return b
	}
	if a.At.IsZero() {
		return b
	}
	if b.At.IsZero() {
		return a
	}
	if b.At.After(a.At) {
		return b
	}
	return a
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
	if secs, ok := firstNumber(bucket, "limit_window_seconds", "window_seconds", "limitWindowSeconds"); ok && secs > 0 {
		mins := secs / 60
		if mins == float64(int64(mins)) {
			return strconv.FormatInt(int64(mins), 10) + "m"
		}
		return fmt.Sprintf("%.1fm", mins)
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
