# User Manual

LimitDock is a Windows dock for quota rows produced from the OpenUsage.sh read model. It is designed to be glanced at while coding, not used as a dashboard.

## Start LimitDock

From a release folder, run:

```text
.\LimitDock.exe
```

From the repository, run:

```text
go run .\cmd\limitdock
```

LimitDock starts the bundled or downloaded OpenUsage.sh daemon, reads the local read model, and renders provider cards that expose quota-like rows.

## Top Or Bottom Ribbon

![Top or bottom ribbon](images/manual-ribbon.png)

The ribbon is best for wide monitors. Each provider card shows:

- provider name and icon
- model or plan bucket, including the metering window when available
- reset countdown only in the timing column
- remaining percent
- compact gauge

When two rows are visible, they use two full-width lines. When three or four rows are visible, they switch to a compact grid.

## Visible Row Picker

Double-click a provider card to choose model/window rows.

![Visible row picker](images/manual-row-picker.png)

Checked rows are visible. Unchecked rows are hidden. Hidden rows stay in `settings.json` and can be restored later; LimitDock never deletes source telemetry.

## Left Or Right Dock

![Left or right dock](images/manual-side-dock.png)

Side docking is useful when a horizontal ribbon would take too much vertical space. Provider cards stack vertically, and each quota row gets its own line. Model labels are shortened before reset countdown or remaining percent are hidden.

## Tray Controls

Right-click the tray icon:

- `Hide Status Bar`: hides the dock, unregisters reserved appbar space, restores the Windows work area, disables hover reveal, and pauses refresh.
- `Show Status Bar`: shows the dock again using saved settings.
- `Settings`: opens LimitDock and OpenUsage settings.
- `Exit`: closes LimitDock and stops the managed OpenUsage daemon.

Hide is not persisted. A fresh launch shows the dock again.

## Docking Settings

Open `Settings` from the tray.

Display modes:

- `reserved`: the default first-run mode. It reserves a Windows work area so maximized windows leave room for the dock.
- `overlay`: floats above other windows. Pin/unpin is available only in overlay mode.

Dock edges:

- `bottom`
- `top`
- `left`
- `right`

Dock mode, dock edge, auto-hide, refresh interval, gauge thresholds, and visible row choices are persisted in local `settings.json`. Change these from `Settings` at any time.

`Start LimitDock when Windows starts` writes a per-user `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` value that points directly to `LimitDock.exe`. Clear the checkbox to remove the value. Older `LimitDock.lnk` startup shortcuts are cleaned up when this setting changes.

## Provider Behavior

Codex:

- Shows `rate_limit_*` rows.
- Keeps Spark/Bengalfox rows and labels them from OpenUsage attributes when available.

Cursor:

- Shows only the plan-cycle quota row from `plan_percent_used`.
- Uses `billing_cycle_end` for reset text when present.

Gemini:

- Shows model-specific quota rows when available.
- Suppresses aggregate duplicate rows such as bare `quota`, `quota_flash`, or `quota_pro` when model rows exist.

Antigravity:

- Appears as quota only when OpenUsage exposes it as a provider or quota-like snapshot.
- Manual path hints can be set in Settings through `antigravity.binaryPath`, `antigravity.dataDir`, and `antigravity.subtitle`.

For the upstream provider list, see [openusage all providers](https://github.com/janekbaraniewski/openusage#all-providers). Providers outside OpenUsage should be added through a LimitDock reader that emits the same normalized card rows.

## Build And Release

```text
go test ./...
go run .\cmd\limitdock-release -version vYYYYMMDD
```

Publish the generated zip from `dist\`. Do not publish `LimitDock.exe` alone.
