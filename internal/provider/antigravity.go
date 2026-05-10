package provider

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"limitdock/internal/readmodel"
	"limitdock/internal/settings"
)

type AntigravityReader struct {
	Config settings.Antigravity
	Log    Logger
}

type antigravityEndpoint struct {
	BaseURL string
	Token   string
}

type antigravityProcess struct {
	PID   int
	Port  int
	Token string
}

var antigravityLogOnce sync.Map

var antigravityEndpointCache = struct {
	sync.Mutex
	key       string
	expiresAt time.Time
	items     []antigravityEndpoint
}{}

func (r AntigravityReader) Name() string {
	return "antigravity"
}

func (r AntigravityReader) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	if !r.Config.Enabled {
		return emptyReadModel(), nil
	}

	for _, status := range r.cachedStatuses() {
		model := antigravityStatusReadModel(status, r.Config)
		if len(model.Snapshots) > 0 {
			return model, nil
		}
	}

	for _, endpoint := range antigravityEndpointCandidates(ctx, r.Config) {
		status, err := fetchAntigravityStatus(ctx, endpoint)
		if err != nil {
			continue
		}
		model := antigravityStatusReadModel(status, r.Config)
		if len(model.Snapshots) > 0 {
			return model, nil
		}
		logAntigravityOnce(r.Log, "endpoint-no-quota", "Antigravity reader found a local endpoint, but no quota metrics were exposed.")
		return emptyReadModel(), nil
	}

	logAntigravityOnce(r.Log, "endpoint-missing", "Antigravity reader skipped: no local quota endpoint found.")
	return emptyReadModel(), nil
}

func (r AntigravityReader) cachedStatuses() []map[string]any {
	root := strings.TrimSpace(r.Config.DataDir)
	if root == "" {
		return nil
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil
	}
	out := []map[string]any{}
	for _, name := range []string{"antigravity-status.json", "user-status.json", "quota.json", "usage.json"} {
		status, err := readJSONMap(filepath.Join(root, name))
		if err == nil {
			out = append(out, status)
		}
	}
	return out
}

func fetchAntigravityStatus(ctx context.Context, endpoint antigravityEndpoint) (map[string]any, error) {
	body, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"ide_name": "antigravity",
			"source":   "limitdock",
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint.BaseURL, "/")+"/exa.language_server_pb.LanguageServerService/GetUserStatus", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if endpoint.Token != "" {
		req.Header.Set("X-Codeium-Csrf-Token", endpoint.Token)
	}

	client := &http.Client{
		Timeout: 2500 * time.Millisecond,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Antigravity status %s", resp.Status)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func antigravityEndpointCandidates(ctx context.Context, cfg settings.Antigravity) []antigravityEndpoint {
	cacheKey := strings.TrimSpace(cfg.BinaryPath) + "|" + strings.TrimSpace(cfg.DataDir)
	now := time.Now()
	antigravityEndpointCache.Lock()
	if antigravityEndpointCache.key == cacheKey && now.Before(antigravityEndpointCache.expiresAt) {
		items := copyEndpoints(antigravityEndpointCache.items)
		antigravityEndpointCache.Unlock()
		return items
	}
	antigravityEndpointCache.Unlock()

	out := []antigravityEndpoint{}
	seen := map[string]bool{}
	add := func(baseURL, token string) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL == "" {
			return
		}
		key := baseURL + "|" + token
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, antigravityEndpoint{BaseURL: baseURL, Token: token})
	}

	for _, raw := range []string{cfg.BinaryPath, cfg.DataDir} {
		if u := configuredEndpointURL(raw); u != "" {
			add(u, "")
		}
	}
	for _, proc := range detectAntigravityProcesses(ctx) {
		ports := []int{}
		if proc.Port > 0 {
			ports = append(ports, proc.Port)
		}
		ports = append(ports, portsForPID(ctx, proc.PID)...)
		for _, port := range uniquePorts(ports) {
			add(fmt.Sprintf("https://127.0.0.1:%d", port), proc.Token)
			add(fmt.Sprintf("http://127.0.0.1:%d", port), proc.Token)
		}
	}
	for _, port := range []int{8080} {
		add(fmt.Sprintf("https://127.0.0.1:%d", port), "")
		add(fmt.Sprintf("http://127.0.0.1:%d", port), "")
	}
	antigravityEndpointCache.Lock()
	antigravityEndpointCache.key = cacheKey
	antigravityEndpointCache.expiresAt = now.Add(60 * time.Second)
	antigravityEndpointCache.items = copyEndpoints(out)
	antigravityEndpointCache.Unlock()
	return out
}

func configuredEndpointURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return strings.TrimRight(u.String(), "/")
}

func detectAntigravityProcesses(ctx context.Context) []antigravityProcess {
	cmdCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "wmic", "process", "where", "(name like '%antigravity%' or commandline like '%antigravity%')", "get", "ProcessId,CommandLine", "/format:csv")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	raw, err := cmd.Output()
	if err != nil {
		return nil
	}
	reader := csv.NewReader(strings.NewReader(string(raw)))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil
	}
	out := []antigravityProcess{}
	for _, record := range records {
		if len(record) < 3 || strings.EqualFold(record[0], "Node") {
			continue
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(record[len(record)-1]))
		command := strings.TrimSpace(strings.Join(record[1:len(record)-1], ","))
		lower := strings.ToLower(command)
		if pid <= 0 || !strings.Contains(lower, "antigravity") {
			continue
		}
		port := intRegex(command, `--extension_server_port(?:=|\s+)(\d+)`)
		token := stringRegex(command, `--csrf_token(?:=|\s+)([^\s"']+)`)
		if port <= 0 && token == "" && !strings.Contains(lower, "language") {
			continue
		}
		out = append(out, antigravityProcess{PID: pid, Port: port, Token: token})
	}
	return out
}

func portsForPID(ctx context.Context, pid int) []int {
	if pid <= 0 {
		return nil
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "netstat", "-ano", "-p", "tcp")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	raw, err := cmd.Output()
	if err != nil {
		return nil
	}
	out := []int{}
	pidText := strconv.Itoa(pid)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[len(fields)-1] != pidText {
			continue
		}
		local := fields[1]
		colon := strings.LastIndex(local, ":")
		if colon < 0 {
			continue
		}
		port, err := strconv.Atoi(local[colon+1:])
		if err == nil && port > 0 {
			out = append(out, port)
		}
	}
	return out
}

func antigravityStatusReadModel(status map[string]any, cfg settings.Antigravity) *readmodel.ReadModel {
	snap := antigravitySnapshot(status, cfg)
	if snap == nil || len(snap.Metrics) == 0 {
		return emptyReadModel()
	}
	key := snapshotKey("antigravity", snap.AccountID)
	return &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{key: snap}}
}

func antigravitySnapshot(status map[string]any, cfg settings.Antigravity) *readmodel.Snapshot {
	user := objectAny(status, "userStatus", "user_status", "user")
	for _, root := range statusRoots(status) {
		if user != nil {
			break
		}
		user = objectAny(root, "userStatus", "user_status", "user")
	}
	if user == nil {
		user = status
	}
	accountID := firstString(user, "email", "id", "userId", "user_id", "name")
	if accountID == "" {
		accountID = "local"
	}
	metrics := map[string]readmodel.Metric{}
	resets := map[string]any{}

	if remaining, ok := firstNumber(user, "availablePromptCredits", "available_prompt_credits", "remainingPromptCredits", "remaining_prompt_credits"); ok {
		if limit, ok := firstNumber(user, "monthlyPromptCredits", "monthly_prompt_credits", "promptCreditLimit", "prompt_credit_limit"); ok && limit > 0 {
			metrics["quota_prompt_credits"] = readmodel.Metric{
				Remaining: floatPtr(remaining),
				Limit:     floatPtr(limit),
				Unit:      "prompt_credits",
				Window:    "monthly",
			}
		}
	}

	for _, item := range antigravityModelConfigs(status) {
		name := antigravityModelName(item)
		quotaInfo := objectAny(item, "quotaInfo", "quota_info", "quota")
		if name == "" || quotaInfo == nil {
			continue
		}
		remaining, ok := firstNumber(quotaInfo, "remainingFraction", "remaining_fraction", "remainingPercent", "remaining_percent", "remaining")
		if !ok {
			continue
		}
		pct := normalizeRemainingPercent(remaining)
		key := uniqueMetricKey(metrics, "quota_model_"+slug(name))
		metrics[key] = readmodel.Metric{
			Remaining: floatPtr(pct),
			Unit:      "%",
			Window:    firstString(quotaInfo, "window", "period"),
		}
		if reset := firstString(quotaInfo, "resetTime", "reset_time", "resetAt", "reset_at"); reset != "" {
			resets[key+"_reset"] = reset
		}
	}

	if len(metrics) == 0 {
		return nil
	}
	attrs := map[string]any{"source": "antigravity-local"}
	if subtitle := strings.TrimSpace(cfg.Subtitle); subtitle != "" {
		attrs["subtitle"] = subtitle
	}
	return &readmodel.Snapshot{
		ProviderID: "antigravity",
		AccountID:  accountID,
		Status:     "ok",
		Metrics:    metrics,
		Resets:     resets,
		Attributes: attrs,
		Raw:        map[string]any{"source": "antigravity-local"},
	}
}

func antigravityModelConfigs(status map[string]any) []map[string]any {
	for _, root := range statusRoots(status) {
		for _, holder := range []map[string]any{
			objectAny(root, "cascadeModelConfigData", "cascade_model_config_data"),
			objectAny(root, "modelConfigData", "model_config_data"),
			root,
		} {
			if holder == nil {
				continue
			}
			if configs := arrayObjectsAny(holder, "clientModelConfigs", "client_model_configs", "models"); len(configs) > 0 {
				return configs
			}
		}
	}
	return nil
}

func statusRoots(status map[string]any) []map[string]any {
	roots := []map[string]any{status}
	for _, key := range []string{"response", "result", "data"} {
		if child := objectAny(status, key); child != nil {
			roots = append(roots, child)
			if user := objectAny(child, "userStatus", "user_status", "user"); user != nil {
				roots = append(roots, user)
			}
		}
	}
	if user := objectAny(status, "userStatus", "user_status", "user"); user != nil {
		roots = append(roots, user)
	}
	return roots
}

func antigravityModelName(item map[string]any) string {
	if name := firstString(item, "model", "modelId", "model_id", "label", "displayName", "display_name"); name != "" {
		return name
	}
	if model := objectAny(item, "modelOrAlias", "model_or_alias"); model != nil {
		return firstString(model, "model", "modelId", "model_id", "alias", "label", "displayName", "display_name")
	}
	return ""
}

func readJSONMap(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func emptyReadModel() *readmodel.ReadModel {
	return &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{}}
}

func uniquePorts(ports []int) []int {
	out := []int{}
	seen := map[int]bool{}
	for _, port := range ports {
		if port <= 0 || seen[port] {
			continue
		}
		seen[port] = true
		out = append(out, port)
	}
	return out
}

func copyEndpoints(in []antigravityEndpoint) []antigravityEndpoint {
	if len(in) == 0 {
		return nil
	}
	out := make([]antigravityEndpoint, len(in))
	copy(out, in)
	return out
}

func uniqueMetricKey(metrics map[string]readmodel.Metric, key string) string {
	if key == "" || key == "quota_model_" {
		key = "quota_model_antigravity"
	}
	if _, exists := metrics[key]; !exists {
		return key
	}
	for i := 2; ; i++ {
		candidate := key + "_" + strconv.Itoa(i)
		if _, exists := metrics[candidate]; !exists {
			return candidate
		}
	}
}

func slug(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return "antigravity"
	}
	return s
}

func normalizeRemainingPercent(v float64) float64 {
	if math.Abs(v) <= 1 {
		v *= 100
	}
	return math.Min(100, math.Max(0, v))
}

func intRegex(s, pattern string) int {
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	if len(m) != 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func stringRegex(s, pattern string) string {
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	if len(m) != 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func objectAny(m map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if child, ok := m[key].(map[string]any); ok {
			return child
		}
	}
	return nil
}

func arrayObjectsAny(m map[string]any, keys ...string) []map[string]any {
	for _, key := range keys {
		items, ok := m[key].([]any)
		if !ok {
			continue
		}
		out := []map[string]any{}
		for _, item := range items {
			if obj, ok := item.(map[string]any); ok {
				out = append(out, obj)
			}
		}
		return out
	}
	return nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		switch v := m[key].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case fmt.Stringer:
			if strings.TrimSpace(v.String()) != "" {
				return strings.TrimSpace(v.String())
			}
		}
	}
	return ""
}

func firstNumber(m map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			return v, true
		case float32:
			return float64(v), true
		case int:
			return float64(v), true
		case int64:
			return float64(v), true
		case json.Number:
			f, err := v.Float64()
			return f, err == nil
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

func floatPtr(v float64) *float64 {
	return &v
}

func logAntigravityOnce(log Logger, key string, format string, args ...any) {
	if log == nil {
		return
	}
	if _, loaded := antigravityLogOnce.LoadOrStore(key, true); loaded {
		return
	}
	log.Printf(format, args...)
}
