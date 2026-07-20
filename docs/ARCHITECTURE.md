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

Status contract (`readmodel.Snapshot.Status`):

| Status | Meaning | Card |
| --- | --- | --- |
| `ok` | Fetch succeeded. | Normal quota bands. |
| `needs_auth` | Local credentials exist but are unusable (expired and refresh failed, rejected, or missing scope). | Stays visible: red, main text `Sign in`, detail `sign-in required`. |
| `stale` | The latest fetch failed but a previous good result exists, or (Antigravity only) the live probe failed and a disk cache newer than 45 minutes was used. | Keeps last-known bands at warn level with a leading `stale` marker. |
| `error` | Fetch failed and no cached data exists. | Red, main text `Error`. |

A provider with no local credential evidence at all emits no snapshot rather than a status — absence stays silent. `readmodel.NeedsAttention` is what keeps a `needs_auth`/`stale`/`error` card visible even when it carries no quota bands.

Cache wrapper (`internal/provider/cache.go`): `WithCache` sits between the aggregator and every reader so a source is not hit more often than it can tolerate, independent of the user's `RefreshSeconds` setting. Claude Code polls no more than every 180s, backing off 3 → 6 → 12 → 15 minutes on consecutive 429s (the usage API allows only ~4-5 calls per 5-minute window, measured live, and shares that budget with the Claude Code CLI); the other four readers poll no more than every 60s with no backoff ladder. A failed fetch replays the last good snapshot flagged `stale` when one exists (status-only needs_auth/error placeholders never count as "last good"), or synthesizes an `error` snapshot when the policy has a `SnapshotKey` and no cached result exists yet. `provider.WithForceRefresh(ctx)` bypasses the throttle unconditionally — used by the `Updated`-panel click and immediately after a successful Connect.

Two Claude-specific rate-limit defenses sit below the cache wrapper:

- Token-endpoint cooldown gate and circuit breaker (`internal/claudeauth`): `platform.claude.com/v1/oauth/token` rate-limits aggressively — observed live to throttle LimitDock's refresh POSTs even while official CLI refreshes from the same public IP succeed, and a saturated window also rejects the user's Connect code exchange on the same endpoint; sustained background refresh retries once locked a machine out of re-authentication entirely for hours. After any 429 the gate blocks further background token POSTs process-wide with an escalating cooldown (2 → 5 → 10 → 15 minute ladder, `Retry-After` honored, any non-429/non-5xx response clears it) and logs the event. After three consecutive 429s a circuit breaker stops background refreshes entirely for the rest of the process and surfaces a needs-auth card pointing at the refresh-free setup-token route — retry loops with a rejected credential are what escalate the throttling in the first place. A user-initiated `ExchangeCode` bypasses both the gate and the breaker, and its result feeds back into them.
- Header-probe fallback (`internal/provider/claudecode.go`): when the usage API answers 429 (budget collision) or 403 (a `user:inference`-only token such as `claude setup-token`), the reader issues a 1-token haiku call and maps the `anthropic-ratelimit-unified-{5h,7d}-utilization/-reset` response headers (utilization is a 0-1 fraction, resets are epoch seconds) onto the same `usage_five_hour`/`usage_seven_day` metrics, so quota keeps updating through usage-API penalty windows. Those headers arrive on 2xx and on `rate_limit_error` 429s (exhausted budget still reports utilization); the probe prefers headers whenever present and only treats the status as failure when they are absent. The probe must still be a minimal valid request rather than a free invalid one, because auth/edge errors omit the headers.

New packages: `internal/claudeauth` (OAuth token resolve/refresh/PKCE against Anthropic's endpoints), `internal/credstore` (DPAPI-encrypted JSON key-value store), `internal/fsutil` (atomic file writes), and `internal/native/dpapi.go` (`CryptProtectData`/`CryptUnprotectData` wrappers). `internal/paths` gained a `Credentials` path (`state/credentials`) for the store's directory.

Token refresh, write-back, and the rotation race: Claude credential resolve order is `CLAUDE_CODE_OAUTH_TOKEN` → LimitDock's own store (setup-token or Connect) → the CLI's `~/.claude/.credentials.json`, and only store tokens are ever refreshed (Anthropic rotates refresh tokens on use, so a refresh counts as successful once the rotated pair is persisted back to the store). The CLI credentials file is strictly read-only: its token is used while still valid and never refreshed or rewritten. LimitDock used to refresh-and-write-back that file too, but that races the CLI's own writer over one rotating lineage (a reuse-detection lockout risk) and, worse, retrying a refresh with an expired credential is exactly the traffic the token endpoint's throttling punishes — the CLI rewrites the file whenever the user actually runs `claude`, so LimitDock just rides valid tokens and shows `Sign in` otherwise. Codex still refreshes and writes back `~/.codex/auth.json` (rotated pair persisted atomically via `internal/fsutil.AtomicWriteFile`, preserving unknown fields; a same-moment CLI refresh is a narrow, self-healing race). Gemini CLI refreshes and writes back the same way, keeping the on-disk `expiry_date`/`expiry` pair in sync; Google does not rotate the refresh token, so there is no race there. Cursor caches a refreshed token in memory across polls instead of writing back to `state.vscdb`.

Public client ids: `claudeauth.ClaudeOAuthClientID`, the Codex reader's OAuth client id, the Gemini CLI client id/secret, and the Cursor reader's OAuth client id are the public client identifiers of the official CLIs/desktop apps, documented as such in source. LimitDock authenticates as the same client so tokens carry the scopes those APIs expect — Claude Connect specifically requests `user:profile user:inference` (full CLI login scope) because a `claude setup-token`-issued token carries only `user:inference` and the usage API rejects it with 403 (such tokens still surface 5h/7d quota through the header-probe fallback, but only Connect-scope tokens can read the full per-model bucket set). As an open-source project, LimitDock hardcodes no personal or private values; these ids are already public in the respective CLI/app binaries, not secrets.

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
