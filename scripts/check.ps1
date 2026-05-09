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
  $probeDir = Join-Path $RepoRoot "probes\openusage-readmodel"
  $go = Get-Command go -ErrorAction SilentlyContinue
  if (-not $go) {
    Write-Warning "Go is not installed; skipping read-model probe tests."
  } else {
    Write-Host "Checking openusage-readmodel..."
    $env:GOCACHE = Join-Path $RepoRoot "engine\.gocache"
    Push-Location $probeDir
    try {
      go test ./...
    } finally {
      Pop-Location
    }
  }
}

Write-Host "LimitDock checks passed."
