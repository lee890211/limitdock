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
- Overlay unpinned reveals on edge hover and hides after the cursor leaves.
- When the bar is visible, clicks hit LimitDock, not the window behind it.
- Settings opens in front.
- The pin/unpin button toggles `autoHide`.
- Tray `Hide Status Bar` restores the work area and pauses reveal.
- Tray `Show Status Bar` restores the saved mode and edge.
- The `Updated` panel click triggers an immediate refresh.
- `light` and `night` themes both render readable text.
- A wide ribbon can render up to five provider cards when five cards exist.

## Provider Checks

Inspect the local machine for installed or running agents and verify expected source behavior:

- OpenUsage-supported providers should come through `internal/connector/openusage`.
- Codex should use OpenUsage first; the custom Codex reader is only a fallback when OpenUsage has no Codex quota rows.
- Antigravity is custom quota-only. It should appear only when the running language server or local cache exposes quota-like rows.
- If an installed agent is absent from the dock, inspect whether the source emitted quota-like rows before treating it as a UI bug.

## Reporting

Report exact edge/mode pairs that passed or failed. Include measured window rect and work-area rect for docking failures.

