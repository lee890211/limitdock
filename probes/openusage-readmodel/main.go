package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: openusage-readmodel <socket-path> [json-body-or-@filepath]")
		os.Exit(2)
	}

	socketPath := os.Args[1]
	payload := normalizePayload(loadPayloadArg())

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	client := &http.Client{
		Timeout: 12 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socketPath)
			},
			DisableCompression: true,
			DisableKeepAlives:  true,
		},
	}

	req, err := http.NewRequest(http.MethodPost, "http://unix/v1/read-model", bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "status %s: %s\n", resp.Status, string(body))
		os.Exit(1)
	}

	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		fmt.Println(string(body))
		return
	}
	pretty, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		fmt.Println(string(body))
		return
	}
	fmt.Println(string(pretty))
}

func loadPayloadArg() []byte {
	if len(os.Args) < 3 {
		return []byte("{}")
	}
	raw := strings.TrimSpace(os.Args[2])
	if raw == "" {
		return []byte("{}")
	}
	if strings.HasPrefix(raw, "@") {
		path := strings.TrimPrefix(raw, "@")
		path = filepath.Clean(path)
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read payload file %s: %v\n", path, err)
			os.Exit(1)
		}
		return data
	}
	return []byte(raw)
}

func normalizePayload(b []byte) []byte {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "" {
		return []byte("{}")
	}
	var tmp any
	if err := json.Unmarshal(b, &tmp); err != nil {
		fmt.Fprintf(os.Stderr, "invalid JSON body: %v\n", err)
		os.Exit(1)
	}
	return b
}
