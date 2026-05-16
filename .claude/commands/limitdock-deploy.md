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

Always bundle the OpenUsage binary. Omitting it produces a 12 MB zip instead of the correct ~18 MB and forces an internet download on first run.

```powershell
go run ./cmd/limitdock-release -version $tag -include-openusage-binary
```

This runs all tests, builds with `-H windowsgui`, copies assets, and writes `dist/LimitDock-$tag.zip`.

Verify the zip is in the 17–20 MB range before continuing. If it is under 15 MB the OpenUsage binary is missing — stop and diagnose.

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

The install directory is `E:\LimitDock-$tag`. If it does not exist, extract the zip to create it. Then overwrite the executable:

```powershell
$installDir = "E:\LimitDock-$tag"
if (-not (Test-Path $installDir)) {
    Expand-Archive "dist/LimitDock-$tag.zip" -DestinationPath $installDir -Force
} else {
    Copy-Item -Force "dist/LimitDock-$tag/LimitDock.exe" "$installDir/LimitDock.exe"
}
```

Note: the build tool places the unpacked files under `dist/LimitDock-$tag/` before zipping, so that path is the source for the copy step.

## Checklist

After all steps, confirm:

- [ ] `dist/LimitDock-$tag.zip` exists and is 17–20 MB
- [ ] Annotated tag `$tag` pushed to origin
- [ ] GitHub release at `https://github.com/lee890211/limitdock/releases/tag/$tag` has the zip asset
- [ ] `E:\LimitDock-$tag\LimitDock.exe` is updated
