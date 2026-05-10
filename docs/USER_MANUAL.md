# User Manual

LimitDock is a Windows dock for quota rows produced from the OpenUsage.sh read model. It is designed to be glanced at while coding, not used as a dashboard.

## Start LimitDock

From a release folder, run:

```text
.\LimitDock.exe
```

From the repository, run:

```text
go run .\cmd\limitdock
```

LimitDock starts the bundled or downloaded OpenUsage.sh daemon, reads the local read model, and renders provider cards that expose quota-like rows.

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

Checked rows are visible. Unchecked rows are hidden. Hidden rows stay in `settings.json` and can be restored later; LimitDock never deletes source telemetry.

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
- `Settings`: opens LimitDock docking, theme, refresh, startup, threshold, OpenUsage, and log/diagnostic controls.
- `Exit`: closes LimitDock and stops the managed OpenUsage daemon.

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

Dock mode, dock edge, theme, overlay opacity, auto-slide, refresh interval, gauge thresholds, and visible row choices are persisted in local `settings.json`. Change these from `Settings` at any time. The `Theme` row uses two visual day/night buttons to switch between the `light` and `night` themes. `Auto slide in overlay mode` controls whether an unpinned overlay dock slides away at the selected edge.

`Overlay opacity %` applies only in overlay mode and can be set from 35 to 100. Dragging it previews the opacity immediately. `Cancel` restores the previous opacity, and `Save` persists it.

The settings window also includes `OpenUsage / logs` controls. Use them to open `%APPDATA%\openusage\settings.json`, open the OpenUsage settings folder, browse LimitDock logs, open `limitdock.log`, or copy diagnostic paths for troubleshooting. Antigravity path settings are intentionally absent because the Go version auto-detects local Antigravity quota sources.

`Start LimitDock when Windows starts` writes a per-user `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` value that points directly to `LimitDock.exe`. Clear the checkbox to remove the value. Older `LimitDock.lnk` startup shortcuts are cleaned up when this setting changes.

## Provider Behavior

LimitDock uses OpenUsage as the source of truth for providers that OpenUsage supports. The current upstream list is documented in [openusage all providers](https://github.com/janekbaraniewski/openusage#all-providers). LimitDock renders only quota-like rows, so usage-only or cost-only provider data may be omitted from the dock.

| Provider or agent | Source | Notes |
| --- | --- | --- |
| Claude Code | OpenUsage | Appears when OpenUsage exposes quota-like rows. |
| Cursor | OpenUsage | Plan-cycle quota only. |
| GitHub Copilot | OpenUsage | Appears when chat/completion quota rows are present. |
| Codex CLI | OpenUsage, then LimitDock fallback | OpenUsage wins. The fallback scans local Codex session rate-limit events only when OpenUsage has no Codex quota rows. |
| Gemini CLI | OpenUsage | Model-specific rows are preferred over aggregate duplicates. |
| OpenCode, Ollama, OpenAI, Anthropic, OpenRouter, Groq, Mistral AI, DeepSeek, Moonshot/Kimi, Perplexity, xAI/Grok, Z.AI, Google Gemini API, Alibaba Cloud | OpenUsage | Appears only when OpenUsage exposes quota-like rows in its read model. |
| Antigravity | LimitDock custom reader | Quota-only. Requires readable local quota data from the running Antigravity language server or common `%APPDATA%\Antigravity` cache files. If Antigravity is closed or no quota row exists, no card is shown. |

Codex:

- Shows `rate_limit_*` rows.
- Keeps Spark/Bengalfox rows and labels them from OpenUsage attributes when available.
- Uses OpenUsage first. If OpenUsage does not expose Codex quota rows, the local fallback reader scans recent Codex session rate-limit events and emits the same quota shape.

Cursor:

- Shows only the plan-cycle quota row from `plan_percent_used`.
- Uses `billing_cycle_end` for reset text when present.

Gemini:

- Shows model-specific quota rows when available.
- Suppresses aggregate duplicate rows such as bare `quota`, `quota_flash`, or `quota_pro` when model rows exist.

Antigravity:

- Appears only when LimitDock can read quota-like Antigravity data locally.
- LimitDock does not show an Antigravity status-only card. It needs percent/reset or prompt-credit quota data.
- LimitDock tries the running local language-server endpoint and common `%APPDATA%\Antigravity` cache locations automatically.
- There are no Antigravity settings. If no quota data is available, no Antigravity card is rendered.

Providers listed in OpenUsage should be handled through OpenUsage first. Providers outside OpenUsage should be added through a LimitDock custom reader that emits the same normalized quota rows.

## Build And Release

```text
go test ./...
go run .\cmd\limitdock-release -version vYYYYMMDD
```

Publish the generated zip from `dist\`. Do not publish `LimitDock.exe` alone.
