package openusage

import (
	"os"
	"os/exec"
	"testing"
	"time"
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

func TestOpenUsageHelperProcess(t *testing.T) {
	if os.Getenv("LIMITDOCK_OPENUSAGE_HELPER") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}
