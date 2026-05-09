$ScriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$Exe = Join-Path $ScriptRoot "LimitDock.exe"
$Script = Join-Path $ScriptRoot "LimitDock.ps1"

if (Test-Path -LiteralPath $Exe) {
  Start-Process $Exe -WorkingDirectory $ScriptRoot -WindowStyle Hidden
} else {
  Start-Process powershell.exe `
    -ArgumentList @("-STA", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", $Script) `
    -WorkingDirectory $ScriptRoot `
    -WindowStyle Hidden
}
