package openusage

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"limitdock/internal/readmodel"
)

func TestWindowsAssetURLAcceptsVersionedWindowsZip(t *testing.T) {
	assets := []githubAsset{
		{Name: "checksums.txt", URL: "ignore"},
		{Name: "openusage_0.10.3_windows_amd64.zip", URL: "https://example.test/openusage.zip"},
	}
	if got := windowsAssetURL(assets); got != "https://example.test/openusage.zip" {
		t.Fatalf("windowsAssetURL() = %q", got)
	}
}

func TestWindowsAssetURLRejectsSignatureFiles(t *testing.T) {
	assets := []githubAsset{
		{Name: "openusage_0.10.3_windows_amd64.zip.sig", URL: "ignore"},
		{Name: "openusage_0.10.3_linux_amd64.tar.gz", URL: "ignore"},
	}
	if got := windowsAssetURL(assets); got != "" {
		t.Fatalf("windowsAssetURL() = %q, want empty", got)
	}
}

func TestManagerStopWaitsForStartedProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestOpenUsageHelperProcess")
	cmd.Env = append(os.Environ(), "LIMITDOCK_OPENUSAGE_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()

	manager := &Manager{cmd: cmd, cmdDone: done}
	manager.Stop()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatalf("manager Stop returned before helper process exited")
	}
}

func TestNormalizeClaudeCodeSnapshotSynthesizesUsageFiveHour(t *testing.T) {
	snap := &readmodel.Snapshot{
		ProviderID: "claude_code",
		Attributes: map[string]any{
			"block_progress_pct": "11",
			"block_end":          "2026-05-16T20:00:00Z",
		},
		Resets: map[string]any{},
	}
	model := &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{"claude-code": snap}}
	normalizeClaudeCodeModel(model)
	m, ok := snap.Metrics["usage_five_hour"]
	if !ok {
		t.Fatal("usage_five_hour metric not synthesized")
	}
	if m.Unit != "%" {
		t.Fatalf("unit = %q, want %%", m.Unit)
	}
	if m.Remaining == nil || *m.Remaining != 89.0 {
		t.Fatalf("remaining = %v, want 89", m.Remaining)
	}
	if snap.Resets["usage_five_hour_reset"] != "2026-05-16T20:00:00Z" {
		t.Fatalf("reset = %v, want block_end", snap.Resets["usage_five_hour_reset"])
	}
}

func TestNormalizeClaudeCodeSnapshotSkipsWhenQuotaExists(t *testing.T) {
	existing := 70.0
	snap := &readmodel.Snapshot{
		ProviderID: "claude_code",
		Attributes: map[string]any{"block_progress_pct": "11"},
		Metrics: map[string]readmodel.Metric{
			"usage_five_hour": {Remaining: &existing, Unit: "%"},
		},
	}
	model := &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{"claude-code": snap}}
	normalizeClaudeCodeModel(model)
	if got := snap.Metrics["usage_five_hour"].Remaining; got == nil || *got != 70.0 {
		t.Fatalf("existing quota metric was overwritten: %v", got)
	}
}

func TestNormalizeClaudeCodeSnapshotIgnoresOtherProviders(t *testing.T) {
	snap := &readmodel.Snapshot{
		ProviderID: "cursor",
		Attributes: map[string]any{"block_progress_pct": "11"},
	}
	model := &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{"cursor": snap}}
	normalizeClaudeCodeModel(model)
	if _, ok := snap.Metrics["usage_five_hour"]; ok {
		t.Fatal("usage_five_hour should not be added for non-claude_code provider")
	}
}

func TestOpenUsageHelperProcess(t *testing.T) {
	if os.Getenv("LIMITDOCK_OPENUSAGE_HELPER") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}
