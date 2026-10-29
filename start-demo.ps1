$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot
$taskGo = Get-Command go -ErrorAction SilentlyContinue
$taskBundledGo = Join-Path $PSScriptRoot 'output\toolchain\go\bin\go.exe'
if (Test-Path -LiteralPath $taskBundledGo) {
    & $taskBundledGo run ./panel --demo --bind 127.0.0.1 --data ./data/go-demo
} elseif ($taskGo) {
    & $taskGo.Source run ./panel --demo --bind 127.0.0.1 --data ./data/go-demo
} else {
    throw 'Go 1.24+ is required for the current panel.'
}
