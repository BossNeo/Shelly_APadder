# Run manually from the built package. Keep the existing panel password/config/data.
$ErrorActionPreference = 'Stop'
if (-not (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'image-size.json'))) {
    throw 'Run update-chateau.ps1 from the built output package, next to image-size.json.'
}
& (Join-Path $PSScriptRoot 'set-panel-password.ps1') -KeepPassword -EnableManagement
