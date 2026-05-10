package readmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type Client struct {
	SocketPath string
	Timeout    time.Duration
}

func (c Client) Read(ctx context.Context, payload any) (*ReadModel, []byte, error) {
	body := []byte("{}")
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, nil, err
		}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", c.SocketPath)
			},
			DisableCompression: true,
			DisableKeepAlives:  true,
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/read-model", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, raw, fmt.Errorf("read model status %s: %s", resp.Status, string(raw))
	}
	var out ReadModel
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, err
	}
	return &out, raw, nil
}
