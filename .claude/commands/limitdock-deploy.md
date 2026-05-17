---
description: Build, tag, publish a GitHub release, and install LimitDock locally. Trigger on "deploy", "release", "릴리즈", "배포", or similar intent.
---

# LimitDock Deploy

Use this command when the user wants to ship a new LimitDock release.

## Version

Compute the version tag from today's date in KST (UTC+9):

```powershell
$tag = "v" + [System.TimeZoneInfo]::ConvertTimeBySystemTimeZoneId(
    [DateTime]::UtcNow, "Korea Standard Time"
).ToString("yyyyMMdd")
```

If a tag for today already exists (local or remote), overwrite it — never append a suffix like `-2`.

## Build

LimitDock is self-contained. Do not bundle OpenUsage.

```powershell
go run ./cmd/limitdock-release -version $tag
```

This runs all tests, builds with `-H windowsgui`, copies assets and docs, and writes `dist/LimitDock-$tag.zip`.

Expect the zip to be roughly 11–14 MB. It must include `LimitDock.exe`, `LimitDock.exe.manifest`, and `assets/icons/`. If the zip is only a few hundred kilobytes, the build failed.

## Git tag

Overwrite any existing tag for today:

```powershell
git tag -d $tag 2>$null
git push origin ":refs/tags/$tag" 2>$null
git tag -a $tag -m "Release $tag"
git push origin $tag
```

## GitHub release

Write 1–2 concise English sentences describing what changed since the previous release. Read recent commits for context:

```powershell
git log --oneline -10
```

Create or update the release and upload the zip:

```powershell
# Create if it does not exist yet; edit if it does
gh release view $tag >$null 2>&1
if ($LASTEXITCODE -ne 0) {
    gh release create $tag --title $tag --notes "<release notes here>"
} else {
    gh release edit $tag --title $tag --notes "<release notes here>"
}

gh release upload $tag "dist/LimitDock-$tag.zip" --clobber
```

## Local install

The install directory is `E:\LimitDock-$tag`. Prefer a full folder install so icons, manifest, and docs stay in sync.

```powershell
$installDir = "E:\LimitDock-$tag"
$src = "dist\LimitDock-$tag"
if (-not (Test-Path $installDir)) {
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    Copy-Item -Path "$src\*" -Destination $installDir -Recurse -Force
} else {
    $stateBackup = Join-Path $env:TEMP "limitdock-state-backup"
    if (Test-Path (Join-Path $installDir "state")) {
        Copy-Item -Path (Join-Path $installDir "state") -Destination $stateBackup -Recurse -Force
    }
    Copy-Item -Path "$src\*" -Destination $installDir -Recurse -Force
    if (Test-Path $stateBackup) {
        Copy-Item -Path $stateBackup -Destination (Join-Path $installDir "state") -Recurse -Force
        Remove-Item -Path $stateBackup -Recurse -Force
    }
}
```

Do not copy only `LimitDock.exe` into an older folder; that leaves out icons and the manifest.

## Checklist

After all steps, confirm:

- [ ] `dist/LimitDock-$tag.zip` exists (~11–14 MB) and contains `assets/icons/`
- [ ] Annotated tag `$tag` pushed to origin
- [ ] GitHub release at `https://github.com/lee890211/limitdock/releases/tag/$tag` has the zip asset
- [ ] `E:\LimitDock-$tag\` has `LimitDock.exe`, manifest, and `assets/icons/`
