package claudeauth

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
)

type cliCredentials struct {
	accessToken  string
	refreshToken string
	expiresAt    int64 // unix milliseconds
}

// readCLICredentials parses the Claude Code CLI credentials file. The file is
// read-only for LimitDock — the CLI owns its token lineage.
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
	creds := cliCredentials{}
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
