# LimitDock

LimitDock is a compact Windows desktop dock that shows remaining agent quota at a glance. It turns local usage/quota output into an always-visible desktop ribbon, focused on one thing: how much model quota is left, and when it resets.

![LimitDock night ribbon](docs/images/manual-ribbon-night.png)

## What It Shows

- Remaining quota only, grouped by provider, model or plan bucket, reset countdown, and percent.
- Codex rate-limit rows, including Spark labels from local Codex session events.
- Cursor plan-cycle quota from `plan_percent_used` with `billing_cycle_end` reset text.
- Gemini model-specific quota rows, while suppressing aggregate duplicates when precise model rows exist.
- Antigravity quota rows from the LimitDock custom reader when local quota-like data is available.
- Exhausted or 0 percent rows, unless the user explicitly hides them.

LimitDock intentionally does not show spend, token totals, request counts, tool-call counts, or generic activity rows.

Bundled provider icons are neutral LimitDock badges, not official brand logos. If you replace files under `assets/icons` with official brand assets, make sure the usage is allowed by that provider's brand guidelines and trademark terms.

LimitDock reads quota directly from each tool's local credentials, session files, or APIs. No external daemon is required.

## Provider Support

LimitDock renders only quota-like rows. A provider with no local credential evidence stays absent. Once local credentials have been discovered, the card can stay visible even without quota bands: `Sign in` when those credentials exist but are unusable, or `stale` when the latest fetch failed and last-known values are shown.

| Provider or agent | Notes |
| --- | --- |
| Claude Code | OAuth usage from `~/.claude/.credentials.json`, `CLAUDE_CODE_OAUTH_TOKEN`, or LimitDock's own Connect sign-in (see below). Expired CLI tokens are refreshed and written back automatically, so quota keeps working without the `claude` CLI running. Real utilization percent, not a time-elapsed estimate. |
| Codex CLI | Merges ChatGPT wham usage with recent `.codex` session/log `rate_limits` events so 5h and 7d windows stay accurate. Expired tokens are refreshed and written back to `auth.json` automatically. |
| Antigravity | Local language-server status first; falls back to a cached `%APPDATA%\Antigravity` quota file only when it is under 45 minutes old (shown as `stale`). No card when neither source has quota rows. |
| Gemini CLI | `~/.gemini/oauth_creds.json` (or related credential files) and Code Assist `retrieveUserQuota`. Expired tokens are refreshed and written back automatically. Model-specific rows are preferred; aggregate duplicates are suppressed when model rows exist. |
| Cursor | Token from `%APPDATA%\Cursor\User\globalStorage\state.vscdb` and Connect `GetCurrentPeriodUsage`. Refreshed tokens are cached in memory across polls. Plan-cycle quota from `plan_percent_used`; reset from `billing_cycle_end`. |

## User Guide

### Ribbon Mode

Use `bottom` or `top` for a horizontal ribbon. Each provider card shows one or more quota rows. Row labels keep the model or plan bucket, including the metering window when available; the timing column shows only the reset countdown. Remaining percent is drawn inside the gauge. When two rows are visible, they use two full-width lines; three or four rows use a compact grid. Long labels are shortened before reset time or remaining percent disappear. On wide displays, the ribbon can fit up to five provider cards before overflowing.

The `Updated` panel is clickable. Click it to force an immediate refresh even if the automatic refresh interval has not elapsed.

Light theme:

![LimitDock light ribbon](docs/images/manual-ribbon-light.png)

Night theme:

![LimitDock night ribbon](docs/images/manual-ribbon-night.png)

### Side Dock

Use `left` or `right` for a vertical strip. Provider cards stack vertically and each quota row gets its own line.

Light theme:

![LimitDock light side dock](docs/images/manual-side-dock-light.png)

Night theme:

![LimitDock night side dock](docs/images/manual-side-dock-night.png)

### Overlay Opacity And Auto Slide

Overlay opacity can be previewed live from Settings. For documentation captures, LimitDock is placed over a neutral backdrop so no desktop or private window content is visible through transparency.

![LimitDock overlay opacity](docs/images/manual-overlay-opacity.png)

When auto slide is enabled in overlay mode, the dock keeps only a thin edge strip visible while unpinned and slides back in on hover.

![LimitDock slide in and out](docs/images/manual-slide-in-out.gif)

### Visible Row Picker

Double-click a provider card to choose which model/window rows are visible. Hidden rows are never deleted and can be restored from the same picker.

![LimitDock visible row picker](docs/images/manual-row-picker.png)

### Tray Menu

Right-click the tray icon:

- `Hide Status Bar`: hides the dock, unregisters reserved mode, restores the Windows work area, disables hover reveal, and pauses refresh. Hide is session-only.
- `Show Status Bar`: restores the saved mode, edge, and pin state, then refreshes.
- `Settings`: opens docking, theme, refresh, startup, threshold, provider connection, and log/diagnostic controls.
- `Exit`: closes LimitDock.

### Connect Claude

If Claude credentials exist but are unusable, its card shows `Sign in`. If Claude was never configured locally, no Claude card appears — use `Settings → Providers → Connect...` for first-time sign-in. Open the Connect flow from the tray's `Connect Claude...` item (visible only while the Claude card is `Sign in`), from `Settings → Providers` (always available), or by double-clicking the Claude card. Click through to open the Claude sign-in page in your browser, approve LimitDock, then copy the code the page shows — LimitDock auto-fills when it looks like a sign-in code, or use the dialog's `Paste` button / Ctrl+V. LimitDock stores the resulting token itself, DPAPI-encrypted under `state\credentials\`, so this works even without the `claude` CLI installed. Disconnect from the same `Settings → Providers` panel.

### Docking

LimitDock supports two display modes:

- `reserved`: the default first-run mode. It registers a Windows appbar and applies a matching work area so maximized windows leave room for the dock.
- `overlay`: floats above other windows. The pin icon appears only in overlay mode.

Edges are `bottom`, `top`, `left`, and `right`. `Settings` shows them as four visual screen-edge buttons in `bottom`, `left`, `top`, `right` order instead of a dropdown so the selected dock side is easier to recognize. Docking choices are stored in local `settings.json`.

The `Theme` row uses two visual day/night buttons. `Overlay opacity %` previews transparency live in overlay mode; Cancel restores the previous value and Save persists it. `Start LimitDock when Windows starts` writes a per-user `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` value that points directly to `LimitDock.exe`, so no administrator rights or script launcher are required. Turn the checkbox off to remove the value. `Auto slide in overlay mode` controls whether an unpinned overlay dock slides away at the selected edge.

Antigravity support is quota-only and automatic. LimitDock renders a card only when it can read local percent/reset or prompt-credit quota data. It does not add an installation/status placeholder card.

## Install

1. Download the latest `LimitDock-<version>.zip` from GitHub Releases.
2. Extract the whole folder. Do not run `LimitDock.exe` by itself; it expects the icons, settings reference, and runtime folders beside it.
3. Run `LimitDock.exe`.

A local `settings.json` is created next to the app on first run. The release includes `settings.example.json` as the portable configuration reference; personal `settings.json` files are never committed or shipped.

## Build From Source

Requirements:

- Go, for the native `LimitDock.exe` and release tool

```text
go test ./...
go build -ldflags "-H windowsgui" -o LimitDock.exe .\cmd\limitdock
go run .\cmd\limitdock-release -version vYYYYMMDD
```

The build creates:

- `dist\LimitDock-<version>\`
- `dist\LimitDock-<version>.zip`

The runtime app is Go-only. Legacy script app and probe files were removed after the migration.

For docking changes, run the project smoke workflow in `.codex/skills/limitdock-smoke/SKILL.md`. Rule-based Go tests cover dock geometry, reserved work-area calculations, appbar DPI correction, five-card ribbon width, settings compatibility, quota normalization, and provider fallback merging. The smoke workflow adds live Windows checks for all edges and display modes.

## Documentation

- [User Manual](docs/USER_MANUAL.md)
- [Product Design](docs/PRODUCT_DESIGN.md)
- [Architecture](docs/ARCHITECTURE.md)
- [EXE Packaging](docs/EXE_PACKAGING.md)

## Repository Hygiene

Runtime databases, logs, PID files, release folders, local Go caches, and `settings.json` are ignored. Keep `settings.example.json` as the shareable default.
