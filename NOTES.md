# LimitDock Notes

LimitDock is a native Windows dock that shows remaining AI tool quota at a glance. It reads quota data directly from each provider's local credentials or session files, with no external daemon required.

Current architecture:

- Read provider data through a single Go provider aggregator (`internal/provider`).
- Claude Code: native reader calls `api.anthropic.com/api/oauth/usage` with a token from `CLAUDE_CODE_OAUTH_TOKEN`, LimitDock's own Connect sign-in, or `~/.claude/.credentials.json`; expired tokens are refreshed and written back automatically. Returns real utilization percent, not a time-elapsed estimate.
- Codex CLI: native reader merges ChatGPT wham usage with `.codex` session/log `rate_limits` rows; refreshes and writes back an expired `auth.json` token automatically.
- Antigravity: native reader checks the live local language-server status first, falling back to a `%APPDATA%\Antigravity` cache only when it is under 45 minutes old (marked stale).
- Gemini CLI: native reader uses `~/.gemini/oauth_creds.json` and Code Assist quota APIs; refreshes and writes back an expired token automatically.
- Cursor: native reader uses `state.vscdb` and Cursor Connect usage on `api2.cursor.sh`; a refreshed token is cached in memory across polls.
- Connect Claude (tray menu, Settings, or double-clicking a `Sign in` card) lets a user sign in without the `claude` CLI installed; LimitDock stores its own token DPAPI-encrypted under `state\credentials\`.
- Snapshot status (`ok`/`needs_auth`/`stale`/`error`) drives card presentation; a provider with no local credentials still shows nothing.
- Background polling is throttled per provider (Claude Code 180s plus backoff, others 60s); a manual `Updated` click always forces a fetch.
- Normalize quota-like rows into compact provider cards.
- Render top, bottom, left, or right docks in overlay or reserved mode, on the primary or a user-selected display; auto-hide hover zones are bounded to the dock's display.

Current product rules:

- Show quota rows only.
- Keep exhausted rows visible unless the user hides them.
- Preserve reset time and remaining percent before long model names.
- Store personal settings in local ignored `settings.json`.
- Ship `settings.example.json` as the portable configuration reference.
- Default new installations to reserved mode so maximized windows leave room for the dock.

Release shape:

- Publish `dist/LimitDock-<version>.zip`.
- Include `LimitDock.exe`, icons, README screenshot assets, and `settings.example.json`.
- Do not include personal settings, runtime databases, logs, or PID files.
