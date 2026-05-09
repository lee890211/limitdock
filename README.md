# LimitDock

LimitDock is a compact Windows agent status dock powered by [OpenUsage.sh](https://github.com/janekbaraniewski/openusage). It shows OpenUsage quota telemetry only, grouped by provider, model, and reset window.

## OpenUsage.sh Dependency

LimitDock is a desktop UI layer for OpenUsage.sh, not a separate quota parser. Provider discovery, telemetry collection, and quota snapshots come from the OpenUsage daemon; LimitDock supervises that daemon, reads `/v1/read-model`, and renders the quota-like rows in a compact Windows dock.

OpenUsage.sh is a terminal-oriented project with a macOS-first workflow. It can be used from Windows through the available shell/terminal path, but that is not an ideal fit for an always-visible Windows desktop status dock. LimitDock exists to make the same OpenUsage telemetry feel native enough for a Windows agent bar.

On first run, LimitDock downloads the Windows binary release from the [OpenUsage.sh repository](https://github.com/janekbaraniewski/openusage) when the binary is not already bundled.

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

Hide unregisters any reserved appbar area, restores the Windows work area, hides the form, disables hover reveal, and pauses UI refresh. The tray icon stays alive so the bar can be shown again. Hide is session-only; a new launch shows the bar.

## Docking

LimitDock supports two display modes:

- `overlay`: floats above other windows. The pin icon is visible only in this mode.
- `reserved`: registers a Windows appbar area and applies a matching work area so maximized windows leave room for the bar.

The dock edge can be `bottom`, `top`, `left`, or `right`. Change the edge from Settings. Settings are persisted in local `settings.json`.

## Quota Rows

Provider cards show quota-like rows only:

- Codex `rate_limit_*` rows, including Spark labels exposed through `rate_limit_codex_bengalfox_name`.
- Gemini model-specific `quota_model_*` rows. Aggregate `quota`, `quota_flash`, and `quota_pro` rows are suppressed when model-specific rows exist.
- Cursor billing-cycle row from `plan_percent_used`.

Spend, request, token, tool-call, and cost rows are not rendered. Exhausted or 0 percent rows stay visible unless the user explicitly hides them. Double-click a provider card to choose visible model/window rows.

## Antigravity

LimitDock does not add custom Antigravity quota parsing. Antigravity quota appears only when OpenUsage exposes it as a provider or quota-like snapshot.

## Checks And Packaging

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\check.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build-release.ps1 -Version 0.1.0
```

`build-release.ps1` prepares `dist\LimitDock-<version>` and `dist\LimitDock-<version>.zip`. It builds the Go read-model probe and creates `LimitDock.exe` when `Invoke-ps2exe` is installed. If `Invoke-ps2exe` is missing, install or import `ps2exe` before expecting an EXE. Distribute the generated zip, not `LimitDock.exe` by itself, because the EXE expects the probe, icons, and runtime folders beside it.

See:

- `docs\USER_MANUAL.md`
- `docs\PRODUCT_DESIGN.md`
- `docs\ARCHITECTURE.md`
- `docs\EXE_PACKAGING.md`

## Repository Hygiene

Runtime databases, logs, PID files, downloaded OpenUsage binaries, release folders, local Go caches, and `settings.json` are ignored by `.gitignore`. Keep `settings.example.json` as the shareable default.
