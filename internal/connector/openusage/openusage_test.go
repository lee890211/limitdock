package openusage

import "testing"

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
