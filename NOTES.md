# LimitDock Notes

LimitDock is a native Windows companion for OpenUsage.sh quota telemetry. It exists to make OpenUsage.sh quota data visible while coding without opening a terminal dashboard.

Current architecture:

- Use the official OpenUsage.sh Windows binary when available, downloading it on first run when it is not bundled.
- Start and stop the OpenUsage telemetry daemon with the LimitDock session.
- Read provider data through a single Go provider aggregator.
- Use OpenUsage's local read-model socket for upstream-supported providers.
- Use custom quota-only readers for providers outside OpenUsage, starting with Antigravity.
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
- Do not include personal settings, runtime databases, logs, PID files, Go caches, or downloaded OpenUsage binaries unless intentionally making an offline release.
