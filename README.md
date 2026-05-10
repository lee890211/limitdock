# LimitDock

LimitDock is a compact Windows desktop dock that shows remaining agent quota at a glance. It turns local usage/quota output into an always-visible desktop ribbon, focused on one thing: how much model quota is left, and when it resets.

![LimitDock night ribbon](docs/images/manual-ribbon-night.png)

## What It Shows

- Remaining quota only, grouped by provider, model or plan bucket, reset countdown, and percent.
- Codex rate-limit rows, including Spark labels exposed by openusage attributes or the local Codex fallback reader.
- Cursor plan-cycle quota from `plan_percent_used` with `billing_cycle_end` reset text.
- Gemini model-specific quota rows, while suppressing aggregate duplicates when precise model rows exist.
- Antigravity quota rows from the LimitDock custom reader when local quota-like data is available.
- Exhausted or 0 percent rows, unless the user explicitly hides them.

LimitDock intentionally does not show spend, token totals, request counts, tool-call counts, or generic activity rows.

Bundled provider icons are neutral LimitDock badges, not official brand logos. If you replace files under `assets/icons` with official brand assets, make sure the usage is allowed by that provider's brand guidelines and trademark terms.

LimitDock is built on [openusage](https://github.com/janekbaraniewski/openusage). It is not a replacement for openusage: provider discovery, telemetry, and the read model belong there. LimitDock supervises the openusage daemon, reads its local read-model endpoint, normalizes quota-like rows, and renders them as a native Windows dock. Many thanks to the openusage maintainers for building the local usage/quota foundation this project depends on.

Provider support follows OpenUsage's upstream provider list where possible. See [openusage all providers](https://github.com/janekbaraniewski/openusage#all-providers). Providers listed there are handled through the `connector/openusage` reader. Providers not exposed by OpenUsage, or providers that need a local fallback when OpenUsage has no quota rows, live in `internal/provider` as custom readers that emit the same internal read model.

## Provider Support

LimitDock renders only quota-like rows. A provider can be detected by OpenUsage and still be absent from LimitDock if OpenUsage does not expose a quota, rate-limit, credit, or reset row for it.

| Provider or agent | Source | LimitDock behavior |
| --- | --- | --- |
| Claude Code | OpenUsage | Rendered when OpenUsage exposes quota-like rows. |
| Cursor | OpenUsage | Plan-cycle quota from `plan_percent_used`; reset comes from `billing_cycle_end` when present. |
| GitHub Copilot | OpenUsage | Rendered when OpenUsage exposes chat/completion quota rows. |
| Codex CLI | OpenUsage first, custom fallback second | OpenUsage rows win. If OpenUsage has no Codex quota rows, the local fallback scans recent Codex session rate-limit events. |
| Gemini CLI | OpenUsage | Model-specific quota rows are preferred; aggregate duplicate rows are suppressed when precise model rows exist. |
| OpenCode, Ollama, OpenAI, Anthropic, OpenRouter, Groq, Mistral AI, DeepSeek, Moonshot/Kimi, Perplexity, xAI/Grok, Z.AI, Google Gemini API, Alibaba Cloud | OpenUsage | Rendered only when OpenUsage exposes quota-like rows in the read model. |
| Antigravity | LimitDock custom reader only | Quota-only and automatic. It can read the running local Antigravity language-server status or common `%APPDATA%\Antigravity` cache data. If Antigravity is closed or no quota rows are present, no card is shown. |

## User Guide

### Ribbon Mode

Use `bottom` or `top` for a horizontal ribbon. Each provider card shows one or more quota rows. Row labels keep the model or plan bucket, including the metering window when openusage exposes it; the timing column shows only the reset countdown. Remaining percent is drawn inside the gauge. When two rows are visible, they use two full-width lines; three or four rows use a compact grid. Long labels are shortened before reset time or remaining percent disappear. On wide displays, the ribbon can fit up to five provider cards before overflowing.

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
- `Settings`: opens docking, theme, refresh, startup, threshold, OpenUsage, and log/diagnostic controls.
- `Exit`: closes LimitDock and stops the managed openusage daemon.

### Docking

LimitDock supports two display modes:

- `reserved`: the default first-run mode. It registers a Windows appbar and applies a matching work area so maximized windows leave room for the dock.
- `overlay`: floats above other windows. The pin icon appears only in overlay mode.

Edges are `bottom`, `top`, `left`, and `right`. `Settings` shows them as four visual screen-edge buttons in `bottom`, `left`, `top`, `right` order instead of a dropdown so the selected dock side is easier to recognize. Docking choices are stored in local `settings.json`.

The `Theme` row uses two visual day/night buttons. `Overlay opacity %` previews transparency live in overlay mode; Cancel restores the previous value and Save persists it. `Start LimitDock when Windows starts` writes a per-user `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` value that points directly to `LimitDock.exe`, so no administrator rights or script launcher are required. Turn the checkbox off to remove the value. `Auto slide in overlay mode` controls whether an unpinned overlay dock slides away at the selected edge.

Antigravity support is quota-only and automatic. If OpenUsage does not expose Antigravity, LimitDock tries its custom reader and renders a card only when it can read local percent/reset or prompt-credit quota data. It does not add an installation/status placeholder card.

## Install

1. Download the latest `LimitDock-<version>.zip` from GitHub Releases.
2. Extract the whole folder. Do not run `LimitDock.exe` by itself; it expects the icons, settings reference, and runtime folders beside it.
3. Run `LimitDock.exe`.

On first run, LimitDock downloads the official openusage Windows binary when it is not bundled. A local `settings.json` is created next to the app. The release includes `settings.example.json` as the portable configuration reference; personal `settings.json` files are never committed or shipped.

## Build From Source

Requirements:

- Go, for the native `LimitDock.exe` and release tool

```text
go test ./...
go build -ldflags "-H windowsgui" -o engine\bin\LimitDock.exe .\cmd\limitdock
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

Runtime databases, logs, PID files, downloaded openusage binaries, release folders, local Go caches, and `settings.json` are ignored. Keep `settings.example.json` as the shareable default.
