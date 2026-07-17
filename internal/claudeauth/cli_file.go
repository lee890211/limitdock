package claudeauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"limitdock/internal/fsutil"
)

type cliCredentials struct {
	accessToken  string
	refreshToken string
	expiresAt    int64 // unix milliseconds
	raw          map[string]any
}

// readCLICredentials parses the Claude Code CLI credentials file, keeping the
// full document (with json.Number literals) so a write-back preserves every
// field LimitDock does not understand.
func readCLICredentials(path string) (cliCredentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return cliCredentials{}, authErrorf("reading %s: %v", path, err)
	}
	raw := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return cliCredentials{}, authErrorf("parsing %s: %v", path, err)
	}
	oauth, _ := raw["claudeAiOauth"].(map[string]any)
	if oauth == nil {
		return cliCredentials{}, authErrorf("%s has no claudeAiOauth section", path)
	}
	creds := cliCredentials{raw: raw}
	if s, ok := oauth["accessToken"].(string); ok {
		creds.accessToken = strings.TrimSpace(s)
	}
	if s, ok := oauth["refreshToken"].(string); ok {
		creds.refreshToken = strings.TrimSpace(s)
	}
	if n, ok := oauth["expiresAt"].(json.Number); ok {
		if v, err := n.Int64(); err == nil {
			creds.expiresAt = v
		}
	}
	if creds.accessToken == "" && creds.refreshToken == "" {
		return cliCredentials{}, authErrorf("%s has no usable tokens", path)
	}
	return creds, nil
}

// writeBackCLICredentials atomically persists a rotated token pair into the
// CLI credentials file, mutating only the claudeAiOauth token fields.
func writeBackCLICredentials(path string, raw map[string]any, res *tokenResponse, now time.Time) error {
	if raw == nil {
		return fmt.Errorf("missing original credentials document")
	}
	oauth, _ := raw["claudeAiOauth"].(map[string]any)
	if oauth == nil {
		oauth = map[string]any{}
	}
	oauth["accessToken"] = res.AccessToken
	if res.RefreshToken != "" {
		oauth["refreshToken"] = res.RefreshToken
	}
	if res.ExpiresIn > 0 {
		oauth["expiresAt"] = json.Number(fmt.Sprintf("%d", now.Add(time.Duration(res.ExpiresIn)*time.Second).UnixMilli()))
	}
	raw["claudeAiOauth"] = oauth
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return fsutil.AtomicWriteFile(path, data, 0o600)
}
