package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	logPath := flag.String("log", "", "LimitDock log file (default: <release>/state/logs/limitdock.log)")
	releaseDir := flag.String("release-dir", "", "release folder used when -log is empty")
	minHits := flag.Int("min-hits", 1, "minimum successful native reader log lines required")
	timeout := flag.Duration("timeout", 90*time.Second, "maximum wait time")
	flag.Parse()

	path := strings.TrimSpace(*logPath)
	if path == "" {
		root := strings.TrimSpace(*releaseDir)
		if root == "" {
			fmt.Fprintln(os.Stderr, "limitdock not ready: pass -log or -release-dir")
			os.Exit(1)
		}
		path = filepath.Join(root, "state", "logs", "limitdock.log")
	}

	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		hits, err := countReaderSuccess(path)
		if err == nil && hits >= *minHits {
			fmt.Printf("ready log=%s reader_success_lines=%d\n", path, hits)
			return
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Fprintf(os.Stderr, "limitdock not ready: need >=%d native reader success lines in %s\n", *minHits, path)
	os.Exit(1)
}

func countReaderSuccess(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	hits := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.ToLower(scanner.Text())
		if strings.Contains(line, "reader captured") ||
			strings.Contains(line, "reader captured quota") ||
			strings.Contains(line, "reader captured local quota") {
			hits++
		}
	}
	return hits, scanner.Err()
}
