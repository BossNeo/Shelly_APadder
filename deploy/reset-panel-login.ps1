# Run manually. Changes only PANEL_PASSWORD and restarts the existing container.
param(
    [ValidatePattern('^[A-Za-z0-9_-]{5,64}$')]
    [string]$PanelPassword = 'ADMIN'
)
$ErrorActionPreference = 'Stop'
$taskSSH = (Get-Command ssh -ErrorAction Stop).Source
$taskOptions = @('-o', 'ConnectTimeout=10', '-o', 'StrictHostKeyChecking=ask',
    '-o', 'MACs=hmac-sha2-256', '-o', 'Ciphers=aes256-ctr')
$taskCode = @'
{
    :if ([/system/resource/get board-name] != "S53UG+5HaxD2HaxD") do={ :error "Wrong router" };
    :local panel [/container/find where name="shelly-panel"];
    :local password [/container/envs/find where list="shelly-panel" and key="PANEL_PASSWORD"];
    :if (([:len $panel] != 1) || ([:len $password] != 1)) do={ :error "Panel entries missing" };
    :if ([/container/get $panel interface] != "sp-veth") do={ :error "Unexpected panel interface" };
    :local root [/container/get $panel root-dir];
    :if (($root != "shelly-panel/root") && ($root != "/shelly-panel/root")) do={ :error "Unexpected panel root" };
    :local oldPassword [/container/envs/get $password value];
    :local wait 0;
    :do {
        :if ([:len [/container/find where name="shelly-panel" and stopped]] != 1) do={ /container/stop $panel };
        :while (([:len [/container/find where name="shelly-panel" and stopped]] != 1) && ($wait < 30)) do={ :delay 1s; :set wait ($wait + 1) };
        :if ([:len [/container/find where name="shelly-panel" and stopped]] != 1) do={ :error "Panel stop timeout" };
        /container/envs/set $password value="SP-NEW-LOGIN-PASSWORD";
        /container/start $panel;
        :set wait 0;
        :while (([:len [/container/find where name="shelly-panel" and running]] != 1) && ($wait < 30)) do={ :delay 1s; :set wait ($wait + 1) };
        :if ([:len [/container/find where name="shelly-panel" and running]] != 1) do={ :error "Panel start timeout" };
    } on-error={
        /container/envs/set $password value=$oldPassword;
        :if ([:len [/container/find where name="shelly-panel" and stopped]] = 1) do={ /container/start $panel };
        :error "Password reset failed; old password restored. Report this error";
    };
    :put "SP-PANEL-LOGIN-RESET-STARTED";
}
'@
$taskCode = $taskCode.Replace('SP-NEW-LOGIN-PASSWORD', $PanelPassword).Replace("`r", '').Replace("`n", ' ')
$taskArgumentMode = Get-Variable PSNativeCommandArgumentPassing -ValueOnly -ErrorAction SilentlyContinue
if ($taskArgumentMode -notin @('Standard', 'Windows')) { $taskCode = $taskCode.Replace('"', '\"') }
Write-Host 'Enter the existing Chateau SSH admin password. Only the PANEL login password will change.'
$taskReply = @(& $taskSSH @taskOptions admin@192.168.89.1 $taskCode)
$taskExit = $LASTEXITCODE
$taskCode = $null
$taskReply | Write-Host
if ($taskExit -ne 0 -or $taskReply -notcontains 'SP-PANEL-LOGIN-RESET-STARTED') {
    throw 'Panel password reset was not confirmed. Report the displayed error.'
}
$taskURL = 'http://192.168.89.1:8088'
$taskReady = $false
for ($taskAttempt = 0; $taskAttempt -lt 10; $taskAttempt++) {
    try {
        $taskInfo = (Invoke-WebRequest -Uri ($taskURL + '/api/session') -UseBasicParsing -TimeoutSec 3 -MaximumRedirection 0).Content | ConvertFrom-Json
        if ($taskInfo.demo -eq $false -and $taskInfo.PSObject.Properties['authenticated']) { $taskReady = $true; break }
    } catch { }
    Start-Sleep -Seconds 2
}
if (-not $taskReady) { throw 'Password was set, but the panel HTTP service was not confirmed. Report this error.' }
$taskSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$taskBody = @{ password = $PanelPassword } | ConvertTo-Json -Compress
try {
    $null = Invoke-WebRequest -Uri ($taskURL + '/api/login') -Method POST -Headers @{ Origin = $taskURL } -ContentType 'application/json' -Body $taskBody -WebSession $taskSession -UseBasicParsing -TimeoutSec 15 -MaximumRedirection 0
    $taskInfo = (Invoke-WebRequest -Uri ($taskURL + '/api/session') -WebSession $taskSession -UseBasicParsing -TimeoutSec 15 -MaximumRedirection 0).Content | ConvertFrom-Json
    if ($taskInfo.authenticated -ne $true) { throw 'Not authenticated' }
} catch { throw 'Password was set, but HTTP login was not confirmed. Report this error without passwords.' }
finally { $taskBody = $null }
Write-Host 'PANEL_PASSWORD_RESET_OK'
Write-Host 'Open http://192.168.89.1:8088/. Use the panel password selected for this command. Router admin password is unchanged.'
