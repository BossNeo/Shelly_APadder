[CmdletBinding(DefaultParameterSetName = 'Change')]
param(
    [Parameter(Mandatory = $true, ParameterSetName = 'Change')]
    [ValidatePattern('^[A-Za-z0-9_-]{5,64}$')]
    [string]$PanelPassword,
    [Parameter(Mandatory = $true, ParameterSetName = 'Keep')]
    [switch]$KeepPassword,
    [switch]$EnableManagement
)
# Run manually in Windows PowerShell. Updates only the existing panel image and
# its PANEL_PASSWORD. Router credentials, config.json, state and Wi-Fi are kept.
$ErrorActionPreference = 'Stop'
$taskPackage = $PSScriptRoot
if (-not (Test-Path -LiteralPath (Join-Path $taskPackage 'image-size.json'))) {
    $taskPackage = Join-Path $PSScriptRoot '..\output\chateau-admin'
}
$taskImage = Join-Path $taskPackage 'shelly-panel-arm64.tar.gz'
$taskReport = Get-Content -LiteralPath (Join-Path $taskPackage 'image-size.json') -Raw | ConvertFrom-Json
if ((Get-FileHash -LiteralPath $taskImage -Algorithm SHA256).Hash -ne $taskReport.sha256) {
    throw 'Image checksum failed. Nothing was changed.'
}
$taskSSH = (Get-Command ssh -ErrorAction Stop).Source
$taskSCP = (Get-Command scp -ErrorAction Stop).Source
$taskOptions = @('-o', 'ConnectTimeout=10', '-o', 'StrictHostKeyChecking=ask',
    '-o', 'MACs=hmac-sha2-256', '-o', 'Ciphers=aes256-ctr')
$taskArgumentMode = Get-Variable PSNativeCommandArgumentPassing -ValueOnly -ErrorAction SilentlyContinue
function Convert-RouterCommand([string]$Code) {
    $Code = $Code.Replace("`r", '').Replace("`n", ' ')
    if ($taskArgumentMode -notin @('Standard', 'Windows')) { $Code = $Code.Replace('"', '\"') }
    return $Code
}
$taskPreflight = @'
{
    :if ([/system/resource/get board-name] != "S53UG+5HaxD2HaxD") do={ :error "Wrong router" };
    :if ([/system/resource/get version] !~ "^7.21") do={ :error "Updater prepared for RouterOS 7.21" };
    :if ([/system/resource/get free-hdd-space] < 32000000) do={ :error "Need 32 MB free storage" };
    :local panel [/container/find where name="shelly-panel"];
    :if ([:len $panel] != 1) do={ :error "Panel container missing" };
    :if ([/container/get $panel interface] != "sp-veth") do={ :error "Unexpected panel interface" };
    :if ([/container/get $panel root-dir] != "shelly-panel/root") do={ :error "Unexpected root directory" };
    :if ([:len [/container/mounts/find where list="shelly-panel-data" and src="shelly-panel/data" and dst="/data"]] != 1) do={ :error "Persistent data mount missing" };
    :if ([:len [/file/find where name="shelly-panel/data/config.json"]] != 1) do={ :error "Panel config missing" };
    :if ([:len [/file/find where name="shelly-panel-arm64.tar.gz"]] != 1) do={ :error "Original image needed for rollback" };
    :local password [/container/envs/find where list="shelly-panel" and key="PANEL_PASSWORD"];
    :if ([:len $password] != 1) do={ :error "Panel password entry missing" };
    :put "SP-ADMIN-PREFLIGHT-OK";
    :put ("SP-PRIVATE=" . [/container/envs/get $password value]);
}
'@
Write-Host 'Connect to Chateau Wi-Fi or LAN. Enter the existing SSH admin password locally.'
$taskReply = & $taskSSH @taskOptions admin@192.168.89.1 (Convert-RouterCommand $taskPreflight)
if ($LASTEXITCODE -ne 0 -or $taskReply -notcontains 'SP-ADMIN-PREFLIGHT-OK') {
    $taskReply | Where-Object { $_ -notlike 'SP-PRIVATE=*' } | Write-Host
    throw 'Preflight failed. Nothing was changed.'
}
$taskPrivate = @($taskReply | Where-Object { $_ -like 'SP-PRIVATE=*' })
if ($taskPrivate.Count -ne 1) { throw 'Could not read the panel credential privately.' }
$taskOldPassword = $taskPrivate[0].Substring(11)
if ($KeepPassword) { $PanelPassword = $taskOldPassword }
$taskReply = $null; $taskPrivate = $null
$taskURL = 'http://192.168.89.1:8088'
$taskSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
function Invoke-PanelJSON([string]$Path, [string]$Password = '') {
    $taskRequest = @{ Uri = $taskURL + $Path; WebSession = $taskSession;
        UseBasicParsing = $true; TimeoutSec = 30; MaximumRedirection = 0 }
    if ($Password) {
        $taskRequest.Method = 'POST'; $taskRequest.Headers = @{ Origin = $taskURL }
        $taskRequest.ContentType = 'application/json'
        $taskRequest.Body = @{ password = $Password } | ConvertTo-Json -Compress
    }
    try { return (Invoke-WebRequest @taskRequest).Content | ConvertFrom-Json }
    catch { throw 'Panel HTTP check failed. No credential will be printed.' }
}
$taskInfo = Invoke-PanelJSON '/api/session'
if ($taskInfo.demo -ne $false) { throw 'Unexpected service. Nothing was changed.' }
$null = Invoke-PanelJSON '/api/login' $taskOldPassword
$taskInfo = Invoke-PanelJSON '/api/state'
if ($taskInfo.resource.'board-name' -ne 'S53UG+5HaxD2HaxD' -or $taskInfo.job.state -eq 'running' -or $taskInfo.recovery -or $taskInfo.wps) {
    throw 'Wrong board, active operation or pending recovery. Finish that operation first.'
}
Write-Host 'Panel idle. Uploading the new image; existing data and original image are kept.'
$taskProbe = Join-Path $taskPackage 'native-syntax-probe.rsc'
if ($taskReport.native_script_adapter -and -not (Test-Path -LiteralPath $taskProbe)) {
    throw 'Native syntax probe missing. Nothing was changed.'
}
if (Test-Path -LiteralPath $taskProbe) {
    & $taskSCP -O @taskOptions $taskProbe 'admin@192.168.89.1:sp-native-syntax.rsc'
    if ($LASTEXITCODE -ne 0) { throw 'Probe upload failed. Running panel was not changed.' }
    $taskProbeReply = & $taskSSH @taskOptions admin@192.168.89.1 '/import file-name=sp-native-syntax.rsc'
    $taskProbeReply | Write-Host
    if ($LASTEXITCODE -ne 0 -or $taskProbeReply -notcontains 'SP-NATIVE-SYNTAX-OK') {
        throw 'Native RouterOS syntax was not confirmed. Existing panel and Wi-Fi were not changed.'
    }
}
& $taskSCP -O @taskOptions $taskImage 'admin@192.168.89.1:shelly-panel-update-arm64.tar.gz'
if ($LASTEXITCODE -ne 0) { throw 'Upload failed. Running panel was not changed.' }
$taskUpdate = @'
{
    :if ([/system/resource/get board-name] != "S53UG+5HaxD2HaxD") do={ :error "Wrong router" };
    :local panel [/container/find where name="shelly-panel"];
    :local password [/container/envs/find where list="shelly-panel" and key="PANEL_PASSWORD"];
    :if (([:len $panel] != 1) || ([:len $password] != 1)) do={ :error "Panel entries missing" };
    :local oldPassword [/container/envs/get $password value];
    :local configFile [/file/find where name="shelly-panel/data/config.json"];
    :local oldConfig [/file/get $configFile contents];
    :local cfg [:deserialize from=json value=$oldConfig];
    :if ((($cfg->"expected_board") != "S53UG+5HaxD2HaxD") || (($cfg->"radio_2g") != "wifi2") || (($cfg->"protected_5g") != "wifi1")) do={ :error "Unexpected config; nothing changed" };
    /container/stop $panel;
    :local wait 0;
    :while (([/container/get $panel status] != "stopped") && ($wait < 30)) do={ :delay 1s; :set wait ($wait + 1) };
    :if ([/container/get $panel status] != "stopped") do={ :error "Panel did not stop; password unchanged" };
    :do {
        /container/repull $panel file=shelly-panel-update-arm64.tar.gz;
        :delay 3s;
        :set wait 0;
        :while (([/container/get $panel status] != "stopped") && ($wait < 120)) do={ :delay 1s; :set wait ($wait + 1) };
        :if ([/container/get $panel status] != "stopped") do={ :error "Extraction did not finish" };
        SP-PASSWORD-COMMAND
        SP-MANAGEMENT-COMMAND
        /container/start $panel;
        :delay 5s;
        :if ([/container/get $panel status] != "running") do={ :error "Updated panel did not start" };
    } on-error={
        /container/envs/set $password value=$oldPassword;
        /file/set $configFile contents=$oldConfig;
        :do {
            /container/stop $panel;
            :delay 3s;
            /container/repull $panel file=shelly-panel-arm64.tar.gz;
            :delay 3s;
            :set wait 0;
            :while (([/container/get $panel status] != "stopped") && ($wait < 120)) do={ :delay 1s; :set wait ($wait + 1) };
            :if ([/container/get $panel status] != "stopped") do={ :error "Rollback extraction timeout" };
            /container/start $panel;
        } on-error={ :error "Update and original-image rollback failed; use WinBox container view. Wi-Fi was not changed" };
        :error "Update failed; original image, config and password were restored. Report this error";
    };
    :put "SP-PANEL-ADMIN-STARTED";
}
'@
if ($EnableManagement) {
    $taskManagementCommand = ':if ([:len [/file/find where name="shelly-panel/data/config.before-integration.json"]] = 0) do={ /file/add name=shelly-panel/data/config.before-integration.json type=file; /file/set [/file/find where name="shelly-panel/data/config.before-integration.json"] contents=$oldConfig }; :set ($cfg->"allow_changes") true; /file/set $configFile contents=[:serialize to=json value=$cfg];'
    $taskUpdate = $taskUpdate.Replace('SP-MANAGEMENT-COMMAND', $taskManagementCommand)
} else {
    $taskUpdate = $taskUpdate.Replace('SP-MANAGEMENT-COMMAND', '')
}
if ($KeepPassword) {
    $taskUpdate = $taskUpdate.Replace('SP-PASSWORD-COMMAND', '')
} else {
    $taskPasswordCommand = '/container/envs/set $password value="' + $PanelPassword + '";'
    $taskUpdate = $taskUpdate.Replace('SP-PASSWORD-COMMAND', $taskPasswordCommand)
}
$taskReply = & $taskSSH @taskOptions admin@192.168.89.1 (Convert-RouterCommand $taskUpdate)
$taskReply | Write-Host
if ($LASTEXITCODE -ne 0 -or $taskReply -notcontains 'SP-PANEL-ADMIN-STARTED') {
    throw 'Update was not confirmed. Keep the original archive and report the displayed error.'
}
$taskSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$null = Invoke-PanelJSON '/api/login' $PanelPassword
$taskInfo = Invoke-PanelJSON '/api/session'
if ($taskReport.panel_version -and $taskInfo.version -ne $taskReport.panel_version) {
    throw 'Panel version was not confirmed. Report the error before continuing.'
}
$taskInfo = Invoke-PanelJSON '/api/state'
if ($taskInfo.resource.'board-name' -ne 'S53UG+5HaxD2HaxD') { throw 'Updated panel did not confirm Chateau.' }
$taskOldPassword = $null
Write-Host ('PANEL_VERSION=' + $taskReport.panel_version)
if ($KeepPassword) { Write-Host 'PANEL_UPDATE_LOGIN_OK' } else { Write-Host 'NEW_PASSWORD_LOGIN_OK' }
Write-Host 'Open http://192.168.89.1:8088/ and use your selected panel password. SSH/WinBox password is unchanged.'
