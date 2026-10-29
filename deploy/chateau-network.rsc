# Verified Chateau layout: LAN bridge 192.168.89.1/24, ether1 WAN.
# Add only panel interfaces/rules. Execute once; stop on error, do not repeat.
{
    :if ([/system/resource/get board-name] != "S53UG+5HaxD2HaxD") do={ :error "Wrong router" };
    :if ([:len [/ip/address/find where address="192.168.89.1/24"]] = 0) do={ :error "Wrong LAN address" };
    :if ([:len [/interface/find where name="sp-containers"]] > 0) do={ :error "Panel network already exists; stop" };
    :if ([:len [/interface/find where name="sp-veth"]] > 0) do={ :error "Panel veth already exists; stop" };
    :if ([:len [/ip/firewall/filter/find where comment="shelly-panel REST"]] > 0) do={ :error "Panel rules already exist; stop" };
    :local rules4 [/ip/firewall/filter/find where dynamic=no];
    :local rules6 [/ipv6/firewall/filter/find where dynamic=no];
    :if (([:len $rules4] = 0) || ([:len $rules6] = 0)) do={ :error "Firewall changed; stop" };
    :local first4 [:pick $rules4 0];
    :local first6 [:pick $rules6 0];
    /interface/bridge/add name=sp-containers comment="shelly-panel container";
    /ip/address/add address=172.31.253.1/29 interface=sp-containers comment="shelly-panel container";
    /interface/veth/add name=sp-veth address=172.31.253.2/29 gateway=172.31.253.1;
    /interface/bridge/port/add interface=sp-veth bridge=sp-containers;
    /ip/firewall/filter/add chain=input action=accept in-interface=sp-containers src-address=172.31.253.2 dst-address=172.31.253.1 protocol=tcp dst-port=80 place-before=$first4 comment="shelly-panel REST";
    /ip/firewall/filter/add chain=input action=drop in-interface=sp-containers place-before=$first4 comment="shelly-panel input isolation";
    /ip/firewall/filter/add chain=forward action=accept in-interface=bridge out-interface=sp-containers src-address=192.168.89.0/24 dst-address=172.31.253.2 protocol=tcp dst-port=8088 place-before=$first4 comment="shelly-panel LAN access";
    /ip/firewall/filter/add chain=forward action=accept in-interface=sp-containers out-interface=bridge src-address=172.31.253.2 dst-address=192.168.89.0/24 connection-state=established,related place-before=$first4 comment="shelly-panel replies";
    /ip/firewall/filter/add chain=forward action=drop in-interface=sp-containers place-before=$first4 comment="shelly-panel outgoing isolation";
    /ip/firewall/filter/add chain=forward action=drop out-interface=sp-containers place-before=$first4 comment="shelly-panel incoming isolation";
    /ipv6/firewall/filter/add chain=input action=drop in-interface=sp-containers place-before=$first6 comment="shelly-panel IPv6 input";
    /ipv6/firewall/filter/add chain=forward action=drop in-interface=sp-containers place-before=$first6 comment="shelly-panel IPv6 outgoing";
    /ipv6/firewall/filter/add chain=forward action=drop out-interface=sp-containers place-before=$first6 comment="shelly-panel IPv6 incoming";
    /ip/firewall/nat/add chain=dstnat in-interface=bridge src-address=192.168.89.0/24 dst-address=192.168.89.1 protocol=tcp dst-port=8088 action=dst-nat to-addresses=172.31.253.2 to-ports=8088 comment="shelly-panel local UI";
    :put "SP-NETWORK-READY";
}
