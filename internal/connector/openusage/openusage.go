package openusage

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"limitdock/internal/readmodel"
)

type Logger interface {
	Printf(format string, args ...any)
}

type Manager struct {
	ExePath    string
	SocketPath string
	Downloads  string
	ExtractDir string
	DaemonPID  string
	OutLog     string
	ErrLog     string
	Log        Logger

	cmd *exec.Cmd
}

func (m *Manager) EnsureBinary(ctx context.Context, noDownload bool) error {
	if fileExists(m.ExePath) {
		return nil
	}
	if noDownload {
		return fmt.Errorf("OpenUsage.sh Windows binary is missing: %s", m.ExePath)
	}
	if m.Log != nil {
		m.Log.Printf("Downloading OpenUsage.sh Windows binary")
	}
	assetURL, err := latestWindowsAsset(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.Downloads, 0o755); err != nil {
		return err
	}
	zipPath := filepath.Join(m.Downloads, "openusage_windows_amd64.zip")
	if err := downloadFile(ctx, assetURL, zipPath); err != nil {
		return err
	}
	if err := os.RemoveAll(m.ExtractDir); err != nil {
		return err
	}
	if err := unzip(zipPath, m.ExtractDir); err != nil {
		return err
	}
	if !fileExists(m.ExePath) {
		return fmt.Errorf("downloaded OpenUsage.sh archive did not contain expected binary: %s", m.ExePath)
	}
	return nil
}

func (m *Manager) Start() error {
	_ = os.Remove(m.SocketPath)
	if err := os.MkdirAll(filepath.Dir(m.SocketPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.OutLog), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(m.OutLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	errf, err := os.OpenFile(m.ErrLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		_ = out.Close()
		return err
	}
	cmd := exec.Command(m.ExePath, "telemetry", "daemon", "run", "--socket-path", m.SocketPath)
	cmd.Stdout = out
	cmd.Stderr = errf
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		_ = out.Close()
		_ = errf.Close()
		return err
	}
	m.cmd = cmd
	if m.Log != nil {
		m.Log.Printf("Started OpenUsage.sh daemon pid=%d socket=%s", cmd.Process.Pid, m.SocketPath)
	}
	if m.DaemonPID != "" {
		_ = os.WriteFile(m.DaemonPID, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0o644)
	}
	go func() {
		_ = cmd.Wait()
		_ = out.Close()
		_ = errf.Close()
	}()
	return nil
}

func (m *Manager) WaitReady(ctx context.Context, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	deadline := time.Now().Add(timeout)
	client := readmodel.Client{SocketPath: m.SocketPath, Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		if _, err := os.Stat(m.SocketPath); err == nil {
			if _, _, err := client.Read(ctx, map[string]any{}); err == nil {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(350 * time.Millisecond):
		}
	}
	return false
}

func (m *Manager) Stop() {
	if m.cmd != nil && m.cmd.Process != nil {
		if m.Log != nil {
			m.Log.Printf("Stopping OpenUsage.sh daemon pid=%d", m.cmd.Process.Pid)
		}
		_ = m.cmd.Process.Kill()
	}
	_ = os.Remove(m.SocketPath)
	if m.DaemonPID != "" {
		_ = os.Remove(m.DaemonPID)
	}
}

func latestWindowsAsset(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/janekbaraniewski/openusage/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "LimitDock")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("GitHub release status %s", resp.Status)
	}
	var release struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	for _, asset := range release.Assets {
		if strings.Contains(asset.Name, "windows_amd64") && strings.HasSuffix(asset.Name, ".zip") {
			return asset.URL, nil
		}
	}
	return "", errors.New("could not find OpenUsage.sh windows_amd64 release asset")
}

func downloadFile(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "LimitDock")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("download status %s", resp.Status)
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func unzip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		target := filepath.Join(dst, f.Name)
		cleanDst, err := filepath.Abs(dst)
		if err != nil {
			return err
		}
		cleanTarget, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		if cleanTarget != cleanDst && !strings.HasPrefix(cleanTarget, cleanDst+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry escapes destination: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode())
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		closeErr := out.Close()
		_ = rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
