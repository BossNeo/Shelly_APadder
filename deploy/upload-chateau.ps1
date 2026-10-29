param(
    [ValidatePattern('^[a-zA-Z0-9_.-]+$')]
    [string]$RouterUser = 'admin',
    [string]$PackageDirectory = ''
)

# Run manually in PowerShell. SSH asks YOU for the router password; it is never
# saved here, passed as an argument, or sent to Codex. No Wi-Fi/network changes.
$ErrorActionPreference = 'Stop'
if (-not $PackageDirectory) {
    if (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'image-size.json')) {
        $PackageDirectory = $PSScriptRoot
    } else {
        $PackageDirectory = Join-Path $PSScriptRoot '..\output\chateau-install'
    }
}
$taskPackage = (Resolve-Path -LiteralPath $PackageDirectory).Path
$taskImage = Join-Path $taskPackage 'shelly-panel-arm64.tar.gz'
$taskConfig = Join-Path $taskPackage 'config.json'
$taskReport = Get-Content -LiteralPath (Join-Path $taskPackage 'image-size.json') -Raw | ConvertFrom-Json
if ((Get-FileHash -LiteralPath $taskImage -Algorithm SHA256).Hash -ne $taskReport.sha256) {
    throw 'Image checksum does not match image-size.json.'
}
$taskSettings = Get-Content -LiteralPath $taskConfig -Raw | ConvertFrom-Json
if ($taskSettings.allow_changes -ne $false -or $taskSettings.expected_board -ne 'S53UG+5HaxD2HaxD' -or $taskSettings.radio_2g -ne 'wifi2' -or $taskSettings.protected_5g -ne 'wifi1') {
    throw 'Expected the verified Chateau configuration in read-only mode.'
}
$taskSSH = (Get-Command ssh -ErrorAction Stop).Source
$taskSCP = (Get-Command scp -ErrorAction Stop).Source
$taskDestination = $RouterUser + '@192.168.89.1'
$taskSSHOptions = @('-o', 'ConnectTimeout=10', '-o', 'StrictHostKeyChecking=ask',
    '-o', 'MACs=hmac-sha2-256', '-o', 'Ciphers=aes256-ctr')

# Keep RouterOS code literal: PowerShell must not interpolate its variables.
$taskPrepare = ':if ([/system/resource/get board-name] != "S53UG+5HaxD2HaxD") do={ :error "Wrong router board" }; :if ([:len [/container/find where name="shelly-panel"]] > 0) do={ :error "Panel already installed; stop" }; :if ([:len [/file/find where name="shelly-panel/data/config.json"]] > 0) do={ :error "Existing panel config; stop" }; :if ([:len [/file/find where name="shelly-panel/data/state.json"]] > 0) do={ :error "Existing panel data; stop" }; :if ([:len [/file/find where name="shelly-panel"]] = 0) do={ /file/add name=shelly-panel type=directory }; :if ([:len [/file/find where name="shelly-panel/data"]] = 0) do={ /file/add name=shelly-panel/data type=directory }; :put "SP-UPLOAD-READY"'
# Windows PowerShell 5.1 strips embedded quotes when invoking native programs.
# Preserve the RouterOS command exactly; PowerShell 7 Standard/Windows does this itself.
$taskArgumentMode = Get-Variable PSNativeCommandArgumentPassing -ValueOnly -ErrorAction SilentlyContinue
if ($taskArgumentMode -notin @('Standard', 'Windows')) {
    $taskPrepare = $taskPrepare.Replace('"', '\"')
}

Write-Host 'Upload only to Chateau 192.168.89.1. Enter credentials locally when SSH asks.'
Write-Host 'The laptop must be connected to Chateau Wi-Fi or LAN (192.168.89.x).'
$taskReply = & $taskSSH @taskSSHOptions $taskDestination $taskPrepare
if ($LASTEXITCODE -ne 0 -or $taskReply -notcontains 'SP-UPLOAD-READY') {
    $taskReply | Write-Host
    throw 'Router preflight failed. Nothing will be uploaded.'
}
# -O selects SCP because RouterOS does not require an SFTP subsystem for this.
& $taskSCP -O @taskSSHOptions $taskImage "${taskDestination}:shelly-panel-arm64.tar.gz"
if ($LASTEXITCODE -ne 0) { throw 'Image transfer failed; config was not uploaded.' }
& $taskSCP -O @taskSSHOptions $taskConfig "${taskDestination}:shelly-panel/data/config.json"
if ($LASTEXITCODE -ne 0) { throw 'Config transfer failed; do not start installation.' }
Write-Host 'Files uploaded. The container is NOT installed or started. Continue with the reviewed installation steps.'
