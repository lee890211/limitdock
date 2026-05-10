package openusage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureCodexOpenUsageSettingsAddsAccountAndLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	initial := `{"telemetry":{"provider_links":{"google":"gemini_api"}},"accounts":[{"id":"openai","provider":"openai"}]}`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureCodexOpenUsageSettings(path); err != nil {
		t.Fatal(err)
	}

	var cfg map[string]any
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	telemetry := cfg["telemetry"].(map[string]any)
	links := telemetry["provider_links"].(map[string]any)
	if links["codex"] != "codex-cli" {
		t.Fatalf("expected codex provider link, got %#v", links["codex"])
	}
	accounts := cfg["accounts"].([]any)
	found := false
	for _, item := range accounts {
		acct := item.(map[string]any)
		if acct["id"] == "codex-cli" && acct["provider"] == "codex" {
			found = true
		}
	}
	if !found {
		t.Fatalf("codex-cli account not added: %#v", accounts)
	}
}

func TestRepairCodexNotifyConfigMovesOpenUsageNotifyToTop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	input := "model = \"gpt-5.5\"\r\nmodel_reasoning_effort = \"xhigh\"\r\n\r\n[tui.model_availability_nux]\r\n\"gpt-5.5\" = 3\r\nnotify = [\"C:\\Users\\USER\\.config\\openusage\\hooks\\codex-notify.sh\"]\r\n"
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	hook := `C:\Users\USER\.config\openusage\hooks\codex-notify.sh`
	bash := `C:\Program Files\Git\bin\bash.exe`
	if err := repairCodexNotifyConfig(path, hook, bash); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	want := `notify = ["C:\\Program Files\\Git\\bin\\bash.exe", "C:\\Users\\USER\\.config\\openusage\\hooks\\codex-notify.sh"]`
	if !strings.Contains(out, want) {
		t.Fatalf("repaired notify missing\nwant: %s\nout:\n%s", want, out)
	}
	if strings.Index(out, "notify =") > strings.Index(out, "[tui.model_availability_nux]") {
		t.Fatalf("notify should be top-level before first table:\n%s", out)
	}
	if strings.Count(out, "notify =") != 1 {
		t.Fatalf("expected one notify line, got %d:\n%s", strings.Count(out, "notify ="), out)
	}
}
