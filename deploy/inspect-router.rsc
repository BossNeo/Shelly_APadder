# Read-only inventory. No credentials, serial numbers or MAC addresses in output.
# Paste in RouterOS Terminal, not Windows Terminal.
:put "=== RESOURCE ==="
/system/resource/print
:put "=== PACKAGES ==="
/system/package/print
:put "=== DEVICE MODE ==="
/system/device-mode/print
:put "=== WIFI (selected properties) ==="
:foreach item in=[/interface/wifi/print as-value detail] do={
    :put ("name=" . ($item->"name") . " master=" . ($item->"master-interface") . " mld=" . ($item->"mld-interface") . " band=" . ($item->"channel.band") . " mode=" . ($item->"configuration.mode") . " ssid=" . ($item->"configuration.ssid") . " disabled=" . ($item->"disabled"))
}
:put "=== BRIDGES ==="
:foreach item in=[/interface/bridge/print as-value] do={ :put ("name=" . ($item->"name")) }
:put "=== BRIDGE PORTS ==="
:foreach item in=[/interface/bridge/port/print as-value] do={ :put ("interface=" . ($item->"interface") . " bridge=" . ($item->"bridge") . " disabled=" . ($item->"disabled") . " dynamic=" . ($item->"dynamic")) }
:put "=== DISKS (names only) ==="
:foreach item in=[/disk/print as-value] do={ :put ("slot=" . ($item->"slot") . " type=" . ($item->"type") . " fs=" . ($item->"fs")) }
