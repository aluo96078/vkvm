param(
  [string]$Root = "$PSScriptRoot\..\..",
  [string]$Output = "$PSScriptRoot\..\..\VKVM-Setup.msi"
)
$ErrorActionPreference = "Stop"
$wix = Get-Command wix.exe -ErrorAction SilentlyContinue
if ($null -eq $wix) { throw "WiX v4 (wix.exe) is required to build the MSI" }
$version = (Get-Content (Join-Path $Root "VERSION") -Raw).Trim().TrimStart('v')
if ($version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+$') { throw "invalid VERSION: $version" }
Push-Location $Root
try {
  & $wix.Source build "packaging\windows\vkvm.wxs" "-dProductVersion=$version" -o $Output
  if (-not (Test-Path $Output)) { throw "MSI output missing: $Output" }
} finally {
  Pop-Location
}
Write-Host "created $Output"
