package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"limitdock/internal/native"
	"limitdock/internal/paths"
	"limitdock/internal/ui"
)

func main() {
	noDownload := flag.Bool("NoDownload", false, "do not download OpenUsage when the binary is missing")
	noDownloadLower := flag.Bool("no-download", false, "do not download OpenUsage when the binary is missing")
	refresh := flag.Int("RefreshSeconds", 30, "refresh interval in seconds")
	restoreWorkArea := flag.String("restore-workarea", "", "internal: restore workarea as left,top,right,bottom")
	restoreDelay := flag.Int("restore-delay-ms", 1200, "internal: delay before restoring workarea")
	flag.Parse()

	if *restoreWorkArea != "" {
		if *restoreDelay > 0 {
			time.Sleep(time.Duration(*restoreDelay) * time.Millisecond)
		}
		rect, err := parseRestoreRect(*restoreWorkArea)
		if err != nil {
			fmt.Fprintf(os.Stderr, "LimitDock restore-workarea failed: %v\n", err)
			os.Exit(2)
		}
		if err := native.SetWorkArea(rect); err != nil {
			fmt.Fprintf(os.Stderr, "LimitDock restore-workarea failed: %v\n", err)
			os.Exit(2)
		}
		return
	}

	mutex, created, err := native.AcquireSingleInstance("Local\\LimitDock.OpenUsageHud")
	if err != nil {
		fmt.Fprintf(os.Stderr, "LimitDock cannot create its single-instance guard: %v\n", err)
		os.Exit(1)
	}
	if !created {
		fmt.Fprintln(os.Stderr, "LimitDock is already running in this Windows session.")
		return
	}
	defer mutex.Close()

	root := paths.ResolveRoot()
	p := paths.New(root)
	opts := ui.Options{NoDownload: *noDownload || *noDownloadLower, RefreshSeconds: *refresh}
	if err := ui.Run(p, opts); err != nil {
		fmt.Fprintf(os.Stderr, "LimitDock failed: %v\n", err)
		os.Exit(1)
	}
}

func parseRestoreRect(raw string) (native.Rect, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return native.Rect{}, fmt.Errorf("expected left,top,right,bottom")
	}
	vals := [4]int32{}
	for i, part := range parts {
		n, err := strconv.ParseInt(strings.TrimSpace(part), 10, 32)
		if err != nil {
			return native.Rect{}, err
		}
		vals[i] = int32(n)
	}
	return native.Rect{Left: vals[0], Top: vals[1], Right: vals[2], Bottom: vals[3]}, nil
}
