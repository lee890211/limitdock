# Architecture

LimitDock is a Go native Windows shell around a normalized quota read model. OpenUsage.sh is the primary connector: LimitDock supervises the daemon, reads snapshots, normalizes quota rows, and renders the bar. Local custom readers can fill provider gaps when OpenUsage does not expose quota rows.

## Runtime Boundary

`cmd/limitdock` and the `internal/*` Go packages own:

- single-instance mutex and PID file
- OpenUsage daemon start/stop
- OpenUsage read-model socket calls
- local custom provider fallback reads
- native Windows status bar and tray icon
- settings persistence

The runtime app does not invoke external shell scripts. Startup registration, docking, tray behavior, OpenUsage process management, and work-area restore are implemented from Go.

OpenUsage.sh owns provider discovery and telemetry for upstream-supported providers. LimitDock custom readers are intentionally narrow and quota-only; they are used for missing providers or fallbacks, not as a replacement telemetry system.

## Provider Readers

`internal/provider` is the single read boundary for quota sources. The UI asks the provider `Aggregator` for one `readmodel.ReadModel`; it does not know whether a snapshot came from OpenUsage or a LimitDock custom reader.

- `internal/connector/openusage` owns the external OpenUsage connector: daemon management, settings/account bootstrap, socket reads, and the Codex supplemental OpenUsage merge.
- `OpenUsageReader` reads `/v1/read-model` from the OpenUsage daemon socket and is registered first.
- Custom readers emit the same `readmodel.Snapshot` and `readmodel.Metric` shape, so `internal/quota` can normalize all providers through one path.
- Duplicate snapshot keys keep the first reader's data. Fallback readers also declare a provider id; when OpenUsage already has quota rows for that provider, the fallback snapshot is skipped even if its account key differs.

Quota normalization is intentionally narrow:

- `rate_limit_*`
- `quota*`
- `usage_five_hour`
- `usage_seven_day*`
- Cursor plan row: `plan_percent_used`

Throughput, spend, request, token, and cost metrics are filtered before rendering.

## Provider-Specific Rules

Codex:

- Codex needs integration setup because local Codex telemetry is delivered through an OpenUsage notify hook and a `codex-cli` OpenUsage account/link.
- Codex also needs a supplemental read because OpenUsage's default read model can omit quota rows unless Codex is queried with its account/provider filter and configured time window.
- If OpenUsage still has no Codex quota rows, the custom Codex fallback reader scans recent `.codex/sessions` JSONL events for `rate_limits`.
- Keep `rate_limit_codex_bengalfox_*` rows.
- Prefer `attributes.rate_limit_codex_bengalfox_name` or matching raw name fields for labels such as `GPT-5.3-Codex-Spark`.

Gemini:

- If `quota_model_*` rows exist, suppress aggregate rows like `quota`, `quota_flash`, and `quota_pro`.
- Model rows stay visible even when remaining quota is 0 percent.

Cursor:

- Treat plan percent rows as billing-cycle quota rows when they expose `%` with `remaining` or `used`.
- Use `billing_cycle_end` for reset text.

Antigravity:

- Antigravity is handled as a quota-only custom reader when OpenUsage does not expose it.
- The custom reader looks for local Antigravity language-server status, common `%APPDATA%\Antigravity` cache locations, or explicit JSON cache hints and emits a snapshot only when percent/reset or prompt-credit quota rows are present.
- If no quota rows are available, Antigravity is not rendered as a status-only card.

## Settings

`settings.json` is local and ignored. It persists:

- `dockMode`
- `dockEdge`
- `theme`
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
