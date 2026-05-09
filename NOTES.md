# LimitDock

LimitDock is a Windows status overlay that consumes OpenUsage.sh data without inheriting OpenUsage UI or code.

Current architecture:

- Download or use the official OpenUsage.sh Windows binary under `engine/downloads`.
- Build only the small `probes/openusage-readmodel` bridge locally when needed.
- Run the OpenUsage telemetry daemon on its default per-user Windows socket.
- Read the daemon through its Unix-socket HTTP endpoint, `/v1/read-model`.
- Render a WinForms always-on-top bottom HUD with provider cards, details, settings, and tray controls.

This keeps the boundary clean: LimitDock talks to OpenUsage.sh over a runtime API and can later swap in a TCP bridge if needed. A single-instance mutex prevents multiple HUDs from deleting the same socket or racing daemon ownership.

## 2026-05-09 Probe Result

Result: viable.

- Downloaded the official `openusage_0.9.6_windows_amd64.zip` release into `engine/downloads`.
- Official `openusage.exe` reports `0.9.6 (8147e92) built 2026-04-30T10:40:03Z`.
- The daemon exposes HTTP endpoints over a Unix socket:
  - `GET /healthz`
  - `POST /v1/read-model`
  - `POST /v1/hook/{source}`
- `probes/openusage-readmodel` successfully called `/v1/read-model` without importing OpenUsage packages.
- The returned snapshots included Codex CLI, Cursor, and Gemini CLI on this Windows machine.

Important constraint:

- OpenUsage.sh does not currently expose the OpenUsage.ai/CrossUsage-style TCP endpoint `localhost:6736/v1/usage`.
- LimitDock should either speak directly to the Unix socket from its backend, or run a tiny local bridge that converts the socket API into a stable `http://127.0.0.1:<port>` API for the UI.

Recommended next architecture if the PowerShell prototype keeps growing:

1. `limitdock-engine`: starts and supervises the official OpenUsage.sh binary.
2. `openusage-adapter`: reads `/v1/read-model` over the socket and normalizes provider cards.
3. `status-overlay`: Windows borderless bottom bar, always-on-top by default, hidden during fullscreen.
4. `details-popover`: click-to-expand dashboard with raw provider cards and reset timers.
5. `settings`: OpenUsage binary path, socket path, refresh interval, thresholds, provider visibility.
