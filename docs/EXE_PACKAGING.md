# LimitDock EXE Packaging

LimitDock is packaged as a small Windows shell around two runtime pieces:

- `LimitDock.exe` or `LimitDock.ps1`: WinForms status bar, tray controller, settings, and docking.
- `engine/bin/openusage-readmodel.exe`: Go bridge that reads the OpenUsage.sh daemon socket.
- `engine/downloads/openusage_windows_amd64/openusage.exe`: official OpenUsage.sh daemon binary, downloaded on first run unless bundled intentionally.

## Release Shape

```text
LimitDock-<version>/
  LimitDock.exe
  LimitDock.ps1                    # only when ps2exe is not installed
  run-limitdock.ps1
  launch-limitdock.vbs
  stop-limitdock.ps1
  README.md
  NOTES.md
  settings.example.json
  assets/icons/*.png
  engine/bin/openusage-readmodel.exe
  engine/downloads/
  engine/state/
```

Do not ship `settings.json`, databases, WAL files, PID files, logs, Go caches, OpenUsage.sh source, or personal runtime downloads. LimitDock fetches the official OpenUsage.sh Windows release when the runtime binary is missing.

Publish `dist/LimitDock-<version>.zip`. Do not publish `LimitDock.exe` alone: the EXE expects `engine/bin/openusage-readmodel.exe`, `assets/icons`, launch scripts, and runtime directories beside it. The full Markdown docs stay in the repository and are not copied into the default release folder.

## Build And Test

Run checks first:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\check.ps1
```

Prepare a release folder and archive:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build-release.ps1 -Version 0.1.0
```

If `Invoke-ps2exe` is not available, the release folder is still created with `LimitDock.ps1`. Install or import `ps2exe` before expecting `LimitDock.exe`.

Typical ps2exe install:

```powershell
Install-Module ps2exe -Scope CurrentUser
```

The script removes local `settings.json` before creating `dist/LimitDock-<version>.zip`. If you run the EXE from the release folder, a fresh local `settings.json` can appear there; rebuild before publishing the zip. The script does not bundle OpenUsage.sh by default. Use `-IncludeOpenUsageBinary` only for an explicit offline release after verifying the cached binary is the official upstream build.

## EXE Smoke Test

1. Run `scripts\check.ps1`.
2. Run `scripts\build-release.ps1 -Version 0.1.0`.
3. Start `dist\LimitDock-0.1.0\LimitDock.exe` when present, or `run-limitdock.ps1` from the release folder when ps2exe is unavailable.
4. Open the tray menu and verify `Hide Status Bar`, `Settings`, and `Exit`.
5. Switch overlay/reserved and each dock edge. In reserved mode, maximize a normal window and verify it does not cover the reserved bar.
6. Double-click quota cards and verify hidden rows can be restored.

## Future Split

Keep the root `LimitDock.ps1` as the compatibility entrypoint until the first EXE release is stable. If the script is split later, keep the change mechanical:

- `src/LimitDock.Bootstrap.ps1`: paths, single-instance guard, lifecycle.
- `src/LimitDock.OpenUsage.ps1`: daemon start/stop, socket read-model calls, snapshot normalization.
- `src/LimitDock.Cards.ps1`: provider card conversion and quota/gauge selection.
- `src/LimitDock.Ui.ps1`: WinForms controls, tray, docking, dialogs.
- `src/LimitDock.Settings.ps1`: LimitDock settings and OpenUsage settings editor.

Each split should pass `scripts\check.ps1` without changing behavior.
