---
name: limitdock-manual-update
description: Maintain LimitDock README and User Manual visual assets with privacy-safe screenshots or GIFs. Use when updating documentation images, capturing LimitDock dark/light ribbon or side dock states, overlay opacity examples, slide in/out examples, row picker screenshots, or refreshing docs after UI changes while avoiding desktop/private data capture.
---

# LimitDock Manual Update

Use this skill to refresh LimitDock documentation screenshots and richer visual guide assets.

## Safety Rules

- Capture only LimitDock windows or a tightly cropped LimitDock region.
- Never include the Codex chat, browser, terminal, desktop, taskbar, notifications, email, file explorer contents, or other private windows.
- For opacity captures, place a neutral solid backdrop behind LimitDock before capturing. Do not capture real desktop content through transparency.
- If any non-LimitDock content appears in a candidate image, discard it or blur/mosaic it before adding it to `docs/images`.
- Wait until native provider readers have logged at least one successful quota fetch before capturing provider cards.
- Reader readiness alone is not enough. Also wait for the LimitDock UI to refresh afterward; use at least `-UiSettleSeconds 60` for final manual assets.
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
  -ReleaseDir E:\LimitDock-v20260517 `
  -SettingsPath E:\LimitDock-v20260517\settings.json `
  -OutputDir .\docs\images `
  -UiSettleSeconds 60
```

Use a validated release folder for `-ReleaseDir`, not an ad-hoc `dist\...-manual` build unless you just built it from `main`. Pass `-SettingsPath` so `hiddenQuotaBands` and other user trims are preserved; the script only changes theme, dock edge/mode, opacity, and auto-hide per shot.

3. Verify the generated images visually. Reject any image that includes private content or still shows an empty/waiting dock with no provider cards.

4. Update `README.md` and `docs/USER_MANUAL.md` so the guide shows:

- light and night ribbon mode
- light and night side dock mode
- overlay opacity behavior
- slide-in and slide-out behavior
- row picker

5. Keep captions short and task-focused. Explain that screenshots are captured after native readers return quota rows, so provider cards reflect real data.

6. Run checks:

```powershell
go test ./...
git diff --check
```

## Helper Details

- `scripts/readmodel-ready.go` tails `state/logs/limitdock.log` and succeeds when native reader success lines appear.
- `scripts/capture-limitdock-manual.ps1` writes temporary release-local `settings.json` states, launches the release EXE, waits for native reader readiness, waits for the UI to settle, captures only the LimitDock window/edge region, and creates `manual-slide-in-out.gif` with the bundled Go helper when `go` is available.

If the helper cannot prove reader readiness, do not capture final manual assets. Fix credentials or reader errors first, or document the blocker.
