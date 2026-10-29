# Run after chateau-network.rsc and file upload, on the verified Chateau only.
# Prints the panel password locally. DO NOT SHARE THIS SCRIPT'S OUTPUT.
# Adds an unstarted container. No Wi-Fi, WAN or existing admin-user changes.
{
    :if ([/system/resource/get board-name] != "S53UG+5HaxD2HaxD") do={ :error "Wrong router board" };
    :if ([/system/resource/get free-hdd-space] < 67108864) do={ :error "Need at least 64 MiB free before import" };
    :if ([/system/device-mode/get container] != true) do={ :error "Container disabled by device-mode" };
    :if ([/system/device-mode/get fetch] != true) do={ :error "Fetch disabled by device-mode" };
    :if ([:len [/interface/veth/find where name="sp-veth"]] != 1) do={ :error "Panel network is missing" };
    :if ([:len [/ip/firewall/filter/find where comment="shelly-panel REST"]] != 1) do={ :error "Panel firewall is missing" };
    :if ([:len [/ip/firewall/nat/find where comment="shelly-panel local UI"]] != 1) do={ :error "Panel NAT is missing" };
    :if ([:len [/file/find where name="shelly-panel-arm64.tar.gz"]] != 1) do={ :error "Upload the ARM64 image first" };
    :if ([:len [/file/find where name="shelly-panel/data/config.json"]] != 1) do={ :error "Upload config.json first" };
    :if ([:len [/container/find where name="shelly-panel"]] > 0) do={ :error "Container already exists; stop" };
    :if ([:len [/container/mounts/find where list="shelly-panel-data"]] > 0) do={ :error "Mount already exists; stop" };
    :if ([:len [/user/find where name="shelly-panel"]] > 0) do={ :error "User already exists; stop" };
    :if ([:len [/user/group/find where name="sp-panel"]] > 0) do={ :error "Group already exists; stop" };
    :if ([:len [/container/envs/find where list="shelly-panel"]] > 0) do={ :error "Environment already exists; stop" };
    :local routerPass [:rndstr length=32];
    :local panelPass [:rndstr length=24];
    /user/group/add name=sp-panel policy=read,write,test,sensitive,rest-api;
    /user/add name=shelly-panel group=sp-panel address=172.31.253.2/32 password=$routerPass comment="shelly-panel container only";
    /container/envs/add list=shelly-panel key=ROUTER_URL value="http://172.31.253.1";
    /container/envs/add list=shelly-panel key=ROUTER_USER value="shelly-panel";
    /container/envs/add list=shelly-panel key=ROUTER_PASSWORD value=$routerPass;
    /container/envs/add list=shelly-panel key=PANEL_PASSWORD value=$panelPass;
    /container/envs/add list=shelly-panel key=GOMEMLIMIT value="64MiB";
    /container/envs/add list=shelly-panel key=GOMAXPROCS value="2";
    :put ("PANEL LOGIN PASSWORD (keep private): " . $panelPass);
    /container/mounts/add list=shelly-panel-data src=shelly-panel/data dst=/data;
    /container/add file=shelly-panel-arm64.tar.gz name=shelly-panel interface=sp-veth root-dir=shelly-panel/root mountlists=shelly-panel-data envlist=shelly-panel start-on-boot=yes logging=yes;
    :put "SP-CONTAINER-ADDED";
}
