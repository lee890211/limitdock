# LimitDock

LimitDock is a compact Windows status dock for agent quota telemetry from [OpenUsage.sh](https://github.com/janekbaraniewski/openusage). It turns the useful quota output from the OpenUsage terminal workflow into an always-visible desktop ribbon, focused on one thing: how much model quota is left, and when it resets.

![LimitDock top or bottom ribbon](docs/images/manual-ribbon.png)

LimitDock is not a replacement for OpenUsage.sh. Provider discovery, telemetry, and the read model belong to OpenUsage.sh; LimitDock supervises the OpenUsage daemon, reads its local read-model endpoint, normalizes quota-like rows, and renders them as a native WinForms dock. Many thanks to the OpenUsage.sh maintainers for building the local usage/quota foundation this project depends on.

## What It Shows

- Remaining quota only, grouped by provider, model or plan bucket, metering window, reset countdown, and percent.
- Codex rate-limit rows, including Spark labels exposed by OpenUsage attributes.
- Cursor plan-cycle quota from `plan_percent_used` with `billing_cycle_end` reset text.
- Gemini model-specific quota rows, while suppressing aggregate duplicates when precise model rows exist.
- Exhausted or 0 percent rows, unless the user explicitly hides them.

LimitDock intentionally does not show spend, token totals, request counts, tool-call counts, or generic activity rows.

## User Guide

### Ribbon Mode

Use `bottom` or `top` for a horizontal ribbon. Each provider card shows one or more quota rows. When two rows are visible, they use two full-width lines; three or four rows use a compact grid. Long model names are shortened before reset time or remaining percent disappear.

![LimitDock top or bottom ribbon](docs/images/manual-ribbon.png)

### Side Dock

Use `left` or `right` for a vertical strip. Provider cards stack vertically and each quota row gets its own line.

![LimitDock left or right dock](docs/images/manual-side-dock.png)

### Visible Row Picker

Double-click a provider card to choose which model/window rows are visible. Hidden rows are never deleted and can be restored from the same picker.

![LimitDock visible row picker](docs/images/manual-row-picker.png)

### Tray Menu

Right-click the tray icon:

- `Hide Status Bar`: hides the dock, unregisters reserved mode, restores the Windows work area, disables hover reveal, and pauses refresh. Hide is session-only.
- `Show Status Bar`: restores the saved mode, edge, and pin state, then refreshes.
- `Settings`: opens docking, refresh, threshold, and provider path settings.
- `Exit`: closes LimitDock and stops the managed OpenUsage daemon.

### Docking

LimitDock supports two display modes:

- `overlay`: floats above other windows. The pin icon appears only in overlay mode.
- `reserved`: registers a Windows appbar and applies a matching work area so maximized windows leave room for the dock.

Edges are `bottom`, `top`, `left`, and `right`. Docking choices are stored in local `settings.json`.

## Install

1. Download the latest `LimitDock-<version>.zip` from GitHub Releases.
2. Extract the whole folder. Do not run `LimitDock.exe` by itself; it expects the probe, icons, scripts, and runtime folders beside it.
3. Run `LimitDock.exe`.

On first run, LimitDock downloads the official OpenUsage.sh Windows binary when it is not bundled. A local `settings.json` is created next to the app. The release includes `settings.example.json` as the portable configuration reference; personal `settings.json` files are never committed or shipped.

For script mode:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\run-limitdock.ps1
```

For a hidden script launch, double-click `launch-limitdock.vbs`.

## Build From Source

Requirements:

- Windows PowerShell
- Go, for `engine/bin/openusage-readmodel.exe`
- `ps2exe`, when building `LimitDock.exe`

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\check.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build-release.ps1 -Version vYYYYMMDD
```

The build creates:

- `dist\LimitDock-<version>\`
- `dist\LimitDock-<version>.zip`

If `Invoke-ps2exe` is unavailable, the release folder is still created with the PowerShell entrypoint, but no EXE.

## Provider Notes

Antigravity quota appears only when OpenUsage exposes it as a provider or quota-like snapshot. LimitDock does not add a custom Antigravity parser. You can still set local Antigravity hints in Settings:

- `antigravity.binaryPath`
- `antigravity.dataDir`
- `antigravity.subtitle`

## Documentation

- [User Manual](docs/USER_MANUAL.md)
- [Product Design](docs/PRODUCT_DESIGN.md)
- [Architecture](docs/ARCHITECTURE.md)
- [EXE Packaging](docs/EXE_PACKAGING.md)

## Repository Hygiene

Runtime databases, logs, PID files, downloaded OpenUsage binaries, release folders, local Go caches, and `settings.json` are ignored. Keep `settings.example.json` as the shareable default.
