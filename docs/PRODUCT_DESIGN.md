# Product Design

LimitDock is a working status bar, not a dashboard. Its purpose is to keep quota risk visible without stealing attention from the editor.

## Product Rules

- Show quota only. Do not show spend, token totals, request counts, tool-call rates, or generic activity.
- Preserve exhausted rows. A 0 percent model is meaningful and must remain visible unless the user hides it.
- Prefer model plus window in the row label, with reset countdown in its own column. A row should make it clear what model or plan bucket is being metered and when it resets without repeating the window twice.
- Keep local state local. `settings.json` is personal and ignored; `settings.example.json` is the shareable default.

## Ribbon Density

Top and bottom edges use a compact ribbon around 88 to 96 WinForms logical pixels tall, clamped by the current monitor work area so different screens keep a similar footprint. Provider cards are compact chips but wide enough for two full-width quota rows. Quota rows are capped to a 2 by 2 grid per card. Two visible rows use one full-width row each; three or four rows switch to a two-column grid. Model labels keep the metering window when available, while the separate timing column shows reset countdown only. Extra rows remain available through the double-click row picker.

Left and right edges use a narrow vertical strip whose width is also clamped by the monitor work area. Cards stack vertically, and quota rows use one full-width row per model/window. Model labels are deliberately truncated before reset and percent are allowed to disappear.

## Tool Rail

The tool rail stays compact:

- Reserved mode: settings.
- Overlay mode: settings, pin/unpin.

The pin/unpin icon appears only in overlay mode because reserved mode is always fixed. Dock edge changes live in Settings so the reserved appbar is predictable.

## Overlay Versus Reserved

Overlay mode floats above windows. It can be pinned or set to slide away at the selected edge.

Reserved mode is the default first-run mode. It registers a Windows appbar and applies a matching work area. Maximized windows should leave the reserved edge free. Hide from the tray always unregisters the reserved area and restores the previous work area. Hide is session-only and does not survive app restart.

## Provider Row Selection

Double-clicking a card opens a row picker. Checked rows are visible, unchecked rows are hidden. The hidden set is persisted by snapshot/provider key in `hiddenQuotaBands`, so user choices survive restart.

The picker is also the recovery path for rows that were hidden by mistake. LimitDock never deletes rows from the source data.
