param(
  [string]$Version = "dev",
  [switch]$IncludeOpenUsageBinary,
  [switch]$SkipOpenUsageDownload
)

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$DistRoot = Join-Path $RepoRoot "dist"
$ReleaseDir = Join-Path $DistRoot "LimitDock-$Version"
$ReleaseZip = Join-Path $DistRoot "LimitDock-$Version.zip"
$GoExeOut = Join-Path $ReleaseDir "LimitDock.exe"

& (Join-Path $RepoRoot "scripts\check.ps1")

if (Test-Path -LiteralPath $ReleaseDir) {
  Remove-Item -LiteralPath $ReleaseDir -Recurse -Force
}
if (Test-Path -LiteralPath $ReleaseZip) {
  Remove-Item -LiteralPath $ReleaseZip -Force
}

New-Item -ItemType Directory -Force -Path `
  $ReleaseDir, `
  (Join-Path $ReleaseDir "assets"), `
  (Join-Path $ReleaseDir "docs\images"), `
  (Join-Path $ReleaseDir "engine\bin"), `
  (Join-Path $ReleaseDir "engine\downloads"), `
  (Join-Path $ReleaseDir "engine\state") | Out-Null

Copy-Item -LiteralPath (Join-Path $RepoRoot "assets\icons") -Destination (Join-Path $ReleaseDir "assets\icons") -Recurse -Force
Copy-Item -Path (Join-Path $RepoRoot "docs\images\*") -Destination (Join-Path $ReleaseDir "docs\images") -Recurse -Force
Copy-Item -LiteralPath (Join-Path $RepoRoot "README.md") -Destination $ReleaseDir -Force
Copy-Item -LiteralPath (Join-Path $RepoRoot "NOTES.md") -Destination $ReleaseDir -Force
Copy-Item -LiteralPath (Join-Path $RepoRoot "settings.example.json") -Destination $ReleaseDir -Force
Copy-Item -LiteralPath (Join-Path $RepoRoot "run-limitdock.ps1") -Destination $ReleaseDir -Force
Copy-Item -LiteralPath (Join-Path $RepoRoot "stop-limitdock.ps1") -Destination $ReleaseDir -Force
Copy-Item -LiteralPath (Join-Path $RepoRoot "launch-limitdock.vbs") -Destination $ReleaseDir -Force

$go = Get-Command go -ErrorAction SilentlyContinue
if (-not $go) {
  throw "Go is required to build LimitDock.exe for release."
}
Push-Location $RepoRoot
try {
  $env:GOCACHE = Join-Path $RepoRoot "engine\.gocache"
  go build -ldflags "-H windowsgui" -o $GoExeOut .\cmd\limitdock
} finally {
  Pop-Location
}

$manifest = Join-Path $RepoRoot "cmd\limitdock\LimitDock.exe.manifest"
if (Test-Path -LiteralPath $manifest) {
  Copy-Item -LiteralPath $manifest -Destination (Join-Path $ReleaseDir "LimitDock.exe.manifest") -Force
}

if ($IncludeOpenUsageBinary -and (-not $SkipOpenUsageDownload)) {
  $openUsageDir = Join-Path $RepoRoot "engine\downloads\openusage_windows_amd64"
  if (Test-Path -LiteralPath (Join-Path $openUsageDir "openusage.exe")) {
    Copy-Item -LiteralPath $openUsageDir -Destination (Join-Path $ReleaseDir "engine\downloads\openusage_windows_amd64") -Recurse -Force
  } else {
    Write-Warning "openusage binary is not cached locally. First run will download it unless LimitDock is launched with -NoDownload."
  }
} else {
  Write-Host "openusage binary not bundled; first run will download the official Windows release."
}

Remove-Item -LiteralPath (Join-Path $ReleaseDir "settings.json") -Force -ErrorAction SilentlyContinue
Compress-Archive -Path (Join-Path $ReleaseDir "*") -DestinationPath $ReleaseZip -Force
Write-Host "Release prepared: $ReleaseDir"
Write-Host "Release archive: $ReleaseZip"
