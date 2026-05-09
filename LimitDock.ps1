param(
  [switch]$NoDownload,
  [int]$RefreshSeconds = 30
)

$ErrorActionPreference = "Continue"
$ScriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$EngineDir = Join-Path $ScriptRoot "engine"
$BinDir = Join-Path $EngineDir "bin"
$StateDir = Join-Path $EngineDir "state"
$DownloadsDir = Join-Path $EngineDir "downloads"
$OpenUsageDir = Join-Path $DownloadsDir "openusage_windows_amd64"
$OpenUsageExe = Join-Path $OpenUsageDir "openusage.exe"
$ProbeExe = Join-Path $BinDir "openusage-readmodel.exe"
$ProbeSourceDir = Join-Path $ScriptRoot "probes\openusage-readmodel"
$SocketPath = Join-Path ([Environment]::GetFolderPath("UserProfile")) ".local\state\openusage\telemetry.sock"
$DbPath = Join-Path $StateDir "limitdock-telemetry.db"
$SpoolDir = Join-Path $StateDir "limitdock-spool"
$LogDir = Join-Path $StateDir "logs"
$DaemonOutLog = Join-Path $LogDir "openusage-daemon.out.log"
$DaemonErrLog = Join-Path $LogDir "openusage-daemon.err.log"
$AppPidPath = Join-Path $StateDir "limitdock.pid"
$DaemonPidPath = Join-Path $StateDir "openusage-daemon.pid"
$IconDir = Join-Path $ScriptRoot "assets\icons"
$SettingsPath = Join-Path $ScriptRoot "settings.json"

try {
  New-Item -ItemType Directory -Force -Path $BinDir, $StateDir, $DownloadsDir, $SpoolDir, $LogDir -ErrorAction Stop | Out-Null
} catch {
  [Console]::Error.WriteLine("LimitDock cannot create state directories: $($_.Exception.Message)")
  exit 1
}

$script:LimitDockMutex = $null
$mutexCreated = $false
try {
  $script:LimitDockMutex = New-Object System.Threading.Mutex($true, "Local\LimitDock.OpenUsageHud", [ref]$mutexCreated)
  if (-not $mutexCreated) {
    $msg = "LimitDock is already running in this Windows session."
    try {
      Add-Content -Path (Join-Path $LogDir "limitdock.log") -Value "$(Get-Date -Format o) $msg"
    } catch {}
    [Console]::Error.WriteLine($msg)
    exit 0
  }
} catch {
  [Console]::Error.WriteLine("LimitDock cannot create its single-instance guard: $($_.Exception.Message)")
  exit 1
}

try {
  $PID | Set-Content -Path $AppPidPath -ErrorAction Stop
} catch {
  try {
    Add-Content -Path (Join-Path $LogDir "limitdock.log") -Value "$(Get-Date -Format o) Could not write app pid: $($_.Exception.Message)"
  } catch {}
}

function Write-Log {
  param([string]$Message)
  $line = "$(Get-Date -Format o) $Message"
  Add-Content -Path (Join-Path $LogDir "limitdock.log") -Value $line
}

function Load-LimitDockSettings {
  $defaults = [pscustomobject]@{
    autoHide = $false
    dockMode = "overlay"
    dockEdge = "bottom"
    statusBarVisible = $true
    hiddenQuotaBands = [pscustomobject]@{}
    refreshSeconds = 30
    antigravity = [pscustomobject]@{
      enabled = $true
      binaryPath = ""
      dataDir = ""
      subtitle = ""
    }
    gaugeMaxBands = 4
    gaugeWarnPercent = 72
    gaugeCritPercent = 90
  }

  function Ensure-AntigravityBlock($Sg) {
    if ($null -eq $Sg) {
      return [pscustomobject]@{ enabled = $true; binaryPath = ""; dataDir = "" }
    }
    if ($null -eq $Sg.enabled) {
      $Sg | Add-Member -NotePropertyName enabled -NotePropertyValue $defaults.antigravity.enabled
    }
    if ($null -eq $Sg.binaryPath) {
      $Sg | Add-Member -NotePropertyName binaryPath -NotePropertyValue $defaults.antigravity.binaryPath
    }
    if ($null -eq $Sg.dataDir) {
      $Sg | Add-Member -NotePropertyName dataDir -NotePropertyValue $defaults.antigravity.dataDir
    }
    if (-not ($Sg.PSObject.Properties.Name -contains "subtitle")) {
      $Sg | Add-Member -NotePropertyName subtitle -NotePropertyValue $defaults.antigravity.subtitle
    }
    if ($null -eq $Sg.subtitle) {
      $Sg.subtitle = $defaults.antigravity.subtitle
    }
    return $Sg
  }

  if (-not (Test-Path $SettingsPath)) {
    return $defaults
  }

  try {
    $settings = Get-Content $SettingsPath -Raw | ConvertFrom-Json
    if ($null -eq $settings.autoHide) {
      $settings | Add-Member -NotePropertyName autoHide -NotePropertyValue $defaults.autoHide -Force
    }
    if (-not ($settings.PSObject.Properties.Name -contains "dockMode")) {
      $settings | Add-Member -NotePropertyName dockMode -NotePropertyValue $defaults.dockMode -Force
    }
    $dm = ([string]$settings.dockMode).Trim().ToLowerInvariant()
    if (($dm -ne "overlay") -and ($dm -ne "reserved")) {
      $dm = $defaults.dockMode
    }
    $settings.dockMode = $dm
    if (-not ($settings.PSObject.Properties.Name -contains "dockEdge")) {
      $settings | Add-Member -NotePropertyName dockEdge -NotePropertyValue $defaults.dockEdge -Force
    }
    $de = ([string]$settings.dockEdge).Trim().ToLowerInvariant()
    if (($de -ne "top") -and ($de -ne "bottom") -and ($de -ne "left") -and ($de -ne "right")) {
      $de = $defaults.dockEdge
    }
    $settings.dockEdge = $de
    if (-not ($settings.PSObject.Properties.Name -contains "statusBarVisible")) {
      $settings | Add-Member -NotePropertyName statusBarVisible -NotePropertyValue $defaults.statusBarVisible -Force
    }
    if ($null -eq $settings.statusBarVisible) {
      $settings.statusBarVisible = $defaults.statusBarVisible
    }
    if (-not ($settings.PSObject.Properties.Name -contains "hiddenQuotaBands")) {
      $settings | Add-Member -NotePropertyName hiddenQuotaBands -NotePropertyValue ([pscustomobject]@{}) -Force
    }
    if ($null -eq $settings.hiddenQuotaBands) {
      $settings.hiddenQuotaBands = [pscustomobject]@{}
    }
    if (-not ($settings.PSObject.Properties.Name -contains "refreshSeconds")) {
      $settings | Add-Member -NotePropertyName refreshSeconds -NotePropertyValue $defaults.refreshSeconds -Force
    }
    elseif ($null -eq $settings.refreshSeconds -or ([int]$settings.refreshSeconds -lt 5)) {
      $settings.refreshSeconds = [int]$defaults.refreshSeconds
    }
    $settings.antigravity = Ensure-AntigravityBlock $settings.antigravity
    if (-not ($settings.PSObject.Properties.Name -contains "gaugeMaxBands")) {
      $settings | Add-Member -NotePropertyName gaugeMaxBands -NotePropertyValue $defaults.gaugeMaxBands -Force
    }
    if (-not ($settings.PSObject.Properties.Name -contains "gaugeWarnPercent")) {
      $settings | Add-Member -NotePropertyName gaugeWarnPercent -NotePropertyValue $defaults.gaugeWarnPercent -Force
    }
    if (-not ($settings.PSObject.Properties.Name -contains "gaugeCritPercent")) {
      $settings | Add-Member -NotePropertyName gaugeCritPercent -NotePropertyValue $defaults.gaugeCritPercent -Force
    }
    return $settings
  } catch {
    Write-Log "Failed to load settings, using defaults: $($_.Exception.Message)"
    return $defaults
  }
}

function Save-LimitDockSettings {
  param($Settings)
  try {
    $Settings | ConvertTo-Json -Depth 12 | Set-Content -Path $SettingsPath -Encoding UTF8
  } catch {
    Write-Log "Failed to save settings: $($_.Exception.Message)"
  }
}

function ConvertTo-LdHashtable {
  param([AllowNull()]$Value)
  $h = @{}
  if ($null -eq $Value) {
    return $h
  }
  if ($Value -is [hashtable]) {
    foreach ($k in $Value.Keys) {
      $v = $Value[$k]
      if (($v -is [hashtable]) -or ($v -is [pscustomobject])) {
        $h[[string]$k] = ConvertTo-LdHashtable $v
      } else {
        $h[[string]$k] = $v
      }
    }
    return $h
  }
  try {
    foreach ($p in $Value.PSObject.Properties) {
      if (($p.Value -is [hashtable]) -or ($p.Value -is [pscustomobject])) {
        $h[[string]$p.Name] = ConvertTo-LdHashtable $p.Value
      } else {
        $h[[string]$p.Name] = $p.Value
      }
    }
  } catch {}
  return $h
}

trap {
  $message = $_.Exception.Message
  $stack = $_.ScriptStackTrace
  try {
    Add-Content -Path (Join-Path $LogDir "limitdock.log") -Value "$(Get-Date -Format o) Trapped error: $message"
    if ($stack) {
      Add-Content -Path (Join-Path $LogDir "limitdock.log") -Value $stack
    }
  } catch {}
  continue
}

function Ensure-OpenUsage {
  if (Test-Path $OpenUsageExe) {
    return
  }
  if ($NoDownload) {
    throw "OpenUsage.sh Windows binary is missing: $OpenUsageExe"
  }

  Write-Log "Downloading OpenUsage.sh Windows binary"
  $release = Invoke-RestMethod `
    -Uri "https://api.github.com/repos/janekbaraniewski/openusage/releases/latest" `
    -Headers @{ "User-Agent" = "LimitDock" }
  $asset = $release.assets | Where-Object { $_.name -like "openusage_*_windows_amd64.zip" } | Select-Object -First 1
  if (-not $asset) {
    throw "Could not find OpenUsage.sh windows_amd64 release asset."
  }

  $zipPath = Join-Path $DownloadsDir "openusage_windows_amd64.zip"
  Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $zipPath
  if (Test-Path $OpenUsageDir) {
    Remove-Item -LiteralPath $OpenUsageDir -Recurse -Force
  }
  Expand-Archive -Path $zipPath -DestinationPath $OpenUsageDir -Force
  if (-not (Test-Path $OpenUsageExe)) {
    throw "Downloaded OpenUsage.sh archive did not contain expected binary: $OpenUsageExe"
  }
}

function Ensure-Probe {
  if (Test-Path $ProbeExe) {
    return
  }
  $go = Get-Command go -ErrorAction SilentlyContinue
  if (-not $go) {
    throw "LimitDock probe is missing and Go is not installed: $ProbeExe"
  }

  Write-Log "Building OpenUsage read-model probe"
  Push-Location $ProbeSourceDir
  try {
    $env:GOCACHE = Join-Path $EngineDir ".gocache"
    go build -o $ProbeExe .
  } finally {
    Pop-Location
  }
}

function Remove-StaleSocket {
  if (Test-Path $SocketPath) {
    try {
      Remove-Item -LiteralPath $SocketPath -Force
    } catch {
      Write-Log "Could not remove stale socket: $($_.Exception.Message)"
    }
  }
}

function Start-OpenUsageDaemon {
  Remove-StaleSocket

  $socketDir = Split-Path -Parent $SocketPath
  if (-not (Test-Path $socketDir)) {
    try { New-Item -ItemType Directory -Force -Path $socketDir | Out-Null } catch {}
  }

  $psi = New-Object System.Diagnostics.ProcessStartInfo
  $psi.FileName = $OpenUsageExe
  $argList = @(
    "telemetry", "daemon", "run",
    "--socket-path", "`"$SocketPath`""
  )
  $psi.Arguments = $argList -join " "
  $psi.WorkingDirectory = $ScriptRoot
  $psi.UseShellExecute = $true
  $psi.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Hidden

  try {
    $process = [System.Diagnostics.Process]::Start($psi)
  } catch {
    Write-Log "Failed to launch daemon process: $($_.Exception.Message)"
    return $null
  }
  if (-not $process) {
    Write-Log "Failed to start OpenUsage.sh daemon (Process.Start returned null)."
    return $null
  }
  Write-Log "Started OpenUsage.sh daemon pid=$($process.Id) socket=$SocketPath"
  try { $process.Id | Set-Content -Path $DaemonPidPath -ErrorAction Stop } catch {
    Write-Log "Could not write daemon pid file: $($_.Exception.Message)"
  }
  return $process
}

function Wait-OpenUsageReady {
  param([int]$TimeoutSeconds = 12)
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  while ((Get-Date) -lt $deadline) {
    if (Test-Path $SocketPath) {
      try {
        $raw = & $ProbeExe $SocketPath "{}" 2>&1
        if ($LASTEXITCODE -eq 0) { return $true }
      } catch {}
    }
    Start-Sleep -Milliseconds 350
  }
  return $false
}

function Stop-OpenUsageDaemon {
  param($Process)
  if ($Process -and -not $Process.HasExited) {
    Write-Log "Stopping OpenUsage.sh daemon pid=$($Process.Id)"
    try {
      $Process.Kill()
      $Process.WaitForExit(3000) | Out-Null
    } catch {
      Write-Log "Failed to stop daemon cleanly: $($_.Exception.Message)"
    }
  }
  Remove-StaleSocket
  Remove-Item -LiteralPath $DaemonPidPath -Force -ErrorAction SilentlyContinue
}

function Get-MetricValue {
  param($Metrics, [string[]]$Names)
  foreach ($name in $Names) {
    if ($Metrics -and $Metrics.PSObject.Properties.Name -contains $name) {
      return @{
        Name = $name
        Metric = $Metrics.$name
      }
    }
  }
  return $null
}

function Normalize-UsageResetUtc {
  param([datetime]$Raw)
  if ($Raw.Kind -eq [DateTimeKind]::Unspecified) {
    return ([DateTime]::SpecifyKind($Raw, [DateTimeKind]::Utc))
  }
  return $Raw.ToUniversalTime()
}

function Format-UsageResetPhrase {
  param([datetime]$ResetUtc)
  try {
    $utcNow = (Get-Date).ToUniversalTime()
    [TimeSpan]$d = ($ResetUtc - $utcNow)
    [string]$clock = $ResetUtc.ToLocalTime().ToString("M/d h:mm tt")
    if (($d.TotalMinutes -gt 2) -and ($d.TotalMinutes -lt ([double](401 * 24 * 60)))) {
      if ($d.TotalHours -ge 48) {
        return ("rolls {0:d} (~{1:0.#}d)" -f $ResetUtc.ToLocalTime(), ($d.TotalDays))
      }
      if ($d.TotalHours -ge 1) {
        [int]$hrs = [int][Math]::Floor([double]$d.TotalHours)
        [int]$totMin = [int][Math]::Floor([double]$d.TotalMinutes)
        [int]$remMin = ($totMin - ([int]$hrs * 60))
        if ($remMin -lt 0) {
          $remMin = [Math]::Abs($remMin) % 60
        }
        return ("rolls in ~{0}h {1}m (at {2})" -f $hrs, $remMin, $clock)
      }
      return ("rolls in ~{0}m (at {1})" -f [Math]::Max(1, [Math]::Ceiling($d.TotalMinutes)), $clock)
    }
    if (($d.TotalMinutes -gt 0) -and ($d.TotalMinutes -le 2)) {
      return ("rolls very soon (~{0}s, at {1})" -f [Math]::Max(1, [Math]::Ceiling($d.TotalSeconds)), $clock)
    }
    return ("last mark at {0} (rolling window; refreshed with telemetry)" -f $clock)
  } catch {
    return ""
  }
}

function Get-ResetText {
  param($Snapshot, [string]$MetricName)
  if (-not $Snapshot.resets) {
    return ""
  }

  $names = New-Object System.Collections.Generic.List[string]
  $mn = [string]$MetricName
  foreach ($candidate in @(
      "${mn}_reset",
      "rate_limit_primary",
      "rate_limit_secondary",
      "rate_limit_code_review_primary",
      "rate_limit_code_review_secondary",
      "billing_cycle_end",
      "quota_reset",
      "quota_flash_reset")) {
    if ((-not ([string]::IsNullOrWhiteSpace([string]$candidate))) -and (-not ($names.Contains($candidate)))) {
      [void]$names.Add($candidate)
    }
  }

  # Prefer resets that match the headline metric key prefix (Codex PTE keys share rate_limit_primary reset).
  if ((-not ([string]::IsNullOrWhiteSpace([string]$mn))) -and ($mn -match "^(rate_limit_\w+)")) {
    $pfx = [string]$Matches[1]
    try {
      if ($Snapshot.resets.PSObject.Properties.Name -contains $pfx) {
        [void]$names.Insert(0, $pfx)
      }
    } catch {}
  }

  foreach ($name in @( $names.ToArray())) {
    if (-not ($Snapshot.resets.PSObject.Properties.Name -contains $name)) {
      continue
    }
    try {
      $resetUtc = (Normalize-UsageResetUtc ([datetime]$Snapshot.resets.$name))
      $phrase = Format-UsageResetPhrase $resetUtc
      if (-not ([string]::IsNullOrWhiteSpace([string]$phrase))) {
        return $phrase
      }
    } catch {}
  }

  $bestFut = $null
  foreach ($rp in @( $Snapshot.resets.PSObject.Properties)) {
    try {
      $resetUtc = (Normalize-UsageResetUtc ([datetime]$rp.Value))
      $dFut = ($resetUtc - ((Get-Date).ToUniversalTime()))
      if ($dFut.TotalMinutes -gt 0) {
        if (($null -eq $bestFut) -or ($resetUtc -lt $bestFut)) {
          $bestFut = $resetUtc
        }
      }
    } catch {}
  }
  if ($null -ne $bestFut) {
    return (Format-UsageResetPhrase $bestFut)
  }

  $latest = $null
  foreach ($rp in @( $Snapshot.resets.PSObject.Properties)) {
    try {
      $resetUtc = (Normalize-UsageResetUtc ([datetime]$rp.Value))
      if (($null -eq $latest) -or ($resetUtc -gt $latest)) {
        $latest = $resetUtc
      }
    } catch {}
  }
  if ($null -ne $latest) {
    return (Format-UsageResetPhrase $latest)
  }

  return ""
}

function Get-WindowsTheme {
  $isLight = $false
  try {
    $personalize = Get-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Themes\Personalize" -ErrorAction Stop
    $isLight = ([int]$personalize.AppsUseLightTheme -eq 1)
  } catch {
    $isLight = $false
  }

  if ($isLight) {
    return @{
      Bar = [System.Drawing.Color]::FromArgb(243, 243, 243)
      Panel = [System.Drawing.Color]::FromArgb(243, 243, 243)
      Fore = [System.Drawing.Color]::FromArgb(32, 32, 32)
      MutedFore = [System.Drawing.Color]::FromArgb(96, 96, 96)
      OkBack = [System.Drawing.Color]::FromArgb(255, 255, 255)
      WarnBack = [System.Drawing.Color]::FromArgb(255, 244, 214)
      WarnFore = [System.Drawing.Color]::FromArgb(94, 66, 0)
      CriticalBack = [System.Drawing.Color]::FromArgb(255, 226, 226)
      CriticalFore = [System.Drawing.Color]::FromArgb(117, 21, 31)
      StatusBack = [System.Drawing.Color]::FromArgb(232, 232, 232)
      StatusAccent = [System.Drawing.Color]::FromArgb(0, 99, 177)
      GaugeTrack = [System.Drawing.Color]::FromArgb(210, 210, 210)
      GaugeOk = [System.Drawing.Color]::FromArgb(16, 124, 16)
      GaugeWarn = [System.Drawing.Color]::FromArgb(196, 128, 0)
      GaugeCrit = [System.Drawing.Color]::FromArgb(180, 32, 32)
    }
  }

  return @{
    Bar = [System.Drawing.Color]::FromArgb(32, 32, 32)
    Panel = [System.Drawing.Color]::FromArgb(32, 32, 32)
    Fore = [System.Drawing.Color]::FromArgb(245, 245, 245)
    MutedFore = [System.Drawing.Color]::FromArgb(190, 190, 190)
    OkBack = [System.Drawing.Color]::FromArgb(47, 47, 47)
    WarnBack = [System.Drawing.Color]::FromArgb(92, 68, 20)
    WarnFore = [System.Drawing.Color]::FromArgb(255, 236, 186)
    CriticalBack = [System.Drawing.Color]::FromArgb(102, 31, 38)
    CriticalFore = [System.Drawing.Color]::FromArgb(255, 224, 226)
    StatusBack = [System.Drawing.Color]::FromArgb(54, 54, 54)
    StatusAccent = [System.Drawing.Color]::FromArgb(120, 200, 255)
    GaugeTrack = [System.Drawing.Color]::FromArgb(76, 76, 76)
    GaugeOk = [System.Drawing.Color]::FromArgb(110, 200, 120)
    GaugeWarn = [System.Drawing.Color]::FromArgb(235, 180, 72)
    GaugeCrit = [System.Drawing.Color]::FromArgb(243, 120, 120)
  }
}

function Convert-ToDisplayText {
  param([AllowNull()][object]$Value)
  if ($null -eq $Value) {
    return ""
  }

  $text = [string]$Value
  $text = $text.Replace([string][char]0x2013, "-")
  $text = $text.Replace([string][char]0x2014, "-")
  $text = $text.Replace([string][char]0x2212, "-")
  $text = $text.Replace([string][char]0x00B7, "|")
  $text = $text.Replace([string][char]0x2022, "|")
  $text = $text.Replace([string][char]0x2018, "'")
  $text = $text.Replace([string][char]0x2019, "'")
  $text = $text.Replace([string][char]0x201C, '"')
  $text = $text.Replace([string][char]0x201D, '"')
  $text = $text.Replace([string][char]0x2026, "...")
  return ([regex]::Replace($text, "[^\u0009\u000A\u000D\u0020-\u007E]", "")).Trim()
}

function Write-Utf8NoBom {
  param([string]$TargetPath, [string]$Payload)
  try {
    $enc = New-Object System.Text.UTF8Encoding $false
    [System.IO.File]::WriteAllText($TargetPath, $Payload, $enc)
  } catch {
    Write-Log "WriteUtf8NoBom failed: $($_.Exception.Message)"
    throw $_
  }
}

function Get-OpenUsageJsonPath {
  Join-Path ([Environment]::GetFolderPath("ApplicationData")) "openusage\settings.json"
}

function Get-OpenUsageDataTimeWindow {
  $cfgPath = Get-OpenUsageJsonPath
  if (-not (Test-Path $cfgPath)) {
    return "30d"
  }
  try {
    $ouk = Get-Content -LiteralPath $cfgPath -Raw | ConvertFrom-Json
    if ($ouk.data -and $ouk.data.time_window) {
      return ([string]$ouk.data.time_window).Trim().ToLowerInvariant()
    }
  } catch {}
  return "30d"
}

function Invoke-ProbeReadJson {
  param([string]$PayloadAbsolutePathOrEmpty)
  if ($PayloadAbsolutePathOrEmpty -and (Test-Path -LiteralPath $PayloadAbsolutePathOrEmpty)) {
    $marker = "@$PayloadAbsolutePathOrEmpty"
    $raw = & $ProbeExe $SocketPath $marker 2>&1
  } else {
    $raw = & $ProbeExe $SocketPath "{}" 2>&1
  }
  if ($LASTEXITCODE -ne 0) {
    throw ($raw -join "`n")
  }
  return (($raw | Where-Object { $_ -is [string] }) -join "`n")
}

function Get-CodexQuotaMetricCount($Snapshot) {
  if (-not $Snapshot -or -not $Snapshot.metrics) {
    return 0
  }
  [int]$count = 0
  foreach ($prop in $Snapshot.metrics.PSObject.Properties) {
    [string]$k = ""
    try { $k = ([string]$prop.Name).Trim().ToLowerInvariant() } catch { $k = "" }
    if (($k -eq "quota") -or
        ($k -eq "quota_pro") -or
        ($k -eq "quota_flash") -or
        ($k -eq "usage_five_hour") -or
        ($k.StartsWith("usage_seven_day")) -or
        ($k.StartsWith("rate_limit_"))) {
      $count++
    }
  }
  return $count
}

function Test-SnapshotMapNeedsCodexSupplement($SnapshotsNode) {
  if (-not $SnapshotsNode) { return $true }
  foreach ($prop in $SnapshotsNode.PSObject.Properties) {
    try {
      if ([string]$prop.Value.provider_id -ne "codex") {
        continue
      }
      if ((Get-CodexQuotaMetricCount $prop.Value) -gt 0) {
        return $false
      }
    } catch {}
  }
  return $true
}

function Merge-LdSnapshotsNode {
  param($Primary, $Supplement)
  $map = @{}
  if ($Primary) {
    foreach ($prop in $Primary.PSObject.Properties) {
      if ($prop.Name -ne "__invalid") {
        if ($map.ContainsKey($prop.Name)) {
          Write-Log "Merge snapshot skip duplicate $($prop.Name)"
        } else {
          $map[$prop.Name] = $prop.Value
        }
      }
    }
  }
  if (-not $Supplement) {
    return $Primary
  }
  foreach ($prop in $Supplement.PSObject.Properties) {
    if ($prop.Name -eq "__invalid") { continue }
    $provId = ""
    try { $provId = [string]$prop.Value.provider_id } catch {}
    if ($provId -ne "codex") { continue }
    $acctId = ""
    try { $acctId = [string]$prop.Value.account_id } catch {}
    # Prefer supplemental codex-cli when absent or visibly empty metrics.
    $metricCountPrimary = -1
    if ($map.ContainsKey($prop.Name)) {
      try {
        $prim = $map[$prop.Name]
        $metricCountPrimary = $prim.metrics.PSObject.Properties.Count
      } catch { $metricCountPrimary = -1 }
    }
    $metricCountFall = -1
    try { $metricCountFall = $prop.Value.metrics.PSObject.Properties.Count } catch {}
    [int]$quotaCountPrimary = 0
    [int]$quotaCountFall = 0
    if ($map.ContainsKey($prop.Name)) {
      try { $quotaCountPrimary = Get-CodexQuotaMetricCount $map[$prop.Name] } catch {}
    }
    try { $quotaCountFall = Get-CodexQuotaMetricCount $prop.Value } catch {}
    $shouldPreferFallback = (($metricCountFall -gt 0) -and (
        (-not ($map.ContainsKey($prop.Name))) -or
        ($metricCountPrimary -le 0) -or
        (($quotaCountPrimary -le 0) -and ($quotaCountFall -gt 0))
      ))
    if (($map.ContainsKey($prop.Name))) {
      try {
        if ([string]$map[$prop.Name].provider_id -ne "codex") { continue }
      } catch { continue }
    }
    if ($shouldPreferFallback -or (-not ($map.ContainsKey($prop.Name)))) {
      try {
        [void]$map.Remove($prop.Name)
      } catch {}
      $map[$prop.Name] = $prop.Value
    }
  }
  $sorted = ($map.Keys | Sort-Object)
  $out = New-Object psobject
  foreach ($key in $sorted) {
    $out | Add-Member -NotePropertyName $key -NotePropertyValue $map[$key] -Force
  }
  return $out
}

function Get-MergedUsageReadModelPayload {
  $primaryPath = Join-Path $SpoolDir "read-model-primary.json"
  Write-Utf8NoBom $primaryPath "{}"
  $jsonPrimary = Invoke-ProbeReadJson $primaryPath
  $dataPrimary = $jsonPrimary | ConvertFrom-Json

  if (Test-SnapshotMapNeedsCodexSupplement $dataPrimary.snapshots) {
    try {
      $tw = Get-OpenUsageDataTimeWindow
      $codexPath = Join-Path $SpoolDir "read-model-codex-cli.json"
      $codexObj = @{ time_window = $tw; accounts = @(@{ account_id = "codex-cli"; provider_id = "codex" }) }
      Write-Utf8NoBom $codexPath ($codexObj | ConvertTo-Json -Compress -Depth 8)
      $jsonFb = Invoke-ProbeReadJson $codexPath
      $dataFb = $jsonFb | ConvertFrom-Json
      if ($dataFb -and $dataFb.snapshots) {
        $dataPrimary.snapshots = Merge-LdSnapshotsNode $dataPrimary.snapshots $dataFb.snapshots
      }
    } catch {
      Write-Log "Codex read-model merge skipped: $($_.Exception.Message)"
    }
  }

  return $dataPrimary
}

function Format-QuotaWindowLabel {
  param([AllowNull()][object]$WindowTextOrNull)
  [string]$w = ""
  try { $w = ([string]$WindowTextOrNull).Trim() } catch { $w = "" }
  if ([string]::IsNullOrWhiteSpace($w)) {
    return ""
  }

  [string]$s = $w.ToLowerInvariant()
  if (($s -eq "all") -or ($s -eq "lifetime")) {
    return "total"
  }
  if ($s -match "^pt(?<n>\d+(?:\.\d+)?)h$") {
    return ("{0:g}h" -f [double]$Matches["n"])
  }
  if ($s -match "^pt(?<n>\d+(?:\.\d+)?)m$") {
    $mins = [double]$Matches["n"]
    if (($mins % 60) -eq 0) {
      return ("{0:g}h" -f ($mins / 60.0))
    }
    return ("{0:g}m" -f $mins)
  }
  if ($s -match "^(?<n>\d+(?:\.\d+)?)\s*(?:hour|hours|h)(?:\s+rolling)?$") {
    return ("{0:g}h" -f [double]$Matches["n"])
  }
  if ($s -match "^(?<n>\d+(?:\.\d+)?)\s*(?:minute|minutes|min|m)(?:\s+window)?$") {
    return ("{0:g}m" -f [double]$Matches["n"])
  }
  if ($s -match "^(?<n>\d+)\s*d(?:ay)?s?$") {
    return ("{0}d" -f [int]$Matches["n"])
  }
  if (($s -eq "daily") -or ($s -eq "day")) {
    return "1d"
  }
  if (($s -eq "weekly") -or ($s -eq "week")) {
    return "7d"
  }
  if ($s -eq "billing-cycle") {
    return "cycle"
  }
  if ($s.Length -le 10) {
    return $w
  }
  return $w.Substring(0, 10)
}

function Format-UsageResetShort {
  param([datetime]$ResetUtc)
  try {
    [TimeSpan]$d = ($ResetUtc - (Get-Date).ToUniversalTime())
    if ($d.TotalSeconds -le 0) {
      return "rolling"
    }
    if ($d.TotalMinutes -lt 2) {
      return "~now"
    }
    if ($d.TotalHours -lt 1) {
      return ("~{0}m" -f [Math]::Max(1, [Math]::Ceiling($d.TotalMinutes)))
    }
    if ($d.TotalHours -lt 48) {
      [int]$hrs = [int][Math]::Floor([double]$d.TotalHours)
      [int]$mins = [int]([Math]::Floor([double]$d.TotalMinutes) - ($hrs * 60))
      if ($mins -le 0) {
        return ("~{0}h" -f $hrs)
      }
      return ("~{0}h{1}m" -f $hrs, $mins)
    }
    return ("~{0:0.#}d" -f $d.TotalDays)
  } catch {
    return ""
  }
}

function Convert-ShortDurationLabelToMinutesOrNull {
  param([AllowNull()][object]$TextOrNull)
  [string]$s = ""
  try { $s = ([string]$TextOrNull).Trim().ToLowerInvariant() } catch { $s = "" }
  if ([string]::IsNullOrWhiteSpace($s)) {
    return $null
  }
  $s = $s.TrimStart("~")
  if ($s -match "^(?<h>\d+(?:\.\d+)?)h(?:(?<m>\d+(?:\.\d+)?)m)?$") {
    [double]$mins = ([double]$Matches["h"] * 60.0)
    if ($Matches.ContainsKey("m") -and (-not [string]::IsNullOrWhiteSpace([string]$Matches["m"]))) {
      $mins += [double]$Matches["m"]
    }
    return $mins
  }
  if ($s -match "^(?<m>\d+(?:\.\d+)?)m$") {
    return ([double]$Matches["m"])
  }
  if ($s -match "^(?<d>\d+(?:\.\d+)?)d$") {
    return ([double]$Matches["d"] * 1440.0)
  }
  return $null
}

function Get-ResetShortText {
  param($Snapshot, [string]$MetricName)
  if (-not $Snapshot.resets) {
    return ""
  }

  $names = New-Object System.Collections.Generic.List[string]
  [string]$mn = [string]$MetricName
  foreach ($candidate in @("${mn}_reset", $mn)) {
    if ((-not ([string]::IsNullOrWhiteSpace([string]$candidate))) -and (-not ($names.Contains($candidate)))) {
      [void]$names.Add($candidate)
    }
  }
  if ($mn -eq "quota") {
    [void]$names.Add("quota_reset")
  }
  elseif ($mn -eq "quota_flash") {
    [void]$names.Add("quota_flash_reset")
  }
  elseif ($mn -eq "quota_pro") {
    [void]$names.Add("quota_pro_reset")
  }
  elseif ($mn -in @("plan_percent_used", "plan_api_percent_used", "plan_auto_percent_used")) {
    [void]$names.Add("billing_cycle_end")
  }

  foreach ($name in @( $names.ToArray())) {
    if (-not ($Snapshot.resets.PSObject.Properties.Name -contains $name)) {
      continue
    }
    try {
      return (Format-UsageResetShort (Normalize-UsageResetUtc ([datetime]$Snapshot.resets.$name)))
    } catch {}
  }

  return ""
}

function Format-QuotaBandCaption {
  param($Snapshot, [string]$RawKey, [AllowNull()][object]$WindowTextOrNull, [string]$ResetText = "")
  [string]$k = ""
  try { $k = ([string]$RawKey).Trim().ToLowerInvariant() } catch { $k = "" }

  [string]$model = ""
  if (-not ([string]::IsNullOrWhiteSpace($k)) -and $k.StartsWith("rate_limit_")) {
    [string]$core = $k.Substring("rate_limit_".Length)
    $core = [regex]::Replace($core, "_(?:primary|secondary)$", "")
    if ($Snapshot -and $Snapshot.raw) {
      [string]$rawNameKey = "rate_limit_${core}_name"
      try {
        if ($Snapshot.raw.PSObject.Properties.Name -contains $rawNameKey) {
          $model = ([string]$Snapshot.raw.$rawNameKey).Trim()
        }
      } catch {}
    }
    if ((-not $model) -and $Snapshot -and $Snapshot.attributes) {
      [string]$attrNameKey = "rate_limit_${core}_name"
      try {
        if ($Snapshot.attributes.PSObject.Properties.Name -contains $attrNameKey) {
          $model = ([string]$Snapshot.attributes.$attrNameKey).Trim()
        }
      } catch {}
    }
    if (-not $model) {
      if ($core -in @("primary", "secondary", "code_review")) {
        $model = ""
      } else {
        $model = $core
      }
    }
  }
  elseif ($k.StartsWith("quota_model_")) {
    $model = $k.Substring("quota_model_".Length)
    $model = [regex]::Replace($model, "_(?:requests|tokens|input_tokens|output_tokens|total_tokens)$", "")
  }
  elseif (($k -eq "quota") -or ($k.EndsWith("_quota"))) {
    $model = ""
  }
  elseif ($k.StartsWith("quota_")) {
    $model = $k.Substring("quota_".Length)
  }
  elseif ($k -eq "usage_five_hour") {
    $model = "claude"
    if ([string]::IsNullOrWhiteSpace([string]$WindowTextOrNull)) {
      $WindowTextOrNull = "5h"
    }
  }
  elseif ($k.StartsWith("usage_seven_day")) {
    if ($k -match "sonnet") {
      $model = "sonnet"
    }
    elseif ($k -match "opus") {
      $model = "opus"
    }
    elseif ($k -match "cowork") {
      $model = "team"
    }
    else {
      $model = "claude"
    }
    if ([string]::IsNullOrWhiteSpace([string]$WindowTextOrNull)) {
      $WindowTextOrNull = "7d"
    }
  }
  elseif ($k -eq "plan_percent_used") {
    $model = "plan"
  }
  elseif ($k -eq "plan_api_percent_used") {
    $model = "api"
  }
  elseif ($k -eq "plan_auto_percent_used") {
    $model = "auto"
  }

  $model = ([string]$model).Trim(" _-")
  $model = ($model -replace "_", "-")
  $model = ($model -replace "(?i)\bgemini-(\d+)-(\d+)\b", 'gemini-$1.$2')
  $model = ($model -replace "(?i)-preview\b", "")
  $model = ($model -replace "(?i)-latest\b", "")
  [string]$win = Format-QuotaWindowLabel $WindowTextOrNull
  [string]$reset = ([string]$ResetText).Trim()
  if ((-not [string]::IsNullOrWhiteSpace($win)) -and (-not [string]::IsNullOrWhiteSpace($reset))) {
    $winMinutes = Convert-ShortDurationLabelToMinutesOrNull $win
    $resetMinutes = Convert-ShortDurationLabelToMinutesOrNull $reset
    if (($null -ne $winMinutes) -and ($null -ne $resetMinutes) -and ([Math]::Abs([double]$winMinutes - [double]$resetMinutes) -le 30.0)) {
      if (([double]$winMinutes -ge (18 * 60)) -and ([double]$winMinutes -le (30 * 60))) {
        $win = "1d"
      }
      elseif (([double]$winMinutes -ge (4 * 60)) -and ([double]$winMinutes -le (6 * 60))) {
        $win = "5h"
      }
      elseif (([double]$winMinutes -ge (6 * 24 * 60)) -and ([double]$winMinutes -le (8 * 24 * 60))) {
        $win = "7d"
      }
      else {
        $win = ""
      }
    }
  }

  [string]$label = ""
  if (-not ([string]::IsNullOrWhiteSpace($model)) -and (-not ([string]::IsNullOrWhiteSpace($win)))) {
    $label = "$model $win"
  }
  elseif (-not ([string]::IsNullOrWhiteSpace($model))) {
    $label = $model
  }
  elseif (-not ([string]::IsNullOrWhiteSpace($win))) {
    $label = $win
  }
  else {
    $label = "limit"
  }
  if (-not ([string]::IsNullOrWhiteSpace($reset))) {
    $label = "$label $reset"
  }

  [int]$maxLabelLen = 42
  if ($label.Length -le $maxLabelLen) {
    return $label
  }

  if ((-not ([string]::IsNullOrWhiteSpace($model))) -and (-not ([string]::IsNullOrWhiteSpace($win)))) {
    [int]$tailLen = $win.Length
    if (-not ([string]::IsNullOrWhiteSpace($reset))) {
      $tailLen += (1 + $reset.Length)
    }
    [int]$modelBudget = [Math]::Max(4, $maxLabelLen - $tailLen - 1)
    if ($model.Length -gt $modelBudget) {
      $model = $model.Substring(0, [Math]::Max(1, $modelBudget - 1)) + "."
    }
    if (-not ([string]::IsNullOrWhiteSpace($reset))) {
      return "$model $win $reset"
    }
    return "$model $win"
  }

  return $label.Substring(0, ($maxLabelLen - 1)) + "."
}

function Get-SortHoursForMetricWindow {
  param([AllowNull()][object]$WindowTextOrNull)
  $sRaw = ""
  try { $sRaw = ([string]$WindowTextOrNull).Trim() } catch { $sRaw = "" }
  if (-not $sRaw) {
    return 999999
  }

  $s = $sRaw.ToLowerInvariant()
  if (($s -eq "all") -or ($s -like "*lifetime*")) {
    return 999997
  }

  if ($s -match "pt(?<n>\d+(?:\.\d+)?)h") {
    return [Math]::Min(8760, [Math]::Max(0.01, [double]$Matches["n"]))
  }
  if ($s -match "pt(?<n>\d+(?:\.\d+)?)m") {
    return [Math]::Min(8760, [Math]::Max(0.01, ([double]$Matches["n"]) / 60))
  }

  foreach ($regex in @(
      "(?ix)(?<!\d)(\d+(?:\.\d+)?)\s*(?:hour|hours)\b",
      "(?ix)(?<!\d)(\d+(?:\.\d+)?)\s*h\b",
      "(?ix)\b(?:last\s+)?(?<!\d)(\d+)\s*h\b")) {
    if ($sRaw -match $regex) {
      return [Math]::Min(8760, [Math]::Max(0.01, [double]$Matches[1]))
    }
  }

  if ($s -match "(?ix)(?<!\d)(\d+)\s*(?:minute|minutes|min)\s*(?:window)?") {
    return [Math]::Min(8760, [Math]::Max(0.01, ([double]$Matches[1]) / 60))
  }

  if ($s -match "(?ix)(?<!\d)(\d+)\s*d(?:ay)?s?\b") {
    return [Math]::Min(8760, [Math]::Max(0.05, ([double]$Matches[1]) * 24))
  }

  foreach ($needle in @("5h rolling", "5 hour", "5-hour", "5 hours", "5h ", " five hour")) {
    if ($s -like "*$needle*") {
      return 5
    }
  }

  if ($s -match "\b\d+\/\d+h\b") {
    try {
      $left = ([regex]::Matches($s, "\d+(?=\/)")[0]).Value
      return [Math]::Min(8760, [Math]::Max(0.01, [double]$left))
    } catch {}
  }

  foreach ($guess in @(1, 3, 5, 12, 24, 168, 720, 744)) {
    if (($s -like "*${guess}h*") -or ($s -like "*${guess} hour*")) {
      return [double]$guess
    }
  }

  return 999998
}

function Test-ThroughputOrCallRateMetricKey {
  param([string]$MetricKeyRaw)
  if ([string]::IsNullOrWhiteSpace([string]$MetricKeyRaw)) {
    return $false
  }
  [string]$k = ([string]$MetricKeyRaw).Trim().ToLowerInvariant()
  foreach ($blocked in @("rpm", "tpm", "window_requests")) {
    if ($k -eq $blocked) {
      return $true
    }
  }

  foreach ($needle in @( "calls_per_min", "callsperminute", "tokens_per_min", "_per_min", "_per_minute")) {
    if ($k.IndexOf($needle, [System.StringComparison]::Ordinal) -ge 0) {
      return $true
    }
  }

  if ([regex]::IsMatch([string]$k, '(?xi)(\b|^|\.|_|,)tool.?calls?_?rate|(^|[._\-])calls_rate(?:[._\-]|$)|\btool_calls?_rate\b')) {
    return $true
  }

  return $false
}

function Test-QuotaFamilyMetricKey {
  param([string]$MetricKeyRaw)
  if ([string]::IsNullOrWhiteSpace([string]$MetricKeyRaw)) {
    return $false
  }
  [string]$k = ([string]$MetricKeyRaw).Trim().ToLowerInvariant()
  if ($k -eq "quota") {
    return $true
  }
  if ($k.StartsWith("quota_")) {
    return $true
  }
  if ($k.StartsWith("rate_limit_")) {
    return $true
  }
  if (($k -eq "usage_five_hour") -or ($k.StartsWith("usage_seven_day"))) {
    return $true
  }
  if ($k -in @("plan_percent_used", "plan_api_percent_used", "plan_auto_percent_used")) {
    return $true
  }
  return $k.EndsWith("_quota")
}

function Test-DisplayQuotaMetricKey {
  param($Snapshot, [string]$MetricKeyRaw)
  if (-not (Test-QuotaFamilyMetricKey $MetricKeyRaw)) {
    return $false
  }
  [string]$k = ""
  try { $k = ([string]$MetricKeyRaw).Trim().ToLowerInvariant() } catch { $k = "" }
  [string]$providerKey = ""
  try { $providerKey = ([string]$Snapshot.provider_id).Trim().ToLowerInvariant() } catch { $providerKey = "" }

  if (($providerKey -eq "codex") -and $k.StartsWith("rate_limit_")) {
    [string]$core = $k.Substring("rate_limit_".Length)
    $core = [regex]::Replace($core, "_(?:primary|secondary)$", "")
    if ($core -in @("primary", "secondary", "code_review")) {
      return $true
    }
  }

  if ($k -in @("plan_percent_used", "plan_api_percent_used", "plan_auto_percent_used")) {
    return ($providerKey -eq "cursor")
  }

  return $true
}

function Test-PreferSpecificQuotaModelRows {
  param($Snapshot, [string]$MetricKeyRaw)
  [string]$k = ""
  try { $k = ([string]$MetricKeyRaw).Trim().ToLowerInvariant() } catch { $k = "" }
  if ($k -notin @("quota", "quota_pro", "quota_flash")) {
    return $false
  }
  try {
    if (-not $Snapshot.metrics) {
      return $false
    }
    foreach ($pn in $Snapshot.metrics.PSObject.Properties.Name) {
      [string]$mk = ([string]$pn).Trim().ToLowerInvariant()
      if ($mk.StartsWith("quota_model_")) {
        return $true
      }
    }
  } catch {}
  return $false
}

function Get-RemainingQuotaPercentFromMetric {
  param([AllowNull()]$M, [string]$MetricKey = "")
  if (-not $M) {
    return $null
  }

  if (Test-ThroughputOrCallRateMetricKey ([string]$MetricKey)) {
    return $null
  }

  $unit = ""
  try { $unit = ([string]$M.unit).Trim() } catch { $unit = "" }

  $used = $null
  try {
    if (($M.PSObject.Properties.Name -contains "used") -and ($null -ne $M.used)) {
      $used = [double]$M.used
    }
  } catch { $used = $null }
  $limit = $null
  try {
    if (($M.PSObject.Properties.Name -contains "limit") -and ($null -ne $M.limit)) {
      $limit = [double]$M.limit
    }
  } catch { $limit = $null }
  $rem = $null
  try {
    if (($M.PSObject.Properties.Name -contains "remaining") -and ($null -ne $M.remaining)) {
      $rem = [double]$M.remaining
    }
  } catch { $rem = $null }

  $pct = $null
  if (($unit -eq "%") -and ($rem -ne $null)) {
    $pct = [double]$rem
  }
  elseif (($unit -eq "%") -and ($used -ne $null)) {
    $pct = 100.0 - [double]$used
  }
  elseif (($limit -ne $null) -and ($rem -ne $null) -and ([double]$limit -gt 0)) {
    $pct = 100.0 * [double]$rem / [double]$limit
  }
  elseif (($limit -ne $null) -and ($used -ne $null) -and ([double]$limit -gt 0)) {
    $pct = 100.0 - (100.0 * [double]$used / [double]$limit)
  }

  if ($pct -eq $null) {
    return $null
  }
  return ([Math]::Min(100.0, [Math]::Max(0.0, [double]$pct)))
}

function Get-CodexEmptyWindowSortHoursGuess {
  param([string]$MetricKeyRaw)
  [string]$k = ([string]$MetricKeyRaw).Trim().ToLowerInvariant()
  if ($k -eq "rate_limit_primary") {
    return 5.0
  }
  if ($k -eq "rate_limit_secondary") {
    return 168.0
  }
  if ($k.StartsWith("rate_limit_code_review")) {
    if ($k.EndsWith("_primary")) {
      return 12.0
    }
    if ($k.EndsWith("_secondary")) {
      return 168.0
    }
    return 12.0
  }
  if (($k.StartsWith("rate_limit_")) -and ($k.EndsWith("_primary"))) {
    return 5.0
  }
  if (($k.StartsWith("rate_limit_")) -and ($k.EndsWith("_secondary"))) {
    return 168.0
  }
  return $null
}

function Optimize-CodexBandWindowPlaceholder {
  param([string]$ProviderId, [string]$MetricKey, [string]$WindowFromMetric)
  try {
    if (([string]$ProviderId).Trim().ToLowerInvariant() -ne "codex") {
      try {
        return (([string]$WindowFromMetric).Trim())
      } catch {
        return ""
      }
    }
  } catch {
    try { return ([string]$WindowFromMetric).Trim() } catch { return "" }
  }

  [string]$w = ""
  try { $w = ([string]$WindowFromMetric).Trim() } catch { $w = "" }
  if (-not ([string]::IsNullOrWhiteSpace($w))) {
    return $w
  }
  try {
    $guess = Get-CodexEmptyWindowSortHoursGuess ([string]$MetricKey)
    if (($null -ne $guess) -and ([double]$guess -gt 0.01) -and ([double]$guess -lt 720)) {
      [int]$g = [Math]::Max(1, [int][Math]::Round([double]$guess))
      return ("~{0}h" -f $g)
    }
  } catch {}

  return $w
}

function Get-LdRibbonSortHoursForBand {
  param([string]$ProviderId, [string]$MetricKey, [AllowNull()][object]$WindowTextOrNull)

  try {
    $wk = ([string]$MetricKey).Trim()
  } catch {
    $wk = ""
  }
  try {
    $providerKey = ([string]$ProviderId).Trim().ToLowerInvariant()
  } catch {
    $providerKey = ""
  }

  [double]$h = Get-SortHoursForMetricWindow $WindowTextOrNull
  [bool]$missingLabel = $true
  try {
    [string]$wProbe = ([string]$WindowTextOrNull).Trim()
    $missingLabel = [string]::IsNullOrWhiteSpace($wProbe)
  } catch {
    $missingLabel = $true
  }

  if (($providerKey -eq "codex") -and ($wk.StartsWith("rate_limit_")) -and ($missingLabel -or ($h -ge 999998))) {
    $guess = Get-CodexEmptyWindowSortHoursGuess $wk
    if (($null -ne $guess) -and ([double]$guess -gt 0)) {
      return ([double]$guess)
    }
  }

  return [double]$h
}

function Get-RibbonMeterSortTierLimitedDock {
  param([string]$ProviderId, [string]$MetricKey)
  try {
    if (([string]$ProviderId).Trim().ToLowerInvariant() -ne "codex") {
      return 0
    }
  } catch {
    return 0
  }
  try {
    $mk = ([string]$MetricKey).Trim().ToLowerInvariant()
  } catch {
    return 10000
  }
  if ($mk -eq "rate_limit_primary") {
    return 5
  }
  if ($mk -eq "rate_limit_secondary") {
    return 22
  }
  if ($mk.StartsWith("rate_limit_code_review")) {
    if ($mk.EndsWith("_secondary")) {
      return 42
    }
    return 30
  }
  if (($mk.StartsWith("rate_limit_")) -and ($mk.EndsWith("_primary"))) {
    return 38
  }
  if (($mk.StartsWith("rate_limit_")) -and ($mk.EndsWith("_secondary"))) {
    return 45
  }
  if ($mk.StartsWith("rate_limit_")) {
    return 55
  }
  if (($mk.StartsWith("plan_")) -or ($mk.StartsWith("composer_"))) {
    return 210
  }
  return 5000
}

function Get-HiddenQuotaBandSet {
  param([string]$SnapshotKey)
  if (-not ($script:QuotaBandHidden -is [hashtable])) {
    $script:QuotaBandHidden = @{}
  }
  [string]$sk = ([string]$SnapshotKey).Trim()
  if ([string]::IsNullOrWhiteSpace($sk)) {
    $sk = "__default"
  }
  if (-not ($script:QuotaBandHidden.ContainsKey($sk))) {
    $script:QuotaBandHidden[$sk] = @{}
  }
  return $script:QuotaBandHidden[$sk]
}

function Collect-BandsFromMetrics {
  param($Snapshot, [int]$Cap, [string]$BandRotateKey = "", [bool]$IncludeHidden = $false)
  $rows = New-Object System.Collections.Generic.List[hashtable]

  [string]$provSortId = ""
  try { $provSortId = [string]$Snapshot.provider_id } catch { $provSortId = "" }

  if (-not $Snapshot.metrics) {
    return @()
  }

  $priority = @(
    "rate_limit_primary",
    "usage_five_hour",
    "usage_seven_day",
    "usage_seven_day_sonnet",
    "usage_seven_day_opus",
    "usage_seven_day_cowork",
    "quota",
    "quota_pro",
    "quota_flash",
    "plan_percent_used",
    "plan_api_percent_used",
    "plan_auto_percent_used",
    "rate_limit_secondary",
    "rate_limit_code_review_primary",
    "rate_limit_code_review_secondary",
    "completions_quota",
    "chat_quota"
  )

  if ($Snapshot.metrics) {
    foreach ($name in $priority) {
      if (-not ($Snapshot.metrics.PSObject.Properties.Name -contains $name)) {
        continue
      }
      if (-not (Test-DisplayQuotaMetricKey $Snapshot ([string]$name))) {
        continue
      }
      if (Test-PreferSpecificQuotaModelRows $Snapshot ([string]$name)) {
        continue
      }
      if (Test-ThroughputOrCallRateMetricKey ([string]$name)) {
        continue
      }
      $mObj = $Snapshot.metrics.$name
      $pct = Get-RemainingQuotaPercentFromMetric $mObj ([string]$name)
      if ($pct -eq $null) { continue }
      $pClamped = [Math]::Min(100.0, [Math]::Max(0.0, [double]$pct))
      [string]$winDispPri = ""
      try { $winDispPri = [string]$mObj.window } catch { $winDispPri = "" }
      $winDispPri = Optimize-CodexBandWindowPlaceholder $provSortId $name $winDispPri
      [string]$resetShortPri = Get-ResetShortText $Snapshot $name
      $rows.Add(@{
        Key           = $name
        Caption       = (Format-QuotaBandCaption $Snapshot $name $winDispPri $resetShortPri)
        Percent       = $pClamped
        Unit          = [string]$mObj.unit
        Window        = $winDispPri
        Reset         = $resetShortPri
        DisplayDetail = ""
      })
    }

    foreach ($prop in $Snapshot.metrics.PSObject.Properties) {
      $already = $false
      foreach ($r in $rows) {
        if ($r.Key -eq $prop.Name) {
          $already = $true
          break
        }
      }
      if ($already) { continue }

      if (-not (Test-DisplayQuotaMetricKey $Snapshot ([string]$prop.Name))) {
        continue
      }
      if (Test-PreferSpecificQuotaModelRows $Snapshot ([string]$prop.Name)) {
        continue
      }

      if (Test-ThroughputOrCallRateMetricKey ([string]$prop.Name)) {
        continue
      }

      $mObj = $prop.Value
      $pct = Get-RemainingQuotaPercentFromMetric $mObj ([string]$prop.Name)
      if ($pct -eq $null) {
        continue
      }
      $pClamped = [Math]::Min(100.0, [Math]::Max(0.0, [double]$pct))
      [string]$winDispMx = ""
      try { $winDispMx = [string]$mObj.window } catch { $winDispMx = "" }
      $winDispMx = Optimize-CodexBandWindowPlaceholder $provSortId ([string]$prop.Name) $winDispMx
      [string]$resetShortMx = Get-ResetShortText $Snapshot ([string]$prop.Name)
      $rows.Add(@{
        Key           = $prop.Name
        Caption       = (Format-QuotaBandCaption $Snapshot ([string]$prop.Name) $winDispMx $resetShortMx)
        Percent       = $pClamped
        Unit          = [string]$mObj.unit
        Window        = $winDispMx
        Reset         = $resetShortMx
        DisplayDetail = ""
      })
    }
  }

  # Codex prioritizes Codex PTE rate_limit_* headline ahead of synthesized plan_* rows; others still sort by inferred hours/%/key.
  $withPct = @(
    ($rows.ToArray()) |
      Where-Object { $_.Percent -ne $null } |
      Sort-Object `
        @{ Expression = { Get-RibbonMeterSortTierLimitedDock $provSortId ([string]$_.Key) }; Ascending = $true }, `
        @{ Expression = { Get-LdRibbonSortHoursForBand $provSortId ([string]$_.Key) $_.Window }; Ascending = $true }, `
        @{ Expression = { [double]$_.Percent }; Ascending = $true }, `
        @{ Expression = { [string]$_.Key }; Ascending = $true }
  )

  $textOnly = @(($rows.ToArray()) | Where-Object { $_.Percent -eq $null })
  if (-not $IncludeHidden) {
    $hiddenSet = Get-HiddenQuotaBandSet $BandRotateKey
    if ($hiddenSet.Count -gt 0) {
      $withPct = @( $withPct | Where-Object { -not ($hiddenSet.ContainsKey([string]$_.Key)) } )
    }
  }
  $exhausted = @( $withPct | Where-Object { [double]$_.Percent -le 0.0001 } )
  $rotatable = @( $withPct | Where-Object { [double]$_.Percent -gt 0.0001 } )
  $rotatePool = $rotatable
  [bool]$pinExhausted = ($exhausted.Count -lt $Cap)
  if (-not $pinExhausted) {
    $rotatePool = $exhausted
  }
  if (($rotatePool.Count -gt 1) -and ($Cap -gt 0)) {
    $rk = ""
    try { $rk = ([string]$BandRotateKey).Trim() } catch { $rk = "" }
    [int]$off = 0
    if (($rk.Length -gt 0) -and ($script:QuotaBandOffsets -is [hashtable]) -and ($script:QuotaBandOffsets.ContainsKey($rk))) {
      try { $off = [int]$script:QuotaBandOffsets[$rk] } catch { $off = 0 }
    }
    $off = (($off % $rotatePool.Count) + $rotatePool.Count) % $rotatePool.Count
    if ($off -gt 0) {
      $rotated = New-Object System.Collections.Generic.List[object]
      for ($ri = 0; $ri -lt $rotatePool.Count; $ri++) {
        [void]$rotated.Add($rotatePool[($ri + $off) % $rotatePool.Count])
      }
      $rotatePool = @($rotated.ToArray())
    }
  }
  if ($pinExhausted) {
    $withPct = @($exhausted + $rotatePool)
  } else {
    $withPct = @($rotatePool + $rotatable)
  }

  $combined = New-Object System.Collections.Generic.List[object]
  foreach ($w in $withPct) {
    if ($combined.Count -ge $Cap) {
      break
    }
    [void]$combined.Add($w)
  }
  foreach ($w in $textOnly) {
    if ($combined.Count -ge $Cap) {
      break
    }
    [void]$combined.Add($w)
  }

  return @($combined.ToArray())
}

function Count-RemainingQuotaBandsInMetrics {
  param($Snapshot)
  if (-not $Snapshot.metrics) {
    return 0
  }
  [int]$n = 0
  foreach ($prop in $Snapshot.metrics.PSObject.Properties) {
    [string]$kn = [string]$prop.Name
    if (-not (Test-DisplayQuotaMetricKey $Snapshot $kn)) {
      continue
    }
    if (Test-PreferSpecificQuotaModelRows $Snapshot $kn) {
      continue
    }
    if (Test-ThroughputOrCallRateMetricKey $kn) {
      continue
    }
    if ((Get-RemainingQuotaPercentFromMetric $prop.Value $kn) -ne $null) {
      $n++
    }
  }
  return $n
}

function New-UsageGaugePanel {
  param([int]$GaugeWidthPx, [int]$GaugeHeightPx, [AllowNull()]$PercentValueOrNull, [AllowNull()]$AttachedCardOrNull)

  function New-GaugePlaceholderTag([AllowNull()]$Cc) {
    $h = @{}
    if ($null -ne $Cc) { $h.Card = $Cc }
    return $h
  }

  $warnPct = [int]$script:Settings.gaugeWarnPercent
  if (-not $warnPct) { $warnPct = 72 }
  $critPct = [int]$script:Settings.gaugeCritPercent
  if (-not $critPct) { $critPct = 90 }
  $panel = New-Object System.Windows.Forms.Panel
  $mh = [Math]::Max(12, [Math]::Min(22, [int]$GaugeHeightPx))
  $mw = [Math]::Max(40, [int]$GaugeWidthPx)
  $panel.Width = $mw
  $panel.Height = $mh
  $panel.Margin = New-Object System.Windows.Forms.Padding(4, 0, 4, 0)
  try { $panel.MinimumSize = New-Object System.Drawing.Size $mw, $mh } catch {}
  try { $panel.DoubleBuffered = $true } catch {}

  if ($null -eq $PercentValueOrNull) {
    $panel.Tag = (New-GaugePlaceholderTag $AttachedCardOrNull)
    return $panel
  }

  $remaining = [double]$PercentValueOrNull
  if ([double]::IsNaN($remaining)) {
    $panel.Tag = (New-GaugePlaceholderTag $AttachedCardOrNull)
    return $panel
  }

  $remaining = [Math]::Min(100.0, [Math]::Max(0.0, $remaining))
  $usedPctForTint = 100.0 - $remaining
  $ratio = $remaining / 100.0
  $panel.Tag = @{
    FillRatio       = [double]$ratio
    WarnThreshold   = [double]$warnPct
    CritThreshold   = [double]$critPct
    ConsumptionPct  = [double]$usedPctForTint
    Card            = $AttachedCardOrNull
  }

  $panel.Add_Paint({
    param($snd, $paintArgs)
    $edge = $null; $fgBrush = $null; $trBrush = $null
    try {
      $g = $paintArgs.Graphics
      $p = [System.Windows.Forms.Panel]$snd
      $bucket = $p.Tag
      if ((-not ($bucket -is [hashtable])) -or (-not ($bucket.ContainsKey("FillRatio")))) {
        return
      }

      $wi = [int]([Math]::Max(1.0, [double]$p.ClientSize.Width - 2))
      $hi = [int]([Math]::Max(1.0, [double]$p.ClientSize.Height - 2))
      $backRect = New-Object System.Drawing.Rectangle 1, 1, $wi, $hi
      $trBrush = New-Object System.Drawing.SolidBrush $script:Theme.GaugeTrack
      $g.FillRectangle($trBrush, $backRect)

      $r = [double]$bucket["FillRatio"]
      if ([double]::IsNaN($r)) { return }
      if ($r -lt 0) { $r = 0 } elseif ($r -gt 1) { $r = 1 }

      [int]$fillPx = [int]([Math]::Floor($wi * $r))
      if ($fillPx -gt $wi) {
        $fillPx = $wi
      }

      $cPct = [double]$bucket["ConsumptionPct"]
      $fillClr = $script:Theme.GaugeOk
      $wThr = [double]$bucket["WarnThreshold"]
      $cThr = [double]$bucket["CritThreshold"]
      if ($cPct -ge $cThr) { $fillClr = $script:Theme.GaugeCrit }
      elseif ($cPct -ge $wThr) { $fillClr = $script:Theme.GaugeWarn }

      $fgBrush = New-Object System.Drawing.SolidBrush $fillClr
      if ($fillPx -gt 0) {
        $fillRect = New-Object System.Drawing.Rectangle 1, 1, $fillPx, $hi
        $g.FillRectangle($fgBrush, $fillRect)
      }

      $edge = New-Object System.Drawing.Pen ([System.Drawing.Color]::FromArgb(110, $script:Theme.Fore.R, $script:Theme.Fore.G, $script:Theme.Fore.B)), 1
      $g.DrawRectangle($edge, $backRect)
    } finally {
      if ($edge) { try { $edge.Dispose() } catch {} }
      if ($fgBrush) { try { $fgBrush.Dispose() } catch {} }
      if ($trBrush) { try { $trBrush.Dispose() } catch {} }
    }
  })

  try { $panel.Invalidate($true); $panel.Refresh() } catch {}
  return $panel
}

function New-PinBitmap {
  param([bool]$Pinned)
  $bit = New-Object System.Drawing.Bitmap 20, 20
  $g = [System.Drawing.Graphics]::FromImage($bit)
  $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
  $g.Clear([System.Drawing.Color]::Transparent)
  $pinBrush = New-Object System.Drawing.SolidBrush $script:Theme.Fore
  $accentBrush = New-Object System.Drawing.SolidBrush $script:Theme.StatusAccent
  $mutedPen = New-Object System.Drawing.Pen $script:Theme.MutedFore, ([float]2.0)
  $pinPen = New-Object System.Drawing.Pen $script:Theme.Fore, ([float]1.7)
  if ($Pinned) {
    $g.FillRectangle($accentBrush, 6, 2, 8, 4)
    $g.FillPolygon($pinBrush, @(
        (New-Object System.Drawing.Point 5, 7),
        (New-Object System.Drawing.Point 15, 7),
        (New-Object System.Drawing.Point 12, 12),
        (New-Object System.Drawing.Point 8, 12)
      ))
    $g.DrawLine($pinPen, 10, 11, 10, 18)
  } else {
    $g.TranslateTransform(10, 10)
    $g.RotateTransform(-35)
    $g.TranslateTransform(-10, -10)
    $g.FillRectangle($pinBrush, 6, 2, 8, 4)
    $g.FillPolygon($pinBrush, @(
        (New-Object System.Drawing.Point 5, 7),
        (New-Object System.Drawing.Point 15, 7),
        (New-Object System.Drawing.Point 12, 12),
        (New-Object System.Drawing.Point 8, 12)
      ))
    $g.DrawLine($pinPen, 10, 11, 10, 18)
    $g.ResetTransform()
    $g.DrawLine($mutedPen, 3, 17, 17, 3)
  }
  $pinBrush.Dispose(); $accentBrush.Dispose(); $mutedPen.Dispose(); $pinPen.Dispose(); $g.Dispose()
  return $bit
}

function New-GearBitmapSmall {
  $size = 15
  $bitmap = New-Object System.Drawing.Bitmap $size, $size
  $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
  $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
  $graphics.Clear([System.Drawing.Color]::Transparent)
  $pen = New-Object System.Drawing.Pen $script:Theme.MutedFore, ([float]1.65)
  $brush = New-Object System.Drawing.SolidBrush $script:Theme.MutedFore
  $c = [Math]::Floor($size / 2)
  $rOuter = [float]([Math]::Max(3, $size / 2 - 1))
  $scaleTips = [float]0.55
  for ($i = 0; $i -lt 8; $i++) {
    $angle = ($i * 45) * [Math]::PI / 180
    $x1 = [float]$c + [Math]::Cos($angle) * ($rOuter * $scaleTips)
    $y1 = [float]$c + [Math]::Sin($angle) * ($rOuter * $scaleTips)
    $x2 = [float]$c + [Math]::Cos($angle) * $rOuter
    $y2 = [float]$c + [Math]::Sin($angle) * $rOuter
    $graphics.DrawLine($pen, $x1, $y1, $x2, $y2)
  }
  $w = $rOuter * 2 * $scaleTips
  $half = [float]$w / [float]2
  $cf = [float]$c
  $graphics.DrawEllipse($pen, $cf - $half, $cf - $half, $w, $w)
  $rInner = [Math]::Max([float]1, [float]$c / [float]4.5)
  $graphics.FillEllipse($brush, $c - $rInner, $c - $rInner, $rInner * 2, $rInner * 2)
  $pen.Dispose(); $brush.Dispose(); $graphics.Dispose()
  return $bitmap
}

function New-LogoBitmap {
  param([string]$Name)

  $bitmap = New-Object System.Drawing.Bitmap 20, 20
  $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
  $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
  $graphics.Clear([System.Drawing.Color]::Transparent)

  $black = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(24, 24, 24))
  $white = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::White)
  $font = New-Object System.Drawing.Font("Segoe UI", 8, [System.Drawing.FontStyle]::Bold)
  $format = New-Object System.Drawing.StringFormat
  $format.Alignment = [System.Drawing.StringAlignment]::Center
  $format.LineAlignment = [System.Drawing.StringAlignment]::Center
  $rect = New-Object System.Drawing.RectangleF 0, 0, 20, 20

  $handled = $false
  switch ($Name) {
    "Cursor" {
      $graphics.FillRectangle($black, 1, 1, 18, 18)
      $points = @(
        (New-Object System.Drawing.Point 5, 3),
        (New-Object System.Drawing.Point 16, 10),
        (New-Object System.Drawing.Point 11, 11),
        (New-Object System.Drawing.Point 14, 17),
        (New-Object System.Drawing.Point 11, 18),
        (New-Object System.Drawing.Point 8, 12),
        (New-Object System.Drawing.Point 5, 15)
      )
      $graphics.FillPolygon($white, $points)
      $handled = $true
    }
    "Gemini" {
      $blue = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(94, 114, 255))
      $graphics.FillEllipse($blue, 1, 1, 18, 18)
      $points = @(
        (New-Object System.Drawing.Point 10, 3),
        (New-Object System.Drawing.Point 12, 8),
        (New-Object System.Drawing.Point 17, 10),
        (New-Object System.Drawing.Point 12, 12),
        (New-Object System.Drawing.Point 10, 17),
        (New-Object System.Drawing.Point 8, 12),
        (New-Object System.Drawing.Point 3, 10),
        (New-Object System.Drawing.Point 8, 8)
      )
      $graphics.FillPolygon($white, $points)
      $blue.Dispose()
      $handled = $true
    }
    "Codex" {
      $green = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(16, 163, 127))
      $graphics.FillEllipse($green, 1, 1, 18, 18)
      $graphics.DrawString("O", $font, $white, $rect, $format)
      $green.Dispose()
      $handled = $true
    }
    "Antigravity" {
      # Indigo orbital ring with a small arrow/comet inside.
      $indigo = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(63, 81, 181))
      $graphics.FillEllipse($indigo, 1, 1, 18, 18)
      $ringPen = New-Object System.Drawing.Pen ([System.Drawing.Color]::FromArgb(220, 226, 255)), 1.4
      $graphics.DrawEllipse($ringPen, 4, 6, 12, 8)
      # Upward chevron (anti-gravity arrow)
      $points = @(
        (New-Object System.Drawing.Point 10, 5),
        (New-Object System.Drawing.Point 14, 12),
        (New-Object System.Drawing.Point 11, 12),
        (New-Object System.Drawing.Point 11, 16),
        (New-Object System.Drawing.Point 9, 16),
        (New-Object System.Drawing.Point 9, 12),
        (New-Object System.Drawing.Point 6, 12)
      )
      $graphics.FillPolygon($white, $points)
      $ringPen.Dispose()
      $indigo.Dispose()
      $handled = $true
    }
  }

  if (-not $handled) {
    $iconPath = Join-Path $IconDir "$Name.png"
    if (Test-Path $iconPath) {
      $circleBrush = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(248, 248, 248))
      $graphics.FillEllipse($circleBrush, 0, 0, 20, 20)
      $circleBrush.Dispose()
      try {
        $stream = [System.IO.File]::OpenRead($iconPath)
        try {
          $loaded = [System.Drawing.Image]::FromStream($stream)
          $graphics.DrawImage($loaded, 3, 3, 14, 14)
          $loaded.Dispose()
          $handled = $true
        } finally {
          $stream.Dispose()
        }
      } catch {
        Write-Log "Failed to load icon ${Name}: $($_.Exception.Message)"
      }
    }
  }

  if (-not $handled) {
    $graphics.FillEllipse($black, 1, 1, 18, 18)
    $initial = "?"
    if ($Name) { $initial = $Name.Substring(0, 1).ToUpperInvariant() }
    $graphics.DrawString($initial, $font, $white, $rect, $format)
  }

  $black.Dispose()
  $white.Dispose()
  $font.Dispose()
  $format.Dispose()
  $graphics.Dispose()
  return $bitmap
}

function Convert-SnapshotToCard {
  param([string]$Key, $Snapshot)

  $warnLine = [int]$script:Settings.gaugeWarnPercent
  if (-not $warnLine) { $warnLine = 72 }
  $critLine = [int]$script:Settings.gaugeCritPercent
  if (-not $critLine) { $critLine = 90 }
  $gaugeCap = [int]$script:Settings.gaugeMaxBands
  if (-not $gaugeCap) { $gaugeCap = [int]$script:DockRibbonGaugeCap }
  if ($gaugeCap -lt 1) {
    $gaugeCap = 1
  }
  if ($gaugeCap -gt [int]$script:DockRibbonGaugeCap) {
    $gaugeCap = [int]$script:DockRibbonGaugeCap
  }

  $providerId = [string]$Snapshot.provider_id
  $nameMap = @{
    "codex" = "Codex"
    "cursor" = "Cursor"
    "gemini_cli" = "Gemini"
    "claude_code" = "Claude"
    "antigravity" = "Antigravity"
    "copilot" = "Copilot"
  }
  $name = $providerId
  if ($nameMap.ContainsKey($providerId)) {
    $name = $nameMap[$providerId]
  } elseif ($Key) {
    $name = ($Key -replace "-", " ")
  }

  [int]$quotaBandCount = Count-RemainingQuotaBandsInMetrics $Snapshot
  $bands = @(Collect-BandsFromMetrics $Snapshot $gaugeCap ([string]$Key))
  $allBands = @(Collect-BandsFromMetrics $Snapshot 128 ([string]$Key) $true)
  $lowestRemaining = [double]100
  foreach ($segment in ($bands | Where-Object { $_.Percent -ne $null })) {
    if ([double]$segment.Percent -lt $lowestRemaining) {
      $lowestRemaining = [double]$segment.Percent
    }
  }

  $pctBandList = @( $bands | Where-Object { $_.Percent -ne $null } )

  $headBand = $null
  if ($pctBandList.Count -gt 0) {
    $headBand = $pctBandList[0]
  }

  $metric = $null
  $metricName = ""
  $window = ""

  if ($headBand) {
    try { $metricName = [string]$headBand.Key } catch { $metricName = "" }
    try {
      $window = ([string]$headBand.Window).Trim()
    } catch {
      $window = ""
    }
    if ($Snapshot.metrics -and $metricName -and ($Snapshot.metrics.PSObject.Properties.Name -contains $metricName)) {
      $metric = $Snapshot.metrics.$metricName
      try {
        $w2 = ([string]$metric.window).Trim()
        if ($w2) { $window = $w2 }
      } catch {}
    }
  }

  if (-not $metric -and $Snapshot.metrics) {
    $fallback = Get-MetricValue $Snapshot.metrics @(
      "rate_limit_primary",
      "usage_five_hour",
      "usage_seven_day",
      "usage_seven_day_sonnet",
      "usage_seven_day_opus",
      "quota",
      "quota_pro",
      "quota_flash",
      "plan_percent_used",
      "plan_api_percent_used",
      "plan_auto_percent_used",
      "rate_limit_secondary",
      "rate_limit_code_review_primary",
      "rate_limit_code_review_secondary",
      "completions_quota",
      "chat_quota"
    )
    if ($fallback) {
      $metricName = [string]$fallback.Name
      $metric = $fallback.Metric
      $window = ""
      try { $window = ([string]$metric.window).Trim() } catch {}
    }
  }

  $percent = $null
  if ($metric) {
    $percent = Get-RemainingQuotaPercentFromMetric $metric ([string]$metricName)
  }

  $primaryPct = $null
  if ($headBand) {
    try { $primaryPct = [double]$headBand.Percent } catch { $primaryPct = $null }
  }
  elseif ($percent -ne $null) {
    $primaryPct = [double]$percent
  }

  $metered = ($pctBandList.Count -gt 0) -or ($percent -ne $null)
  $levelPct = 100.0 - [double]$lowestRemaining
  if (($bands | Where-Object { $_.Percent -ne $null }).Count -le 0 -and $percent -ne $null) {
    $levelPct = 100.0 - [double]$percent
  }

  $level = "ok"
  if (-not $metered) {
    try {
      if ([string]$Snapshot.status -ne "" -and [string]$Snapshot.status -ne "OK") {
        $level = "status"
      }
    } catch {}
  } elseif ($levelPct -ge [double]$critLine) {
    $level = "critical"
  } elseif ($levelPct -ge [double]$warnLine) {
    $level = "warn"
  }

  $main = "Loading"
  $loadingMain = $true
  if ($primaryPct -ne $null) {
    $main = "{0:0.#}%" -f [double]$primaryPct
    $loadingMain = $false
  } elseif ($bands.Count -gt 0 -and $bands[0].Percent -ne $null) {
    $main = "{0:0.#}%" -f [double]$bands[0].Percent
    $loadingMain = $false
  } elseif ($percent -ne $null) {
    $main = "{0:0.#}%" -f [double]$percent
    $loadingMain = $false
  }

  if ($loadingMain) {
    $level = "status"
  }

  $resetText = Get-ResetText $Snapshot $metricName
  $detailParts = @()
  if ($window) { $detailParts += $window }
  if ($resetText) { $detailParts += $resetText }
  if ($loadingMain -and $detailParts.Count -eq 0) {
    $detailParts += "waiting for telemetry"
  }

  # Flatten gauges for serialization on the PSCustomObject
  $bandsOut = @()
  foreach ($b in $bands) {
    $bandsOut += ([pscustomobject]@{
      Key           = $b.Key
      Caption       = $b.Caption
      Percent       = $b.Percent
      Unit          = $b.Unit
      Window        = $b.Window
      Reset         = $b.Reset
      DisplayDetail = $b.DisplayDetail
    })
  }

  $allBandsOut = @()
  foreach ($b in $allBands) {
    $allBandsOut += ([pscustomobject]@{
      Key           = $b.Key
      Caption       = $b.Caption
      Percent       = $b.Percent
      Unit          = $b.Unit
      Window        = $b.Window
      Reset         = $b.Reset
      DisplayDetail = $b.DisplayDetail
    })
  }

  $messageText = $Snapshot.message
  if ((-not $messageText) -and $loadingMain) {
    $messageText = "Waiting for OpenUsage telemetry."
  }

  return [pscustomobject]@{
    Name = (Convert-ToDisplayText $name)
    Main = (Convert-ToDisplayText $main)
    Detail = (Convert-ToDisplayText ($detailParts -join " | "))
    Level = $level
    Bands = @($bandsOut)
    AllBands = @($allBandsOut)
    Message = (Convert-ToDisplayText $messageText)
    SnapshotKey = [string]$Key
    ProviderId = [string]$providerId
    QuotaRotateCue = ([int]$quotaBandCount -gt [int]$gaugeCap)
  }
}

function Get-AntigravityCard {
  if ($script:Settings.antigravity -and $script:Settings.antigravity.enabled -eq $false) {
    return $null
  }

  $userHome = [Environment]::GetFolderPath("UserProfile")
  $localAppData = [Environment]::GetFolderPath("LocalApplicationData")
  $appDataRoaming = [Environment]::GetFolderPath("ApplicationData")

  $agRoot = Join-Path $userHome ".gemini\antigravity"
  if ($script:Settings.antigravity -and (-not ([string]::IsNullOrWhiteSpace([string]$script:Settings.antigravity.dataDir)))) {
    $dd = ([string]$script:Settings.antigravity.dataDir).Trim()
    if ($dd.Length -gt 0) {
      $agRoot = $dd
    }
  }

  $binarySource = ""
  $binConfigured = ""
  if ($script:Settings.antigravity -and (-not ([string]::IsNullOrWhiteSpace([string]$script:Settings.antigravity.binaryPath)))) {
    $binConfigured = ([string]$script:Settings.antigravity.binaryPath).Trim()
    if ($binConfigured -and (Test-Path -LiteralPath $binConfigured)) {
      $binarySource = $binConfigured
    }
  }
  if (-not $binarySource) {
    $cmd = Get-Command antigravity -ErrorAction SilentlyContinue
    if ($cmd) {
      $binarySource = [string]$cmd.Source
    }
  }

  $faces = New-Object System.Collections.Generic.List[string]
  $probeRows = New-Object System.Collections.Generic.List[string]

  function Add-FaceUnique([string]$Label) {
    if ([string]::IsNullOrWhiteSpace($Label)) { return }
    foreach ($fx in @( $faces.ToArray() )) {
      if ([string]::Equals([string]$fx, $Label, [System.StringComparison]::OrdinalIgnoreCase)) {
        return
      }
    }
    [void]$faces.Add($Label)
  }

  function Summarize-Conversations([string]$Title, [string]$RootDir) {
    if (-not (Test-Path -LiteralPath $RootDir)) { return }

    if ($Title -match "(?i)claude") {
      Add-FaceUnique "Claude"
    }
    elseif ($Title -match "(?i)gemini") {
      Add-FaceUnique "Gemini"
    }
    $conv = Join-Path $RootDir "conversations"
    if (-not (Test-Path -LiteralPath $conv)) {
      [void]$probeRows.Add(("{0}: workspace present (no conversations folder)" -f $Title))
      return
    }

    try {
      $files = @(Get-ChildItem -LiteralPath $conv -File -ErrorAction SilentlyContinue)
    } catch {
      $files = @()
    }

    if ($files.Count -le 0) {
      [void]$probeRows.Add(("{0}: conversations folder empty" -f $Title))
      return
    }

    $latest = @( $files | Sort-Object LastWriteTime -Descending | Select-Object -First 1 )
    [void]$probeRows.Add(("{0}: {1} transcripts, touched {2:MM/dd HH:mm}" -f $Title, $files.Count, $latest[0].LastWriteTime))
  }

  function Mark-PresenceWorkspace([string]$Title, [string]$RootDir) {
    if (-not (Test-Path -LiteralPath $RootDir)) { return }

    if ($Title -match "(?i)claude") {
      Add-FaceUnique "Claude"
    }
    elseif ($Title -match "(?i)gemini") {
      Add-FaceUnique "Gemini"
    }

    try {
      $peek = @(Get-ChildItem -LiteralPath $RootDir -Force -Depth 1 -ErrorAction SilentlyContinue)
    } catch {
      $peek = @()
    }

    if ($peek.Count -eq 0) {
      [void]$probeRows.Add(("{0}: folder exists but is empty" -f $Title))
    }
    elseif ($peek.Count -le 4) {
      [void]$probeRows.Add(("{0}: sparse workspace ({1} entries)" -f $Title, $peek.Count))
    }
    else {
      [void]$probeRows.Add(("{0}: active workspace (~{1} hints)" -f $Title, $peek.Count))
    }
  }

  function Add-ModelHintsFromFiles([string]$Title, [string]$RootDir) {
    if (-not (Test-Path -LiteralPath $RootDir)) { return }
    try {
      $files = @(Get-ChildItem -LiteralPath $RootDir -File -Recurse -Depth 3 -ErrorAction SilentlyContinue |
          Where-Object { $_.Extension -in @(".json", ".jsonl", ".log", ".txt") } |
          Sort-Object LastWriteTime -Descending |
          Select-Object -First 24)
      if ($files.Count -le 0) { return }
      $hits = @(Select-String -LiteralPath ($files.FullName) -Pattern "claude|sonnet|opus|haiku|gemini|flash|pro" -SimpleMatch:$false -ErrorAction SilentlyContinue | Select-Object -First 12)
      if ($hits.Count -le 0) { return }
      $joined = (($hits | ForEach-Object { $_.Line }) -join " ").ToLowerInvariant()
      if ($joined -match "claude|sonnet|opus|haiku") {
        Add-FaceUnique "Claude"
        [void]$probeRows.Add(("{0}: Claude model hints found" -f $Title))
      }
      if ($joined -match "gemini|flash|pro") {
        Add-FaceUnique "Gemini"
        [void]$probeRows.Add(("{0}: Gemini model hints found" -f $Title))
      }
    } catch {}
  }

  Summarize-Conversations "Gemini" $agRoot

  $presenceTargets = @(
    @{ Title = "Antigravity roaming"; Path = (Join-Path $appDataRoaming "Antigravity") },
    @{ Title = "Claude Code"; Path = (Join-Path $userHome ".claude") },
    @{ Title = ".gemini home"; Path = (Join-Path $userHome ".gemini") },
    @{ Title = "Cursor"; Path = (Join-Path $userHome ".cursor") },
    @{ Title = "VS Code"; Path = (Join-Path $userHome ".vscode") },
    @{ Title = "Claude roaming"; Path = (Join-Path $appDataRoaming "Claude") },
    @{ Title = "Claude local"; Path = (Join-Path $localAppData "Claude") },
    @{ Title = "Anthropic local"; Path = (Join-Path $localAppData "Anthropic") },
    @{ Title = "Codeium / Windsurf"; Path = (Join-Path $userHome ".codeium") }
  )

  foreach ($pt in $presenceTargets) {
    Mark-PresenceWorkspace ([string]$pt.Title) ([string]$pt.Path)
  }

  Add-ModelHintsFromFiles "Antigravity roaming" (Join-Path $appDataRoaming "Antigravity")
  Add-ModelHintsFromFiles "Gemini" $agRoot

  if ($binarySource) {
    [void]$probeRows.Add(("Antigravity binary: $($binarySource)"))
  }
  elseif ($binConfigured.Length -gt 0) {
    [void]$probeRows.Add(("Configured binary path missing: $($binConfigured)"))
  }

  $subtitle = ""
  if ($script:Settings.antigravity -and (-not ([string]::IsNullOrWhiteSpace([string]$script:Settings.antigravity.subtitle)))) {
    $subtitle = Convert-ToDisplayText ([string]$script:Settings.antigravity.subtitle)
  }

  $mainChunks = New-Object System.Collections.Generic.List[string]

  $faceSnap = @( $faces.ToArray() )
  if ($faceSnap.Count -gt 0) {
    $trimmed = @( $faceSnap | Select-Object -First 4 )
    $summary = (($trimmed) -join " | ")
    if ($faceSnap.Count -gt $trimmed.Count) {
      [void]$mainChunks.Add(("{0} (+{1})" -f $summary, ($faceSnap.Count - $trimmed.Count)))
    }
    else {
      [void]$mainChunks.Add($summary)
    }
  }
  elseif ($binarySource.Length -gt 0) {
    [void]$mainChunks.Add("Local tool detected")
  }
  elseif ($subtitle.Length -gt 0 -or ($binConfigured.Length -gt 0)) {
    [void]$mainChunks.Add("Configure local AI paths")
  }
  else {
    [void]$mainChunks.Add("No CLI + no obvious caches")
    [void]$probeRows.Insert(0, "Tip: set data/bin paths in LimitDock settings.")
  }

  $mainJoined = (($mainChunks | Where-Object { $_ }) -join " - ")
  if ($mainJoined.Length -gt 92) {
    $mainJoined = $mainJoined.Substring(0, 88) + "..."
  }

  $detailPieces = New-Object System.Collections.Generic.List[string]
  if ($subtitle.Length -gt 0) {
    [void]$detailPieces.Add($subtitle)
  }
  $peekRows = @( $probeRows.ToArray())
  $takeProbe = [Math]::Min(4, [Math]::Max(0, $peekRows.Length))
  for ($ri = 0; $ri -lt $takeProbe; $ri++) {
    [void]$detailPieces.Add($peekRows[$ri])
  }

  if ($detailPieces.Count -eq 0) {
    [void]$detailPieces.Add("Open settings to steer detection or add subtitles.")
  }

  $detailJoined = (($detailPieces | Where-Object { $_ }) -join " | ")
  if ($detailJoined.Length -gt 132) {
    $detailJoined = $detailJoined.Substring(0, 128) + "..."
  }

  $messageBlocks = New-Object System.Collections.Generic.List[string]
  [void]$messageBlocks.Add("Local AI footprint card (Gemini/Antigravity, Cursor-ish tools, Claude, Anthropic, Codeium).")
  [void]$messageBlocks.Add(("Primary Antigravity data root: $($agRoot)"))
  if ($peekRows.Count -gt 0) {
    foreach ($line in @($peekRows | Select-Object -First 24)) {
      [void]$messageBlocks.Add($line)
    }
  }

  return [pscustomobject]@{
    Name           = "Antigravity"
    Main           = (Convert-ToDisplayText $mainJoined)
    Detail         = (Convert-ToDisplayText $detailJoined)
    Level          = "info"
    Bands          = @()
    AllBands       = @()
    Message        = (Convert-ToDisplayText (($messageBlocks | Where-Object { $_ }) -join "`r`n"))
    SnapshotKey    = "footprint-antigravity"
    ProviderId     = "local_footprint"
    QuotaRotateCue = $false
  }
}

function Get-LimitDockCards {
  $data = Get-MergedUsageReadModelPayload
  $cards = @()
  foreach ($prop in $data.snapshots.PSObject.Properties) {
    $card = Convert-SnapshotToCard $prop.Name $prop.Value
    if (@( $card.AllBands | Where-Object { $null -ne $_.Percent } ).Count -gt 0) {
      $cards += $card
    }
  }

  [bool]$hasTelemetryAntigravity = $false
  foreach ($card in $cards) {
    try {
      if ([string]$card.ProviderId -eq "antigravity") {
        $hasTelemetryAntigravity = $true
        break
      }
    } catch {}
  }

  [bool]$hasAntigravityName = $false
  foreach ($card in $cards) {
    try {
      if ([string]$card.Name -eq "Antigravity") {
        $hasAntigravityName = $true
        break
      }
    } catch {}
  }

  if ((-not $hasTelemetryAntigravity) -and (-not $hasAntigravityName)) {
    $agCard = Get-AntigravityCard
    if ($agCard -and (@( $agCard.AllBands | Where-Object { $null -ne $_.Percent } ).Count -gt 0)) {
      $cards += $agCard
    }
  }

  return $cards | Sort-Object Name
}

function Resolve-ClickedCardPayload([AllowNull()]$TagPayload) {
  if ($null -eq $TagPayload) {
    return $null
  }
  if ($TagPayload -is [hashtable] -and $TagPayload.ContainsKey("Card") -and ($null -ne $TagPayload.Card)) {
    return $TagPayload.Card
  }
  return $TagPayload
}

function Show-QuotaBandPicker {
  param($Card)
  if (-not $Card -or -not ($Card.PSObject.Properties.Name -contains "AllBands")) {
    return
  }
  $allBands = @( $Card.AllBands | Where-Object { $null -ne $_.Percent } )
  if ($allBands.Count -le 0) {
    return
  }

  [string]$sk = ""
  try { $sk = ([string]$Card.SnapshotKey).Trim() } catch { $sk = "" }
  if ([string]::IsNullOrWhiteSpace($sk)) {
    return
  }
  $hiddenSet = Get-HiddenQuotaBandSet $sk

  $dlg = New-Object System.Windows.Forms.Form
  $dlg.Text = "LimitDock - visible rows"
  $dlg.StartPosition = [System.Windows.Forms.FormStartPosition]::CenterParent
  $dlg.FormBorderStyle = [System.Windows.Forms.FormBorderStyle]::FixedDialog
  $dlg.MinimizeBox = $false
  $dlg.MaximizeBox = $false
  $dlg.ShowInTaskbar = $false
  $dlg.Width = 720
  [int]$rowsNeeded = [Math]::Max(1, [int][Math]::Ceiling([double]$allBands.Count / 2.0))
  [int]$bodyH = [Math]::Min(420, [Math]::Max(120, (($rowsNeeded * 28) + 10)))
  $dlg.Height = $bodyH + 104

  $scrollHost = New-Object System.Windows.Forms.Panel
  $scrollHost.Left = 12
  $scrollHost.Top = 12
  $scrollHost.Width = 680
  $scrollHost.Height = $bodyH
  $scrollHost.AutoScroll = $true
  $scrollHost.BorderStyle = [System.Windows.Forms.BorderStyle]::FixedSingle

  $grid = New-Object System.Windows.Forms.TableLayoutPanel
  $grid.Left = 0
  $grid.Top = 0
  $grid.Width = 660
  $grid.AutoSize = $true
  $grid.ColumnCount = 2
  $grid.RowCount = $rowsNeeded
  $grid.Margin = New-Object System.Windows.Forms.Padding(0)
  $grid.Padding = New-Object System.Windows.Forms.Padding(6, 6, 6, 6)
  [void]$grid.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle -ArgumentList ([System.Windows.Forms.SizeType]::Percent), 50))
  [void]$grid.ColumnStyles.Add((New-Object System.Windows.Forms.ColumnStyle -ArgumentList ([System.Windows.Forms.SizeType]::Percent), 50))
  for ($ri = 0; $ri -lt $rowsNeeded; $ri++) {
    [void]$grid.RowStyles.Add((New-Object System.Windows.Forms.RowStyle -ArgumentList ([System.Windows.Forms.SizeType]::Absolute), 28))
  }

  $checks = New-Object System.Collections.Generic.List[System.Windows.Forms.CheckBox]
  [int]$idx = 0
  foreach ($b in $allBands) {
    [string]$key = [string]$b.Key
    [string]$txt = "{0}  {1:0.#}%" -f (Convert-ToDisplayText $b.Caption), [double]$b.Percent
    $cb = New-Object System.Windows.Forms.CheckBox
    $cb.Text = $txt
    $cb.Tag = $key
    $cb.Checked = (-not ($hiddenSet.ContainsKey($key)))
    $cb.AutoSize = $false
    $cb.Width = 310
    $cb.Height = 24
    $cb.AutoEllipsis = $true
    $cb.Font = New-Object System.Drawing.Font("Segoe UI", 9)
    $cb.Margin = New-Object System.Windows.Forms.Padding(3, 1, 3, 1)
    [void]$checks.Add($cb)
    [void]$grid.Controls.Add($cb, ($idx % 2), [int][Math]::Floor($idx / 2))
    $idx++
  }

  $ok = New-Object System.Windows.Forms.Button
  $ok.Text = "Apply"
  $ok.Left = 520
  $ok.Top = $bodyH + 24
  $ok.Width = 80
  $ok.DialogResult = [System.Windows.Forms.DialogResult]::OK

  $cancel = New-Object System.Windows.Forms.Button
  $cancel.Text = "Cancel"
  $cancel.Left = 612
  $cancel.Top = $bodyH + 24
  $cancel.Width = 80
  $cancel.DialogResult = [System.Windows.Forms.DialogResult]::Cancel

  [void]$scrollHost.Controls.Add($grid)
  $dlg.Controls.Add($scrollHost)
  $dlg.Controls.Add($ok)
  $dlg.Controls.Add($cancel)
  $dlg.AcceptButton = $ok
  $dlg.CancelButton = $cancel

  try {
    $owner = $form
    $result = $dlg.ShowDialog($owner)
    if ($result -eq [System.Windows.Forms.DialogResult]::OK) {
      $newHidden = @{}
      foreach ($cb in @($checks.ToArray())) {
        if (-not $cb.Checked) {
          $newHidden[[string]$cb.Tag] = $true
        }
      }
      $script:QuotaBandHidden[$sk] = $newHidden
      if ($script:Settings) {
        $script:Settings.hiddenQuotaBands = $script:QuotaBandHidden
        Save-LimitDockSettings $script:Settings
      }
      Render-Cards
    }
  } finally {
    try { $dlg.Dispose() } catch {}
  }
}

function Set-CardLabelStyle {
  param($Label, [string]$Level)
  switch ($Level) {
    "critical" {
      $Label.BackColor = $script:Theme.CriticalBack
      $Label.ForeColor = $script:Theme.CriticalFore
    }
    "warn" {
      $Label.BackColor = $script:Theme.WarnBack
      $Label.ForeColor = $script:Theme.WarnFore
    }
    "status" {
      $Label.BackColor = $script:Theme.StatusBack
      $Label.ForeColor = $script:Theme.MutedFore
    }
    "info" {
      $Label.BackColor = $script:Theme.StatusBack
      $Label.ForeColor = $script:Theme.Fore
    }
    default {
      $Label.BackColor = $script:Theme.OkBack
      $Label.ForeColor = $script:Theme.Fore
    }
  }
}

function New-ProviderCardControl {
  param($Card)

  $level = "ok"
  if ($Card.Level) { $level = [string]$Card.Level }

  [int]$chipW = [int]$script:DockCardChipWidth
  if ($chipW -lt 340) {
    $chipW = 340
  }
  [int]$chipH = [int]$script:DockCardChipHeight
  if ($chipH -lt 52) {
    $chipH = 52
  }
  [int]$gaugeCapRibbon = [int]$script:DockRibbonGaugeCap
  if ($gaugeCapRibbon -lt 1) {
    $gaugeCapRibbon = 2
  }
  elseif ($gaugeCapRibbon -gt 6) {
    $gaugeCapRibbon = 6
  }

  $container = New-Object System.Windows.Forms.FlowLayoutPanel
  $container.AutoSize = $false
  $container.FlowDirection = [System.Windows.Forms.FlowDirection]::LeftToRight
  $container.WrapContents = $false
  $container.Width = $chipW
  $container.Height = $chipH
  $container.Padding = New-Object System.Windows.Forms.Padding(6, 2, 6, 4)
  $container.Margin = New-Object System.Windows.Forms.Padding(3, 1, 3, 1)
  $container.Tag = $Card
  Set-CardLabelStyle $container $level

  $bands = @()
  if ($Card.PSObject.Properties.Name -contains "Bands") {
    $bands = @( $Card.Bands )
  }

  $clickHandler = {
    $item = [System.Windows.Forms.Control]$this
    $c = Resolve-ClickedCardPayload $item.Tag
    if (-not $c -or (-not ($c.PSObject.Properties.Name -contains "Name"))) {
      return
    }

    if (($c.PSObject.Properties.Name -contains "AllBands") -and (@($c.AllBands | Where-Object { $null -ne $_.Percent }).Count -gt 0)) {
      return
    }

    $messageParts = @()
    if ($c.Message) { $messageParts += $c.Message }
    $messageParts += ("Summary: {0}" -f $c.Main)
    if ($c.Detail) {
      $messageParts += ("Timing: {0}" -f $c.Detail)
    }
    if ($c.Bands -and @( $c.Bands ).Count -gt 0) {
      $messageParts += "--- Remaining ---"
      foreach ($line in @( $c.Bands )) {
        if ($null -ne $line.Percent) {
          $wnd = [string]$line.Window
          $rst = ""
          try { $rst = ([string]$line.Reset).Trim() } catch { $rst = "" }
          $suffixBits = @()
          if ($wnd) { $suffixBits += $wnd }
          if ($rst) { $suffixBits += "reset $rst" }
          $suffixWin = ""
          if ($suffixBits.Count -gt 0) { $suffixWin = " ({0})" -f ($suffixBits -join ", ") }
          $messageParts += ("{0}: {1:0.#}%{2}" -f $line.Caption, [double]$line.Percent, $suffixWin)
        }
        elseif ($line.DisplayDetail) {
          $messageParts += ("{0}: {1}" -f $line.Caption, $line.DisplayDetail)
        }
      }
    }
    [System.Windows.Forms.MessageBox]::Show((Convert-ToDisplayText ($messageParts -join "`r`n")), $c.Name, "OK", "Information") | Out-Null
  }

  $doubleClickHandler = {
    $item = [System.Windows.Forms.Control]$this
    $c = Resolve-ClickedCardPayload $item.Tag
    try { Show-QuotaBandPicker $c } catch { Write-Log "Quota band picker failed: $($_.Exception.Message)" }
  }

  $logo = New-Object System.Windows.Forms.PictureBox
  $logo.Width = 20
  $logo.Height = 20
  $logo.Margin = New-Object System.Windows.Forms.Padding(0, 4, 7, 0)
  $logo.SizeMode = [System.Windows.Forms.PictureBoxSizeMode]::Zoom
  $logo.Image = New-LogoBitmap $Card.Name
  $logo.Tag = $Card
  $logo.Add_Click($clickHandler)
  $logo.Add_DoubleClick($doubleClickHandler)

  [int]$padSides = ([int]$container.Padding.Left + [int]$container.Padding.Right)
  [int]$innerW = $chipW - 36 - $padSides
  if ($innerW -lt 268) {
    $innerW = 268
  }

  [int]$meteredRibbonRows = @( $bands | Where-Object { $_.Percent -ne $null } ).Length
  [bool]$showDetailLineRibbon = ([bool]$Card.Detail) -and ($meteredRibbonRows -eq 0)

  [string]$quotaRotateHintPrefix = ""
  try {
    if (($Card.PSObject.Properties.Name -contains "QuotaRotateCue") -and ([bool]$Card.QuotaRotateCue)) {
      $quotaRotateHintPrefix = "Double-click: choose model/window rows | "
    }
  } catch {}

  try {
    if (-not ($script:LdRibbonToolTip -is [System.Windows.Forms.ToolTip])) {
      $script:LdRibbonToolTip = New-Object System.Windows.Forms.ToolTip
      $script:LdRibbonToolTip.InitialDelay = 250
      $script:LdRibbonToolTip.ReshowDelay = 120
      $script:LdRibbonToolTip.AutoPopDelay = 16000
    }
    elseif ($script:LdRibbonToolTip.AutoPopDelay -lt 16000) {
      $script:LdRibbonToolTip.AutoPopDelay = 16000
    }
    if ((-not ([string]::IsNullOrWhiteSpace([string]$Card.Detail))) -and (-not $showDetailLineRibbon)) {
      $script:LdRibbonToolTip.SetToolTip($container, (Convert-ToDisplayText ($quotaRotateHintPrefix + ([string]$Card.Detail))))
    }
    elseif ((-not ([string]::IsNullOrWhiteSpace([string]$Card.Main))) -and $showDetailLineRibbon) {
      [string]$baseTipCombine = "$(Convert-ToDisplayText $Card.Name) - $(Convert-ToDisplayText $Card.Main)"
      $script:LdRibbonToolTip.SetToolTip($container, (Convert-ToDisplayText ($quotaRotateHintPrefix + $baseTipCombine)))
    }
  }
  catch {
    Write-Log "Ribbon tooltip setup failed for $($Card.Name): $($_.Exception.Message)"
  }

  $ribbonBands = @( $bands | Select-Object -First $gaugeCapRibbon )

  $stack = New-Object System.Windows.Forms.FlowLayoutPanel
  $stack.FlowDirection = [System.Windows.Forms.FlowDirection]::TopDown
  $stack.WrapContents = $false
  $stack.AutoSize = $false
  $stack.Width = $innerW
  $stack.Margin = New-Object System.Windows.Forms.Padding(0, 1, 0, 1)
  $stack.Padding = New-Object System.Windows.Forms.Padding(0, 1, 0, 2)
  $stack.BackColor = $container.BackColor
  $stack.Tag = $Card

  $titleStyle = [System.Drawing.FontStyle]::Bold
  if ($level -eq "info") {
    $titleStyle = [System.Drawing.FontStyle]::Regular
  }
  $title = New-Object System.Windows.Forms.Label
  $title.AutoSize = $false
  $title.Width = $innerW
  $title.Height = 18
  $title.AutoEllipsis = $true
  $title.Font = New-Object System.Drawing.Font("Segoe UI", 9, $titleStyle)
  $title.ForeColor = $container.ForeColor
  $title.BackColor = $container.BackColor
  $title.TextAlign = [System.Drawing.ContentAlignment]::MiddleLeft
  if ($meteredRibbonRows -gt 0) {
    $title.Text = "$(Convert-ToDisplayText $Card.Name)"
  } else {
    $title.Text = "$(Convert-ToDisplayText $Card.Name) $(Convert-ToDisplayText $Card.Main)"
  }
  $title.Margin = New-Object System.Windows.Forms.Padding(0, 0, 0, 1)
  $title.Tag = $Card
  $title.Add_Click($clickHandler)
  $title.Add_DoubleClick($doubleClickHandler)

  $stack.Controls.Add($title) | Out-Null

  if ($Card.Detail -and $showDetailLineRibbon) {
    $sub = New-Object System.Windows.Forms.Label
    $sub.AutoSize = $false
    $sub.Width = $innerW
    $sub.Height = 17
    $sub.AutoEllipsis = $true
    $sub.Font = New-Object System.Drawing.Font("Segoe UI", 8, [System.Drawing.FontStyle]::Regular)
    $sub.ForeColor = $script:Theme.MutedFore
    $sub.BackColor = $container.BackColor
    $sub.TextAlign = [System.Drawing.ContentAlignment]::MiddleLeft
    $sub.Text = (Convert-ToDisplayText $Card.Detail)
    $sub.Margin = New-Object System.Windows.Forms.Padding(0, 0, 0, 1)
    $sub.Tag = $Card
    $sub.Add_Click($clickHandler)
    $stack.Controls.Add($sub) | Out-Null
  }

  [int]$ribbonBandCount = @( $ribbonBands ).Count
  [int]$ribbonCols = 1
  if ($ribbonBandCount -gt 1) {
    $ribbonCols = 2
  }
  [int]$ribbonRows = 0
  if ($ribbonBandCount -gt 0) {
    $ribbonRows = [int][Math]::Ceiling([double]$ribbonBandCount / [double]$ribbonCols)
  }
  [int]$cellGap = 6
  [int]$cellW = $innerW
  if ($ribbonCols -gt 1) {
    $cellW = [Math]::Max(130, [int][Math]::Floor(($innerW - $cellGap) / 2.0))
  }

  for ($rbRow = 0; $rbRow -lt $ribbonRows; $rbRow++) {
    $row = New-Object System.Windows.Forms.Panel
    $row.Width = $innerW
    $row.Height = 18
    $row.BackColor = $container.BackColor
    $row.Margin = New-Object System.Windows.Forms.Padding(0, 1, 0, 2)
    $row.Tag = $Card
    try { $row.Add_Click($clickHandler) } catch {}
    try { $row.Add_DoubleClick($doubleClickHandler) } catch {}

    for ($rbCol = 0; $rbCol -lt $ribbonCols; $rbCol++) {
      [int]$bandIndex = ($rbRow * $ribbonCols) + $rbCol
      if ($bandIndex -ge $ribbonBandCount) {
        continue
      }
      $segment = $ribbonBands[$bandIndex]
      [int]$cellLeft = $rbCol * ($cellW + $cellGap)
      [int]$capW = [Math]::Max(56, [Math]::Min(96, ($cellW - 86)))
      [int]$pctW = 38
      [int]$pctLeft = $cellLeft + $capW + 2
      [int]$gaugeLeft = $pctLeft + $pctW + 6
      [int]$gaugeW = [Math]::Max(34, ($cellLeft + $cellW - $gaugeLeft - 2))

      $cap = New-Object System.Windows.Forms.Label
      $cap.AutoSize = $false
      $cap.Left = $cellLeft
      $cap.Top = 0
      $cap.Width = $capW
      $cap.Height = 18
      $cap.TextAlign = [System.Drawing.ContentAlignment]::MiddleLeft
      $cap.Font = New-Object System.Drawing.Font("Segoe UI", 8, [System.Drawing.FontStyle]::Regular)
      $cap.ForeColor = $script:Theme.MutedFore
      $cap.BackColor = $container.BackColor
      $cap.AutoEllipsis = $true
      $cap.Text = Convert-ToDisplayText $segment.Caption
      $cap.Tag = $Card
      $cap.Add_Click($clickHandler)
      $cap.Add_DoubleClick($doubleClickHandler)

      [void]$row.Controls.Add($cap)

      if ($null -ne $segment.Percent) {
        try {
          $pctVal = [double]$segment.Percent
        } catch {
          $pctVal = $null
        }
        $pctLbl = New-Object System.Windows.Forms.Label
        $pctLbl.AutoSize = $false
        $pctLbl.Left = $pctLeft
        $pctLbl.Top = 0
        $pctLbl.Width = $pctW
        $pctLbl.Height = 18
        $pctLbl.TextAlign = [System.Drawing.ContentAlignment]::MiddleRight
        $pctLbl.Font = New-Object System.Drawing.Font("Segoe UI", 8, [System.Drawing.FontStyle]::Regular)
        $pctLbl.ForeColor = $container.ForeColor
        $pctLbl.BackColor = $container.BackColor
        $pctLbl.Text = ("{0:0.#}%" -f $pctVal)
        $pctLbl.Tag = $Card
        $pctLbl.Add_Click($clickHandler)
        $pctLbl.Add_DoubleClick($doubleClickHandler)
        [void]$row.Controls.Add($pctLbl)

        $gauge = New-UsageGaugePanel $gaugeW 13 $pctVal $Card
        $gauge.Anchor = [System.Windows.Forms.AnchorStyles]::Top -bor [System.Windows.Forms.AnchorStyles]::Left
        $gauge.Left = $gaugeLeft
        $gauge.Top = 2
        $gauge.Width = $gaugeW
        $gauge.BackColor = $container.BackColor
        $gauge.Add_Click($clickHandler)
        $gauge.Add_DoubleClick($doubleClickHandler)
        try {
          [void]$row.Controls.Add($gauge)
        } catch {
          Write-Log "Gauge add failed on $($Card.Name): $($_.Exception.Message)"
        }
        $gauge.BringToFront() | Out-Null
        try {
          $gauge.Refresh()
        } catch {}
      }
      elseif ($segment.DisplayDetail) {
        $info = New-Object System.Windows.Forms.Label
        $info.AutoSize = $false
        $info.Left = $pctLeft
        $info.Top = 0
        $info.Width = [Math]::Max(44, ($cellLeft + $cellW - $info.Left - 2))
        $info.Height = 17
        $info.AutoEllipsis = $true
        $info.Font = New-Object System.Drawing.Font("Segoe UI", 8, [System.Drawing.FontStyle]::Regular)
        $info.ForeColor = $container.ForeColor
        $info.BackColor = $container.BackColor
        $info.TextAlign = [System.Drawing.ContentAlignment]::MiddleLeft
        $info.Text = (Convert-ToDisplayText $segment.DisplayDetail)
        $info.Tag = $Card
        $info.Add_Click($clickHandler)
        $info.Add_DoubleClick($doubleClickHandler)
        [void]$row.Controls.Add($info)
      }
    }

    $row.Tag = $Card
    [void]$stack.Controls.Add($row)
  }

  [int]$needStackH = 23 + ([Math]::Max(0, $ribbonRows) * 21)
  if ($showDetailLineRibbon) {
    $needStackH += 18
  }
  [int]$padVertical = ([int]$container.Padding.Top + [int]$container.Padding.Bottom)
  [int]$slotMaxForStack = $chipH - $padVertical - 4
  if ($slotMaxForStack -lt 44) {
    $slotMaxForStack = 44
  }
  $stack.Height = ([Math]::Max(42, ([Math]::Min($needStackH, $slotMaxForStack))))

  $stack.Tag = $Card
  foreach ($stkChild in @( $stack.Controls )) {
    if ($null -eq $stkChild.Tag) {
      $stkChild.Tag = $Card
    }
  }

  $container.Add_Click($clickHandler)
  $container.Add_DoubleClick($doubleClickHandler)

  [void]$container.Controls.Add($logo)
  [void]$container.Controls.Add($stack)

  try {
    $stack.PerformLayout() | Out-Null
    $container.PerformLayout() | Out-Null
  }
  catch {
  }

  return $container
}

function New-GearBitmap {
  $bitmap = New-Object System.Drawing.Bitmap 18, 18
  $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
  $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
  $graphics.Clear([System.Drawing.Color]::Transparent)
  $pen = New-Object System.Drawing.Pen $script:Theme.MutedFore, 2
  $brush = New-Object System.Drawing.SolidBrush $script:Theme.MutedFore
  for ($i = 0; $i -lt 8; $i++) {
    $angle = ($i * 45) * [Math]::PI / 180
    $x1 = 9 + [Math]::Cos($angle) * 6
    $y1 = 9 + [Math]::Sin($angle) * 6
    $x2 = 9 + [Math]::Cos($angle) * 8
    $y2 = 9 + [Math]::Sin($angle) * 8
    $graphics.DrawLine($pen, [float]$x1, [float]$y1, [float]$x2, [float]$y2)
  }
  $graphics.DrawEllipse($pen, 4, 4, 10, 10)
  $graphics.FillEllipse($brush, 7, 7, 4, 4)
  $pen.Dispose()
  $brush.Dispose()
  $graphics.Dispose()
  return $bitmap
}

function Sync-AutoHidePinGlyph {
  if ($null -ne $script:AutoHidePinPb) {
    try {
      $prev = $script:AutoHidePinPb.Image
      # Pinned = bar always docked visible (Auto Hide off). Unpinned = slides off edge peek.
      $script:AutoHidePinPb.Image = New-PinBitmap ((-not [bool]$script:AutoHideEnabled))
      if ($prev) {
        try { $prev.Dispose() } catch {}
      }
    } catch {
      Write-Log "Pin glyph refresh failed: $($_.Exception.Message)"
    }
  }
}

function Get-AutoHidePinLabelText {
  if ([bool]$script:AutoHideEnabled) {
    return "Slides at edge"
  }
  return "Pinned"
}

function New-AutoHideButton {
  param([scriptblock]$OnClick)

  $container = New-Object System.Windows.Forms.Panel
  $container.AutoSize = $false
  $container.Margin = New-Object System.Windows.Forms.Padding(1, 1, 1, 1)
  $container.Width = 28
  $container.Height = 28
  try { $container.MinimumSize = New-Object System.Drawing.Size 28, 28 } catch {}
  try { $container.MaximumSize = New-Object System.Drawing.Size 28, 28 } catch {}
  Set-CardLabelStyle $container "status"

  $icon = New-Object System.Windows.Forms.PictureBox
  $icon.Width = 20
  $icon.Height = 20
  $icon.Left = 4
  $icon.Top = 4
  $icon.SizeMode = [System.Windows.Forms.PictureBoxSizeMode]::Zoom
  $pinned = ((-not [bool]$script:AutoHideEnabled))
  $icon.Image = New-PinBitmap $pinned

  $container.Add_Click($OnClick)
  $icon.Add_Click($OnClick)
  $container.Controls.Add($icon) | Out-Null
  $script:AutoHideLabel = $null
  $script:AutoHidePinPb = $icon
  return $container
}

function New-SettingsButton {
  param([scriptblock]$OnClick)

  $wrap = New-Object System.Windows.Forms.Panel
  $wrap.AutoSize = $false
  $wrap.Width = 28
  $wrap.Height = 28
  $wrap.Margin = New-Object System.Windows.Forms.Padding(1, 1, 1, 1)
  $wrap.Cursor = [System.Windows.Forms.Cursors]::SizeAll
  Set-CardLabelStyle $wrap "status"

  $gear = New-Object System.Windows.Forms.PictureBox
  $gear.Width = 18
  $gear.Height = 18
  $gear.Left = 5
  $gear.Top = 5
  $gear.SizeMode = [System.Windows.Forms.PictureBoxSizeMode]::Zoom
  $gear.Image = New-GearBitmapSmall

  $wrap.Add_Click($OnClick)
  $gear.Add_Click($OnClick)
  $wrap.Controls.Add($gear) | Out-Null
  return $wrap
}

function New-DragHandleBitmap {
  $bitmap = New-Object System.Drawing.Bitmap 18, 18
  $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
  $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
  $graphics.Clear([System.Drawing.Color]::Transparent)
  $brush = New-Object System.Drawing.SolidBrush $script:Theme.MutedFore
  foreach ($y in @(4, 9, 14)) {
    $graphics.FillEllipse($brush, 7, $y, 4, 4)
  }
  $brush.Dispose()
  $graphics.Dispose()
  return $bitmap
}

function New-DockDragHandleButton {
  $wrap = New-Object System.Windows.Forms.Panel
  $wrap.AutoSize = $false
  $wrap.Width = 28
  $wrap.Height = 28
  $wrap.Margin = New-Object System.Windows.Forms.Padding(1, 1, 1, 1)
  Set-CardLabelStyle $wrap "status"

  $dots = New-Object System.Windows.Forms.PictureBox
  $dots.Width = 18
  $dots.Height = 18
  $dots.Left = 5
  $dots.Top = 5
  $dots.SizeMode = [System.Windows.Forms.PictureBoxSizeMode]::Zoom
  $dots.Cursor = [System.Windows.Forms.Cursors]::SizeAll
  $dots.Image = New-DragHandleBitmap

  $onDown = {
    param($sender, $args)
    try {
      if ($args.Button -eq [System.Windows.Forms.MouseButtons]::Left) {
        $script:DockDragActive = $true
      }
    } catch {}
  }
  $onUp = {
    param($sender, $args)
    try {
      if (-not [bool]$script:DockDragActive) {
        return
      }
      $script:DockDragActive = $false
      $edge = Get-NearestDockEdgeFromPoint ([System.Windows.Forms.Cursor]::Position)
      if ($edge) {
        Set-DockEdgeAndApply $edge
      }
    } catch {
      Write-Log "Dock snap failed: $($_.Exception.Message)"
    }
  }
  $wrap.Add_MouseDown($onDown)
  $wrap.Add_MouseUp($onUp)
  $dots.Add_MouseDown($onDown)
  $dots.Add_MouseUp($onUp)

  $wrap.Controls.Add($dots) | Out-Null
  return $wrap
}

function New-ToolRail {
  $rail = New-Object System.Windows.Forms.FlowLayoutPanel
  $rail.AutoSize = $false
  $rail.FlowDirection = [System.Windows.Forms.FlowDirection]::TopDown
  $rail.WrapContents = $false
  $rail.Width = 32
  $rail.Height = [int]$script:DockCardChipHeight
  $rail.Margin = New-Object System.Windows.Forms.Padding(2, 1, 3, 1)
  $rail.Padding = New-Object System.Windows.Forms.Padding(1, 4, 1, 2)
  Set-CardLabelStyle $rail "status"

  $pinBtn = $null
  $dragBtn = $null
  $settingsBtn = New-SettingsButton {
    try { Show-SettingsDialog } catch { Write-Log "Settings open failed: $($_.Exception.Message)" }
  }
  if ((Normalize-DockMode $script:DockMode) -eq "reserved") {
    $dragBtn = New-DockDragHandleButton
    [void]$rail.Controls.Add($dragBtn)
  }
  [void]$rail.Controls.Add($settingsBtn)
  if ((Normalize-DockMode $script:DockMode) -eq "overlay") {
    $pinBtn = New-AutoHideButton {
      try { Toggle-AutoHide } catch { Write-Log "Toggle auto-hide failed: $($_.Exception.Message)" }
    }
    [void]$rail.Controls.Add($pinBtn)
  }

  try {
    if (-not ($script:LdRibbonToolTip -is [System.Windows.Forms.ToolTip])) {
      $script:LdRibbonToolTip = New-Object System.Windows.Forms.ToolTip
    }
    if ($dragBtn) { $script:LdRibbonToolTip.SetToolTip($dragBtn, "Drag to dock edge") }
    $script:LdRibbonToolTip.SetToolTip($settingsBtn, "Settings")
    if ($pinBtn) { $script:LdRibbonToolTip.SetToolTip($pinBtn, (Get-AutoHidePinLabelText)) }
  } catch {}
  return $rail
}

function New-ControlButton {
  param([string]$Text, [scriptblock]$OnClick)

  $button = New-Object System.Windows.Forms.Label
  $button.AutoSize = $true
  $button.Font = New-Object System.Drawing.Font("Segoe UI", 9)
  $button.Padding = New-Object System.Windows.Forms.Padding(10, 4, 10, 5)
  $button.Margin = New-Object System.Windows.Forms.Padding(4, 0, 4, 0)
  $button.Text = $Text
  Set-CardLabelStyle $button "status"
  $button.Add_Click($OnClick)
  return $button
}

Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;

public static class LimitDockNative {
  [DllImport("user32.dll")]
  public static extern IntPtr SendMessage(IntPtr hWnd, int msg, IntPtr wParam, IntPtr lParam);

  [DllImport("user32.dll", SetLastError = true)]
  public static extern bool SetWindowPos(IntPtr hWnd, IntPtr hWndInsertAfter, int X, int Y, int cx, int cy, uint uFlags);

  [DllImport("user32.dll", SetLastError = true)]
  public static extern bool SystemParametersInfo(uint uiAction, uint uiParam, ref LimitDockRect pvParam, uint fWinIni);

  [DllImport("user32.dll", SetLastError = true)]
  public static extern uint GetDpiForWindow(IntPtr hWnd);

  [DllImport("user32.dll")]
  public static extern IntPtr MonitorFromWindow(IntPtr hwnd, uint dwFlags);

  [DllImport("shcore.dll")]
  public static extern int GetDpiForMonitor(IntPtr hmonitor, int dpiType, out uint dpiX, out uint dpiY);

  [DllImport("shell32.dll", SetLastError = true)]
  public static extern UIntPtr SHAppBarMessage(uint dwMessage, ref LimitDockAppBarData pData);

  public static bool AppBarNew(IntPtr hWnd, uint callbackMessage) {
    LimitDockAppBarData data = new LimitDockAppBarData();
    data.cbSize = Marshal.SizeOf(typeof(LimitDockAppBarData));
    data.hWnd = hWnd;
    data.uCallbackMessage = callbackMessage;
    return SHAppBarMessage(0x00000000, ref data).ToUInt64() != 0;
  }

  public static void AppBarRemove(IntPtr hWnd, uint callbackMessage) {
    LimitDockAppBarData data = new LimitDockAppBarData();
    data.cbSize = Marshal.SizeOf(typeof(LimitDockAppBarData));
    data.hWnd = hWnd;
    data.uCallbackMessage = callbackMessage;
    SHAppBarMessage(0x00000001, ref data);
  }

  public static LimitDockAppBarResult AppBarSet(IntPtr hWnd, uint callbackMessage, uint edge, int left, int top, int right, int bottom) {
    LimitDockAppBarData data = new LimitDockAppBarData();
    data.cbSize = Marshal.SizeOf(typeof(LimitDockAppBarData));
    data.hWnd = hWnd;
    data.uCallbackMessage = callbackMessage;
    data.uEdge = edge;
    data.rc.left = left;
    data.rc.top = top;
    data.rc.right = right;
    data.rc.bottom = bottom;
    UIntPtr query = SHAppBarMessage(0x00000002, ref data);
    int width = Math.Max(1, right - left);
    int height = Math.Max(1, bottom - top);
    if (edge == 0) {
      data.rc.left = left;
      data.rc.right = left + width;
      data.rc.top = top;
      data.rc.bottom = top + height;
    } else if (edge == 1) {
      data.rc.left = left;
      data.rc.right = left + width;
      data.rc.top = top;
      data.rc.bottom = top + height;
    } else if (edge == 2) {
      data.rc.right = right;
      data.rc.left = right - width;
      data.rc.top = top;
      data.rc.bottom = top + height;
    } else if (edge == 3) {
      data.rc.left = left;
      data.rc.right = left + width;
      data.rc.bottom = bottom;
      data.rc.top = bottom - height;
    } else {
      data.rc.left = left;
      data.rc.top = top;
      data.rc.right = right;
      data.rc.bottom = bottom;
    }
    UIntPtr set = SHAppBarMessage(0x00000003, ref data);
    LimitDockAppBarResult result = new LimitDockAppBarResult();
    result.queryResult = query.ToUInt64();
    result.setResult = set.ToUInt64();
    result.rc = data.rc;
    return result;
  }
}

[StructLayout(LayoutKind.Sequential)]
public struct LimitDockRect {
  public int left;
  public int top;
  public int right;
  public int bottom;
}

[StructLayout(LayoutKind.Sequential)]
public struct LimitDockAppBarData {
  public int cbSize;
  public IntPtr hWnd;
  public uint uCallbackMessage;
  public uint uEdge;
  public LimitDockRect rc;
  public IntPtr lParam;
}

[StructLayout(LayoutKind.Sequential)]
public struct LimitDockAppBarResult {
  public ulong queryResult;
  public ulong setResult;
  public LimitDockRect rc;
}
"@
Write-Log "Loaded WinForms assemblies"

function Set-ControlRedraw {
  param(
    [System.Windows.Forms.Control]$Control,
    [bool]$Enabled
  )
  if ($null -eq $Control -or $Control.IsDisposed -or -not $Control.IsHandleCreated) {
    return
  }
  try {
    [void][LimitDockNative]::SendMessage($Control.Handle, 0x000B, [intptr]([int]$Enabled), [intptr]::Zero)
  } catch {}
}

function Normalize-DockMode {
  param([AllowNull()][object]$Mode)
  $m = ""
  try { $m = ([string]$Mode).Trim().ToLowerInvariant() } catch {}
  if (($m -eq "reserved") -or ($m -eq "reserve") -or ($m -eq "appbar")) {
    return "reserved"
  }
  return "overlay"
}

function Normalize-DockEdge {
  param([AllowNull()][object]$Edge)
  $e = ""
  try { $e = ([string]$Edge).Trim().ToLowerInvariant() } catch {}
  if (($e -eq "top") -or ($e -eq "up")) {
    return "top"
  }
  if (($e -eq "left") -or ($e -eq "start")) {
    return "left"
  }
  if (($e -eq "right") -or ($e -eq "end")) {
    return "right"
  }
  return "bottom"
}

Ensure-OpenUsage
Ensure-Probe
$script:Settings = Load-LimitDockSettings
[int]$script:DockRefreshSeconds = [Math]::Max(5, [int]$script:Settings.refreshSeconds)
[string]$script:DockMode = Normalize-DockMode $script:Settings.dockMode
[string]$script:DockEdge = Normalize-DockEdge $script:Settings.dockEdge
[bool]$script:StatusBarVisible = [bool]$script:Settings.statusBarVisible
[bool]$script:AppBarRegistered = $false
[uint32]$script:AppBarCallbackMessage = 0x8001
$script:ReservedBaseWorkBottom = $null
$script:ReservedBaseWorkArea = $null

$daemon = Start-OpenUsageDaemon
Write-Log "Waiting for OpenUsage daemon socket"
$daemonReady = Wait-OpenUsageReady -TimeoutSeconds 15
if ($daemonReady) {
  Write-Log "OpenUsage daemon socket is ready"
} else {
  Write-Log "OpenUsage daemon did not become ready in time; UI will start anyway and retry"
}
Write-Log "Creating statusbar UI"
$script:Theme = Get-WindowsTheme

$form = New-Object System.Windows.Forms.Form
$form.Text = "LimitDock"
$form.FormBorderStyle = [System.Windows.Forms.FormBorderStyle]::None
$form.TopMost = $true
$form.ShowInTaskbar = $false
$form.BackColor = $script:Theme.Bar
$form.ForeColor = $script:Theme.Fore
$form.Height = 84
[int]$script:DockHorizontalHeight = 84
[int]$script:DockSideWidth = 456
[int]$script:DockCardChipWidth = 420
[int]$script:DockCardChipHeight = 70
[int]$script:DockRibbonGaugeCap = 4
# Per snapshot rotate index for cards with more model/window rows than the ribbon can show.
if (-not ($script:QuotaBandOffsets -is [hashtable])) {
  $script:QuotaBandOffsets = @{}
}
$script:QuotaBandHidden = ConvertTo-LdHashtable $script:Settings.hiddenQuotaBands
$form.Opacity = 0.98
$form.StartPosition = [System.Windows.Forms.FormStartPosition]::Manual

$script:Screen = [System.Windows.Forms.Screen]::PrimaryScreen.WorkingArea
$script:Bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
$taskbarReserve = $script:Bounds.Bottom - $script:Screen.Bottom
if ($taskbarReserve -le 0) {
  $taskbarReserve = 48
}
$form.Width = $script:Screen.Width
$form.Left = $script:Screen.Left
$script:ShowTop = $script:Bounds.Bottom - $taskbarReserve - $form.Height - 2
$script:HideTop = $script:Bounds.Bottom + 2
$script:ShowLeft = $script:Bounds.Left
$script:HideLeft = $script:Bounds.Left
$script:TaskbarReserve = $taskbarReserve
$script:AutoHideEnabled = [bool]$script:Settings.autoHide
$form.Top = $script:ShowTop
Write-Log "Showing statusbar left=$($form.Left) top=$($form.Top) width=$($form.Width) height=$($form.Height)"

function Update-LimitDockScreenMetrics {
  $script:Screen = [System.Windows.Forms.Screen]::PrimaryScreen.WorkingArea
  $script:Bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
  $reserve = $script:Bounds.Bottom - $script:Screen.Bottom
  if ($reserve -le 0) {
    $reserve = 48
  }
  $script:TaskbarReserve = $reserve
}

function Capture-LimitDockWorkArea {
  return @{
    Left   = [int]$script:Screen.Left
    Top    = [int]$script:Screen.Top
    Right  = [int]$script:Screen.Right
    Bottom = [int]$script:Screen.Bottom
  }
}

function Set-LimitDockSystemWorkArea {
  param($WorkArea)
  if (-not $WorkArea) {
    return
  }
  try {
    $rect = New-Object LimitDockRect
    $rect.left = [int]$WorkArea.Left
    $rect.top = [int]$WorkArea.Top
    $rect.right = [int]$WorkArea.Right
    $rect.bottom = [int]$WorkArea.Bottom
    [void][LimitDockNative]::SystemParametersInfo(0x002F, 0, [ref]$rect, 0x0002)
  } catch {
    Write-Log "Could not set Windows work area: $($_.Exception.Message)"
  }
}

function Get-LimitDockSystemWorkArea {
  try {
    $rect = New-Object LimitDockRect
    if ([LimitDockNative]::SystemParametersInfo(0x0030, 0, [ref]$rect, 0)) {
      return @{
        Left   = [int]$rect.left
        Top    = [int]$rect.top
        Right  = [int]$rect.right
        Bottom = [int]$rect.bottom
      }
    }
  } catch {}
  return $null
}

function Restore-LimitDockSystemWorkArea {
  if ($null -eq $script:ReservedBaseWorkArea) {
    return
  }
  Set-LimitDockSystemWorkArea $script:ReservedBaseWorkArea
  $script:ReservedBaseWorkArea = $null
}

function Get-DockEdgeAppBarCode {
  if ((Normalize-DockEdge $script:DockEdge) -eq "left") {
    return 0
  }
  if ((Normalize-DockEdge $script:DockEdge) -eq "top") {
    return 1
  }
  if ((Normalize-DockEdge $script:DockEdge) -eq "right") {
    return 2
  }
  return 3
}

function Get-LimitDockDpiScale {
  try {
    $monitor = [LimitDockNative]::MonitorFromWindow($form.Handle, 2)
    [uint32]$dx = 0
    [uint32]$dy = 0
    $hr = [LimitDockNative]::GetDpiForMonitor($monitor, 0, [ref]$dx, [ref]$dy)
    if (($hr -eq 0) -and ($dx -ge 72) -and ($dx -le 384)) {
      return ([double]$dx / 96.0)
    }
  } catch {}
  try {
    $dpi = [double][LimitDockNative]::GetDpiForWindow($form.Handle)
    if ($dpi -ge 72 -and $dpi -le 384) {
      return ($dpi / 96.0)
    }
  } catch {}
  return 1.0
}

function Test-DockEdgeIsSide {
  param([AllowNull()][object]$Edge)
  $e = Normalize-DockEdge $Edge
  return (($e -eq "left") -or ($e -eq "right"))
}

function Set-DockFormLayoutForEdge {
  param([string]$Edge)
  $e = Normalize-DockEdge $Edge
  [bool]$side = Test-DockEdgeIsSide $e
  if ($side) {
    $form.Width = [int]$script:DockSideWidth
    $form.Height = [Math]::Max(320, [int]($script:Screen.Bottom - $script:Screen.Top))
    $panel.FlowDirection = [System.Windows.Forms.FlowDirection]::TopDown
    $panel.WrapContents = $false
    $panel.Padding = New-Object System.Windows.Forms.Padding(6, 6, 6, 6)
    if ($statusLabel) { $statusLabel.Visible = $false }
  } else {
    $form.Width = [int]$script:Screen.Width
    $form.Height = [int]$script:DockHorizontalHeight
    $panel.FlowDirection = [System.Windows.Forms.FlowDirection]::LeftToRight
    $panel.WrapContents = $false
    $panel.Padding = New-Object System.Windows.Forms.Padding(8, 5, 8, 4)
    if ($statusLabel) { $statusLabel.Visible = $true }
  }
}

function Move-LimitDockToDockState {
  param([bool]$Shown)
  [int]$left = $script:ShowLeft
  [int]$top = $script:ShowTop
  if (-not $Shown) {
    $left = $script:HideLeft
    $top = $script:HideTop
  }
  Set-LimitDockWindowBounds $left $top $form.Width $form.Height
}

function Get-NearestDockEdgeFromPoint {
  param([System.Drawing.Point]$Point)
  Update-LimitDockScreenMetrics
  $candidates = @(
    @{ Edge = "left"; Distance = [Math]::Abs([int]$Point.X - [int]$script:Bounds.Left) },
    @{ Edge = "right"; Distance = [Math]::Abs([int]$script:Bounds.Right - [int]$Point.X) },
    @{ Edge = "top"; Distance = [Math]::Abs([int]$Point.Y - [int]$script:Bounds.Top) },
    @{ Edge = "bottom"; Distance = [Math]::Abs([int]$script:Bounds.Bottom - [int]$Point.Y) }
  )
  return [string](@($candidates | Sort-Object Distance | Select-Object -First 1)[0].Edge)
}

function Set-DockEdgeAndApply {
  param([AllowNull()][object]$Edge)
  [string]$newEdge = Normalize-DockEdge $Edge
  $script:DockEdge = $newEdge
  if ($script:Settings) {
    $script:Settings.dockEdge = $newEdge
    Save-LimitDockSettings $script:Settings
  }
  Apply-DockMode $script:DockMode
  Render-Cards
}

function Set-LimitDockWindowBounds {
  param([int]$Left, [int]$Top, [int]$Width, [int]$Height)
  [int]$nominalHeight = [Math]::Max(76, ([int]$script:DockCardChipHeight + 14))
  if ($Height -lt 60) {
    $Height = $nominalHeight
  }
  if ($Width -lt 100) {
    try { $Width = [int]$script:Bounds.Width } catch {}
  }
  $form.Visible = $true
  if ($form.WindowState -ne [System.Windows.Forms.FormWindowState]::Normal) {
    $form.WindowState = [System.Windows.Forms.FormWindowState]::Normal
  }
  $form.SetBounds($Left, $Top, $Width, $Height)
  $form.TopMost = $true
  try {
    [void][LimitDockNative]::SetWindowPos($form.Handle, [intptr](-1), $Left, $Top, $Width, $Height, 0x0040)
  } catch {}
  try { $form.Show() } catch {}
  try { $form.BringToFront() } catch {}
}

function Unregister-LimitDockAppBar {
  if (-not $script:AppBarRegistered) {
    $script:ReservedBaseWorkBottom = $null
    Restore-LimitDockSystemWorkArea
    return
  }
  try {
    [LimitDockNative]::AppBarRemove($form.Handle, [uint32]$script:AppBarCallbackMessage)
  } catch {
    Write-Log "Could not unregister reserved dock area: $($_.Exception.Message)"
  } finally {
    $script:AppBarRegistered = $false
    $script:ReservedBaseWorkBottom = $null
    Restore-LimitDockSystemWorkArea
  }
}

function Set-OverlayDockBounds {
  Update-LimitDockScreenMetrics
  Set-DockFormLayoutForEdge $script:DockEdge
  [string]$edge = Normalize-DockEdge $script:DockEdge
  $script:ShowLeft = $script:Bounds.Left
  $script:HideLeft = $script:Bounds.Left
  if ($edge -eq "top") {
    $script:ShowTop = $script:Screen.Top + 2
    $script:HideTop = $script:Bounds.Top - $form.Height - 2
  } elseif ($edge -eq "left") {
    $script:ShowLeft = $script:Screen.Left + 2
    $script:HideLeft = $script:Bounds.Left - $form.Width - 2
    $script:ShowTop = $script:Screen.Top
    $script:HideTop = $script:Screen.Top
  } elseif ($edge -eq "right") {
    $script:ShowLeft = $script:Screen.Right - $form.Width - 2
    $script:HideLeft = $script:Bounds.Right + 2
    $script:ShowTop = $script:Screen.Top
    $script:HideTop = $script:Screen.Top
  } else {
    $script:ShowTop = $script:Screen.Bottom - $form.Height - 2
    $script:HideTop = $script:Bounds.Bottom + 2
  }
  Move-LimitDockToDockState (-not [bool]$script:AutoHideEnabled)
  Write-Log "Overlay dock bounds left=$($form.Left) top=$($form.Top) width=$($form.Width) height=$($form.Height) showTop=$script:ShowTop hideTop=$script:HideTop reserve=$script:TaskbarReserve autoHide=$script:AutoHideEnabled"
}

function Set-ReservedDockBounds {
  if (-not $form.IsHandleCreated) {
    return
  }
  try {
    Update-LimitDockScreenMetrics
    Set-DockFormLayoutForEdge $script:DockEdge
    if ((-not $script:AppBarRegistered) -or ($null -eq $script:ReservedBaseWorkArea)) {
      $script:ReservedBaseWorkArea = Capture-LimitDockWorkArea
      $script:ReservedBaseWorkBottom = [int]$script:Screen.Bottom
    }

    if (-not $script:AppBarRegistered) {
      $script:AppBarRegistered = [LimitDockNative]::AppBarNew($form.Handle, [uint32]$script:AppBarCallbackMessage)
      Write-Log "Reserved dock ABM_NEW registered=$script:AppBarRegistered"
      if (-not $script:AppBarRegistered) {
        throw "ABM_NEW returned false"
      }
    }

    $baseArea = $script:ReservedBaseWorkArea
    [int]$baseLeft = [int]$baseArea.Left
    [int]$baseTop = [int]$baseArea.Top
    [int]$baseRight = [int]$baseArea.Right
    [int]$baseBottom = [int]$baseArea.Bottom
    if (($baseRight -le $baseLeft) -or ($baseBottom -le $baseTop)) {
      $baseLeft = [int]$script:Screen.Left
      $baseTop = [int]$script:Screen.Top
      $baseRight = [int]$script:Screen.Right
      $baseBottom = [int]$script:Screen.Bottom
    }
    [string]$edge = Normalize-DockEdge $script:DockEdge
    [bool]$side = Test-DockEdgeIsSide $edge
    [int]$barHeight = [Math]::Max([int]$form.Height, ([int]$script:DockCardChipHeight + 14))
    [int]$barWidth = [Math]::Max(240, [int]$form.Width)
    if (-not $side -and $barHeight -lt 76) {
      $barHeight = 76
    }
    if ($side) {
      $barHeight = [Math]::Max(320, ($baseBottom - $baseTop))
      $form.Height = $barHeight
    } elseif ($form.Height -lt 60) {
      $form.Height = $barHeight
    }
    [int]$desiredLeft = $baseLeft
    [int]$desiredRight = $baseRight
    [int]$desiredTop = $baseBottom - $barHeight
    [int]$desiredBottom = $baseBottom
    if ($edge -eq "top") {
      $desiredTop = $baseTop
      $desiredBottom = $baseTop + $barHeight
    } elseif ($edge -eq "left") {
      $desiredTop = $baseTop
      $desiredBottom = $baseBottom
      $desiredLeft = $baseLeft
      $desiredRight = $baseLeft + $barWidth
    } elseif ($edge -eq "right") {
      $desiredTop = $baseTop
      $desiredBottom = $baseBottom
      $desiredLeft = $baseRight - $barWidth
      $desiredRight = $baseRight
    }

    [double]$dpiScale = Get-LimitDockDpiScale
    [int]$appLeft = [int][Math]::Round([double]$desiredLeft * $dpiScale)
    [int]$appTop = [int][Math]::Round([double]$desiredTop * $dpiScale)
    [int]$appRight = [int][Math]::Round([double]$desiredRight * $dpiScale)
    [int]$appBottom = [int][Math]::Round([double]$desiredBottom * $dpiScale)
    $appBarResult = [LimitDockNative]::AppBarSet(
      $form.Handle,
      [uint32]$script:AppBarCallbackMessage,
      [uint32](Get-DockEdgeAppBarCode),
      $appLeft,
      $appTop,
      $appRight,
      $appBottom
    )
    $reportedWorkArea = Get-LimitDockSystemWorkArea
    [double]$autoScale = 1.0
    if ($reportedWorkArea) {
      if (($edge -eq "bottom") -and ([int]$reportedWorkArea.Bottom -gt 0) -and ([int]$reportedWorkArea.Bottom -lt ($desiredTop - 4))) {
        $autoScale = [double]$desiredTop / [double]$reportedWorkArea.Bottom
      }
      elseif (($edge -eq "top") -and ([int]$reportedWorkArea.Top -gt 0) -and ([int]$reportedWorkArea.Top -lt ($desiredBottom - 4))) {
        $autoScale = [double]$desiredBottom / [double]$reportedWorkArea.Top
      }
      elseif (($edge -eq "left") -and ([int]$reportedWorkArea.Left -gt 0) -and ([int]$reportedWorkArea.Left -lt ($desiredRight - 4))) {
        $autoScale = [double]$desiredRight / [double]$reportedWorkArea.Left
      }
      elseif (($edge -eq "right") -and ([int]$reportedWorkArea.Right -gt 0) -and ([int]$reportedWorkArea.Right -lt ($desiredLeft - 4))) {
        $autoScale = [double]$desiredLeft / [double]$reportedWorkArea.Right
      }
    }
    if (($autoScale -gt 1.05) -and ($autoScale -lt 3.0)) {
      $appLeft = [int][Math]::Round([double]$desiredLeft * $autoScale)
      $appTop = [int][Math]::Round([double]$desiredTop * $autoScale)
      $appRight = [int][Math]::Round([double]$desiredRight * $autoScale)
      $appBottom = [int][Math]::Round([double]$desiredBottom * $autoScale)
      $appBarResult = [LimitDockNative]::AppBarSet(
        $form.Handle,
        [uint32]$script:AppBarCallbackMessage,
        [uint32](Get-DockEdgeAppBarCode),
        $appLeft,
        $appTop,
        $appRight,
        $appBottom
      )
      $reportedWorkArea = Get-LimitDockSystemWorkArea
    }

    $script:ShowTop = $desiredTop
    $script:HideTop = $desiredTop
    $script:ShowLeft = $desiredLeft
    $script:HideLeft = $desiredLeft
    $workArea = @{
      Left   = $baseLeft
      Top    = $baseTop
      Right  = $baseRight
      Bottom = $baseBottom
    }
    if ($edge -eq "top") {
      $workArea.Top = $desiredBottom
    } elseif ($edge -eq "left") {
      $workArea.Left = $desiredRight
    } elseif ($edge -eq "right") {
      $workArea.Right = $desiredLeft
    } else {
      $workArea.Bottom = $desiredTop
    }
    Set-LimitDockSystemWorkArea $workArea
    Set-LimitDockWindowBounds $desiredLeft $desiredTop ($desiredRight - $desiredLeft) ($desiredBottom - $desiredTop)
    $reportedText = ""
    if ($reportedWorkArea) {
      $reportedText = " reportedWorkArea=($($reportedWorkArea.Left),$($reportedWorkArea.Top),$($reportedWorkArea.Right),$($reportedWorkArea.Bottom))"
    }
    Write-Log "Reserved dock bounds edge=$edge left=$($form.Left) top=$($form.Top) width=$($form.Width) height=$($form.Height) dpiScale=$('{0:0.###}' -f $dpiScale) autoScale=$('{0:0.###}' -f $autoScale) queryResult=$($appBarResult.queryResult) setResult=$($appBarResult.setResult) applied=($desiredLeft,$desiredTop,$desiredRight,$desiredBottom) appbar=($appLeft,$appTop,$appRight,$appBottom) workArea=($($workArea.Left),$($workArea.Top),$($workArea.Right),$($workArea.Bottom))$reportedText shellRc=($($appBarResult.rc.left),$($appBarResult.rc.top),$($appBarResult.rc.right),$($appBarResult.rc.bottom))"
  } catch {
    Write-Log "Could not reserve dock area; falling back to overlay: $($_.Exception.Message)"
    Unregister-LimitDockAppBar
    $script:DockMode = "overlay"
    Set-OverlayDockBounds
  }
}

function Apply-DockMode {
  param([AllowNull()][object]$Mode)
  $script:DockMode = Normalize-DockMode $Mode
  if ($script:Settings) {
    $script:Settings.dockMode = $script:DockMode
    $script:Settings.dockEdge = Normalize-DockEdge $script:DockEdge
  }
  if (-not [bool]$script:StatusBarVisible) {
    Unregister-LimitDockAppBar
    Update-AutoHideButtonText
    return
  }
  if ($script:DockMode -eq "reserved") {
    $script:AutoHideEnabled = $false
    if ($script:Settings) {
      $script:Settings.autoHide = $false
    }
    Set-ReservedDockBounds
  } else {
    Unregister-LimitDockAppBar
    try {
      [System.Windows.Forms.Application]::DoEvents()
      Start-Sleep -Milliseconds 160
      [System.Windows.Forms.Application]::DoEvents()
    } catch {}
    Set-OverlayDockBounds
  }
  Update-AutoHideButtonText
}

$panel = New-Object System.Windows.Forms.FlowLayoutPanel
$panel.Dock = [System.Windows.Forms.DockStyle]::Fill
$panel.FlowDirection = [System.Windows.Forms.FlowDirection]::LeftToRight
$panel.WrapContents = $false
$panel.Padding = New-Object System.Windows.Forms.Padding(8, 6, 8, 5)
$panel.BackColor = $script:Theme.Panel
$form.Controls.Add($panel)
Write-Log "Created form and panel"

$statusLabel = New-Object System.Windows.Forms.Label
$statusLabel.AutoSize = $false
$statusLabel.Dock = [System.Windows.Forms.DockStyle]::Right
$statusLabel.Width = 158
$statusLabel.TextAlign = [System.Drawing.ContentAlignment]::MiddleCenter
$statusLabel.Font = New-Object System.Drawing.Font("Segoe UI", 9, [System.Drawing.FontStyle]::Bold)
$statusLabel.Padding = New-Object System.Windows.Forms.Padding(8, 4, 8, 5)
$statusLabel.Margin = New-Object System.Windows.Forms.Padding(4, 0, 4, 0)
Set-CardLabelStyle $statusLabel "status"
$statusLabel.ForeColor = $script:Theme.StatusAccent
$form.Controls.Add($statusLabel)

$notify = New-Object System.Windows.Forms.NotifyIcon
$notify.Text = "LimitDock"
$notify.Icon = [System.Drawing.SystemIcons]::Information
$notify.Visible = $true
$menu = New-Object System.Windows.Forms.ContextMenuStrip
$visibilityItem = $menu.Items.Add("")
$settingsItem = $menu.Items.Add("Settings")
$exitItem = $menu.Items.Add("Exit")
$notify.ContextMenuStrip = $menu
$notify.add_DoubleClick({
  try { Set-StatusBarVisible (-not [bool]$script:StatusBarVisible) } catch { Write-Log "Toggle visibility failed: $($_.Exception.Message)" }
})
$exitItem.add_Click({
  try { $form.Close() } catch { Write-Log "Exit failed: $($_.Exception.Message)" }
})
$settingsItem.add_Click({
  try { Show-SettingsDialog } catch { Write-Log "Settings open failed: $($_.Exception.Message)" }
})
Write-Log "Created tray icon"

function Update-StatusBarVisibilityMenu {
  try {
    if ($null -ne $visibilityItem) {
      if ([bool]$script:StatusBarVisible) {
        $visibilityItem.Text = "Hide Status Bar"
      } else {
        $visibilityItem.Text = "Show Status Bar"
      }
    }
  } catch {}
}
Update-StatusBarVisibilityMenu

function Set-AutoHide {
  param([bool]$Enabled)
  if ($script:DockMode -eq "reserved") {
    $Enabled = $false
  }
  $script:AutoHideEnabled = $Enabled
  if (-not [bool]$script:StatusBarVisible) {
    if ($script:Settings) {
      $script:Settings.autoHide = [bool]$script:AutoHideEnabled
      Save-LimitDockSettings $script:Settings
    }
    Update-AutoHideButtonText
    Sync-AutoHidePinGlyph
    return
  }
  $form.Visible = $true
  if ($form.WindowState -ne [System.Windows.Forms.FormWindowState]::Normal) {
    $form.WindowState = [System.Windows.Forms.FormWindowState]::Normal
  }
  $form.TopMost = $true
  Move-LimitDockToDockState (-not [bool]$script:AutoHideEnabled)
  if ($script:Settings) {
    $script:Settings.autoHide = [bool]$script:AutoHideEnabled
    Save-LimitDockSettings $script:Settings
  }
  Update-AutoHideButtonText
  Sync-AutoHidePinGlyph
}

function Set-StatusBarVisible {
  param([bool]$Visible)
  $script:StatusBarVisible = [bool]$Visible
  if ($script:Settings) {
    if (-not ($script:Settings.PSObject.Properties.Name -contains "statusBarVisible")) {
      $script:Settings | Add-Member -NotePropertyName statusBarVisible -NotePropertyValue $script:StatusBarVisible -Force
    } else {
      $script:Settings.statusBarVisible = $script:StatusBarVisible
    }
    Save-LimitDockSettings $script:Settings
  }
  Update-StatusBarVisibilityMenu

  if (-not [bool]$script:StatusBarVisible) {
    try { $timer.Stop() } catch {}
    try { $hoverTimer.Stop() } catch {}
    Unregister-LimitDockAppBar
    try { $form.Hide() } catch { $form.Visible = $false }
    return
  }

  try { $timer.Start() } catch {}
  try { $hoverTimer.Start() } catch {}
  $form.Visible = $true
  Apply-DockMode $script:DockMode
  Set-AutoHide ([bool]$script:AutoHideEnabled)
  Render-Cards
}

function Toggle-AutoHide {
  Set-AutoHide (-not $script:AutoHideEnabled)
}

function Update-AutoHideButtonText {
  if ($null -ne $script:AutoHideLabel) {
    $script:AutoHideLabel.Text = Get-AutoHidePinLabelText
  }
}

function Invoke-FindDockControl {
  param(
    [System.Windows.Forms.Control]$Root,
    [string]$Name
  )
  if ($null -eq $Root) {
    return $null
  }
  $found = $Root.Controls.Find($Name, $true)
  if ($found -and $found.Length -gt 0) {
    return $found[0]
  }
  return $null
}

function Load-OuDocumentForEditor {
  param([string]$Path)
  $template = '{"auto_detect":true,"data":{"time_window":"30d","retention_days":30},"ui":{"refresh_interval_seconds":30,"warn_threshold":0.2,"crit_threshold":0.05},"experimental":{"analytics":false}}'
  if (-not (Test-Path -LiteralPath $Path)) {
    return ($template | ConvertFrom-Json)
  }
  try {
    $raw = Get-Content -LiteralPath $Path -Raw
    if ([string]::IsNullOrWhiteSpace($raw)) {
      throw "empty"
    }
    return ($raw | ConvertFrom-Json)
  } catch {
    Write-Log "OpenUsage settings unreadable (${Path}): $($_.Message)"
    return ($template | ConvertFrom-Json)
  }
}

function Ensure-OuEditorBranches([pscustomobject]$ouk) {
  if ($null -eq $ouk) {
    return
  }
  $pn = @($ouk.PSObject.Properties | ForEach-Object { $_.Name })
  if (-not ($pn -contains "auto_detect")) {
    $ouk | Add-Member -NotePropertyName auto_detect -NotePropertyValue $true -Force
  }
  if ($null -eq $ouk.auto_detect) {
    $ouk.auto_detect = $true
  }

  if (-not ($pn -contains "data") -or ($null -eq $ouk.data) -or (-not ($ouk.data -is [pscustomobject]))) {
    $ouk | Add-Member -NotePropertyName data -NotePropertyValue ([pscustomobject]@{ time_window = "30d"; retention_days = 30 }) -Force
  }
  $dn = @($ouk.data.PSObject.Properties | ForEach-Object { $_.Name })
  if (-not ($dn -contains "time_window")) {
    $ouk.data | Add-Member -NotePropertyName time_window -NotePropertyValue "30d" -Force
  }
  if (-not ($dn -contains "retention_days")) {
    $ouk.data | Add-Member -NotePropertyName retention_days -NotePropertyValue 30 -Force
  }

  if (-not ($pn -contains "ui") -or ($null -eq $ouk.ui) -or (-not ($ouk.ui -is [pscustomobject]))) {
    $ouk | Add-Member -NotePropertyName ui -NotePropertyValue ([pscustomobject]@{ refresh_interval_seconds = 30; warn_threshold = 0.2; crit_threshold = 0.05 }) -Force
  }
  $un = @($ouk.ui.PSObject.Properties | ForEach-Object { $_.Name })
  if (-not ($un -contains "refresh_interval_seconds")) {
    $ouk.ui | Add-Member -NotePropertyName refresh_interval_seconds -NotePropertyValue 30 -Force
  }
  if (-not ($un -contains "warn_threshold")) {
    $ouk.ui | Add-Member -NotePropertyName warn_threshold -NotePropertyValue 0.2 -Force
  }
  if (-not ($un -contains "crit_threshold")) {
    $ouk.ui | Add-Member -NotePropertyName crit_threshold -NotePropertyValue 0.05 -Force
  }

  if (-not ($pn -contains "experimental") -or ($null -eq $ouk.experimental) -or (-not ($ouk.experimental -is [pscustomobject]))) {
    $ouk | Add-Member -NotePropertyName experimental -NotePropertyValue ([pscustomobject]@{ analytics = $false }) -Force
  }
  $en = @($ouk.experimental.PSObject.Properties | ForEach-Object { $_.Name })
  if (-not ($en -contains "analytics")) {
    $ouk.experimental | Add-Member -NotePropertyName analytics -NotePropertyValue $false -Force
  }
}

function Show-SettingsDialog {
  try {
    if ($script:SettingsDialogOpen) {
      Write-Log "Settings dialog already open"
      return
    }
    $script:SettingsDialogOpen = $true

    $ouCfgPath = Get-OpenUsageJsonPath
    $ouk = Load-OuDocumentForEditor $ouCfgPath
    Ensure-OuEditorBranches $ouk

    $settingsForm = New-Object System.Windows.Forms.Form
    $settingsForm.Text = "LimitDock Settings"
    $settingsForm.Width = 640
    $settingsForm.Height = 540
    $settingsForm.StartPosition = [System.Windows.Forms.FormStartPosition]::CenterScreen
    $settingsForm.TopMost = $true
    $settingsForm.MaximizeBox = $false
    $settingsForm.MinimizeBox = $false
    $settingsForm.FormBorderStyle = [System.Windows.Forms.FormBorderStyle]::FixedDialog
    $settingsForm.BackColor = $script:Theme.Bar
    $settingsForm.ForeColor = $script:Theme.Fore
    $settingsForm.Tag = @{ OpenUsageDraft = $ouk }

    $labelFont = New-Object System.Drawing.Font("Segoe UI", 9)
    $hintFont = New-Object System.Drawing.Font("Segoe UI", 8)

    function Add-ChkTab($scroll, [ref]$y, $text, $checked, [string]$name) {
      $c = New-Object System.Windows.Forms.CheckBox
      $c.Text = $text
      $c.Checked = $checked
      $c.Left = 16
      $c.Top = $y.Value
      $c.Width = 580
      $c.Font = $labelFont
      $c.ForeColor = $script:Theme.Fore
      $c.BackColor = $scroll.BackColor
      $c.Name = $name
      $scroll.Controls.Add($c)
      $y.Value += 30
      return $c
    }

    function Add-NudPair($scroll, [ref]$y, $labelTxt, [decimal]$min, [decimal]$max, [decimal]$val, [string]$name) {
      $lab = New-Object System.Windows.Forms.Label
      $lab.Text = $labelTxt
      $lab.Left = 16
      $lab.Top = $y.Value + 3
      $lab.Width = 220
      $lab.Font = $labelFont
      $lab.ForeColor = $script:Theme.Fore
      $lab.BackColor = $scroll.BackColor
      $scroll.Controls.Add($lab)

      $n = New-Object System.Windows.Forms.NumericUpDown
      $n.Left = 240
      $n.Top = $y.Value
      $n.Width = 100
      $n.Minimum = $min
      $n.Maximum = $max
      $n.Value = [Math]::Max($min, [Math]::Min($max, $val))
      $n.Font = $labelFont
      $n.Name = $name
      $scroll.Controls.Add($n)
      $y.Value += 32
      return $n
    }

    function Add-LabelBlock($scroll, [ref]$y, $text, $height, [int]$ContentWidthPx = 580) {
      $lab = New-Object System.Windows.Forms.Label
      $lab.Text = $text
      $lab.Left = 16
      $lab.Top = $y.Value
      $lab.Width = $ContentWidthPx
      $lab.Height = $height
      $lab.Font = $hintFont
      $lab.ForeColor = $script:Theme.MutedFore
      $lab.BackColor = $scroll.BackColor
      $scroll.Controls.Add($lab)
      $y.Value += $height + 6
    }

    function Add-SectionHeading($scroll, [ref]$y, [string]$Title, [int]$ContentWidthPx = 580) {
      $t = New-Object System.Windows.Forms.Label
      $t.Text = $Title
      $t.Left = 16
      $t.Top = $y.Value
      $t.Width = $ContentWidthPx
      $t.Height = 22
      $t.Font = New-Object System.Drawing.Font("Segoe UI", 9.5, [System.Drawing.FontStyle]::Bold)
      $t.ForeColor = $script:Theme.Fore
      $t.BackColor = $scroll.BackColor
      $scroll.Controls.Add($t)
      $line = New-Object System.Windows.Forms.Panel
      $line.Left = 16
      $line.Top = $y.Value + 26
      $line.Width = $ContentWidthPx
      $line.Height = 2
      $line.BackColor = $script:Theme.MutedFore
      $scroll.Controls.Add($line)
      $y.Value += 42
    }

    $tabs = New-Object System.Windows.Forms.TabControl
    $tabs.Dock = [System.Windows.Forms.DockStyle]::Fill
    $tabs.Font = $labelFont

    $tpLd = New-Object System.Windows.Forms.TabPage
    $tpLd.Text = "LimitDock"
    $tpLd.BackColor = $script:Theme.Bar
    $scLd = New-Object System.Windows.Forms.Panel
    $scLd.Dock = [System.Windows.Forms.DockStyle]::Fill
    $scLd.AutoScroll = $true
    $scLd.BackColor = $script:Theme.Bar
    $y0 = 14

    Add-SectionHeading $scLd ([ref]$y0) "Status bar"
    $modeLabel = New-Object System.Windows.Forms.Label
    $modeLabel.Text = "Display mode"
    $modeLabel.Left = 16
    $modeLabel.Top = $y0 + 4
    $modeLabel.Width = 220
    $modeLabel.Font = $labelFont
    $modeLabel.ForeColor = $script:Theme.Fore
    $modeLabel.BackColor = $scLd.BackColor
    $scLd.Controls.Add($modeLabel)

    $modeCombo = New-Object System.Windows.Forms.ComboBox
    $modeCombo.Left = 240
    $modeCombo.Top = $y0
    $modeCombo.Width = 160
    $modeCombo.DropDownStyle = [System.Windows.Forms.ComboBoxStyle]::DropDownList
    $modeCombo.Name = "limitdock_dockMode"
    [void]$modeCombo.Items.Add("overlay")
    [void]$modeCombo.Items.Add("reserved")
    $modeCombo.SelectedItem = (Normalize-DockMode $script:DockMode)
    $scLd.Controls.Add($modeCombo)
    $y0 += 34

    $edgeLabel = New-Object System.Windows.Forms.Label
    $edgeLabel.Text = "Dock edge"
    $edgeLabel.Left = 16
    $edgeLabel.Top = $y0 + 4
    $edgeLabel.Width = 220
    $edgeLabel.Font = $labelFont
    $edgeLabel.ForeColor = $script:Theme.Fore
    $edgeLabel.BackColor = $scLd.BackColor
    $scLd.Controls.Add($edgeLabel)

    $edgeCombo = New-Object System.Windows.Forms.ComboBox
    $edgeCombo.Left = 240
    $edgeCombo.Top = $y0
    $edgeCombo.Width = 160
    $edgeCombo.DropDownStyle = [System.Windows.Forms.ComboBoxStyle]::DropDownList
    $edgeCombo.Name = "limitdock_dockEdge"
    [void]$edgeCombo.Items.Add("bottom")
    [void]$edgeCombo.Items.Add("top")
    [void]$edgeCombo.Items.Add("left")
    [void]$edgeCombo.Items.Add("right")
    $edgeCombo.SelectedItem = (Normalize-DockEdge $script:DockEdge)
    $scLd.Controls.Add($edgeCombo)
    $y0 += 34
    Add-LabelBlock $scLd ([ref]$y0) "Overlay floats above other windows. Reserved sets a Windows reserved work area so maximized windows leave room for the ribbon." 44

    $slideCheck = Add-ChkTab $scLd ([ref]$y0) "Bar slides away at edge (unpinned peek)" ([bool]$script:AutoHideEnabled) "limitdock_slideCheck"
    Add-LabelBlock $scLd ([ref]$y0) "Pinned keeps the ribbon visible above your taskbar. Unpinned docks it just off-screen and reveals it when you skim the monitor edge." 44

    Add-SectionHeading $scLd ([ref]$y0) "LimitDock refresh loop"
    $dockRefSec = [decimal][int]$script:Settings.refreshSeconds
    if ($dockRefSec -lt 5) {
      $dockRefSec = 5
    }
    if ($dockRefSec -gt 600) {
      $dockRefSec = 600
    }
    [void](Add-NudPair $scLd ([ref]$y0) "Polling interval seconds (bar + probes)" 5 600 $dockRefSec "limitdock_refreshSec")
    Add-LabelBlock $scLd ([ref]$y0) "Lower intervals feel more live but hammer OpenUsage. Match this with your telemetry tolerance (OpenUsage tab also has ui.refresh_interval_seconds)." 52

    Add-SectionHeading $scLd ([ref]$y0) "Usage gauges per provider card"
    Add-LabelBlock $scLd ([ref]$y0) (
      "Gauge rows show remaining quota percentages only, with the shortest OpenUsage metering window first and the lowest remaining percent before higher.`r`n" +
      "Throughput/tool-call, spend, request, token, and model-usage rows stay off the ribbon.`r`n" +
      "The summary line pulls from that same prioritized row so non-quota activity can't crowd the bar.") 70

    $bands = [int]$script:Settings.gaugeMaxBands
    if ($bands -lt 1) {
      $bands = 1
    }
    if ($bands -gt [int]$script:DockRibbonGaugeCap) {
      $bands = [int]$script:DockRibbonGaugeCap
    }
    [void](Add-NudPair $scLd ([ref]$y0) "Max visible quota rows per provider card (1..4)" 1 4 $bands "limitdock_bandMax")

    $gw = [int]$script:Settings.gaugeWarnPercent
    $gc = [int]$script:Settings.gaugeCritPercent
    $gw = [Math]::Max(1, [Math]::Min(99, $gw))
    $gc = [Math]::Max(1, [Math]::Min(99, $gc))
    [void](Add-NudPair $scLd ([ref]$y0) "Card warn tint crosses at used (%)" 1 99 $gw "limitdock_gaugeWarn")
    [void](Add-NudPair $scLd ([ref]$y0) "Card critical tint crosses at used (%)" 1 99 $gc "limitdock_gaugeCrit")

    Add-SectionHeading $scLd ([ref]$y0) "Local AI footprint helper"
    Add-LabelBlock $scLd ([ref]$y0) ("Even when Gemini Antigravity is not installed, LimitDock probes Cursor, Gemini, Claude, Anthropic, and Codeium cache folders.`r`n" +
      "Customize the subtitle to label mixed stacks (`"Claude + Gemini CLI`"). Leave paths blank unless you relocate installs.") 78

    $agEnabledNow = $true
    if ($script:Settings.antigravity -and $script:Settings.antigravity.enabled -eq $false) {
      $agEnabledNow = $false
    }
    $agCheck = Add-ChkTab $scLd ([ref]$y0) "Keep the consolidated local-AI footprint card pinned in the ribbon" $agEnabledNow "limitdock_agCheck"

    $subLabel = New-Object System.Windows.Forms.Label
    $subLabel.Text = "Card subtitle (optional, e.g. Claude | Gemini Codespaces)"
    $subLabel.Left = 16
    $subLabel.Top = $y0 + 3
    $subLabel.Width = 360
    $subLabel.Font = $labelFont
    $subLabel.ForeColor = $script:Theme.Fore
    $subLabel.BackColor = $scLd.BackColor
    $scLd.Controls.Add($subLabel)

    $subText = New-Object System.Windows.Forms.TextBox
    $subText.Name = "limitdock_agSubtitle"
    $subText.Left = 16
    $subText.Top = $y0 + 28
    $subText.Width = 580
    if ($script:Settings.antigravity) {
      $subText.Text = [string]$script:Settings.antigravity.subtitle
    }
    $subText.Multiline = $false
    $subText.ScrollBars = [System.Windows.Forms.ScrollBars]::None
    $scLd.Controls.Add($subText)

    $y0 += 64

    $dataLabel = New-Object System.Windows.Forms.Label
    $dataLabel.Text = "Override Antigravity / Gemini workspace path"
    $dataLabel.Left = 16
    $dataLabel.Top = $y0 + 3
    $dataLabel.Width = 360
    $dataLabel.Font = $labelFont
    $dataLabel.ForeColor = $script:Theme.Fore
    $dataLabel.BackColor = $scLd.BackColor
    $scLd.Controls.Add($dataLabel)

    $dataText = New-Object System.Windows.Forms.TextBox
    $dataText.Left = 16
    $dataText.Top = $y0 + 28
    $dataText.Width = 580
    if ($script:Settings.antigravity) {
      $dataText.Text = [string]$script:Settings.antigravity.dataDir
    }
    $dataText.Name = "limitdock_dataText"
    $scLd.Controls.Add($dataText)
    $y0 += 64

    $binLabel = New-Object System.Windows.Forms.Label
    $binLabel.Text = "Override Gemini Antigravity binary"
    $binLabel.Left = 16
    $binLabel.Top = $y0 + 3
    $binLabel.Width = 360
    $binLabel.Font = $labelFont
    $binLabel.ForeColor = $script:Theme.Fore
    $binLabel.BackColor = $scLd.BackColor
    $scLd.Controls.Add($binLabel)

    $binText = New-Object System.Windows.Forms.TextBox
    $binText.Left = 16
    $binText.Top = $y0 + 28
    $binText.Width = 580
    if ($script:Settings.antigravity) {
      $binText.Text = [string]$script:Settings.antigravity.binaryPath
    }
    $binText.Name = "limitdock_binText"
    $scLd.Controls.Add($binText)

    $tpLd.Controls.Add($scLd)
    $tabs.TabPages.Add($tpLd)

    $tpOu = New-Object System.Windows.Forms.TabPage
    $tpOu.Text = "OpenUsage"
    $tpOu.BackColor = $script:Theme.Bar
    $scOu = New-Object System.Windows.Forms.Panel
    $scOu.Dock = [System.Windows.Forms.DockStyle]::Fill
    $scOu.AutoScroll = $true
    $scOu.BackColor = $script:Theme.Bar
    $y1 = 14

    Add-SectionHeading $scOu ([ref]$y1) "Telemetry service mirrors OpenUsage.sh"
    Add-LabelBlock $scOu ([ref]$y1) ("Writes UTF-8 (no BOM) to %APPDATA%\openusage\settings.json.`r`n" +
      "Accounts, dashboards, Codex merges, dashboard themes stay intact because we deserialize the disk file before saving touched keys only.") 70

    $pathBox = New-Object System.Windows.Forms.TextBox
    $pathBox.Name = "ou_pathBox"
    $pathBox.Left = 16
    $pathBox.Top = $y1
    $pathBox.Width = 580
    $pathBox.ReadOnly = $true
    $pathBox.BorderStyle = [System.Windows.Forms.BorderStyle]::None
    $pathBox.BackColor = $scOu.BackColor
    $pathBox.ForeColor = $script:Theme.Fore
    $pathBox.Font = $labelFont
    $pathBox.Text = $ouCfgPath
    $scOu.Controls.Add($pathBox)
    $y1 += 28

    $openDirBtn = New-Object System.Windows.Forms.Button
    $openDirBtn.Text = "Open settings folder"
    $openDirBtn.Left = 16
    $openDirBtn.Top = $y1
    $openDirBtn.Width = 160
    $openDirBtn.Add_Click({
      try {
        $dir = Split-Path -Parent $ouCfgPath
        if (-not (Test-Path -LiteralPath $dir)) {
          New-Item -ItemType Directory -Force -Path $dir | Out-Null
        }
        Start-Process explorer.exe $dir
      } catch {
        Write-Log "Open OpenUsage folder failed: $($_.Exception.Message)"
      }
    })
    $scOu.Controls.Add($openDirBtn)
    $y1 += 40

    $twLab = New-Object System.Windows.Forms.Label
    $twLab.Text = "data.time_window"
    $twLab.Left = 16
    $twLab.Top = $y1 + 4
    $twLab.Width = 200
    $twLab.Font = $labelFont
    $twLab.ForeColor = $script:Theme.Fore
    $twLab.BackColor = $scOu.BackColor
    $scOu.Controls.Add($twLab)

    $cmbTw = New-Object System.Windows.Forms.ComboBox
    $cmbTw.Left = 240
    $cmbTw.Top = $y1
    $cmbTw.Width = 120
    $cmbTw.DropDownStyle = [System.Windows.Forms.ComboBoxStyle]::DropDownList
    $cmbTw.Name = "ou_timeWindow"
    foreach ($opt in @("1d", "3d", "7d", "30d", "all")) {
      [void]$cmbTw.Items.Add($opt)
    }
    $curTw = ([string]$ouk.data.time_window).Trim().ToLowerInvariant()
    if ([string]::IsNullOrWhiteSpace($curTw)) {
      $curTw = "30d"
    }
    if ($cmbTw.Items.IndexOf($curTw) -ge 0) {
      $cmbTw.SelectedItem = $curTw
    } else {
      [void]$cmbTw.Items.Insert(0, $curTw)
      $cmbTw.SelectedIndex = 0
    }
    $scOu.Controls.Add($cmbTw)
    $y1 += 36

    $retDays = [decimal][int]$ouk.data.retention_days
    if ($retDays -lt 1) {
      $retDays = 1
    }
    if ($retDays -gt 90) {
      $retDays = 90
    }
    [void](Add-NudPair $scOu ([ref]$y1) "data.retention_days (1..90)" 1 90 $retDays "ou_retention")

    $refSec = [decimal][int]$ouk.ui.refresh_interval_seconds
    if ($refSec -lt 5) {
      $refSec = 5
    }
    if ($refSec -gt 3600) {
      $refSec = 3600
    }
    [void](Add-NudPair $scOu ([ref]$y1) "ui.refresh_interval_seconds (5..3600)" 5 3600 $refSec "ou_refresh")

    $wFrac = [decimal][double]$ouk.ui.warn_threshold
    if ($wFrac -lt 0) {
      $wFrac = 0
    }
    if ($wFrac -gt 1) {
      $wFrac = 1
    }
    $nudWarn = Add-NudPair $scOu ([ref]$y1) "ui.warn_threshold (0..1 fraction)" 0 1 $wFrac "ou_warn"
    $nudWarn.DecimalPlaces = 3
    $nudWarn.Increment = 0.01

    $cFrac = [decimal][double]$ouk.ui.crit_threshold
    if ($cFrac -lt 0) {
      $cFrac = 0
    }
    if ($cFrac -gt 1) {
      $cFrac = 1
    }
    $nudCrit = Add-NudPair $scOu ([ref]$y1) "ui.crit_threshold (0..1 fraction)" 0 1 $cFrac "ou_crit"
    $nudCrit.DecimalPlaces = 3
    $nudCrit.Increment = 0.01

    $autoDet = Add-ChkTab $scOu ([ref]$y1) "auto_detect (discover providers / accounts)" ([bool]$ouk.auto_detect) "ou_autoDetect"
    $analytics = Add-ChkTab $scOu ([ref]$y1) "experimental.analytics" ([bool]$ouk.experimental.analytics) "ou_analytics"

    $tpOu.Controls.Add($scOu)
    $tabs.TabPages.Add($tpOu)

    $tpDiag = New-Object System.Windows.Forms.TabPage
    $tpDiag.Text = "Paths / logs"
    $tpDiag.BackColor = $script:Theme.Bar

    $scDx = New-Object System.Windows.Forms.Panel
    $scDx.Dock = [System.Windows.Forms.DockStyle]::Fill
    $scDx.AutoScroll = $true
    $scDx.BackColor = $script:Theme.Bar
    $diagText = ""
    $diagText += ("LimitDock root: $($ScriptRoot)`r`n")
    $diagText += ("settings.json : $($SettingsPath)`r`n")
    $diagText += ("Engine state/logs: $($EngineDir)`r`n")
    $diagText += ("OpenUsage daemon exe: $($OpenUsageExe)`r`n")
    $diagText += ("Read-model probe: $($ProbeExe)`r`n")
    $diagText += ("Daemon socket path: $($SocketPath)`r`n")
    $diagText += ("SQLite / spool dirs: $($StateDir)`r`n")
    $diagText += "`r`nUse this tab like an OpenUsage diagnostics console to copy routes when filing issues.`r`n"

    $dxBanner = New-Object System.Windows.Forms.Label
    $dxBanner.Text = "Runtime paths snapshot"
    $dxBanner.Left = 16
    $dxBanner.Top = 12
    $dxBanner.Width = 580
    $dxBanner.Height = 26
    $dxBanner.Font = New-Object System.Drawing.Font("Segoe UI", 10, [System.Drawing.FontStyle]::Bold)
    $dxBanner.ForeColor = $script:Theme.Fore
    $dxBanner.BackColor = $scDx.BackColor
    $scDx.Controls.Add($dxBanner)

    $dividerDx = New-Object System.Windows.Forms.Panel
    $dividerDx.Left = 16
    $dividerDx.Top = 40
    $dividerDx.Width = 580
    $dividerDx.Height = 2
    $dividerDx.BackColor = $script:Theme.MutedFore
    $scDx.Controls.Add($dividerDx)

    $diagMemTop = 48
    $diagBox = New-Object System.Windows.Forms.TextBox
    $diagBox.Name = "limitdock_diagMemo"
    $diagBox.Multiline = $true
    $diagBox.ScrollBars = [System.Windows.Forms.ScrollBars]::Vertical
    $diagBox.ReadOnly = $true
    $diagBox.WordWrap = $false
    $diagBox.BorderStyle = [System.Windows.Forms.BorderStyle]::FixedSingle
    $diagBox.Left = 16
    $diagBox.Top = $diagMemTop
    $diagBox.Width = 580
    $diagBox.Height = 288
    $diagBox.BackColor = $script:Theme.Panel
    $diagBox.ForeColor = $script:Theme.Fore
    $diagBox.Font = New-Object System.Drawing.Font("Consolas", 9)
    $diagBox.Text = $diagText

    $scDx.Controls.Add($diagBox)

    $openRootBtn = New-Object System.Windows.Forms.Button
    $openRootBtn.Text = "Browse LimitDock root"
    $openRootBtn.Left = 16
    $openRootBtn.Top = $diagMemTop + 298
    $openRootBtn.Width = 164
    $openRootBtn.Add_Click({
      try { Start-Process explorer.exe $ScriptRoot } catch {
        Write-Log "Explorer open root failed: $($_.Exception.Message)"
      }
    })
    $openEngineBtn = New-Object System.Windows.Forms.Button
    $openEngineBtn.Text = "Browse engine/logs"
    $openEngineBtn.Left = 188
    $openEngineBtn.Top = $diagMemTop + 298
    $openEngineBtn.Width = 164
    $openEngineBtn.Add_Click({
      try { Start-Process explorer.exe $EngineDir } catch {
        Write-Log "Explorer open engine dir failed: $($_.Exception.Message)"
      }
    })
    $copyDiagBtn = New-Object System.Windows.Forms.Button
    $copyDiagBtn.Text = "Copy block"
    $copyDiagBtn.Left = 360
    $copyDiagBtn.Top = $diagMemTop + 298
    $copyDiagBtn.Width = 120
    $copyDiagBtn.Add_Click({
      try {
        [System.Windows.Forms.Clipboard]::SetText([string]$diagBox.Text)
      } catch {
        Write-Log "Clipboard failed: $($_.Exception.Message)"
      }
    })

    $scDx.Controls.Add($openRootBtn)
    $scDx.Controls.Add($openEngineBtn)
    $scDx.Controls.Add($copyDiagBtn)
    $tpDiag.Controls.Add($scDx)
    $tabs.TabPages.Add($tpDiag)

    $footer = New-Object System.Windows.Forms.Panel
    $footer.Dock = [System.Windows.Forms.DockStyle]::Bottom
    $footer.Height = 52
    $footer.BackColor = $script:Theme.Bar

    $saveButton = New-Object System.Windows.Forms.Button
    $saveButton.Text = "Save"
    $saveButton.Left = 406
    $saveButton.Top = 12
    $saveButton.Width = 88
    $saveButton.Add_Click({
      try {
        $form2 = $this.FindForm()
        $slide = [System.Windows.Forms.CheckBox](Invoke-FindDockControl $form2 "limitdock_slideCheck")
        $modeCtl = [System.Windows.Forms.ComboBox](Invoke-FindDockControl $form2 "limitdock_dockMode")
        $edgeCtl = [System.Windows.Forms.ComboBox](Invoke-FindDockControl $form2 "limitdock_dockEdge")
        $ag = [System.Windows.Forms.CheckBox](Invoke-FindDockControl $form2 "limitdock_agCheck")
        $agSubCtl = Invoke-FindDockControl $form2 "limitdock_agSubtitle"
        $data = [System.Windows.Forms.TextBox](Invoke-FindDockControl $form2 "limitdock_dataText")
        $bin = [System.Windows.Forms.TextBox](Invoke-FindDockControl $form2 "limitdock_binText")
        $nb = [System.Windows.Forms.NumericUpDown](Invoke-FindDockControl $form2 "limitdock_bandMax")
        $nw = [System.Windows.Forms.NumericUpDown](Invoke-FindDockControl $form2 "limitdock_gaugeWarn")
        $nc = [System.Windows.Forms.NumericUpDown](Invoke-FindDockControl $form2 "limitdock_gaugeCrit")
        $dockRefCtl = [System.Windows.Forms.NumericUpDown](Invoke-FindDockControl $form2 "limitdock_refreshSec")

        $cmb = [System.Windows.Forms.ComboBox](Invoke-FindDockControl $form2 "ou_timeWindow")
        $nRet = [System.Windows.Forms.NumericUpDown](Invoke-FindDockControl $form2 "ou_retention")
        $nRef = [System.Windows.Forms.NumericUpDown](Invoke-FindDockControl $form2 "ou_refresh")
        $nW = [System.Windows.Forms.NumericUpDown](Invoke-FindDockControl $form2 "ou_warn")
        $nC = [System.Windows.Forms.NumericUpDown](Invoke-FindDockControl $form2 "ou_crit")
        $chkDet = [System.Windows.Forms.CheckBox](Invoke-FindDockControl $form2 "ou_autoDetect")
        $chkAn = [System.Windows.Forms.CheckBox](Invoke-FindDockControl $form2 "ou_analytics")

        if (-not $script:Settings.antigravity) {
          $script:Settings | Add-Member -NotePropertyName antigravity -NotePropertyValue ([pscustomobject]@{
              enabled    = $true
              binaryPath = ""
              dataDir    = ""
              subtitle   = ""
            }) -Force
        }
        elseif (-not ($script:Settings.antigravity.PSObject.Properties.Name -contains "subtitle")) {
          $script:Settings.antigravity | Add-Member -NotePropertyName subtitle -NotePropertyValue ""
        }

        [int]$newDockRefresh = [Math]::Max(5, [int]$dockRefCtl.Value)
        [string]$newDockMode = Normalize-DockMode $modeCtl.SelectedItem
        [string]$newDockEdge = Normalize-DockEdge $edgeCtl.SelectedItem

        $script:Settings.dockMode = $newDockMode
        $script:Settings.dockEdge = $newDockEdge
        $script:DockEdge = $newDockEdge
        if ($newDockMode -eq "reserved") {
          $script:Settings.autoHide = $false
        } else {
          $script:Settings.autoHide = [bool]$slide.Checked
        }
        $script:Settings.refreshSeconds = $newDockRefresh
        $script:DockRefreshSeconds = $newDockRefresh
        try { $timer.Interval = [Math]::Max(5000, $newDockRefresh * 1000) } catch { Write-Log "Timer reschedule failed: $($_.Exception.Message)" }

        $script:Settings.antigravity.enabled = [bool]$ag.Checked
        $script:Settings.antigravity.dataDir = [string]$data.Text
        $script:Settings.antigravity.binaryPath = [string]$bin.Text
        if ($null -eq $script:Settings.antigravity.subtitle) {
          $script:Settings.antigravity.subtitle = ""
        }
        if ($agSubCtl -is [System.Windows.Forms.TextBox]) {
          $script:Settings.antigravity.subtitle = [string]$agSubCtl.Text
        }
        $script:Settings.gaugeMaxBands = [int]$nb.Value
        $script:Settings.gaugeWarnPercent = [int]$nw.Value
        $script:Settings.gaugeCritPercent = [int]$nc.Value

        $ouPatch = $form2.Tag.OpenUsageDraft
        Ensure-OuEditorBranches $ouPatch
        $ouPatch.auto_detect = [bool]$chkDet.Checked
        $ouPatch.data.time_window = [string]$cmb.SelectedItem
        $ouPatch.data.retention_days = [int]$nRet.Value
        $ouPatch.ui.refresh_interval_seconds = [int]$nRef.Value
        $ouPatch.ui.warn_threshold = [double]$nW.Value
        $ouPatch.ui.crit_threshold = [double]$nC.Value
        $ouPatch.experimental.analytics = [bool]$chkAn.Checked

        $jsonOu = $ouPatch | ConvertTo-Json -Depth 35
        $dirOu = Split-Path -Parent $ouCfgPath
        if (-not (Test-Path -LiteralPath $dirOu)) {
          New-Item -ItemType Directory -Force -Path $dirOu | Out-Null
        }
        Write-Utf8NoBom $ouCfgPath $jsonOu

        Save-LimitDockSettings $script:Settings
        Apply-DockMode $newDockMode
        Set-AutoHide ([bool]$script:Settings.autoHide)
        Render-Cards
        $form2.DialogResult = [System.Windows.Forms.DialogResult]::OK
        $form2.Close()
      } catch {
        Write-Log "Settings save failed: $($_.Exception.Message)"
        try {
          [System.Windows.Forms.MessageBox]::Show(
            "Could not save settings: $($_.Exception.Message)",
            "LimitDock", "OK", "Warning"
          ) | Out-Null
        } catch {}
      }
    })

    $cancelButton = New-Object System.Windows.Forms.Button
    $cancelButton.Text = "Cancel"
    $cancelButton.Left = 502
    $cancelButton.Top = 12
    $cancelButton.Width = 88
    $cancelButton.Add_Click({
      try {
        $form2 = $this.FindForm()
        $form2.DialogResult = [System.Windows.Forms.DialogResult]::Cancel
        $form2.Close()
      } catch {
        Write-Log "Settings cancel failed: $($_.Exception.Message)"
      }
    })

    $footer.Controls.Add($saveButton)
    $footer.Controls.Add($cancelButton)
    # Bottom-docked bar first, then Fill tab area (WinForms: Fill control last).
    $settingsForm.Controls.Add($footer)
    $settingsForm.Controls.Add($tabs)

    $settingsForm.AcceptButton = $saveButton
    $settingsForm.CancelButton = $cancelButton

    [void]$settingsForm.ShowDialog()
    $settingsForm.Dispose()
  } catch {
    Write-Log "Show-SettingsDialog failed: $($_.Exception.Message)"
    try {
      [System.Windows.Forms.MessageBox]::Show(
        "Settings dialog failed to open: $($_.Exception.Message)",
        "LimitDock", "OK", "Warning"
      ) | Out-Null
    } catch {}
  } finally {
    $script:SettingsDialogOpen = $false
  }
}

function Render-Cards {
  if (-not [bool]$script:StatusBarVisible) {
    return
  }
  $oldControls = @($panel.Controls)
  Set-ControlRedraw $panel $false
  $panel.SuspendLayout()
  try {
    $panel.Controls.Clear()
    $panel.Controls.Add((New-ToolRail)) | Out-Null

    $cards = Get-LimitDockCards
    foreach ($card in $cards) {
      $panel.Controls.Add((New-ProviderCardControl $card)) | Out-Null
    }

    $statusLabel.Text = "Updated " + (Get-Date -Format "HH:mm:ss")
    Set-CardLabelStyle $statusLabel "status"
    $statusLabel.ForeColor = $script:Theme.StatusAccent
    $tooltip = "LimitDock - " + (($cards | ForEach-Object { "$($_.Name) $($_.Main)" }) -join " | ")
    if ($tooltip.Length -gt 63) {
      $tooltip = $tooltip.Substring(0, 60) + "..."
    }
    $notify.Text = $tooltip
  } catch {
    $statusLabel.Text = "LimitDock waiting for OpenUsage"
    Set-CardLabelStyle $statusLabel "warn"
    $statusLabel.ForeColor = $script:Theme.WarnFore
    $notify.Text = "LimitDock - waiting"
    Write-Log "Render failed: $($_.Exception.Message)"
  } finally {
    $panel.ResumeLayout($true)
    Set-ControlRedraw $panel $true
    try { $panel.Invalidate($true) } catch {}
    foreach ($old in $oldControls) {
      try { $old.Dispose() } catch {}
    }
  }
}

$visibilityItem.add_Click({
  try { Set-StatusBarVisible (-not [bool]$script:StatusBarVisible) } catch { Write-Log "Toggle status bar failed: $($_.Exception.Message)" }
})
$timer = New-Object System.Windows.Forms.Timer
$timer.Interval = [math]::Max(5, $script:DockRefreshSeconds) * 1000
$timer.Add_Tick({
  try {
    if ([bool]$script:StatusBarVisible) {
      Render-Cards
    }
  } catch { Write-Log "Timer tick failed: $($_.Exception.Message)" }
})
$timer.Start()

$hoverTimer = New-Object System.Windows.Forms.Timer
$hoverTimer.Interval = 120
$script:LastRevealAt = [datetime]::MinValue
$hoverTimer.Add_Tick({
  try {
    if ((-not [bool]$script:StatusBarVisible) -or (-not [bool]$script:AutoHideEnabled)) {
      return
    }

    $cursor = [System.Windows.Forms.Cursor]::Position
    [string]$edge = Normalize-DockEdge $script:DockEdge
    [bool]$side = Test-DockEdgeIsSide $edge
    [bool]$shown = $false
    if ($side) {
      $shown = ([Math]::Abs([int]$form.Left - [int]$script:ShowLeft) -le 2)
    } else {
      $shown = ([Math]::Abs([int]$form.Top - [int]$script:ShowTop) -le 2)
    }

    if ($shown) {
      [int]$extendedLeft = [int]$script:ShowLeft
      [int]$extendedRight = [int]($script:ShowLeft + $form.Width)
      [int]$extendedTop = [int]$script:ShowTop
      [int]$extendedBottom = [int]($script:ShowTop + $form.Height)
      if ($edge -eq "top") {
        $extendedTop = [int]$script:Bounds.Top
        $extendedBottom += 40
      } elseif ($edge -eq "bottom") {
        $extendedTop -= 40
        $extendedBottom = [int]$script:Bounds.Bottom
      } elseif ($edge -eq "left") {
        $extendedLeft = [int]$script:Bounds.Left
        $extendedRight += 40
      } elseif ($edge -eq "right") {
        $extendedLeft -= 40
        $extendedRight = [int]$script:Bounds.Right
      }
      $stayVisible = ($cursor.X -ge $extendedLeft) -and ($cursor.X -le $extendedRight) -and ($cursor.Y -ge $extendedTop) -and ($cursor.Y -le $extendedBottom)
      if ($stayVisible) {
        $script:LastRevealAt = Get-Date
        if ($form.TopMost -ne $true) { $form.TopMost = $true }
      } else {
        $recentReveal = (((Get-Date) - $script:LastRevealAt).TotalMilliseconds -lt 700)
        if (-not $recentReveal) {
          Move-LimitDockToDockState $false
        }
      }
    } else {
      [bool]$inTrigger = $false
      if ($edge -eq "top") {
        $inTrigger = ($cursor.Y -ge [int]$script:Bounds.Top) -and ($cursor.Y -le ([int]$script:Screen.Top + 4))
      } elseif ($edge -eq "bottom") {
        $inTrigger = ($cursor.Y -ge ([int]$script:Screen.Bottom - 4)) -and ($cursor.Y -le [int]$script:Bounds.Bottom)
      } elseif ($edge -eq "left") {
        $inTrigger = ($cursor.X -ge [int]$script:Bounds.Left) -and ($cursor.X -le ([int]$script:Screen.Left + 4))
      } elseif ($edge -eq "right") {
        $inTrigger = ($cursor.X -ge ([int]$script:Screen.Right - 4)) -and ($cursor.X -le [int]$script:Bounds.Right)
      }
      if ($inTrigger) {
        $form.TopMost = $true
        Move-LimitDockToDockState $true
        $form.BringToFront()
        $script:LastRevealAt = Get-Date
      }
    }
  } catch {
    Write-Log "HoverTimer tick failed: $($_.Exception.Message)"
  }
})
$hoverTimer.Start()

$form.Add_Shown({
  try {
    if (-not [bool]$script:StatusBarVisible) {
      Set-StatusBarVisible $false
      return
    }
    Apply-DockMode $script:DockMode
    Set-AutoHide ([bool]$script:AutoHideEnabled)
    Render-Cards
  } catch {
    Write-Log "Add_Shown failed: $($_.Exception.Message)"
  }
})
$form.Add_FormClosed({
  try {
    $timer.Stop()
    $hoverTimer.Stop()
    Unregister-LimitDockAppBar
    $notify.Visible = $false
    $notify.Dispose()
    Stop-OpenUsageDaemon $daemon
    Remove-Item -LiteralPath $AppPidPath -Force -ErrorAction SilentlyContinue
  } catch {
    Write-Log "FormClosed cleanup failed: $($_.Exception.Message)"
  } finally {
    try {
      if ($script:LimitDockMutex) {
        $script:LimitDockMutex.ReleaseMutex()
        $script:LimitDockMutex.Dispose()
        $script:LimitDockMutex = $null
      }
    } catch {
      Write-Log "Mutex release failed: $($_.Exception.Message)"
    }
  }
})

[System.Windows.Forms.Application]::EnableVisualStyles()
Write-Log "Entering WinForms application loop"
[System.Windows.Forms.Application]::Run($form)
Write-Log "Exited WinForms application loop"
