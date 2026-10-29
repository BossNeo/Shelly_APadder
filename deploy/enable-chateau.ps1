# Run manually. Enable the already installed panel, preserving remote config.
# No passwords are read, stored or printed by this script.
$ErrorActionPreference = 'Stop'
$taskSSH = (Get-Command ssh -ErrorAction Stop).Source
$taskSSHOptions = @('-o', 'ConnectTimeout=10', '-o', 'StrictHostKeyChecking=ask',
    '-o', 'MACs=hmac-sha2-256', '-o', 'Ciphers=aes256-ctr')
$taskCode = @'
{
    :if ([/system/resource/get board-name] != "S53UG+5HaxD2HaxD") do={ :error "Wrong router" };
    :local configFile [/file/find where name="shelly-panel/data/config.json"];
    :if ([:len $configFile] != 1) do={ :error "Panel config missing" };
    :local oldConfig [/file/get $configFile contents];
    :local cfg [:deserialize from=json value=$oldConfig];
    :if (($cfg->"expected_board") != "S53UG+5HaxD2HaxD") do={ :error "Wrong configured board" };
    :if ((($cfg->"radio_2g") != "wifi2") || (($cfg->"protected_5g") != "wifi1")) do={ :error "Unexpected radio mapping" };
    :if (($cfg->"allow_changes") = true) do={ :error "Management already enabled; report the panel warning instead" };
    :local panel [/container/find where name="shelly-panel"];
    :if ([:len $panel] != 1) do={ :error "Panel container missing" };
    :if ([:len [/file/find where name="shelly-panel/data/config.before-enable.json"]] = 0) do={
        /file/add name=shelly-panel/data/config.before-enable.json type=file;
        /file/set [/file/find where name="shelly-panel/data/config.before-enable.json"] contents=$oldConfig;
    };
    :set ($cfg->"allow_changes") true;
    :do {
        /file/set $configFile contents=[:serialize $cfg to=json];
        :local checked [:deserialize from=json value=[/file/get $configFile contents]];
        :if (($checked->"allow_changes") != true) do={ :error "Config verification failed" };
        /container/stop $panel;
        :delay 5s;
        /container/start $panel;
    } on-error={
        /file/set $configFile contents=$oldConfig;
        :error "Enable failed; original config restored";
    };
    :put "SP-MANAGEMENT-ENABLED";
}
'@
# RouterOS SSH accepts a single command line; keep explicit semicolon separators.
$taskCode = $taskCode.Replace("`r", '').Replace("`n", ' ')
$taskArgumentMode = Get-Variable PSNativeCommandArgumentPassing -ValueOnly -ErrorAction SilentlyContinue
if ($taskArgumentMode -notin @('Standard', 'Windows')) { $taskCode = $taskCode.Replace('"', '\"') }
Write-Host 'Connect through CONTROLDOM-5G or LAN before using Shelly scan/provision.'
$taskOutput = & $taskSSH @taskSSHOptions admin@192.168.89.1 $taskCode
$taskOutput | Write-Host
if ($LASTEXITCODE -ne 0 -or $taskOutput -notcontains 'SP-MANAGEMENT-ENABLED') {
    throw 'Management enable was not confirmed. Send the displayed error, without passwords.'
}
Write-Host 'Refresh http://192.168.89.1:8088/ and log in with the same panel password.'
