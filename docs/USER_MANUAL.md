# User Manual

LimitDock is a Windows dock that shows remaining AI tool quota at a glance. It reads quota data directly from each provider's local credentials or session files. No external daemon is required.

## Start LimitDock

From a release folder, run:

```text
.\LimitDock.exe
```

From the repository, run:

```text
go run .\cmd\limitdock
```

LimitDock reads quota directly from each provider and renders provider cards that expose quota-like rows.

## Top Or Bottom Ribbon

Light theme:

![Top or bottom ribbon in light theme](images/manual-ribbon-light.png)

Night theme:

![Top or bottom ribbon in night theme](images/manual-ribbon-night.png)

The ribbon is best for wide monitors. Each provider card shows:

- provider name and icon
- model or plan bucket, including the metering window when available
- reset countdown with a clock icon
- remaining percent inside the gauge
- compact gauge

When two rows are visible, they use two full-width lines. When three or four rows are visible, they switch to a compact grid. On wide displays, the ribbon can fit up to five provider cards before overflowing.

The `Updated` panel is clickable. Click it to force an immediate refresh even if the automatic refresh interval has not elapsed.

## Visible Row Picker

Double-click a provider card to choose model/window rows.

![Visible row picker](images/manual-row-picker.png)

Checked rows are visible. Unchecked rows are hidden. Hidden rows stay in `settings.json` and can be restored later; LimitDock never deletes underlying quota data.

## Left Or Right Dock

Light theme:

![Left or right dock in light theme](images/manual-side-dock-light.png)

Night theme:

![Left or right dock in night theme](images/manual-side-dock-night.png)

Side docking is useful when a horizontal ribbon would take too much vertical space. Provider cards stack vertically, and each quota row gets its own line. Model labels are shortened before reset countdown or remaining percent are hidden.

## Overlay Opacity And Auto Slide

Overlay opacity is previewed live in `Settings`. Documentation captures use a neutral backdrop so transparency does not reveal the desktop or private windows.

![Overlay opacity preview](images/manual-overlay-opacity.png)

When `Auto slide in overlay mode` is enabled, an unpinned overlay dock collapses to a thin edge strip and slides back in when hovered.

![Slide-in and slide-out behavior](images/manual-slide-in-out.gif)

## Tray Controls

Right-click the tray icon:

- `Hide Status Bar`: hides the dock, unregisters reserved appbar space, restores the Windows work area, disables hover reveal, and pauses refresh.
- `Show Status Bar`: shows the dock again using saved settings.
- `Settings`: opens LimitDock docking, theme, refresh, startup, threshold, provider connection, and log/diagnostic controls.
- `Exit`: closes LimitDock.

Hide is not persisted. A fresh launch shows the dock again.

## Docking Settings

Open `Settings` from the tray.

Display modes:

- `reserved`: the default first-run mode. It reserves a Windows work area so maximized windows leave room for the dock.
- `overlay`: floats above other windows. Pin/unpin is available only in overlay mode.

Dock edges:

- `bottom`
- `top`
- `left`
- `right`

Dock edge is selected with four visual screen-edge buttons in `Settings` instead of a dropdown. The order is `bottom`, `left`, `top`, `right`.

The settings window groups controls into `Appearance`, `Docking`, `Quota display`, `Providers`, and `Diagnostics` sections. Dock mode, dock edge, monitor, theme, overlay opacity, auto-slide, refresh interval, gauge thresholds, and visible row choices are persisted in local `settings.json`. Change these from `Settings` at any time. The `Theme` row uses two visual day/night buttons to switch between the `light` and `night` themes. `Auto slide in overlay mode` controls whether an unpinned overlay dock slides away at the selected edge.

`Overlay opacity` applies only in overlay mode and can be set from 35 to 100. Dragging it previews the opacity immediately. `Cancel` restores the previous opacity, and `Save` persists it.

### Multiple Monitors

The `Monitor` row in the `Docking` section picks the display the dock lives on. `Automatic (primary display)` follows the Windows primary; selecting a specific display pins the dock — including its reserved work area in `reserved` mode — to that display. If the selected display is disconnected, LimitDock falls back to the primary display and moves back automatically once the display is reconnected. The selection is stored as the Windows display device name, so it survives restarts as long as Windows keeps the same display numbering.

With `Auto slide in overlay mode`, hover detection is bounded to the dock's own display: touching the matching edge of a different monitor neither reveals nor flickers the dock, and crossing between monitors does not flash it. When the docked edge borders a neighboring display (so the cursor can cross instead of pressing a hard screen edge), revealing requires a brief (~0.2 s) hover on that edge. Settings and other LimitDock dialogs open on the dock's display.

The settings window includes log and diagnostic controls. Use them to browse LimitDock logs, open `limitdock.log`, or copy diagnostic paths for troubleshooting. Antigravity path settings are intentionally absent because LimitDock auto-detects local Antigravity quota sources.

The settings window also includes a `Providers` section listing each provider's current credential state, with `Connect...`/`Disconnect` for Claude. See [Connect a Provider (Claude)](#connect-a-provider-claude).

`Start LimitDock when Windows starts` writes a per-user `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` value that points directly to `LimitDock.exe`. Clear the checkbox to remove the value. Older `LimitDock.lnk` startup shortcuts are cleaned up when this setting changes.

## Provider Behavior

LimitDock renders only quota-like rows by default. A provider with **no local credential evidence at all** stays absent (silent). Once local credentials have been discovered, the card can stay visible even without quota bands: `Sign in` when those credentials exist but are unusable, or `stale` when the latest fetch failed and last-known values are shown.

| Provider or agent | Notes |
| --- | --- |
| Claude Code | OAuth usage from `~/.claude/.credentials.json`, `CLAUDE_CODE_OAUTH_TOKEN`, or a LimitDock Connect sign-in (see below). Expired CLI tokens are refreshed and written back automatically. If credentials exist but cannot be used (expired refresh, rejected token), the card shows `Sign in`. If Claude was never configured locally, no card is shown. Real utilization percent, not a time-elapsed estimate. |
| Codex CLI | Merges ChatGPT wham usage with recent `.codex` session/log `rate_limits` rows. Expired tokens are refreshed and written back to `auth.json` automatically. |
| Gemini CLI | `~/.gemini/oauth_creds.json` and Code Assist `retrieveUserQuota`. Expired tokens are refreshed and written back automatically. Model-specific rows are preferred; aggregate duplicates are suppressed when model rows exist. |
| Cursor | `%APPDATA%\Cursor\User\globalStorage\state.vscdb` and Connect `GetCurrentPeriodUsage`. Refreshed tokens are cached in memory across polls. Plan-cycle quota from `plan_percent_used`; reset text from `billing_cycle_end`. |
| Antigravity | Local language-server status first; falls back to a cached `%APPDATA%\Antigravity` quota file only when it is under 45 minutes old, shown as `stale`. No card when neither source has quota rows. |

Codex:

- Shows `rate_limit_*` rows.
- Keeps Spark/Bengalfox rows and labels them from session event attributes.
- Refreshes an expired access token automatically and writes it back to `auth.json`, so quota keeps working without the `codex` CLI running.

Gemini CLI:

- Shows model-specific quota rows when available.
- Suppresses aggregate duplicate rows such as bare `quota`, `quota_flash`, or `quota_pro` when model rows exist.
- Refreshes an expired access token automatically, so quota keeps working without the `gemini` CLI running.

Cursor:

- Shows only the plan-cycle quota row from `plan_percent_used`.
- Uses `billing_cycle_end` for reset text when present.
- Caches a refreshed token in memory so repeated polls do not force a new refresh every cycle.

Antigravity:

- Appears only when LimitDock can read quota-like Antigravity data locally.
- LimitDock does not show an Antigravity status-only card. It needs percent/reset or prompt-credit quota data.
- LimitDock tries the running local language-server endpoint first; it falls back to a cached `%APPDATA%\Antigravity` quota file only when that cache is under 45 minutes old, shown with a `stale` marker.
- There are no Antigravity settings. If no quota data is available, no Antigravity card is rendered.

## Connect a Provider (Claude)

Connect Claude from LimitDock when you want a LimitDock-owned token, or when an existing Claude credential has become unusable:

1. Open the flow from the tray menu's `Connect Claude...` item (visible only while the Claude card is `Sign in`), from `Settings → Providers → Connect...` (always available, including first-time setup with no Claude card), or by double-clicking the Claude card while it shows `Sign in`.
2. Click `1. Open Claude sign-in` to open the Claude sign-in page in your browser and approve LimitDock.
3. Copy the code the page shows. LimitDock auto-fills the field when the clipboard looks like a sign-in code (`CODE#STATE`). If it does not, click `Paste` (or press Ctrl+V) and then click `Connect`.

LimitDock stores the resulting token itself, DPAPI-encrypted under `state\credentials\`; it never modifies `~/.claude/.credentials.json` and works even if the `claude` CLI is not installed. To disconnect, open `Settings → Providers` and click `Disconnect` next to Claude — this removes only the LimitDock-stored token and does not affect a `claude` CLI login.

A card showing `Sign in` means local Claude credentials exist but are unusable and need a fresh sign-in. If Claude was never configured locally, no Claude card appears — use `Settings → Providers → Connect...` instead. A card showing `stale` means the latest fetch failed but LimitDock is still showing the last known values.

## Build And Release

```text
go test ./...
go run .\cmd\limitdock-release -version vYYYYMMDD
```

Publish the generated zip from `dist\`. Do not publish `LimitDock.exe` alone.
