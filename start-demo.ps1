$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot
$taskPython = Get-Command python -ErrorAction SilentlyContinue
$bundledPython = Join-Path $env:USERPROFILE '.cache\codex-runtimes\codex-primary-runtime\dependencies\python\python.exe'
if (Test-Path -LiteralPath $bundledPython) {
    & $bundledPython -m panel.server --demo --bind 127.0.0.1 --data ./data/demo
} elseif ($taskPython) {
    & $taskPython.Source -m panel.server --demo --bind 127.0.0.1 --data ./data/demo
} else {
    throw 'Python 3.12+ is required.'
}
