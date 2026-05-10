package quota

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"limitdock/internal/readmodel"
	"limitdock/internal/settings"
)

type Card struct {
	Name           string
	Main           string
	Detail         string
	Level          string
	Bands          []Band
	AllBands       []Band
	Message        string
	SnapshotKey    string
	ProviderID     string
	QuotaRotateCue bool
}

type Band struct {
	Key           string
	Caption       string
	Percent       *float64
	Unit          string
	Window        string
	Reset         string
	DisplayDetail string
}

func Cards(model *readmodel.ReadModel, cfg settings.Settings) []Card {
	if model == nil {
		return nil
	}
	keys := make([]string, 0, len(model.Snapshots))
	for key := range model.Snapshots {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]Card, 0, len(keys))
	for _, key := range keys {
		card := SnapshotToCard(key, model.Snapshots[key], cfg)
		if countPercentBands(card.AllBands) > 0 {
			out = append(out, card)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func SnapshotToCard(key string, snap *readmodel.Snapshot, cfg settings.Settings) Card {
	cfg.Normalize()
	cap := cfg.GaugeMaxBands
	providerID := ""
	if snap != nil {
		providerID = snap.ProviderID
	}
	name := providerName(providerID, key)
	bands := CollectBands(snap, cfg, cap, key, false)
	allBands := CollectBands(snap, cfg, 128, key, true)
	lowest := 100.0
	for _, band := range bands {
		if band.Percent != nil && *band.Percent < lowest {
			lowest = *band.Percent
		}
	}
	var primary *float64
	if len(bands) > 0 {
		primary = bands[0].Percent
	}
	main := "Loading"
	if primary != nil {
		main = fmt.Sprintf("%s%%", trimFloat(*primary))
	}
	metered := countPercentBands(bands) > 0
	level := "ok"
	if !metered {
		level = "status"
	} else {
		used := 100.0 - lowest
		if used >= float64(cfg.GaugeCritPercent) {
			level = "critical"
		} else if used >= float64(cfg.GaugeWarnPercent) {
			level = "warn"
		}
	}
	detailParts := []string{}
	if len(bands) > 0 {
		if bands[0].Window != "" {
			detailParts = append(detailParts, bands[0].Window)
		}
		if bands[0].Reset != "" {
			detailParts = append(detailParts, bands[0].Reset)
		}
	}
	if main == "Loading" && len(detailParts) == 0 {
		detailParts = append(detailParts, "waiting for telemetry")
	}
	message := ""
	if snap != nil {
		message = snap.Message
	}
	if message == "" && main == "Loading" {
		message = "Waiting for OpenUsage telemetry."
	}
	return Card{
		Name:           name,
		Main:           main,
		Detail:         strings.Join(detailParts, " | "),
		Level:          level,
		Bands:          bands,
		AllBands:       allBands,
		Message:        message,
		SnapshotKey:    key,
		ProviderID:     providerID,
		QuotaRotateCue: CountRemainingQuotaBands(snap) > cap,
	}
}

func CollectBands(snap *readmodel.Snapshot, cfg settings.Settings, cap int, key string, includeHidden bool) []Band {
	if snap == nil || len(snap.Metrics) == 0 || cap <= 0 {
		return nil
	}
	hidden := settings.HiddenSet(cfg, key)
	rows := make([]Band, 0, len(snap.Metrics))
	seen := map[string]bool{}
	priority := []string{
		"rate_limit_primary",
		"usage_five_hour",
		"usage_seven_day",
		"usage_seven_day_sonnet",
		"usage_seven_day_opus",
		"usage_seven_day_cowork",
		"quota",
		"quota_pro",
		"quota_flash",
		"plan_percent_used",
		"rate_limit_secondary",
		"rate_limit_code_review_primary",
		"rate_limit_code_review_secondary",
		"completions_quota",
		"chat_quota",
	}
	for _, name := range priority {
		metric, ok := snap.Metrics[name]
		if !ok {
			continue
		}
		if band, ok := bandFromMetric(snap, name, metric); ok {
			rows = append(rows, band)
			seen[name] = true
		}
	}
	keys := make([]string, 0, len(snap.Metrics))
	for name := range snap.Metrics {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if seen[name] {
			continue
		}
		if band, ok := bandFromMetric(snap, name, snap.Metrics[name]); ok {
			rows = append(rows, band)
		}
	}
	providerID := ""
	if snap != nil {
		providerID = snap.ProviderID
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		for _, cmp := range []int{
			compareFloat(float64(ribbonSortTier(providerID, a.Key)), float64(ribbonSortTier(providerID, b.Key))),
			compareFloat(sortHours(providerID, a.Key, a.Window), sortHours(providerID, b.Key, b.Window)),
			comparePtr(a.Percent, b.Percent),
			strings.Compare(a.Key, b.Key),
		} {
			if cmp != 0 {
				return cmp < 0
			}
		}
		return false
	})
	if !includeHidden && len(hidden) > 0 {
		filtered := rows[:0]
		for _, row := range rows {
			if !hidden[row.Key] {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	exhausted := []Band{}
	rotatable := []Band{}
	for _, row := range rows {
		if row.Percent == nil {
			rotatable = append(rotatable, row)
			continue
		}
		if *row.Percent <= 0.0001 {
			exhausted = append(exhausted, row)
		} else {
			rotatable = append(rotatable, row)
		}
	}
	if len(exhausted) < cap {
		rows = append(exhausted, rotatable...)
	} else {
		rows = append(exhausted, rotatable...)
	}
	if len(rows) > cap {
		rows = rows[:cap]
	}
	return rows
}

func CountRemainingQuotaBands(snap *readmodel.Snapshot) int {
	if snap == nil {
		return 0
	}
	n := 0
	for key, metric := range snap.Metrics {
		if !DisplayQuotaMetricKey(snap, key) || PreferSpecificQuotaModelRows(snap, key) || ThroughputOrCallRateMetricKey(key) {
			continue
		}
		if RemainingPercent(metric, key) != nil {
			n++
		}
	}
	return n
}

func bandFromMetric(snap *readmodel.Snapshot, key string, metric readmodel.Metric) (Band, bool) {
	if !DisplayQuotaMetricKey(snap, key) || PreferSpecificQuotaModelRows(snap, key) || ThroughputOrCallRateMetricKey(key) {
		return Band{}, false
	}
	pct := RemainingPercent(metric, key)
	if pct == nil {
		return Band{}, false
	}
	p := math.Min(100, math.Max(0, *pct))
	window := optimizeCodexWindow(snap.ProviderID, key, readmodel.String(metric.Window))
	reset := ResetShortText(snap, key, time.Now())
	return Band{
		Key:     key,
		Caption: FormatCaption(snap, key, window, reset),
		Percent: &p,
		Unit:    metric.Unit,
		Window:  window,
		Reset:   reset,
	}, true
}

func ThroughputOrCallRateMetricKey(raw string) bool {
	k := strings.ToLower(strings.TrimSpace(raw))
	if k == "" {
		return false
	}
	for _, blocked := range []string{"rpm", "tpm", "window_requests"} {
		if k == blocked {
			return true
		}
	}
	for _, needle := range []string{"calls_per_min", "callsperminute", "tokens_per_min", "_per_min", "_per_minute"} {
		if strings.Contains(k, needle) {
			return true
		}
	}
	return regexp.MustCompile(`(?i)(\b|^|\.|_|,)tool.?calls?_?rate|(^|[._-])calls_rate(?:[._-]|$)|\btool_calls?_rate\b`).MatchString(k)
}

func QuotaFamilyMetricKey(raw string) bool {
	k := strings.ToLower(strings.TrimSpace(raw))
	return k == "quota" || strings.HasPrefix(k, "quota_") || strings.HasPrefix(k, "rate_limit_") || k == "usage_five_hour" || strings.HasPrefix(k, "usage_seven_day") || k == "plan_percent_used" || strings.HasSuffix(k, "_quota")
}

func DisplayQuotaMetricKey(snap *readmodel.Snapshot, raw string) bool {
	if !QuotaFamilyMetricKey(raw) {
		return false
	}
	k := strings.ToLower(strings.TrimSpace(raw))
	provider := ""
	if snap != nil {
		provider = strings.ToLower(strings.TrimSpace(snap.ProviderID))
	}
	if provider == "codex" && strings.HasPrefix(k, "rate_limit_") {
		core := strings.TrimPrefix(k, "rate_limit_")
		if strings.HasSuffix(core, "_primary") || strings.HasSuffix(core, "_secondary") {
			return true
		}
		core = regexp.MustCompile(`_(primary|secondary)$`).ReplaceAllString(core, "")
		if core == "primary" || core == "secondary" || core == "code_review" {
			return true
		}
	}
	if k == "plan_percent_used" {
		return provider == "cursor"
	}
	return true
}

func PreferSpecificQuotaModelRows(snap *readmodel.Snapshot, raw string) bool {
	k := strings.ToLower(strings.TrimSpace(raw))
	if k != "quota" && k != "quota_pro" && k != "quota_flash" {
		return false
	}
	if snap == nil {
		return false
	}
	for mk := range snap.Metrics {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(mk)), "quota_model_") {
			return true
		}
	}
	return false
}

func RemainingPercent(metric readmodel.Metric, key string) *float64 {
	if ThroughputOrCallRateMetricKey(key) {
		return nil
	}
	var pct *float64
	if metric.Unit == "%" && metric.Remaining != nil {
		v := *metric.Remaining
		pct = &v
	} else if metric.Unit == "%" && metric.Used != nil {
		v := 100.0 - *metric.Used
		pct = &v
	} else if metric.Limit != nil && *metric.Limit > 0 && metric.Remaining != nil {
		v := 100.0 * *metric.Remaining / *metric.Limit
		pct = &v
	} else if metric.Limit != nil && *metric.Limit > 0 && metric.Used != nil {
		v := 100.0 - (100.0 * *metric.Used / *metric.Limit)
		pct = &v
	}
	if pct == nil {
		return nil
	}
	v := math.Min(100, math.Max(0, *pct))
	return &v
}

func FormatWindowLabel(v any) string {
	w := strings.TrimSpace(readmodel.String(v))
	if w == "" {
		return ""
	}
	s := strings.ToLower(w)
	if s == "all" || s == "lifetime" {
		return "total"
	}
	if m := regexp.MustCompile(`^pt(\d+(?:\.\d+)?)h$`).FindStringSubmatch(s); len(m) == 2 {
		return trimFloatString(m[1]) + "h"
	}
	if m := regexp.MustCompile(`^pt(\d+(?:\.\d+)?)m$`).FindStringSubmatch(s); len(m) == 2 {
		mins, _ := strconv.ParseFloat(m[1], 64)
		if math.Mod(mins, 60) == 0 {
			return trimFloat(mins/60) + "h"
		}
		return trimFloat(mins) + "m"
	}
	if m := regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(hour|hours|h)(\s+rolling)?$`).FindStringSubmatch(s); len(m) >= 2 {
		return trimFloatString(m[1]) + "h"
	}
	if m := regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(minute|minutes|min|m)(\s+window)?$`).FindStringSubmatch(s); len(m) >= 2 {
		return trimFloatString(m[1]) + "m"
	}
	if m := regexp.MustCompile(`^(\d+)\s*d(ay)?s?$`).FindStringSubmatch(s); len(m) >= 2 {
		return m[1] + "d"
	}
	if s == "daily" || s == "day" {
		return "1d"
	}
	if s == "weekly" || s == "week" {
		return "7d"
	}
	if s == "billing-cycle" {
		return "cycle"
	}
	if len(w) <= 10 {
		return w
	}
	return w[:10]
}

func ResetShortText(snap *readmodel.Snapshot, key string, now time.Time) string {
	if snap == nil {
		return ""
	}
	for _, name := range resetCandidateNames(key) {
		t, ok := snap.ResetTime(name)
		if !ok {
			continue
		}
		return formatResetShort(t, now)
	}
	return ""
}

func resetCandidateNames(key string) []string {
	names := []string{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		for _, existing := range names {
			if existing == name {
				return
			}
		}
		names = append(names, name)
	}
	add(key + "_reset")
	add(key)
	switch key {
	case "quota":
		add("quota_reset")
	case "quota_flash":
		add("quota_flash_reset")
	case "quota_pro":
		add("quota_pro_reset")
	case "plan_percent_used":
		add("billing_cycle_end")
	}
	return names
}

func formatResetShort(t time.Time, now time.Time) string {
	d := t.Sub(now.UTC())
	if d <= 0 {
		return "rolling"
	}
	if d < 2*time.Minute {
		return "~now"
	}
	if d < time.Hour {
		return fmt.Sprintf("~%sm", trimFloat(math.Max(1, d.Minutes())))
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("~%sh", trimFloat(d.Hours()))
	}
	return fmt.Sprintf("~%sd", trimFloat(d.Hours()/24))
}

func FormatCaption(snap *readmodel.Snapshot, rawKey string, window any, resetText string) string {
	k := strings.ToLower(strings.TrimSpace(rawKey))
	model := ""
	if strings.HasPrefix(k, "rate_limit_") {
		core := strings.TrimPrefix(k, "rate_limit_")
		core = regexp.MustCompile(`_(primary|secondary)$`).ReplaceAllString(core, "")
		rawNameKey := "rate_limit_" + core + "_name"
		if snap != nil {
			model = readmodel.AttrString(snap.Raw, rawNameKey)
			if model == "" {
				model = readmodel.AttrString(snap.Attributes, rawNameKey)
			}
		}
		if model == "" && core != "primary" && core != "secondary" && core != "code_review" {
			model = core
		}
	} else if strings.HasPrefix(k, "quota_model_") {
		model = strings.TrimPrefix(k, "quota_model_")
		model = regexp.MustCompile(`_(requests|tokens|input_tokens|output_tokens|total_tokens)$`).ReplaceAllString(model, "")
	} else if k == "quota" || strings.HasSuffix(k, "_quota") {
		model = ""
	} else if strings.HasPrefix(k, "quota_") {
		model = strings.TrimPrefix(k, "quota_")
	} else if k == "usage_five_hour" {
		model = "claude"
		if strings.TrimSpace(readmodel.String(window)) == "" {
			window = "5h"
		}
	} else if strings.HasPrefix(k, "usage_seven_day") {
		switch {
		case strings.Contains(k, "sonnet"):
			model = "sonnet"
		case strings.Contains(k, "opus"):
			model = "opus"
		case strings.Contains(k, "cowork"):
			model = "team"
		default:
			model = "claude"
		}
		if strings.TrimSpace(readmodel.String(window)) == "" {
			window = "7d"
		}
	} else if k == "plan_percent_used" {
		model = "plan"
	}
	model = strings.Trim(model, " _-")
	model = strings.ReplaceAll(model, "_", "-")
	model = regexp.MustCompile(`(?i)\bgemini-(\d+)-(\d+)\b`).ReplaceAllString(model, "gemini-$1.$2")
	model = regexp.MustCompile(`(?i)-preview\b`).ReplaceAllString(model, "")
	model = regexp.MustCompile(`(?i)-latest\b`).ReplaceAllString(model, "")
	win := FormatWindowLabel(window)
	reset := strings.TrimSpace(resetText)
	if win != "" && reset != "" {
		if wm, ok := shortDurationMinutes(win); ok {
			if rm, ok := shortDurationMinutes(reset); ok && math.Abs(wm-rm) <= 30 {
				switch {
				case wm >= 18*60 && wm <= 30*60:
					win = "1d"
				case wm >= 4*60 && wm <= 6*60:
					win = "5h"
				case wm >= 6*24*60 && wm <= 8*24*60:
					win = "7d"
				default:
					win = ""
				}
			}
		}
	}
	label := "limit"
	switch {
	case model != "" && win != "":
		label = model + " " + win
	case model != "":
		label = model
	case win != "":
		label = win
	}
	if reset != "" {
		label += " " + reset
	}
	if len(label) <= 42 {
		return label
	}
	if model != "" && win != "" {
		tail := len(win)
		if reset != "" {
			tail += 1 + len(reset)
		}
		budget := max(4, 42-tail-1)
		if len(model) > budget {
			model = model[:max(1, budget-1)] + "."
		}
		if reset != "" {
			return model + " " + win + " " + reset
		}
		return model + " " + win
	}
	return label[:41] + "."
}

func providerName(providerID, key string) string {
	if providerID != "" {
		if name, ok := map[string]string{
			"codex":       "Codex",
			"cursor":      "Cursor",
			"gemini_cli":  "Gemini",
			"claude_code": "Claude",
			"antigravity": "Antigravity",
			"copilot":     "Copilot",
		}[providerID]; ok {
			return name
		}
		return providerID
	}
	return strings.ReplaceAll(key, "-", " ")
}

func optimizeCodexWindow(provider, key, window string) string {
	if strings.ToLower(strings.TrimSpace(provider)) != "codex" {
		return strings.TrimSpace(window)
	}
	window = strings.TrimSpace(window)
	if window != "" {
		return window
	}
	if guess := codexEmptyWindowGuess(key); guess > 0 && guess < 720 {
		return fmt.Sprintf("~%dh", int(math.Max(1, math.Round(guess))))
	}
	return window
}

func codexEmptyWindowGuess(key string) float64 {
	k := strings.ToLower(strings.TrimSpace(key))
	switch {
	case k == "rate_limit_primary":
		return 5
	case k == "rate_limit_secondary":
		return 168
	case strings.HasPrefix(k, "rate_limit_code_review") && strings.HasSuffix(k, "_secondary"):
		return 168
	case strings.HasPrefix(k, "rate_limit_code_review"):
		return 12
	case strings.HasPrefix(k, "rate_limit_") && strings.HasSuffix(k, "_primary"):
		return 5
	case strings.HasPrefix(k, "rate_limit_") && strings.HasSuffix(k, "_secondary"):
		return 168
	default:
		return 0
	}
}

func ribbonSortTier(provider, key string) int {
	if strings.ToLower(strings.TrimSpace(provider)) != "codex" {
		return 0
	}
	k := strings.ToLower(strings.TrimSpace(key))
	switch {
	case k == "rate_limit_primary":
		return 5
	case k == "rate_limit_secondary":
		return 22
	case strings.HasPrefix(k, "rate_limit_code_review") && strings.HasSuffix(k, "_secondary"):
		return 42
	case strings.HasPrefix(k, "rate_limit_code_review"):
		return 30
	case strings.HasPrefix(k, "rate_limit_") && strings.HasSuffix(k, "_primary"):
		return 38
	case strings.HasPrefix(k, "rate_limit_") && strings.HasSuffix(k, "_secondary"):
		return 45
	case strings.HasPrefix(k, "rate_limit_"):
		return 55
	case strings.HasPrefix(k, "plan_") || strings.HasPrefix(k, "composer_"):
		return 210
	default:
		return 5000
	}
}

func sortHours(provider, key, window string) float64 {
	h := metricWindowHours(window)
	if strings.ToLower(strings.TrimSpace(provider)) == "codex" && strings.HasPrefix(strings.ToLower(key), "rate_limit_") && (strings.TrimSpace(window) == "" || h >= 999998) {
		if guess := codexEmptyWindowGuess(key); guess > 0 {
			return guess
		}
	}
	return h
}

func metricWindowHours(window string) float64 {
	raw := strings.TrimSpace(window)
	if raw == "" {
		return 999999
	}
	s := strings.ToLower(raw)
	if s == "all" || strings.Contains(s, "lifetime") {
		return 999997
	}
	patterns := []struct {
		re     string
		group  int
		factor float64
	}{
		{`(?i)pt(\d+(?:\.\d+)?)h`, 1, 1},
		{`(?i)pt(\d+(?:\.\d+)?)m`, 1, 1.0 / 60.0},
		{`(?i)(^|[^0-9])(\d+(?:\.\d+)?)\s*(?:hour|hours)\b`, 2, 1},
		{`(?i)(^|[^0-9])(\d+(?:\.\d+)?)\s*h\b`, 2, 1},
		{`(?i)(^|[^0-9])(\d+)\s*(?:minute|minutes|min)\s*(?:window)?`, 2, 1.0 / 60.0},
		{`(?i)(^|[^0-9])(\d+)\s*d(?:ay)?s?\b`, 2, 24},
	}
	for _, p := range patterns {
		if m := regexp.MustCompile(p.re).FindStringSubmatch(raw); len(m) > p.group {
			v, _ := strconv.ParseFloat(m[p.group], 64)
			return math.Min(8760, math.Max(0.01, v*p.factor))
		}
	}
	for _, needle := range []string{"5h rolling", "5 hour", "5-hour", "5 hours", "5h ", " five hour"} {
		if strings.Contains(s, needle) {
			return 5
		}
	}
	for _, guess := range []int{1, 3, 5, 12, 24, 168, 720, 744} {
		if strings.Contains(s, fmt.Sprintf("%dh", guess)) || strings.Contains(s, fmt.Sprintf("%d hour", guess)) {
			return float64(guess)
		}
	}
	return 999998
}

func shortDurationMinutes(text string) (float64, bool) {
	s := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(text)), "~")
	if m := regexp.MustCompile(`^(\d+(?:\.\d+)?)h(?:(\d+(?:\.\d+)?)m)?$`).FindStringSubmatch(s); len(m) >= 2 {
		h, _ := strconv.ParseFloat(m[1], 64)
		mins := h * 60
		if len(m) >= 3 && m[2] != "" {
			x, _ := strconv.ParseFloat(m[2], 64)
			mins += x
		}
		return mins, true
	}
	if m := regexp.MustCompile(`^(\d+(?:\.\d+)?)m$`).FindStringSubmatch(s); len(m) == 2 {
		v, _ := strconv.ParseFloat(m[1], 64)
		return v, true
	}
	if m := regexp.MustCompile(`^(\d+(?:\.\d+)?)d$`).FindStringSubmatch(s); len(m) == 2 {
		v, _ := strconv.ParseFloat(m[1], 64)
		return v * 1440, true
	}
	return 0, false
}

func countPercentBands(bands []Band) int {
	n := 0
	for _, band := range bands {
		if band.Percent != nil {
			n++
		}
	}
	return n
}

func compareFloat(a, b float64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func comparePtr(a, b *float64) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return 1
	}
	if b == nil {
		return -1
	}
	return compareFloat(*a, *b)
}

func trimFloat(v float64) string {
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
}

func trimFloatString(s string) string {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	return trimFloat(v)
}
