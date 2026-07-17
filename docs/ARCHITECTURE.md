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

Cache wrapper (`internal/provider/cache.go`): `WithCache` sits between the aggregator and every reader so a source is not hit more often than it can tolerate, independent of the user's `RefreshSeconds` setting. Claude Code polls no more than every 180s, backing off 3 → 6 → 12 → 15 minutes on consecutive 429s (the usage API rate-limits per token); the other four readers poll no more than every 60s with no backoff ladder. A failed fetch replays the last good snapshot flagged `stale` when one exists, or synthesizes an `error` snapshot when the policy has a `SnapshotKey` and no cached result exists yet. `provider.WithForceRefresh(ctx)` bypasses the throttle unconditionally — used by the `Updated`-panel click and immediately after a successful Connect.

New packages: `internal/claudeauth` (OAuth token resolve/refresh/PKCE against Anthropic's endpoints), `internal/credstore` (DPAPI-encrypted JSON key-value store), `internal/fsutil` (atomic file writes), and `internal/native/dpapi.go` (`CryptProtectData`/`CryptUnprotectData` wrappers). `internal/paths` gained a `Credentials` path (`state/credentials`) for the store's directory.

Token write-back and rotation race: Claude Code and Codex refresh an expired access token in place and, because Anthropic/OpenAI rotate the refresh token on every use, only count the refresh as successful once the rotated pair is written back to the CLI's own credentials file (`~/.claude/.credentials.json`, `~/.codex/auth.json`), preserving every field the reader does not otherwise understand and writing atomically via `internal/fsutil.AtomicWriteFile`. If the `claude`/`codex` CLI happens to refresh at the exact same moment as LimitDock, one side can see a transient error from an already-consumed refresh token; both sides persist their rotated pair atomically, so the next run recovers on its own — narrow and self-healing, no coordination needed. Gemini CLI refreshes and writes back the same way, now keeping the on-disk `expiry_date`/`expiry` pair in sync (fixing a bug that forced a refresh on every poll); Google does not rotate the refresh token itself, so there is no equivalent race there. Cursor caches a refreshed token in memory across polls instead of writing back to `state.vscdb`.

Public client ids: `claudeauth.ClaudeOAuthClientID`, the Codex reader's OAuth client id, the Gemini CLI client id/secret, and the Cursor reader's OAuth client id are the public client identifiers of the official CLIs/desktop apps, documented as such in source. LimitDock authenticates as the same client so tokens carry the scopes those APIs expect — Claude Connect specifically requests `user:profile user:inference` (full CLI login scope) because a `claude setup-token`-issued token carries only `user:inference` and the usage API rejects it with 403. As an open-source project, LimitDock hardcodes no personal or private values; these ids are already public in the respective CLI/app binaries, not secrets.

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
