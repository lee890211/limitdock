# LimitDock EXE Packaging

LimitDock is packaged as a small Windows shell around one managed runtime piece:

- `LimitDock.exe`: Go native status bar, tray controller, settings, socket read-model adapter, and docking.
- `engine/downloads/openusage_windows_amd64/openusage.exe`: official OpenUsage.sh daemon binary, downloaded on first run unless bundled intentionally.

## Release Shape

```text
LimitDock-<version>/
  LimitDock.exe
  LimitDock.exe.manifest
  README.md
  NOTES.md
  settings.example.json
  assets/icons/*.png
  docs/images/*.png
  engine/downloads/
  engine/state/
```

Do not ship `settings.json`, databases, WAL files, PID files, logs, Go caches, OpenUsage.sh source, or personal runtime downloads. LimitDock fetches the official OpenUsage.sh Windows release when the runtime binary is missing.

Publish `dist/LimitDock-<version>.zip`. Do not publish `LimitDock.exe` alone: the EXE expects `assets/icons`, README screenshots, and runtime directories beside it. The full Markdown docs stay in the repository and are not copied into the default release folder.

## Build And Test

Run checks first:

```text
go test ./...
go build -ldflags "-H windowsgui" -o engine\bin\LimitDock.exe .\cmd\limitdock
```

Prepare a release folder and archive:

```text
go run .\cmd\limitdock-release -version vYYYYMMDD
```

The release tool does not copy local `settings.json` into `dist/LimitDock-<version>.zip`. If you run the EXE from the release folder, a fresh local `settings.json` can appear there; rebuild before publishing the zip. The tool does not bundle OpenUsage.sh by default. Use `-include-openusage-binary` only for an explicit offline release after verifying the cached binary is the official upstream build.

## EXE Smoke Test

1. Run `go test ./...`.
2. Run `go run .\cmd\limitdock-release -version vYYYYMMDD`.
3. Start `dist\LimitDock-vYYYYMMDD\LimitDock.exe`.
4. Open the tray menu and verify `Hide Status Bar`, `Settings`, and `Exit`.
5. Switch overlay/reserved and each dock edge. In reserved mode, maximize a normal window and verify it does not cover the reserved bar.
6. Toggle `Start LimitDock when Windows starts`, verify the per-user `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\LimitDock` value appears, then clear it and verify the value is removed.
7. Double-click quota cards and verify hidden rows can be restored.

## Go-Only Runtime

The legacy script app and standalone read-model probe were removed after the Go migration. Runtime behavior belongs in `cmd/limitdock` and `internal/*`; release packaging belongs in `cmd/limitdock-release`.
