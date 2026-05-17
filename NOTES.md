# LimitDock Notes

LimitDock is a native Windows dock that shows remaining AI tool quota at a glance. It reads quota data directly from each provider's local credentials or session files, with no external daemon required.

Current architecture:

- Read provider data through a single Go provider aggregator (`internal/provider`).
- Claude Code: native reader calls `api.anthropic.com/api/oauth/usage` with the OAuth token from `~/.claude/.credentials.json` (or `CLAUDE_CODE_OAUTH_TOKEN`). Returns real utilization percent, not a time-elapsed estimate.
- Codex CLI: native reader scans recent `.codex/sessions` JSONL events for `rate_limits` rows.
- Antigravity: native reader checks local language-server status and `%APPDATA%\Antigravity` cache for quota rows.
- Gemini CLI: native reader reads `~/.gemini/usage.json`.
- Cursor: native reader calls `cursor.com/api/usage` with the token from `%APPDATA%\Cursor\User\globalStorage\state.vscdb`.
- Normalize quota-like rows into compact provider cards.
- Render top, bottom, left, or right docks in overlay or reserved mode.

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
