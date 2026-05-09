# LimitDock

Windows bottom status overlay for OpenUsage.sh data.

## Run

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\run-limitdock.ps1
```

For a fully hidden launch, double-click `launch-limitdock.vbs`.

LimitDock starts the bundled/downloaded OpenUsage.sh daemon, reads `/v1/read-model` through the daemon socket, and stops the daemon when LimitDock exits.
LimitDock uses OpenUsage.sh's default per-user daemon socket under `%USERPROFILE%\.local\state\openusage`.
Only one LimitDock instance runs per Windows session. A second launch exits early so it cannot remove the active socket or stop another instance's daemon.

## Controls

- Click a provider card to show an ASCII-only detail popup.
- Click the `Auto Hide: On/Off` control on the bar to toggle bottom-edge reveal mode. The label always reflects the current state.
- Click the `Settings` control on the bar to open the settings dialog.
- When Auto Hide is on, the bar slides fully offscreen and reappears when the cursor moves into the strip just above the taskbar (or to the very bottom of the screen if the taskbar itself is auto-hidden). The bar stays visible while the cursor is over it.
- Use the tray menu for `Refresh`, `Auto Hide`, `Settings`, `Open logs`, and `Exit`.

## Settings

Settings are saved in `settings.json`.

- `autoHide`: remembers the Auto Hide state.
- `antigravity.enabled`: shows or hides the Antigravity local presence card.
- `antigravity.dataDir`: optional override for the Antigravity data directory.
- `antigravity.binaryPath`: optional override for the Antigravity executable path.

When no Antigravity path is configured, LimitDock checks `antigravity` on `PATH` and `%USERPROFILE%\.gemini\antigravity`. No user-specific local path is hardcoded.

The Antigravity card is rendered in an info-only style (because OpenUsage.sh does not yet expose an Antigravity quota provider). Click the card to see binary path, data dir, and last conversation timestamp.

## Icons

Provider icons are stored as small PNG files under `assets/icons`. Regenerate them from upstream OpenUsage.sh website/release assets when the provider icon set changes.

If `engine/downloads/openusage_windows_amd64/openusage.exe` is missing, LimitDock downloads the latest official OpenUsage.sh Windows release. If `engine/bin/openusage-readmodel.exe` is missing, LimitDock builds it from `probes/openusage-readmodel` when Go is available.

## Checks And Packaging

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\check.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build-release.ps1 -Version 0.1.0
```

The release script prepares `dist\LimitDock-<version>`. It builds the Go read-model probe, copies icons and docs, and creates `LimitDock.exe` when `Invoke-ps2exe` is installed. See `docs\EXE_PACKAGING.md` for the recommended EXE layout and future refactoring path.

## Repository Hygiene

Runtime databases, logs, PID files, downloaded OpenUsage binaries, and local Go build caches are ignored by `.gitignore`.
Keep `assets/icons`, `probes/openusage-readmodel`, and the root launch scripts under version control; treat `engine/state`, `engine/bin`, and `engine/downloads` as local runtime output. Do not vendor or patch upstream OpenUsage.sh source inside this project.
