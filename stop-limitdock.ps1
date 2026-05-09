$ScriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$StateDir = Join-Path $ScriptRoot "engine\state"
$AppPidPath = Join-Path $StateDir "limitdock.pid"
$DaemonPidPath = Join-Path $StateDir "openusage-daemon.pid"

foreach ($pidPath in @($AppPidPath, $DaemonPidPath)) {
  if (Test-Path $pidPath) {
    $pidText = Get-Content $pidPath -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($pidText -match "^\d+$") {
      Stop-Process -Id ([int]$pidText) -Force -ErrorAction SilentlyContinue
    }
    Remove-Item -LiteralPath $pidPath -Force -ErrorAction SilentlyContinue
  }
}

Get-ChildItem -LiteralPath "C:\tmp" -Filter "limitdock-*-openusage.sock" -ErrorAction SilentlyContinue |
  Remove-Item -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath "C:\tmp\limitdock-openusage.sock" -Force -ErrorAction SilentlyContinue

Get-ChildItem -LiteralPath $StateDir -Filter "openusage-*.sock" -ErrorAction SilentlyContinue |
  Remove-Item -Force -ErrorAction SilentlyContinue
