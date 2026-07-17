// Command ldiag is a temporary live diagnostic harness used during
// development verification. It runs one provider reader against real local
// credentials and prints the resulting snapshots (never tokens).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"limitdock/internal/paths"
	"limitdock/internal/provider"
	"limitdock/internal/quota"
	"limitdock/internal/settings"
)

type stdoutLogger struct{}

func (stdoutLogger) Printf(format string, args ...any) {
	fmt.Printf("[log] "+format+"\n", args...)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: ldiag claude|codex|gemini|cursor|antigravity")
		os.Exit(2)
	}
	root := paths.ResolveRoot()
	credDir := filepath.Join(root, "state", "credentials")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var r provider.Reader
	switch os.Args[1] {
	case "claude":
		r = provider.ClaudeCodeReader{Log: stdoutLogger{}, CredStoreDir: credDir}
	case "codex":
		r = provider.CodexReader{Log: stdoutLogger{}}
	case "gemini":
		r = provider.GeminiCLIReader{Log: stdoutLogger{}}
	case "cursor":
		r = provider.CursorReader{Log: stdoutLogger{}}
	case "antigravity":
		r = provider.AntigravityReader{Log: stdoutLogger{}}
	default:
		fmt.Println("unknown provider", os.Args[1])
		os.Exit(2)
	}

	start := time.Now()
	model, err := r.Read(ctx)
	fmt.Printf("== %s read took %s\n", os.Args[1], time.Since(start).Round(time.Millisecond))
	if err != nil {
		fmt.Printf("READ ERROR: %v\n", err)
		os.Exit(1)
	}
	if model == nil || len(model.Snapshots) == 0 {
		fmt.Println("EMPTY MODEL (silent skip)")
		return
	}
	for key, snap := range model.Snapshots {
		fmt.Printf("snapshot=%s provider=%s account=%s status=%q message=%q\n", key, snap.ProviderID, snap.AccountID, snap.Status, snap.Message)
		metrics, _ := json.MarshalIndent(snap.Metrics, "  ", "  ")
		fmt.Println("  metrics:", string(metrics))
		if len(snap.Resets) > 0 {
			resets, _ := json.Marshal(snap.Resets)
			fmt.Println("  resets:", string(resets))
		}
		if len(snap.Attributes) > 0 {
			attrs, _ := json.Marshal(snap.Attributes)
			fmt.Println("  attributes:", string(attrs))
		}
		if len(snap.Raw) > 0 {
			raw, _ := json.Marshal(snap.Raw)
			fmt.Println("  raw:", string(raw))
		}
	}
	for _, card := range quota.Cards(model, settings.Defaults()) {
		fmt.Printf("card name=%s main=%q level=%s status=%q detail=%q bands=%d\n", card.Name, card.Main, card.Level, card.Status, card.Detail, len(card.Bands))
		for _, b := range card.Bands {
			pct := "-"
			if b.Percent != nil {
				pct = fmt.Sprintf("%.1f%%", *b.Percent)
			}
			fmt.Printf("  band key=%s caption=%q remaining=%s window=%q reset=%q\n", b.Key, b.Caption, pct, b.Window, b.Reset)
		}
	}
}
