---
description: Capture privacy-safe LimitDock UI screenshots and refresh README and User Manual visual assets.
---

# LimitDock Manual Update

Use this command to refresh LimitDock documentation screenshots and richer visual guide assets.

## Safety Rules

- Capture only LimitDock windows or a tightly cropped LimitDock region.
- Never include the Claude Code chat, browser, terminal, desktop, taskbar, notifications, email, file explorer contents, or other private windows.
- For opacity captures, place a neutral solid backdrop behind LimitDock before capturing. Do not capture real desktop content through transparency.
- If any non-LimitDock content appears in a candidate image, discard it or blur/mosaic it before adding it to `docs/images`.
- Wait until OpenUsage has loaded and at least one quota-like read-model row is available before capturing provider cards.
- Read-model readiness alone is not enough. Also wait for the LimitDock UI to refresh after readiness; use at least `-UiSettleSeconds 60` for final manual assets.
- Inspect every generated image before updating docs.

## Expected Assets

Prefer these documentation asset names:

- `docs/images/manual-ribbon-light.png`
- `docs/images/manual-ribbon-night.png`
- `docs/images/manual-side-dock-light.png`
- `docs/images/manual-side-dock-night.png`
- `docs/images/manual-overlay-opacity.png`
- `docs/images/manual-slide-in.png`
- `docs/images/manual-slide-out.png`, which may be a neutral blank edge frame when the hidden dock has no visible window
- `docs/images/manual-slide-in-out.gif` when a GIF tool is available
- `docs/images/manual-row-picker.png`

Remove old compatibility images once docs no longer reference them.

## Workflow

1. Build a fresh release:

```powershell
go run .\cmd\limitdock-release -version vYYYYMMDD-manual
```

2. Run the capture helper from the repository root:

```powershell
.\.codex\skills\limitdock-manual-update\scripts\capture-limitdock-manual.ps1 `
  -ReleaseDir .\dist\LimitDock-vYYYYMMDD-manual `
  -OutputDir .\docs\images `
  -UiSettleSeconds 60
```

3. Verify the generated images visually. Reject any image that includes private content or still shows `Waiting OpenUsage`.

4. Update `README.md` and `docs/USER_MANUAL.md` so the guide shows:

- light and night ribbon mode
- light and night side dock mode
- overlay opacity behavior
- slide-in and slide-out behavior
- row picker

5. Keep captions short and task-focused. Explain that screenshots are captured after OpenUsage read-model readiness, so provider cards reflect real quota rows.

6. Run checks:

```powershell
go test ./...
git diff --check
```

## Helper Details

- `.codex/skills/limitdock-manual-update/scripts/readmodel-ready.go` polls the OpenUsage Unix socket and succeeds only when quota-like rows exist.
- `.codex/skills/limitdock-manual-update/scripts/capture-limitdock-manual.ps1` writes temporary release-local `settings.json` states, launches the release EXE, waits for OpenUsage readiness, waits for the UI to settle, captures only the LimitDock window/edge region, and creates `manual-slide-in-out.gif` with the bundled Go helper when `go` is available.

If the helper cannot prove OpenUsage readiness, do not capture final manual assets. Fix OpenUsage first or document the blocker.
