param(
  [switch]$SkipGo
)

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)

Write-Host "Checking LimitDock PowerShell syntax..."
$errors = $null
$tokens = $null
[System.Management.Automation.Language.Parser]::ParseFile((Join-Path $RepoRoot "LimitDock.ps1"), [ref]$tokens, [ref]$errors) | Out-Null
if ($errors) {
  $errors | ForEach-Object {
    Write-Error ("{0}:{1} {2}" -f $_.Extent.StartLineNumber, $_.Extent.StartColumnNumber, $_.Message)
  }
  exit 1
}

if (-not $SkipGo) {
  $go = Get-Command go -ErrorAction SilentlyContinue
  if (-not $go) {
    throw "Go is required to test and build the Go LimitDock app."
  } else {
    Write-Host "Checking Go LimitDock app..."
    $env:GOCACHE = Join-Path $RepoRoot "engine\.gocache"
    New-Item -ItemType Directory -Force -Path (Join-Path $RepoRoot "engine\bin") | Out-Null
    Push-Location $RepoRoot
    try {
      go test ./...
      go build -ldflags "-H windowsgui" -o (Join-Path $RepoRoot "engine\bin\LimitDock.exe") .\cmd\limitdock
      Copy-Item -LiteralPath (Join-Path $RepoRoot "cmd\limitdock\LimitDock.exe.manifest") -Destination (Join-Path $RepoRoot "engine\bin\LimitDock.exe.manifest") -Force
    } finally {
      Pop-Location
    }
  }
}

Write-Host "LimitDock checks passed."
