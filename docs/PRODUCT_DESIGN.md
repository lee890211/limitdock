# Product Design

LimitDock is a working status bar, not a dashboard. Its purpose is to keep quota risk visible without stealing attention from the editor.

## Product Rules

- Show quota only. Do not show spend, token totals, request counts, tool-call rates, or generic activity.
- Keep needs-auth minimal. A card whose local credentials are unusable shows a minimal `Sign in` state instead of quota bands — the quota-only spirit holds even when a provider needs attention, so the card stays visible rather than hiding.
- Preserve exhausted rows. A 0 percent model is meaningful and must remain visible unless the user hides it.
- Prefer model plus window in the row label, with reset countdown in its own column. A row should make it clear what model or plan bucket is being metered and when it resets without repeating the window twice.
- Keep local state local. `settings.json` is personal and ignored; `settings.example.json` is the shareable default.
- Make startup opt-in. The per-user Windows startup entry is controlled from Settings and can be removed by clearing the same checkbox.

## Ribbon Density

Provider icons are neutral LimitDock badges by default, not official brand marks. Official provider logos can be substituted in `assets/icons` only when their brand guidelines and trademark terms allow that usage.

Top and bottom edges use a compact ribbon around 88 to 96 Windows logical pixels tall, clamped by the current monitor work area so different screens keep a similar footprint. Provider cards are compact chips but wide enough for visible quota rows. Model labels keep the metering window when available, while the separate timing column shows reset countdown only. Remaining percent is drawn inside the gauge. Extra rows remain available through the double-click row picker. On wide displays, the ribbon should fit up to five provider cards.

Left and right edges use a narrow vertical strip whose width is also clamped by the monitor work area. Cards stack vertically, and quota rows use one full-width row per model/window. Model labels are deliberately truncated before reset and percent are allowed to disappear.

## Tool Rail

The tool rail stays compact:

- Reserved mode: settings.
- Overlay mode: settings, pin/unpin.

The pin/unpin icon appears only in overlay mode because reserved mode is always fixed. Dock edge changes live in Settings as a four-option visual edge picker ordered `bottom`, `left`, `top`, `right` so the reserved appbar is predictable and the selected side is recognizable without reading a dropdown.

The theme control is the first settings row and uses two compact visual day/night buttons. Display mode remains a plain text selector below the position picker.

Overlay opacity is a live-preview setting: dragging the slider changes only the floating dock, Cancel restores the previous value, and Save persists it.

## Refresh Control

The `Updated` panel is both status and action. It keeps a compact vertical label/time layout and a refresh glyph so it is recognizable as clickable. Clicking it forces a refresh immediately instead of waiting for the automatic interval.

Background polling is throttled per provider, independent of this interval: Claude Code fetches no more than every 180 seconds (backing off further on repeated rate limits), and the other four providers no more than every 60 seconds. Clicking `Updated` always forces a real fetch, bypassing whatever throttle or backoff is currently in effect.

## Overlay Versus Reserved

Overlay mode floats above windows. It can be pinned or set to slide away at the selected edge.

Reserved mode is the default first-run mode. It registers a Windows appbar and applies a matching work area. Maximized windows should leave the reserved edge free. Hide from the tray always unregisters the reserved area and restores the previous work area. Hide is session-only and does not survive app restart.

## Provider Row Selection

Double-clicking a card opens a row picker. Checked rows are visible, unchecked rows are hidden. The hidden set is persisted by snapshot/provider key in `hiddenQuotaBands`, so user choices survive restart.

The picker is also the recovery path for rows that were hidden by mistake. LimitDock never deletes rows from the source data.
