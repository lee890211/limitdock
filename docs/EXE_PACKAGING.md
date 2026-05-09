# LimitDock EXE Packaging

LimitDock should be managed as a small Windows shell around two external runtime pieces:

- `LimitDock.exe` or `LimitDock.ps1`: the WinForms HUD and tray controller.
- `engine/bin/openusage-readmodel.exe`: the tiny Go bridge that reads OpenUsage.sh's Unix-socket HTTP API.
- `engine/downloads/openusage_windows_amd64/openusage.exe`: the official OpenUsage.sh daemon binary, downloaded on first run.

## Recommended Release Shape

```text
LimitDock-<version>/
  LimitDock.exe
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

Do not ship `settings.json`, databases, WAL files, PID files, logs, OpenUsage.sh source, or patched OpenUsage.sh binaries. LimitDock should fetch the official OpenUsage.sh Windows release when the runtime binary is missing.

## Build

Run checks:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\check.ps1
```

Prepare a release folder:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build-release.ps1 -Version 0.1.0
```

`build-release.ps1` builds `openusage-readmodel.exe`, copies runtime assets, and uses `Invoke-ps2exe` when it is installed. If `Invoke-ps2exe` is not installed, it still creates a script-based release folder so packaging can be checked without changing the source layout. By default it does not bundle OpenUsage.sh; pass `-IncludeOpenUsageBinary` only when an explicitly offline release needs the cached official binary.

## Refactoring Path

Keep the current root `LimitDock.ps1` as the compatibility entrypoint until the first EXE release is stable. After that, split the script by responsibility:

- `src/LimitDock.Bootstrap.ps1`: paths, single-instance guard, lifecycle.
- `src/LimitDock.OpenUsage.ps1`: daemon start/stop, socket read-model calls, snapshot normalization.
- `src/LimitDock.Cards.ps1`: provider card conversion and quota/gauge selection.
- `src/LimitDock.Ui.ps1`: WinForms controls, tray, auto-hide, dialogs.
- `src/LimitDock.Settings.ps1`: LimitDock settings and OpenUsage settings editor.

Each split should be mechanical and verified with `scripts/check.ps1`; avoid changing behavior during the split.

## Longer-Term Option

If the HUD grows beyond what PowerShell WinForms can comfortably support, migrate the shell to a small .NET WinForms or WPF app and keep OpenUsage.sh as the external engine. The data boundary should stay the same: supervise the daemon, read `/v1/read-model`, normalize cards, render the HUD.
