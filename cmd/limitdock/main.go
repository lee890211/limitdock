package main

import (
	"flag"
	"fmt"
	"os"

	"limitdock/internal/native"
	"limitdock/internal/paths"
	"limitdock/internal/ui"
)

func main() {
	noDownload := flag.Bool("NoDownload", false, "do not download OpenUsage when the binary is missing")
	noDownloadLower := flag.Bool("no-download", false, "do not download OpenUsage when the binary is missing")
	refresh := flag.Int("RefreshSeconds", 30, "refresh interval in seconds")
	flag.Parse()

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
