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
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"limitdock/internal/readmodel"
)

type AntigravityReader struct {
	Log Logger
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

type antigravityLogFile struct {
	Path    string
	ModTime time.Time
}

const antigravityQuotaWindow = "5h"

var antigravityModelBlacklist = map[string]bool{
	"MODEL_CHAT_20706":                       true,
	"MODEL_CHAT_23310":                       true,
	"MODEL_GOOGLE_GEMINI_2_5_FLASH":          true,
	"MODEL_GOOGLE_GEMINI_2_5_FLASH_THINKING": true,
	"MODEL_GOOGLE_GEMINI_2_5_FLASH_LITE":     true,
	"MODEL_GOOGLE_GEMINI_2_5_PRO":            true,
	"MODEL_PLACEHOLDER_M19":                  true,
	"MODEL_PLACEHOLDER_M9":                   true,
	"MODEL_PLACEHOLDER_M12":                  true,
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

func (r AntigravityReader) FallbackProviderID() string {
	return "antigravity"
}

func (r AntigravityReader) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, status := range r.cachedStatuses() {
		model := antigravityStatusReadModel(status)
		if len(model.Snapshots) > 0 {
			logAntigravityOnce(r.Log, "success", "Antigravity reader captured local quota rows.")
			return model, nil
		}
	}

	endpoints := antigravityEndpointCandidates(readCtx)
	if len(endpoints) > 0 {
		logAntigravityOnce(r.Log, "endpoint-candidates", "Antigravity reader found %d local endpoint candidates.", len(endpoints))
	}
	if len(endpoints) > 0 {
		status, err := fetchFirstAntigravityStatus(readCtx, endpoints, r.Log)
		if err != nil {
			logAntigravityOnce(r.Log, "endpoint-missing", "Antigravity reader skipped: %v", err)
			return emptyReadModel(), nil
		}
		model := antigravityStatusReadModel(status)
		if len(model.Snapshots) > 0 {
			logAntigravityOnce(r.Log, "success", "Antigravity reader captured local quota rows.")
			return model, nil
		}
		logAntigravityOnce(r.Log, "endpoint-no-quota", "Antigravity reader found a local endpoint, but no quota metrics were exposed.")
		return emptyReadModel(), nil
	}

	logAntigravityOnce(r.Log, "endpoint-missing", "Antigravity reader skipped: no local quota endpoint found.")
	return emptyReadModel(), nil
}

type antigravityFetchResult struct {
	Endpoint antigravityEndpoint
	Status   map[string]any
	Err      error
}

func fetchFirstAntigravityStatus(ctx context.Context, endpoints []antigravityEndpoint, log Logger) (map[string]any, error) {
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no local quota endpoint found")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ch := make(chan antigravityFetchResult, len(endpoints))
	for _, endpoint := range endpoints {
		ep := endpoint
		go func() {
			status, err := fetchAntigravityStatus(ctx, ep)
			ch <- antigravityFetchResult{Endpoint: ep, Status: status, Err: err}
		}()
	}
	var firstErr error
	for pending := len(endpoints); pending > 0; pending-- {
		select {
		case <-ctx.Done():
			if firstErr != nil {
				return nil, firstErr
			}
			return nil, ctx.Err()
		case result := <-ch:
			if result.Err != nil {
				if firstErr == nil {
					firstErr = result.Err
				}
				continue
			}
			cancel()
			return result.Status, nil
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fmt.Errorf("no local quota endpoint returned model configs")
}

func (r AntigravityReader) cachedStatuses() []map[string]any {
	roots := defaultAntigravityDataDirs()

	out := []map[string]any{}
	seen := map[string]bool{}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" || seen[strings.ToLower(root)] {
			continue
		}
		seen[strings.ToLower(root)] = true
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		for _, path := range antigravityStatusCandidatePaths(root) {
			status, err := readJSONMap(path)
			if err == nil {
				out = append(out, status)
			}
		}
	}
	return out
}

func defaultAntigravityDataDirs() []string {
	out := []string{}
	if appData := os.Getenv("APPDATA"); appData != "" {
		out = append(out, filepath.Join(appData, "Antigravity"))
	}
	return out
}

func antigravityStatusCandidatePaths(root string) []string {
	candidates := []string{
		filepath.Join(root, "antigravity-status.json"),
		filepath.Join(root, "user-status.json"),
		filepath.Join(root, "quota.json"),
		filepath.Join(root, "usage.json"),
		filepath.Join(root, "User", "globalStorage", "storage.json"),
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			if rel != "." && strings.Count(rel, string(os.PathSeparator)) > 5 {
				return filepath.SkipDir
			}
			return nil
		}
		if len(candidates) >= 40 || strings.ToLower(filepath.Ext(path)) != ".json" {
			return nil
		}
		lower := strings.ToLower(path)
		for _, needle := range []string{"quota", "usage", "status", "storage", "antigravity"} {
			if strings.Contains(lower, needle) {
				candidates = append(candidates, path)
				break
			}
		}
		return nil
	})
	seen := map[string]bool{}
	out := []string{}
	for _, path := range candidates {
		key := strings.ToLower(filepath.Clean(path))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, path)
	}
	return out
}

func fetchAntigravityStatus(ctx context.Context, endpoint antigravityEndpoint) (map[string]any, error) {
	if err := probeAntigravityEndpoint(ctx, endpoint); err != nil {
		return nil, err
	}
	var lastErr error
	for _, method := range []string{"GetUserStatus", "GetCommandModelConfigs"} {
		status, err := callAntigravityRPC(ctx, endpoint, method, antigravityMetadataBody())
		if err != nil {
			lastErr = err
			continue
		}
		if len(antigravityModelConfigs(status)) > 0 {
			return status, nil
		}
		lastErr = fmt.Errorf("%s returned no model configs", method)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("Antigravity endpoint returned no quota model configs")
}

func probeAntigravityEndpoint(ctx context.Context, endpoint antigravityEndpoint) error {
	_, err := callAntigravityRPC(ctx, endpoint, "GetUnleashData", map[string]any{
		"context": map[string]any{
			"properties": map[string]any{
				"devMode":          "false",
				"extensionVersion": "unknown",
				"ide":              "antigravity",
				"ideVersion":       "unknown",
				"os":               "windows",
			},
		},
	})
	if err != nil {
		return err
	}
	return nil
}

func antigravityMetadataBody() map[string]any {
	return map[string]any{
		"metadata": map[string]any{
			"ideName":       "antigravity",
			"extensionName": "antigravity",
			"ideVersion":    "unknown",
			"locale":        "en",
		},
	}
}

func callAntigravityRPC(ctx context.Context, endpoint antigravityEndpoint, method string, body any) (map[string]any, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint.BaseURL, "/")+"/exa.language_server_pb.LanguageServerService/"+method, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if endpoint.Token != "" {
		req.Header["x-codeium-csrf-token"] = []string{endpoint.Token}
	}

	client := &http.Client{
		Timeout: 900 * time.Millisecond,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 350 * time.Millisecond}).DialContext,
			TLSHandshakeTimeout:   350 * time.Millisecond,
			ResponseHeaderTimeout: 500 * time.Millisecond,
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respRaw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		if method == "GetUnleashData" {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("Antigravity %s %s", method, resp.Status)
	}
	var out map[string]any
	if len(strings.TrimSpace(string(respRaw))) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(respRaw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func antigravityEndpointCandidates(ctx context.Context) []antigravityEndpoint {
	cacheKey := "auto"
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

	for _, endpoint := range detectAntigravityLogEndpoints(ctx) {
		add(endpoint.BaseURL, endpoint.Token)
	}
	for _, proc := range detectAntigravityProcesses(ctx) {
		ports := []int{}
		if proc.Port > 0 {
			ports = append(ports, proc.Port)
		}
		ports = append(ports, portsForPID(ctx, proc.PID)...)
		for _, port := range uniquePorts(ports) {
			add(fmt.Sprintf("http://127.0.0.1:%d", port), proc.Token)
		}
	}
	for _, port := range []int{8080} {
		add(fmt.Sprintf("http://127.0.0.1:%d", port), "")
	}
	antigravityEndpointCache.Lock()
	antigravityEndpointCache.key = cacheKey
	antigravityEndpointCache.expiresAt = now.Add(60 * time.Second)
	antigravityEndpointCache.items = copyEndpoints(out)
	antigravityEndpointCache.Unlock()
	return out
}

func detectAntigravityLogEndpoints(ctx context.Context) []antigravityEndpoint {
	files := antigravityLSLogFiles()
	out := []antigravityEndpoint{}
	seen := map[string]bool{}
	add := func(port int, token string) {
		if port <= 0 || len(out) >= 24 {
			return
		}
		baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
		key := baseURL + "|" + token
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, antigravityEndpoint{BaseURL: baseURL, Token: token})
	}
	tokens := []string{}
	logPorts := []int{}
	for _, file := range files {
		port, token := antigravityEndpointFromLog(file.Path)
		if port > 0 && !intSeen(logPorts, port) {
			logPorts = append(logPorts, port)
		}
		if token != "" && !stringSeen(tokens, token) {
			tokens = append(tokens, token)
		}
		if len(tokens) >= 1 {
			break
		}
	}
	lsPorts := []int{}
	for _, pid := range antigravityLanguageServerPIDs(ctx) {
		lsPorts = append(lsPorts, portsForPID(ctx, pid)...)
	}
	for _, port := range uniquePorts(lsPorts) {
		if len(tokens) == 0 {
			add(port, "")
			continue
		}
		for _, token := range tokens {
			add(port, token)
		}
	}
	if len(lsPorts) == 0 {
		for _, port := range logPorts {
			if len(tokens) == 0 {
				add(port, "")
				continue
			}
			for _, token := range tokens {
				add(port, token)
			}
		}
	}
	return out
}

func antigravityLanguageServerPIDs(ctx context.Context) []int {
	cmdCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "tasklist", "/FI", "IMAGENAME eq language_server_windows_x64.exe", "/FO", "CSV", "/NH")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	raw, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseTasklistPIDs(string(raw))
}

func parseTasklistPIDs(raw string) []int {
	reader := csv.NewReader(strings.NewReader(raw))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil
	}
	out := []int{}
	for _, record := range records {
		if len(record) < 2 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(record[0]))
		if !strings.Contains(name, "language_server_windows_x64") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(record[1]))
		if err == nil && pid > 0 {
			out = append(out, pid)
		}
	}
	return out
}

func antigravityLSLogFiles() []antigravityLogFile {
	out := []antigravityLogFile{}
	for _, root := range defaultAntigravityDataDirs() {
		logRoot := filepath.Join(root, "logs")
		_ = filepath.WalkDir(logRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() || !strings.EqualFold(filepath.Base(path), "ls-main.log") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			out = append(out, antigravityLogFile{Path: path, ModTime: info.ModTime()})
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ModTime.After(out[j].ModTime)
	})
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func antigravityEndpointFromLog(path string) (int, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, ""
	}
	lines := strings.Split(string(b), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !strings.Contains(line, "--extension_server_port") && !strings.Contains(line, "--csrf_token") {
			continue
		}
		port := intRegex(line, `--extension_server_port(?:=|\s+)(\d+)`)
		token := stringRegex(line, `--csrf_token(?:=|\s+)([^\s"']+)`)
		if port > 0 {
			return port, token
		}
	}
	return 0, ""
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

func antigravityStatusReadModel(status map[string]any) *readmodel.ReadModel {
	snap := antigravitySnapshot(status)
	if snap == nil || len(snap.Metrics) == 0 {
		return emptyReadModel()
	}
	key := snapshotKey("antigravity", snap.AccountID)
	return &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{key: snap}}
}

func antigravitySnapshot(status map[string]any) *readmodel.Snapshot {
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
		if boolAny(item, "isInternal", "is_internal") {
			continue
		}
		if id := antigravityModelID(item); id != "" && antigravityModelBlacklist[id] {
			continue
		}
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
		pool := antigravityPoolLabel(name)
		key := "quota_model_" + slug(pool)
		if existing, ok := metrics[key]; ok && existing.Remaining != nil && *existing.Remaining <= pct {
			continue
		}
		window := firstString(quotaInfo, "window", "period")
		if window == "" {
			window = antigravityQuotaWindow
		}
		metrics[key] = readmodel.Metric{
			Remaining: floatPtr(pct),
			Unit:      "%",
			Window:    window,
		}
		if reset := firstString(quotaInfo, "resetTime", "reset_time", "resetAt", "reset_at"); reset != "" {
			resets[key+"_reset"] = reset
		}
	}

	if len(metrics) == 0 {
		return nil
	}
	attrs := map[string]any{"source": "antigravity-local"}
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
			if configs := objectValuesAny(holder, "models"); len(configs) > 0 {
				return configs
			}
		}
	}
	return nil
}

func statusRoots(status map[string]any) []map[string]any {
	roots := []map[string]any{}
	collectStatusRoots(status, &roots, 0)
	return roots
}

func collectStatusRoots(v any, out *[]map[string]any, depth int) {
	if depth > 8 || len(*out) >= 256 {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		*out = append(*out, x)
		for _, child := range x {
			collectStatusRoots(child, out, depth+1)
			if len(*out) >= 256 {
				return
			}
		}
	case []any:
		for _, child := range x {
			collectStatusRoots(child, out, depth+1)
			if len(*out) >= 256 {
				return
			}
		}
	}
}

func antigravityModelName(item map[string]any) string {
	if name := firstString(item, "label", "displayName", "display_name", "name", "modelId", "model_id", "model"); name != "" {
		return name
	}
	if model := objectAny(item, "modelOrAlias", "model_or_alias"); model != nil {
		return firstString(model, "label", "displayName", "display_name", "alias", "model", "modelId", "model_id")
	}
	return ""
}

func antigravityModelID(item map[string]any) string {
	if id := firstString(item, "model", "modelId", "model_id"); id != "" {
		return id
	}
	if model := objectAny(item, "modelOrAlias", "model_or_alias"); model != nil {
		return firstString(model, "model", "modelId", "model_id")
	}
	return ""
}

func antigravityPoolLabel(label string) string {
	label = normalizeAntigravityLabel(label)
	lower := strings.ToLower(label)
	if strings.Contains(lower, "gemini") && strings.Contains(lower, "pro") {
		return "Gemini Pro"
	}
	if strings.Contains(lower, "gemini") && strings.Contains(lower, "flash") {
		return "Gemini Flash"
	}
	if strings.Contains(lower, "claude") || strings.Contains(lower, "gpt-oss") || strings.Contains(lower, "gpt oss") {
		return "Claude"
	}
	return label
}

func normalizeAntigravityLabel(label string) string {
	label = strings.TrimSpace(label)
	label = regexp.MustCompile(`\s*\([^)]*\)\s*$`).ReplaceAllString(label, "")
	return strings.TrimSpace(label)
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

func stringSeen(items []string, item string) bool {
	for _, existing := range items {
		if existing == item {
			return true
		}
	}
	return false
}

func intSeen(items []int, item int) bool {
	for _, existing := range items {
		if existing == item {
			return true
		}
	}
	return false
}

func copyEndpoints(in []antigravityEndpoint) []antigravityEndpoint {
	if len(in) == 0 {
		return nil
	}
	out := make([]antigravityEndpoint, len(in))
	copy(out, in)
	return out
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

func objectValuesAny(m map[string]any, keys ...string) []map[string]any {
	for _, key := range keys {
		items, ok := m[key].(map[string]any)
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

func boolAny(m map[string]any, keys ...string) bool {
	for _, key := range keys {
		switch v := m[key].(type) {
		case bool:
			return v
		case string:
			return strings.EqualFold(strings.TrimSpace(v), "true")
		}
	}
	return false
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
