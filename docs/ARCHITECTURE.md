# Architecture

LimitDock is a Go native Windows shell around OpenUsage.sh. The main boundary is the OpenUsage read model: LimitDock supervises the daemon, reads snapshots, normalizes quota rows, and renders the bar.

## Runtime Boundary

`cmd/limitdock` and the `internal/*` Go packages own:

- single-instance mutex and PID file
- OpenUsage daemon start/stop
- read-model socket calls
- native Windows status bar and tray icon
- settings persistence

The runtime app does not invoke external shell scripts. Startup registration, docking, tray behavior, OpenUsage process management, and work-area restore are implemented from Go.

OpenUsage.sh owns provider discovery and telemetry. LimitDock does not parse vendor databases directly for quota.

## Read-Model Adapter

`internal/readmodel` reads `/v1/read-model` from the OpenUsage daemon socket directly from the Go app. `internal/quota` merges snapshots into provider cards.

Quota normalization is intentionally narrow:

- `rate_limit_*`
- `quota*`
- `usage_five_hour`
- `usage_seven_day*`
- Cursor plan row: `plan_percent_used`

Throughput, spend, request, token, and cost metrics are filtered before rendering.

## Provider-Specific Rules

Codex:

- Keep `rate_limit_codex_bengalfox_*` rows.
- Prefer `attributes.rate_limit_codex_bengalfox_name` or matching raw name fields for labels such as `GPT-5.3-Codex-Spark`.

Gemini:

- If `quota_model_*` rows exist, suppress aggregate rows like `quota`, `quota_flash`, and `quota_pro`.
- Model rows stay visible even when remaining quota is 0 percent.

Cursor:

- Treat plan percent rows as billing-cycle quota rows when they expose `%` with `remaining` or `used`.
- Use `billing_cycle_end` for reset text.

Antigravity:

- Antigravity appears as quota only if OpenUsage exposes it as a provider or quota-like snapshot.
- Custom readers for providers outside OpenUsage should emit the same internal read/card shape as the OpenUsage adapter.

## Settings

`settings.json` is local and ignored. It persists:

- `dockMode`
- `dockEdge`
- `autoHide`
- `hiddenQuotaBands`
- `startWithWindows`
- refresh interval and gauge thresholds

`settings.example.json` documents portable defaults.

The Windows startup option is implemented as a per-user `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` value that points directly to `LimitDock.exe`. Disabling startup removes that value and also cleans up the old `LimitDock.lnk` shortcut if it exists from a previous release.

## Appbar And DPI

Reserved mode registers a shell appbar for the selected edge and also applies a Windows work area that matches the reserved bounds. This dual path is deliberate: appbar negotiation alone can be inconsistent under DPI scaling and shell edge changes.

The appbar rectangle is passed to `SHAppBarMessage` in native screen pixels, while UI dimensions are kept in compact Windows logical units and clamped from the active monitor bounds. Hide and overlay transitions unregister the appbar and restore the captured work area. On exit, the app schedules a short delayed `LimitDock.exe --restore-workarea` helper run so Windows shell appbar teardown cannot leave stale reserved space behind.

## Rendering Loop

The UI timer renders cards only while the session-visible flag is true. Tray hide stops the timers and hides the form; tray show restarts timers, reapplies saved docking settings, and renders immediately. Hide is not persisted because a fresh launch implies the user wants to see the bar.
