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
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

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

	cmd     *exec.Cmd
	cmdDone chan error
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
		found, err := findOpenUsageExe(m.ExtractDir)
		if err != nil {
			return fmt.Errorf("downloaded OpenUsage.sh archive did not contain expected binary: %s", m.ExePath)
		}
		if err := os.MkdirAll(filepath.Dir(m.ExePath), 0o755); err != nil {
			return err
		}
		if err := copyFile(found, m.ExePath); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) Start() error {
	m.stopPIDFileProcess("Stopping stale OpenUsage.sh daemon")
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
	m.cmdDone = make(chan error, 1)
	done := m.cmdDone
	if m.Log != nil {
		m.Log.Printf("Started OpenUsage.sh daemon pid=%d socket=%s", cmd.Process.Pid, m.SocketPath)
	}
	if m.DaemonPID != "" {
		_ = os.WriteFile(m.DaemonPID, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0o644)
	}
	go func() {
		done <- cmd.Wait()
		close(done)
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
		waitCmdDone(m.cmdDone, 2*time.Second)
	}
	m.stopPIDFileProcess("Stopping OpenUsage.sh daemon from pid file")
	_ = os.Remove(m.SocketPath)
	if m.DaemonPID != "" {
		_ = os.Remove(m.DaemonPID)
	}
	m.cmd = nil
	m.cmdDone = nil
}

func (m *Manager) stopPIDFileProcess(message string) {
	if m.DaemonPID == "" {
		return
	}
	raw, err := os.ReadFile(m.DaemonPID)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return
	}
	if m.cmd != nil && m.cmd.Process != nil && m.cmd.Process.Pid == pid {
		return
	}
	if m.Log != nil {
		m.Log.Printf("%s pid=%d", message, pid)
	}
	if !m.processMatchesExe(pid) {
		if m.Log != nil {
			m.Log.Printf("Skipped stale OpenUsage.sh pid=%d because executable path did not match", pid)
		}
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
		waitForPIDExit(pid, 2*time.Second)
	}
}

func waitCmdDone(done <-chan error, timeout time.Duration) bool {
	if done == nil {
		return false
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func waitForPIDExit(pid int, timeout time.Duration) bool {
	if pid <= 0 {
		return true
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true
	}
	defer windows.CloseHandle(handle)
	if timeout <= 0 {
		timeout = time.Second
	}
	result, err := windows.WaitForSingleObject(handle, uint32(timeout/time.Millisecond))
	return err == nil && result == windows.WAIT_OBJECT_0
}

func (m *Manager) processMatchesExe(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return false
	}
	got := filepath.Clean(windows.UTF16ToString(buf[:size]))
	want := filepath.Clean(m.ExePath)
	return strings.EqualFold(got, want)
}

type githubRelease struct {
	TagName    string        `json:"tag_name"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func latestWindowsAsset(ctx context.Context) (string, error) {
	var latest githubRelease
	if err := githubGetJSON(ctx, "https://api.github.com/repos/janekbaraniewski/openusage/releases/latest", &latest); err != nil {
		return "", err
	}
	if url := windowsAssetURL(latest.Assets); url != "" {
		return url, nil
	}
	var releases []githubRelease
	if err := githubGetJSON(ctx, "https://api.github.com/repos/janekbaraniewski/openusage/releases?per_page=12", &releases); err != nil {
		return "", err
	}
	for _, release := range releases {
		if release.Draft || release.Prerelease {
			continue
		}
		if url := windowsAssetURL(release.Assets); url != "" {
			return url, nil
		}
	}
	return "", errors.New("could not find OpenUsage.sh Windows amd64 release asset in recent releases")
}

func githubGetJSON(ctx context.Context, url string, out any) error {
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
		return fmt.Errorf("GitHub release status %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func windowsAssetURL(assets []githubAsset) string {
	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		if strings.Contains(name, "windows") && strings.Contains(name, "amd64") && strings.HasSuffix(name, ".zip") {
			return asset.URL
		}
	}
	return ""
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

func findOpenUsageExe(root string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Base(path), "openusage.exe") {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", os.ErrNotExist
	}
	return found, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
