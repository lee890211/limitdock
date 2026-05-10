package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type readModel struct {
	Snapshots map[string]snapshot `json:"snapshots"`
}

type snapshot struct {
	ProviderID string            `json:"provider_id"`
	Metrics    map[string]metric `json:"metrics"`
}

type metric struct {
	Unit string `json:"unit"`
}

func main() {
	socket := flag.String("socket", "", "OpenUsage Unix socket path")
	timeout := flag.Duration("timeout", 90*time.Second, "maximum wait time")
	flag.Parse()
	if strings.TrimSpace(*socket) == "" {
		home, _ := os.UserHomeDir()
		*socket = home + `\.local\state\openusage\telemetry.sock`
	}
	deadline := time.Now().Add(*timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		model, err := readOnce(*socket)
		if err == nil && quotaRows(model) > 0 {
			fmt.Printf("ready snapshots=%d quota_rows=%d\n", len(model.Snapshots), quotaRows(model))
			return
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		fmt.Fprintf(os.Stderr, "OpenUsage read-model not ready: %v\n", lastErr)
	} else {
		fmt.Fprintln(os.Stderr, "OpenUsage read-model not ready: no quota rows")
	}
	os.Exit(1)
}

func readOnce(socket string) (*readModel, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	client := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socket)
			},
			DisableCompression: true,
			DisableKeepAlives:  true,
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/read-model", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %s: %s", resp.Status, string(raw))
	}
	var model readModel
	if err := json.Unmarshal(raw, &model); err != nil {
		return nil, err
	}
	return &model, nil
}

func quotaRows(model *readModel) int {
	if model == nil {
		return 0
	}
	count := 0
	for _, snap := range model.Snapshots {
		for key, metric := range snap.Metrics {
			k := strings.ToLower(strings.TrimSpace(key))
			if metric.Unit == "%" || k == "quota" || strings.HasPrefix(k, "quota_") ||
				strings.HasPrefix(k, "rate_limit_") || k == "usage_five_hour" ||
				k == "usage_seven_day" || k == "plan_percent_used" ||
				strings.HasSuffix(k, "_quota") {
				count++
			}
		}
	}
	return count
}
