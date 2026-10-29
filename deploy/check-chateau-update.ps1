# Read-only diagnosis of the updater preflight. No panel or router passwords are read.
$ErrorActionPreference = 'Stop'
$taskSSH = (Get-Command ssh -ErrorAction Stop).Source
$taskOptions = @('-o', 'ConnectTimeout=10', '-o', 'StrictHostKeyChecking=ask',
    '-o', 'MACs=hmac-sha2-256', '-o', 'Ciphers=aes256-ctr')
$taskCode = @'
{
    :put "CHECK=BOARD";
    :put ("BOARD=" . [/system/resource/get board-name]);
    :if ([/system/resource/get board-name] != "S53UG+5HaxD2HaxD") do={ :error "Wrong router" };
    :put "CHECK=VERSION";
    :put ("VERSION=" . [/system/resource/get version]);
    :local rosVersion [/system/resource/get version];
    :if (($rosVersion != "7.21") && ([:pick $rosVersion 0 5] != "7.21.")) do={ :error "Updater prepared for RouterOS 7.21" };
    :put "CHECK=STORAGE";
    :put ("FREE_BYTES=" . [/system/resource/get free-hdd-space]);
    :if ([/system/resource/get free-hdd-space] < 32000000) do={ :error "Need 32 MB free storage" };
    :put "CHECK=CONTAINER";
    :local panel [/container/find where name="shelly-panel"];
    :put ("PANEL_COUNT=" . [:len $panel]);
    :if ([:len $panel] != 1) do={ :error "Panel container missing" };
    :put ("PANEL_RUNNING=" . [:len [/container/find where name="shelly-panel" and running]]);
    :put ("PANEL_STOPPED=" . [:len [/container/find where name="shelly-panel" and stopped]]);
    :put "CHECK=INTERFACE";
    :put ("INTERFACE=" . [/container/get $panel interface]);
    :if ([/container/get $panel interface] != "sp-veth") do={ :error "Unexpected panel interface" };
    :put "CHECK=ROOT";
    :put ("ROOT=" . [/container/get $panel root-dir]);
    :local panelRoot [/container/get $panel root-dir];
    :if (($panelRoot != "shelly-panel/root") && ($panelRoot != "/shelly-panel/root")) do={ :error "Unexpected root directory" };
    :put "CHECK=MOUNT";
    :foreach m in=[/container/mounts/find where list="shelly-panel-data"] do={ :put ("MOUNT_SRC=" . [/container/mounts/get $m src]); :put ("MOUNT_DST=" . [/container/mounts/get $m dst]) };
    :if ([:len [/container/mounts/find where list="shelly-panel-data" and (src="shelly-panel/data" or src="/shelly-panel/data") and dst="/data"]] != 1) do={ :error "Persistent data mount missing" };
    :put "CHECK=CONFIG";
    :if ([:len [/file/find where name="shelly-panel/data/config.json" or name="/shelly-panel/data/config.json"]] != 1) do={ :error "Panel config missing" };
    :put "CHECK=ROLLBACK_IMAGE";
    :if ([:len [/file/find where name="shelly-panel-arm64.tar.gz" or name="/shelly-panel-arm64.tar.gz"]] != 1) do={ :error "Original image needed for rollback" };
    :put "CHECK=PASSWORD_ENTRY";
    :if ([:len [/container/envs/find where list="shelly-panel" and key="PANEL_PASSWORD"]] != 1) do={ :error "Panel password entry missing" };
    :put "SP-UPDATE-CHECK-OK";
}
'@
$taskCode = $taskCode.Replace("`r", '').Replace("`n", ' ')
$taskArgumentMode = Get-Variable PSNativeCommandArgumentPassing -ValueOnly -ErrorAction SilentlyContinue
if ($taskArgumentMode -notin @('Standard', 'Windows')) { $taskCode = $taskCode.Replace('"', '\"') }
Write-Host 'READ-ONLY CHECK: 192.168.89.1. Enter the Chateau SSH admin password locally.'
$taskReply = @(& $taskSSH @taskOptions admin@192.168.89.1 $taskCode)
$taskExit = $LASTEXITCODE
$taskReply | Write-Host
Write-Host ('SSH_EXIT=' + $taskExit)
if ($taskExit -ne 0 -or $taskReply -notcontains 'SP-UPDATE-CHECK-OK') {
    Write-Host 'CHECK_FAILED: send all lines starting at CHECK= (or the SSH error). Nothing was changed.'
    exit 1
}
