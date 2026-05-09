# Product Design

LimitDock is a working status bar, not a dashboard. Its purpose is to keep quota risk visible without stealing attention from the editor.

## Product Rules

- Show quota only. Do not show spend, token totals, request counts, tool-call rates, or generic activity.
- Preserve exhausted rows. A 0 percent model is meaningful and must remain visible unless the user hides it.
- Prefer model plus window plus reset text over generic labels. A row should make it clear what model or plan bucket is being metered and when it resets.
- Keep local state local. `settings.json` is personal and ignored; `settings.example.json` is the shareable default.

## Ribbon Density

Top and bottom edges use a compact ribbon around 80 to 84 pixels tall. Provider cards are fixed-size chips, and quota rows are capped to a 2 by 2 grid per card. Extra rows remain available through the double-click row picker.

Left and right edges use a vertical strip. Cards stack vertically but keep the same internal row rules so provider behavior remains familiar across edges.

## Tool Rail

The tool rail stays at the left edge of the ribbon/card stack:

- Reserved mode: drag handle, settings.
- Overlay mode: settings, pin/unpin.

The pin/unpin icon appears only in overlay mode because reserved mode is always fixed. The three-dot handle appears only in reserved mode because it changes the registered appbar edge.

## Overlay Versus Reserved

Overlay mode floats above windows. It can be pinned or set to slide away at the selected edge.

Reserved mode registers a Windows appbar and applies a matching work area. Maximized windows should leave the reserved edge free. Hide from the tray always unregisters the reserved area and restores the previous work area.

## Provider Row Selection

Double-clicking a card opens a row picker. Checked rows are visible, unchecked rows are hidden. The hidden set is persisted by snapshot/provider key in `hiddenQuotaBands`, so user choices survive restart.

The picker is also the recovery path for rows that were hidden by mistake. LimitDock never deletes rows from the source data.
