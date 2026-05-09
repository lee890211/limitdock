# LimitDock Notes

LimitDock is a Windows WinForms companion for OpenUsage.sh quota telemetry. It exists to make OpenUsage.sh quota data visible while coding without opening a terminal dashboard.

Current architecture:

- Use the official OpenUsage.sh Windows binary when available, downloading it on first run when it is not bundled.
- Build and ship only the small `probes/openusage-readmodel` bridge locally.
- Start and stop the OpenUsage telemetry daemon with the LimitDock session.
- Read the daemon read model through the local socket endpoint.
- Normalize quota-like rows into compact provider cards.
- Render top, bottom, left, or right docks in overlay or reserved mode.

Current product rules:

- Show quota rows only.
- Keep exhausted rows visible unless the user hides them.
- Preserve reset time and remaining percent before long model names.
- Store personal settings in local ignored `settings.json`.
- Ship `settings.example.json` as the portable configuration reference.

Release shape:

- Publish `dist/LimitDock-<version>.zip`.
- Include `LimitDock.exe`, launch scripts, icons, README screenshot assets, `settings.example.json`, and `engine/bin/openusage-readmodel.exe`.
- Do not include personal settings, runtime databases, logs, PID files, Go caches, or downloaded OpenUsage binaries unless intentionally making an offline release.
