param(
  [string]$Binary = "$PSScriptRoot\..\..\dist\vkvm.exe",
  [string]$ServiceName = "VKVMCore"
)
$ErrorActionPreference = "Stop"
if (-not (Test-Path $Binary)) { throw "binary not found: $Binary" }
$binPath = '"' + (Resolve-Path $Binary) + '" --service'
sc.exe.exe create $ServiceName binPath= $binPath start= delayed-auto DisplayName= "VKVM Core" | Out-Null
sc.exe.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/30000/restart/120000 | Out-Null
sc.exe.exe description $ServiceName "VKVM authenticated network and profile core" | Out-Null
Write-Host "installed $ServiceName (Delayed Auto Start with bounded recovery)"
