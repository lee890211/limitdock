package openusage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func (m *Manager) EnsureCodexIntegration(ctx context.Context) {
	if !codexLocalPresence() {
		return
	}

	if err := ensureCodexOpenUsageSettings(openUsageSettingsPath()); err != nil && m.Log != nil {
		m.Log.Printf("Codex OpenUsage settings ensure skipped: %v", err)
	}

	cfgPath := codexConfigPath()
	hookPath := openUsageCodexHookPath()
	hasOpenUsageNotify, hasNonOpenUsageNotify := codexNotifyState(cfgPath)
	if hasNonOpenUsageNotify {
		if m.Log != nil {
			m.Log.Printf("Codex OpenUsage integration install skipped: non-OpenUsage notify is already configured.")
		}
		return
	}

	if !fileExists(hookPath) || !hasOpenUsageNotify {
		cmd := exec.CommandContext(ctx, m.ExePath, "integrations", "install", "codex")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := cmd.CombinedOutput()
		if err != nil {
			if m.Log != nil {
				m.Log.Printf("Codex OpenUsage integration install failed: %v %s", err, strings.TrimSpace(string(out)))
			}
		} else if m.Log != nil {
			m.Log.Printf("Codex OpenUsage integration install checked.")
		}
	}

	if fileExists(hookPath) {
		bashPath := preferredBashPath()
		if bashPath == "" {
			if m.Log != nil {
				m.Log.Printf("Codex OpenUsage notify skipped: Git Bash was not found for %s", hookPath)
			}
			return
		}
		if err := repairCodexNotifyConfig(cfgPath, hookPath, bashPath); err != nil && m.Log != nil {
			m.Log.Printf("Codex OpenUsage notify repair skipped: %v", err)
		}
	}
}

func codexLocalPresence() bool {
	if _, err := exec.LookPath("codex"); err == nil {
		return true
	}
	if _, err := exec.LookPath("codex.exe"); err == nil {
		return true
	}
	return dirExists(filepath.Join(userHome(), ".codex"))
}

func openUsageSettingsPath() string {
	if appData := os.Getenv("APPDATA"); appData != "" {
		return filepath.Join(appData, "openusage", "settings.json")
	}
	return filepath.Join(userHome(), "AppData", "Roaming", "openusage", "settings.json")
}

func openUsageCodexHookPath() string {
	return filepath.Join(userHome(), ".config", "openusage", "hooks", "codex-notify.sh")
}

func codexConfigPath() string {
	return filepath.Join(userHome(), ".codex", "config.toml")
}

func preferredBashPath() string {
	for _, path := range []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files\Git\usr\bin\bash.exe`,
	} {
		if fileExists(path) {
			return path
		}
	}
	for _, name := range []string{"bash.exe", "bash"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

func ensureCodexOpenUsageSettings(path string) error {
	cfg := map[string]any{"auto_detect": true}
	if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) != "" {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return err
		}
		if cfg == nil {
			cfg = map[string]any{"auto_detect": true}
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}

	telemetry := objectMap(cfg["telemetry"])
	cfg["telemetry"] = telemetry
	providerLinks := objectMap(telemetry["provider_links"])
	telemetry["provider_links"] = providerLinks
	providerLinks["codex"] = "codex-cli"

	accounts, _ := cfg["accounts"].([]any)
	hasCodex := false
	for _, item := range accounts {
		acct, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if stringValue(acct["id"]) == "codex-cli" && stringValue(acct["provider"]) == "codex" {
			hasCodex = true
			break
		}
	}
	if !hasCodex {
		accounts = append(accounts, map[string]any{"id": "codex-cli", "provider": "codex"})
		cfg["accounts"] = accounts
	}

	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func objectMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok && m != nil {
		return m
	}
	return map[string]any{}
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func codexNotifyState(path string) (bool, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	hasOpenUsageNotify := false
	hasNonOpenUsageNotify := false
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "notify") || !strings.Contains(trimmed, "=") {
			continue
		}
		if mentionsOpenUsageNotify(trimmed) {
			hasOpenUsageNotify = true
		} else {
			hasNonOpenUsageNotify = true
		}
	}
	return hasOpenUsageNotify, hasNonOpenUsageNotify
}

func repairCodexNotifyConfig(path, hookPath, bashPath string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(b))
	desired := fmt.Sprintf("notify = [%s, %s]", tomlBasicString(bashPath), tomlBasicString(hookPath))
	if codexNotifyAlreadyDesired(lines, desired) {
		return nil
	}

	out := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "notify") && strings.Contains(trimmed, "=") {
			if mentionsOpenUsageNotify(trimmed) {
				continue
			}
			return fmt.Errorf("non-OpenUsage notify is already configured")
		}
		out = append(out, line)
	}

	insertAt := 0
	for i, line := range out {
		if strings.HasPrefix(strings.TrimSpace(line), "model_reasoning_effort") {
			insertAt = i + 1
			break
		}
	}
	out = append(out, "")
	copy(out[insertAt+1:], out[insertAt:])
	out[insertAt] = desired
	return os.WriteFile(path, []byte(strings.Join(out, "\r\n")+"\r\n"), 0o644)
}

func codexNotifyAlreadyDesired(lines []string, desired string) bool {
	seenTable := false
	foundDesired := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			seenTable = true
		}
		if !strings.HasPrefix(trimmed, "notify") || !strings.Contains(trimmed, "=") {
			continue
		}
		if !mentionsOpenUsageNotify(trimmed) {
			return false
		}
		if seenTable || trimmed != desired {
			return false
		}
		foundDesired = true
	}
	return foundDesired
}

func mentionsOpenUsageNotify(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "openusage") || strings.Contains(lower, "codex-notify")
}

func tomlBasicString(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func userHome() string {
	home, _ := os.UserHomeDir()
	return home
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
