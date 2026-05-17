param(
  [string]$ReleaseDir = (Join-Path (Get-Location) "dist\LimitDock-v20260510-go"),
  [string]$OutputDir = (Join-Path (Get-Location) "docs\images"),
  [string]$SettingsPath = "",
  [int]$TimeoutSeconds = 90,
  [int]$UiSettleSeconds = 60,
  [switch]$KeepRunning
)

$ErrorActionPreference = "Stop"
$script:SettingsTemplate = $null
$script:CaptureLogMarker = 0

Add-Type -AssemblyName System.Drawing
Add-Type -AssemblyName System.Windows.Forms

$nativeCode = @"
using System;
using System.Text;
using System.Runtime.InteropServices;
public class ManualCaptureNative {
  public delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left; public int Top; public int Right; public int Bottom; }
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc lpEnumFunc, IntPtr lParam);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint lpdwProcessId);
  [DllImport("user32.dll")] public static extern int GetWindowText(IntPtr hWnd, StringBuilder lpString, int nMaxCount);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hWnd, out RECT lpRect);
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int X, int Y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint dwFlags, uint dx, uint dy, uint dwData, UIntPtr dwExtraInfo);
  [DllImport("dwmapi.dll")] public static extern int DwmGetWindowAttribute(IntPtr hWnd, int dwAttribute, out RECT pvAttribute, int cbAttribute);
  public const uint LEFTDOWN = 0x0002;
  public const uint LEFTUP = 0x0004;
}
"@
Add-Type $nativeCode -ErrorAction SilentlyContinue

function Stop-LimitDock {
  Get-Process LimitDock -ErrorAction SilentlyContinue | Stop-Process -Force
  Start-Sleep -Milliseconds 800
}

function Initialize-SettingsTemplate {
  $path = $SettingsPath
  if ([string]::IsNullOrWhiteSpace($path)) {
    $path = Join-Path $ReleaseDir "settings.json"
  }
  if (!(Test-Path $path)) {
    return
  }
  $script:SettingsTemplate = Get-Content $path -Raw | ConvertFrom-Json
  $dest = Join-Path $ReleaseDir "settings.json"
  $sameFile = $false
  try {
    $sameFile = ((Resolve-Path -LiteralPath $path).Path -eq (Resolve-Path -LiteralPath $dest).Path)
  } catch {
    $sameFile = ($path.TrimEnd('\') -ieq $dest.TrimEnd('\'))
  }
  if (!$sameFile) {
    Copy-Item -LiteralPath $path -Destination $dest -Force
  }
  Write-Host "Using settings from $path (hiddenQuotaBands preserved for captures)."
}

function Write-Settings([string]$Theme, [string]$Edge, [string]$Mode, [bool]$AutoHide, [int]$Opacity) {
  if ($script:SettingsTemplate) {
    $settings = ($script:SettingsTemplate | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
  } else {
    $settings = [pscustomobject][ordered]@{
      autoHide = $AutoHide
      dockMode = $Mode
      dockEdge = $Edge
      theme = $Theme
      overlayOpacity = $Opacity
      startWithWindows = $false
      hiddenQuotaBands = @{}
      gaugeMaxBands = 4
      gaugeWarnPercent = 72
      gaugeCritPercent = 90
      refreshSeconds = 5
    }
  }
  $settings.theme = $Theme
  $settings.dockEdge = $Edge
  $settings.dockMode = $Mode
  $settings.autoHide = [bool]$AutoHide
  $settings.overlayOpacity = $Opacity
  $settings | ConvertTo-Json -Depth 20 | Set-Content -LiteralPath (Join-Path $ReleaseDir "settings.json") -Encoding UTF8
}

function Restore-SettingsTemplate {
  if ($null -eq $script:SettingsTemplate) {
    return
  }
  $script:SettingsTemplate | ConvertTo-Json -Depth 20 | Set-Content -LiteralPath (Join-Path $ReleaseDir "settings.json") -Encoding UTF8
  Write-Host "Restored original settings.json in $ReleaseDir"
}

function Get-CaptureLogPath {
  Join-Path $ReleaseDir "state\logs\limitdock.log"
}

function Mark-CaptureLog {
  $path = Get-CaptureLogPath
  if (!(Test-Path $path)) {
    $script:CaptureLogMarker = 0
    return
  }
  $script:CaptureLogMarker = @(Get-Content -LiteralPath $path).Count
}

function Get-RecentCaptureLogLines {
  $path = Get-CaptureLogPath
  if (!(Test-Path $path)) { return @() }
  $lines = @(Get-Content -LiteralPath $path)
  if ($script:CaptureLogMarker -ge $lines.Count) { return @() }
  return $lines[$script:CaptureLogMarker..($lines.Count - 1)]
}

function Wait-RecentLogMatch([string]$Pattern, [string]$Label) {
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  while ((Get-Date) -lt $deadline) {
    $recent = Get-RecentCaptureLogLines
    if ($recent -match $Pattern) {
      Write-Host "Log ready: $Label"
      return
    }
    Start-Sleep -Seconds 2
  }
  throw "Timed out waiting for $Label in $(Get-CaptureLogPath) (since launch)."
}

function Start-LimitDock {
  Mark-CaptureLog
  $exe = Join-Path $ReleaseDir "LimitDock.exe"
  if (!(Test-Path $exe)) { throw "LimitDock.exe not found: $exe" }
  Start-Process -FilePath $exe -WorkingDirectory $ReleaseDir | Out-Null
  Start-Sleep -Seconds 3
}

function Wait-ReadModelReady {
  Wait-RecentLogMatch 'reader captured' 'native quota readers'
  Wait-RecentLogMatch 'Antigravity reader captured' 'Antigravity quota rows'
}

function Get-LimitDockWindowRect {
  param([switch]$AllowMissing)
  return Get-LimitDockWindowRectCore -UseDwmFrame -AllowMissing:$AllowMissing
}

function Get-LimitDockLogicalRect {
  return Get-LimitDockWindowRectCore
}

function Get-LimitDockWindowRectCore {
  param([switch]$UseDwmFrame, [switch]$AllowMissing)
  $proc = Get-Process LimitDock -ErrorAction Stop | Select-Object -First 1
  $rects = New-Object System.Collections.Generic.List[object]
  [ManualCaptureNative]::EnumWindows({
    param([IntPtr]$hwnd, [IntPtr]$lp)
    [uint32]$procId = 0
    [void][ManualCaptureNative]::GetWindowThreadProcessId($hwnd, [ref]$procId)
    if ($procId -eq [uint32]$proc.Id -and [ManualCaptureNative]::IsWindowVisible($hwnd)) {
      $txt = [System.Text.StringBuilder]::new(256)
      [void][ManualCaptureNative]::GetWindowText($hwnd, $txt, 256)
      if ($txt.ToString() -eq "LimitDock") {
        $r = New-Object ManualCaptureNative+RECT
        if ($UseDwmFrame) {
          $dwm = New-Object ManualCaptureNative+RECT
          $hr = [ManualCaptureNative]::DwmGetWindowAttribute($hwnd, 9, [ref]$dwm, [Runtime.InteropServices.Marshal]::SizeOf([type][ManualCaptureNative+RECT]))
          if ($hr -eq 0 -and ($dwm.Right - $dwm.Left) -gt 0 -and ($dwm.Bottom - $dwm.Top) -gt 0) {
            $r = $dwm
          } else {
            [void][ManualCaptureNative]::GetWindowRect($hwnd, [ref]$r)
          }
        } else {
          [void][ManualCaptureNative]::GetWindowRect($hwnd, [ref]$r)
        }
        if (($r.Right - $r.Left) -gt 20 -and ($r.Bottom - $r.Top) -gt 20) {
          $rects.Add([pscustomobject]@{ Left=$r.Left; Top=$r.Top; Right=$r.Right; Bottom=$r.Bottom }) | Out-Null
        }
      }
    }
    return $true
  }, [IntPtr]::Zero) | Out-Null
  if ($rects.Count -eq 0) {
    if ($AllowMissing) { return $null }
    throw "Visible LimitDock window not found."
  }
  return $rects | Sort-Object { ($_.Right-$_.Left) * ($_.Bottom-$_.Top) } -Descending | Select-Object -First 1
}

function Capture-Rect([object]$Rect, [string]$Path) {
  $left = [Math]::Max(0, $Rect.Left)
  $top = [Math]::Max(0, $Rect.Top)
  $width = [Math]::Max(1, $Rect.Right - $Rect.Left)
  $height = [Math]::Max(1, $Rect.Bottom - $Rect.Top)
  $bmp = New-Object System.Drawing.Bitmap $width, $height
  $g = [System.Drawing.Graphics]::FromImage($bmp)
  $g.CopyFromScreen($left, $top, 0, 0, [System.Drawing.Size]::new($width, $height))
  New-Item -ItemType Directory -Path (Split-Path $Path -Parent) -Force | Out-Null
  $bmp.Save($Path, [System.Drawing.Imaging.ImageFormat]::Png)
  $g.Dispose()
  $bmp.Dispose()
}

function New-BlankEdgeCapture([string]$Path) {
  $screen = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
  $width = 48
  $height = [Math]::Max(480, $screen.Height)
  $bmp = New-Object System.Drawing.Bitmap $width, $height
  $g = [System.Drawing.Graphics]::FromImage($bmp)
  $g.Clear([System.Drawing.Color]::FromArgb(17, 24, 32))
  New-Item -ItemType Directory -Path (Split-Path $Path -Parent) -Force | Out-Null
  $bmp.Save($Path, [System.Drawing.Imaging.ImageFormat]::Png)
  $g.Dispose()
  $bmp.Dispose()
}

function Click-Point([int]$X, [int]$Y) {
  [ManualCaptureNative]::SetCursorPos($X, $Y) | Out-Null
  Start-Sleep -Milliseconds 120
  [ManualCaptureNative]::mouse_event([ManualCaptureNative]::LEFTDOWN, 0, 0, 0, [UIntPtr]::Zero)
  Start-Sleep -Milliseconds 80
  [ManualCaptureNative]::mouse_event([ManualCaptureNative]::LEFTUP, 0, 0, 0, [UIntPtr]::Zero)
}

function Trigger-LimitDockRefresh {
  $rect = Get-LimitDockLogicalRect
  $width = $rect.Right - $rect.Left
  $height = $rect.Bottom - $rect.Top
  if ($width -gt $height) {
    Click-Point ($rect.Right - 85) ($rect.Top + [int]($height / 2))
  } else {
    Click-Point ($rect.Right - 85) ($rect.Top + 34)
  }
  Start-Sleep -Seconds 3
}

function Capture-LimitDock([string]$Name, [switch]$AllowMissing) {
  $target = Join-Path $OutputDir $Name
  $rect = Get-LimitDockWindowRect -AllowMissing:$AllowMissing
  if ($null -eq $rect) {
    New-BlankEdgeCapture $target
    return
  }
  Capture-Rect $rect $target
}

function With-NeutralBackdrop([scriptblock]$Body) {
  $form = New-Object System.Windows.Forms.Form
  $form.Text = "LimitDock manual capture backdrop"
  $form.FormBorderStyle = [System.Windows.Forms.FormBorderStyle]::None
  $form.BackColor = [System.Drawing.Color]::FromArgb(40, 44, 52)
  $form.StartPosition = [System.Windows.Forms.FormStartPosition]::Manual
  $screen = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
  $form.Bounds = $screen
  $form.Show()
  Start-Sleep -Milliseconds 300
  try { & $Body } finally { $form.Close(); $form.Dispose() }
}

function Capture-State([string]$Name, [string]$Theme, [string]$Edge, [string]$Mode, [bool]$AutoHide, [int]$Opacity) {
  Stop-LimitDock
  Write-Settings -Theme $Theme -Edge $Edge -Mode $Mode -AutoHide $AutoHide -Opacity $Opacity
  Start-LimitDock
  Wait-ReadModelReady
  Write-Host "Native quota readers are ready. Waiting $UiSettleSeconds seconds for LimitDock UI refresh..."
  Start-Sleep -Seconds $UiSettleSeconds
  if ($AutoHide -and ($Edge -eq "left" -or $Edge -eq "right")) {
    [ManualCaptureNative]::SetCursorPos(1, 300) | Out-Null
    Start-Sleep -Milliseconds 900
  }
  Trigger-LimitDockRefresh
  Start-Sleep -Seconds 5
  Capture-LimitDock $Name
}

New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
Initialize-SettingsTemplate

With-NeutralBackdrop {
  Capture-State "manual-ribbon-light.png" "light" "bottom" "reserved" $false 100
  Capture-State "manual-ribbon-night.png" "night" "bottom" "reserved" $false 100
  Capture-State "manual-side-dock-light.png" "light" "left" "overlay" $false 100
  Capture-State "manual-side-dock-night.png" "night" "left" "overlay" $false 100
  Capture-State "manual-overlay-opacity.png" "night" "bottom" "overlay" $false 65
  Capture-State "manual-slide-in.png" "night" "left" "overlay" $true 100
  [ManualCaptureNative]::SetCursorPos(900, 700) | Out-Null
  Start-Sleep -Milliseconds 1000
  Capture-LimitDock "manual-slide-out.png" -AllowMissing
}

  $go = Get-Command go -ErrorAction SilentlyContinue
  $gifHelper = Join-Path $PSScriptRoot "make-slide-gif.go"
  if ($go -and (Test-Path $gifHelper)) {
    & $go.Source run $gifHelper -out (Join-Path $OutputDir "manual-slide-in-out.gif") -delay 90 (Join-Path $OutputDir "manual-slide-out.png") (Join-Path $OutputDir "manual-slide-in.png")
    if ($LASTEXITCODE -ne 0) {
      Write-Warning "Failed to create manual-slide-in-out.gif."
    }
  }

if (!$KeepRunning) {
  Stop-LimitDock
}

Restore-SettingsTemplate
Write-Host "Manual capture assets written to $OutputDir"
