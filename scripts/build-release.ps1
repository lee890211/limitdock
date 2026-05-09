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
$ProbeDir = Join-Path $RepoRoot "probes\openusage-readmodel"
$ProbeOut = Join-Path $ReleaseDir "engine\bin\openusage-readmodel.exe"

function ConvertTo-Ps2ExeVersion {
  param([string]$RawVersion)
  if ($RawVersion -match "^\d+\.\d+\.\d+\.\d+$") {
    return $RawVersion
  }
  if ($RawVersion -match "^\d+\.\d+\.\d+$") {
    return "$RawVersion.0"
  }
  if ($RawVersion -match "^\d+\.\d+$") {
    return "$RawVersion.0.0"
  }
  return "0.0.0.0"
}

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
  (Join-Path $ReleaseDir "engine\bin"), `
  (Join-Path $ReleaseDir "engine\downloads"), `
  (Join-Path $ReleaseDir "engine\state") | Out-Null

Copy-Item -LiteralPath (Join-Path $RepoRoot "assets\icons") -Destination (Join-Path $ReleaseDir "assets\icons") -Recurse -Force
Copy-Item -LiteralPath (Join-Path $RepoRoot "README.md") -Destination $ReleaseDir -Force
Copy-Item -LiteralPath (Join-Path $RepoRoot "NOTES.md") -Destination $ReleaseDir -Force
Copy-Item -LiteralPath (Join-Path $RepoRoot "settings.example.json") -Destination $ReleaseDir -Force
Copy-Item -LiteralPath (Join-Path $RepoRoot "run-limitdock.ps1") -Destination $ReleaseDir -Force
Copy-Item -LiteralPath (Join-Path $RepoRoot "stop-limitdock.ps1") -Destination $ReleaseDir -Force
Copy-Item -LiteralPath (Join-Path $RepoRoot "launch-limitdock.vbs") -Destination $ReleaseDir -Force

$go = Get-Command go -ErrorAction SilentlyContinue
if (-not $go) {
  throw "Go is required to build engine\bin\openusage-readmodel.exe for release."
}
Push-Location $ProbeDir
try {
  $env:GOCACHE = Join-Path $RepoRoot "engine\.gocache"
  go build -o $ProbeOut .
} finally {
  Pop-Location
}

$ps2exe = Get-Command Invoke-ps2exe -ErrorAction SilentlyContinue
if (-not $ps2exe) {
  Copy-Item -LiteralPath (Join-Path $RepoRoot "LimitDock.ps1") -Destination $ReleaseDir -Force
  Write-Warning "Invoke-ps2exe is not installed. Packaged script release only; install ps2exe to produce LimitDock.exe."
} else {
  $exeVersion = ConvertTo-Ps2ExeVersion $Version
  Invoke-ps2exe `
    -inputFile (Join-Path $RepoRoot "LimitDock.ps1") `
    -outputFile (Join-Path $ReleaseDir "LimitDock.exe") `
    -title "LimitDock" `
    -description "Windows HUD statusline for OpenUsage.sh" `
    -product "LimitDock" `
    -version $exeVersion `
    -noConsole `
    -STA `
    -requireAdmin:$false
}

if ($IncludeOpenUsageBinary -and (-not $SkipOpenUsageDownload)) {
  $openUsageDir = Join-Path $RepoRoot "engine\downloads\openusage_windows_amd64"
  if (Test-Path -LiteralPath (Join-Path $openUsageDir "openusage.exe")) {
    Copy-Item -LiteralPath $openUsageDir -Destination (Join-Path $ReleaseDir "engine\downloads\openusage_windows_amd64") -Recurse -Force
  } else {
    Write-Warning "OpenUsage.sh binary is not cached locally. First run will download it unless LimitDock is launched with -NoDownload."
  }
} else {
  Write-Host "OpenUsage.sh binary not bundled; first run will download the official Windows release."
}

Remove-Item -LiteralPath (Join-Path $ReleaseDir "settings.json") -Force -ErrorAction SilentlyContinue
Compress-Archive -Path (Join-Path $ReleaseDir "*") -DestinationPath $ReleaseZip -Force
Write-Host "Release prepared: $ReleaseDir"
Write-Host "Release archive: $ReleaseZip"
