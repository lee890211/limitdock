# LimitDock Smoke Test Skill

Use this skill when validating LimitDock UI, docking, release, or provider-reader changes.

## Goal

Verify that the Go app behaves like a status bar across all dock positions and modes before reporting completion.

## Required Checks

1. Run rule-based tests:

```powershell
go test ./...
```

2. Build a release:

```powershell
go run .\cmd\limitdock-release -version vYYYYMMDD-smoke
```

3. For each edge, test these modes from a release folder:

- `reserved`
- `overlay` pinned (`autoHide=false`)
- `overlay` unpinned (`autoHide=true`)

Edges:

- `bottom`
- `top`
- `left`
- `right`

4. For each run, verify:

- The visible window rect matches the intended dock edge and logical size.
- Reserved mode changes only the selected edge of the Windows work area.
- Overlay pinned does not change the Windows work area.
- In `bottom/overlay` and `top/overlay`, the ribbon must stay inside the Windows work area and must not cover the taskbar/menu region.
- Overlay unpinned reveals on edge hover and hides after the cursor leaves.
- When the bar is visible, clicks hit LimitDock, not the window behind it.
- For `left/overlay/autoHide=true`, use a Windows hit-test (`WindowFromPoint` and `GetAncestor`) before clicking the gear, pin, or Updated areas. The root window under the cursor must belong to `LimitDock.exe`; otherwise the run fails even if the bar is visible on screen.
- Settings opens in front.
- Settings action buttons are left-aligned, not centered.
- Settings includes log/diagnostic controls: browse LimitDock logs, open `limitdock.log`, and copy diagnostic paths.
- The pin/unpin button toggles `autoHide`.
- In left overlay, explicitly test both directions: pinned -> click pin -> hidden/unpinned, then edge hover -> revealed -> click pin -> pinned/visible.
- Tray `Hide Status Bar` restores the work area and pauses reveal.
- Tray `Show Status Bar` restores the saved mode and edge.
- The `Updated` panel click triggers an immediate refresh.
- `light` and `night` themes both render readable text.
- Overlay opacity preview changes the visible bar while Settings is open. Cancel restores the previous opacity; Save persists the new `overlayOpacity`.
- A wide ribbon can render up to five provider cards when five cards exist.

## Provider Checks

Inspect the local machine for installed or running agents and verify expected source behavior:

- Claude Code, Codex, Gemini CLI, Cursor, and Antigravity should come through native readers in `internal/provider`.
- Registration order is ClaudeCode → Codex → GeminiCLI → Cursor → Antigravity; duplicate snapshot keys keep the first reader.
- Antigravity appears only when the running language server or local cache exposes quota-like rows.
- If an installed agent is absent from the dock, inspect whether the source emitted quota-like rows before treating it as a UI bug.
- A `needs_auth` card renders red with main text `Sign in` and no quota bands.
- A `stale` card renders amber, keeps its last-known bands, and shows `stale` in the detail text.
- The tray `Connect Claude...` item is visible only while the Claude card is `needs_auth`.
- `Settings → Providers` lists all five providers with status text, plus Claude `Connect...`/`Disconnect` buttons.
- Clicking the `Updated` panel forces a real refresh even while a provider is in its 429 backoff window.
- In the Connect dialog: the browser opens to the Claude sign-in page, the paste field accepts the pasted code, and `Cancel` closes the dialog without connecting.

## Reporting

Report exact edge/mode pairs that passed or failed. Include measured window rect and work-area rect for docking failures.
