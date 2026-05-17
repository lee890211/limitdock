# Architecture

LimitDock is a Go native Windows shell around a normalized quota read model. All provider data is read directly by LimitDock's native readers; no external daemon is required.

## Runtime Boundary

`cmd/limitdock` and the `internal/*` Go packages own:

- single-instance mutex and PID file
- native provider reads (Claude Code, Codex, Gemini CLI, Cursor, Antigravity)
- native Windows status bar and tray icon
- settings persistence

The runtime app does not invoke external shell scripts or manage any external daemon. Startup registration, docking, tray behavior, and work-area restore are implemented entirely in Go.

The settings dialog exposes log and diagnostic affordances: it can browse LimitDock logs, open the app log, and copy diagnostic paths. It does not expose Antigravity path fields because Antigravity is auto-detected by the custom reader.

## Provider Readers

`internal/provider` is the single read boundary for quota sources. The UI asks the provider `Aggregator` for one `readmodel.ReadModel`; it does not know which reader produced each snapshot.

- `ClaudeCodeReader` is registered first and reads quota directly from `api.anthropic.com/api/oauth/usage`.
- `CodexReader` merges ChatGPT wham usage with `.codex` session/log rate-limit events and is registered second.
- `GeminiCLIReader` reads `~/.gemini/oauth_creds.json`, calls Code Assist quota APIs, and is registered third.
- `CursorReader` reads `%APPDATA%\Cursor\User\globalStorage\state.vscdb`, calls Cursor Connect usage on `api2.cursor.sh`, and is registered fourth.
- `AntigravityReader` checks local language-server status and `%APPDATA%\Antigravity` cache and is registered last.
- All readers emit the same `readmodel.Snapshot` and `readmodel.Metric` shape; `internal/quota` normalizes all providers through one path.
- Duplicate snapshot keys keep the first reader's data.

Quota normalization is intentionally narrow:

- `rate_limit_*`
- `quota*`
- `usage_five_hour`
- `usage_seven_day*`
- Cursor plan row: `plan_percent_used`

Throughput, spend, request, token, and cost metrics are filtered before rendering.

## Provider-Specific Rules

Codex:

- The native Codex reader merges wham usage with recent `.codex` session/log `rate_limits` events.
- Wham windows use `limit_window_seconds` when `window_minutes` is absent.
- Keep `rate_limit_codex_bengalfox_*` rows.
- Prefer `attributes.rate_limit_codex_bengalfox_name` or matching raw name fields for labels such as `GPT-5.3-Codex-Spark`.

Gemini:

- If `quota_model_*` rows exist, suppress aggregate rows like `quota`, `quota_flash`, and `quota_pro`.
- Model rows stay visible even when remaining quota is 0 percent.

Cursor:

- Treat plan percent rows as billing-cycle quota rows when they expose `%` with `remaining` or `used`.
- Use `billing_cycle_end` for reset text.

Antigravity:

- Handled entirely by the LimitDock native reader.
- The reader looks for local Antigravity language-server status and common `%APPDATA%\Antigravity` cache locations, and emits a snapshot only when percent/reset or prompt-credit quota rows are present.
- If no quota rows are available, Antigravity is not rendered as a status-only card.

## Settings

`settings.json` is local and ignored. It persists:

- `dockMode`
- `dockEdge`
- `theme`
- `overlayOpacity`
- `autoHide`
- `hiddenQuotaBands`
- `startWithWindows`
- refresh interval and gauge thresholds

`settings.example.json` documents portable defaults.

The Windows startup option is implemented as a per-user `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` value that points directly to `LimitDock.exe`. Disabling startup removes that value and also cleans up the old `LimitDock.lnk` shortcut if it exists from a previous release.

## Appbar And DPI

Reserved mode registers a shell appbar for the selected edge and also applies a Windows work area that matches the reserved bounds. This dual path is deliberate: appbar negotiation alone can be inconsistent under DPI scaling and shell edge changes.

The appbar rectangle is passed to `SHAppBarMessage` in native screen pixels, while UI dimensions are kept in compact Windows logical units and clamped from the active monitor bounds. Reserved mode uses the current Windows work area. In overlay mode, left/right docks position against the full screen edge so stale reserved work-area values cannot move the floating dock, while top/bottom ribbons use the Windows work area so the taskbar/menu region remains visible. Hide and overlay transitions unregister the appbar and restore the captured work area. On exit, the app schedules a short delayed `LimitDock.exe --restore-workarea` helper run so Windows shell appbar teardown cannot leave stale reserved space behind.

## Rendering Loop

The UI timer renders cards only while the session-visible flag is true. Tray hide stops the timers and hides the form; tray show restarts timers, reapplies saved docking settings, and renders immediately. Hide is not persisted because a fresh launch implies the user wants to see the bar.
