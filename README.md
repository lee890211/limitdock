# LimitDock

LimitDock is a compact Windows status bar for OpenUsage.sh quota telemetry. It shows remaining quota only, grouped by provider, model, and reset window.

## Run

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\run-limitdock.ps1
```

For a fully hidden launch, double-click `launch-limitdock.vbs`.

LimitDock starts the bundled or downloaded OpenUsage.sh daemon, reads `/v1/read-model` through the daemon socket, and stops the daemon when LimitDock exits. Only one LimitDock instance runs per Windows session.

## Tray Menu

The tray menu is intentionally small:

- `Hide Status Bar` or `Show Status Bar`
- `Settings`
- `Exit`

Hide unregisters any reserved appbar area, restores the Windows work area, hides the form, disables hover reveal, and pauses UI refresh. The tray icon stays alive so the bar can be shown again.

## Docking

LimitDock supports two display modes:

- `overlay`: floats above other windows. The pin icon is visible only in this mode.
- `reserved`: registers a Windows appbar area and applies a matching work area so maximized windows leave room for the bar.

The dock edge can be `bottom`, `top`, `left`, or `right`. In reserved mode, use the three-dot handle in the tool rail to snap the bar to the nearest screen edge. Settings are persisted in local `settings.json`.

## Quota Rows

Provider cards show quota-like rows only:

- Codex `rate_limit_*` rows, including Spark labels exposed through `rate_limit_codex_bengalfox_name`.
- Gemini model-specific `quota_model_*` rows. Aggregate `quota`, `quota_flash`, and `quota_pro` rows are suppressed when model-specific rows exist.
- Cursor billing-cycle rows from `plan_percent_used`, `plan_api_percent_used`, and `plan_auto_percent_used`.

Spend, request, token, tool-call, and cost rows are not rendered. Exhausted or 0 percent rows stay visible unless the user explicitly hides them. Double-click a provider card to choose visible model/window rows.

## Antigravity

LimitDock does not add custom Antigravity quota parsing. Antigravity quota appears only when OpenUsage exposes it as a provider or quota-like snapshot.

Use Settings for manual environment hints:

- `antigravity.binaryPath`: path to the Antigravity executable when it is not on `PATH`.
- `antigravity.dataDir`: Antigravity or Gemini conversation/workspace root to probe.
- `antigravity.subtitle`: a manual label such as `Claude + Gemini`.

## Checks And Packaging

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\check.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build-release.ps1 -Version 0.1.0
```

`build-release.ps1` prepares `dist\LimitDock-<version>`. It builds the Go read-model probe and creates `LimitDock.exe` when `Invoke-ps2exe` is installed. If `Invoke-ps2exe` is missing, install or import `ps2exe` before expecting an EXE.

See:

- `docs\USER_MANUAL.md`
- `docs\PRODUCT_DESIGN.md`
- `docs\ARCHITECTURE.md`
- `docs\EXE_PACKAGING.md`

## Repository Hygiene

Runtime databases, logs, PID files, downloaded OpenUsage binaries, release folders, local Go caches, and `settings.json` are ignored by `.gitignore`. Keep `settings.example.json` as the shareable default.
