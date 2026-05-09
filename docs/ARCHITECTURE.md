# Architecture

LimitDock is a PowerShell WinForms shell around OpenUsage.sh. The main boundary is the OpenUsage read model: LimitDock supervises the daemon, reads snapshots, normalizes quota rows, and renders the bar.

## Runtime Boundary

`LimitDock.ps1` owns:

- single-instance mutex and PID file
- OpenUsage daemon start/stop
- Go read-model probe build/use
- WinForms status bar and tray icon
- settings persistence

OpenUsage.sh owns provider discovery and telemetry. LimitDock does not parse vendor databases directly for quota.

## Read-Model Adapter

The Go probe under `probes/openusage-readmodel` reads `/v1/read-model` from the OpenUsage daemon socket and returns JSON to PowerShell. PowerShell merges snapshots into provider cards.

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

- No custom aliasing or quota parser is added in this pass.
- Antigravity appears as quota only if OpenUsage exposes it as a provider or quota-like snapshot.

## Settings

`settings.json` is local and ignored. It persists:

- `dockMode`
- `dockEdge`
- `autoHide`
- `hiddenQuotaBands`
- refresh interval and gauge thresholds
- Antigravity path hints

`settings.example.json` documents portable defaults.

## Appbar And DPI

Reserved mode registers a shell appbar for the selected edge and also applies a Windows work area that matches the reserved bounds. This dual path is deliberate: appbar negotiation alone can be inconsistent under DPI scaling and shell edge changes.

The appbar rectangle is scaled for DPI before `SHAppBarMessage`, while the form and fallback work area stay in WinForms screen coordinates. Hide and overlay transitions unregister the appbar and restore the captured work area.

## Rendering Loop

The UI timer renders cards only while the session-visible flag is true. Tray hide stops the timers and hides the form; tray show restarts timers, reapplies saved docking settings, and renders immediately. Hide is not persisted because a fresh launch implies the user wants to see the bar.
