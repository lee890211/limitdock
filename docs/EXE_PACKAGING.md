# LimitDock EXE Packaging

LimitDock is packaged as a small Windows shell around one managed runtime piece:

- `LimitDock.exe`: Go native status bar, tray controller, settings, socket read-model adapter, and docking.
- `LimitDock.ps1`: legacy fallback kept in the repository during the Go migration.
- `engine/downloads/openusage_windows_amd64/openusage.exe`: official OpenUsage.sh daemon binary, downloaded on first run unless bundled intentionally.

## Release Shape

```text
LimitDock-<version>/
  LimitDock.exe
  LimitDock.exe.manifest
  run-limitdock.ps1
  launch-limitdock.vbs
  stop-limitdock.ps1
  README.md
  NOTES.md
  settings.example.json
  assets/icons/*.png
  docs/images/*.png
  engine/downloads/
  engine/state/
```

Do not ship `settings.json`, databases, WAL files, PID files, logs, Go caches, OpenUsage.sh source, or personal runtime downloads. LimitDock fetches the official OpenUsage.sh Windows release when the runtime binary is missing.

Publish `dist/LimitDock-<version>.zip`. Do not publish `LimitDock.exe` alone: the EXE expects `assets/icons`, launch scripts, README screenshots, and runtime directories beside it. The full Markdown docs stay in the repository and are not copied into the default release folder.

## Build And Test

Run checks first:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\check.ps1
```

Prepare a release folder and archive:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build-release.ps1 -Version vYYYYMMDD
```

The script removes local `settings.json` before creating `dist/LimitDock-<version>.zip`. If you run the EXE from the release folder, a fresh local `settings.json` can appear there; rebuild before publishing the zip. The script does not bundle OpenUsage.sh by default. Use `-IncludeOpenUsageBinary` only for an explicit offline release after verifying the cached binary is the official upstream build.

## EXE Smoke Test

1. Run `scripts\check.ps1`.
2. Run `scripts\build-release.ps1 -Version vYYYYMMDD`.
3. Start `dist\LimitDock-vYYYYMMDD\LimitDock.exe`, or `run-limitdock.ps1` from the release folder.
4. Open the tray menu and verify `Hide Status Bar`, `Settings`, and `Exit`.
5. Switch overlay/reserved and each dock edge. In reserved mode, maximize a normal window and verify it does not cover the reserved bar.
6. Toggle `Start LimitDock when Windows starts`, verify the user Startup shortcut appears, then clear it and verify the shortcut is removed.
7. Double-click quota cards and verify hidden rows can be restored.

## Legacy Fallback

Keep the root `LimitDock.ps1` in the repository as the compatibility fallback until the Go EXE release is stable. New behavior belongs in `cmd/limitdock` and `internal/*`; changes to the fallback script should be limited to emergency compatibility fixes.
