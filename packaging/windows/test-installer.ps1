param([string]$Root = "$PSScriptRoot\..\..")
$ErrorActionPreference = "Stop"
$nsi = Get-Content (Join-Path $Root "packaging\windows\vkvm.nsi") -Raw
$service = Get-Content (Join-Path $Root "packaging\windows\install-service.ps1") -Raw
if ($nsi -notmatch 'VKVMCore' -or $nsi -notmatch 'delayed-auto') { throw "NSIS service policy missing" }
if ($service -notmatch 'failure' -or $service -notmatch 'restart') { throw "SCM recovery policy missing" }
$autostart = Get-Content (Join-Path $Root "internal\autostart\autostart_windows.go") -Raw
if ($autostart -notmatch '--companion') { throw "companion startup argument missing" }
if (-not (Test-Path (Join-Path $Root "dist\vkvm.exe"))) { throw "Windows binary missing" }
if (-not (Test-Path (Join-Path $Root "packaging\windows\vkvm.wxs"))) { throw "MSI source missing" }
if (-not (Test-Path (Join-Path $Root "VERSION"))) { throw "VERSION missing" }
$makensis = Get-Command makensis.exe -ErrorAction SilentlyContinue
if ($null -ne $makensis) {
  Push-Location $Root
  try {
    & $makensis.Source "packaging\windows\vkvm.nsi"
    if (-not (Test-Path (Join-Path $Root "VKVM-Setup.exe"))) { throw "NSIS output missing" }
  } finally {
    Pop-Location
  }
}
Write-Host "Windows NSIS/MSI installer smoke test passed"
