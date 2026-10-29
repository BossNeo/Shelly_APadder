import copy
import json
from pathlib import Path
import re
import tempfile
import unittest

from panel.router import RouterError
from panel.service import Service, WIFI, ros_string
from panel.store import Store


def unquote(value):
    return bytes.fromhex(value.strip('"').replace('\\', '')).decode()


class FakeRouter:
    """Model REST semantics and a narrow recovery-script interpreter, no real I/O."""
    def __init__(self):
        self.tables = {
            "system/resource": [{"board-name": "Chateau LTE7 ax", "version": "7.25"}],
            WIFI: [
                {".id": "*1", "name": "wifi1", "channel.band": "5ghz-ax", "configuration.mode": "ap", "configuration.ssid": "Management", "disabled": "false"},
                {".id": "*2", "name": "wifi2", "channel.band": "2ghz-ax", "configuration.mode": "ap", "configuration.ssid": "Working", "security.passphrase": 'a$"\\special', "disabled": "false"},
                {".id": "*3", "name": "site1", "master-interface": "wifi2", "configuration.ssid": "Project 1", "disabled": "false"},
                {".id": "*4", "name": "site2", "master-interface": "wifi2", "configuration.ssid": "Project 2", "disabled": "true"}],
            "interface/bridge": [{".id": "*B", "name": "bridge"}],
            "interface/bridge/port": [
                {".id": "*P", "interface": "wifi2", "bridge": "bridge", "disabled": "false", "dynamic": "false"},
                {".id": "*Q", "interface": "wifi1", "bridge": "bridge", "disabled": "false", "dynamic": "false"}],
            "ip/address": [], "ip/dhcp-client": [], "system/script/job": [], "ip/dhcp-server/lease": []}
        self.calls = []
        self.fail_patch = False
        self.fail_recover = False
        self.rpc_calls = []
        self.target_ssid = "Project 1"
        self.scan = [{"ssid": "ShellyPlus1-441793ABCD01", "security": "None", "signal": "-50"}]

    def rows(self, path, **params):
        return copy.deepcopy(self.tables[path])

    def patch(self, path, row_id, data):
        self.calls.append(("PATCH", path, row_id, copy.deepcopy(data)))
        if self.fail_patch:
            self.fail_patch = False
            raise RouterError("Injected patch failure")
        row = next(r for r in self.tables[path] if r[".id"] == row_id)
        row.update(data)
        return copy.deepcopy(row)

    def add(self, path, data):
        self.calls.append(("PUT", path, copy.deepcopy(data)))
        row = {".id": "*new"+str(len(self.calls)), **data}
        self.tables[path].append(row)
        return copy.deepcopy(row)

    def remove(self, path, row_id):
        self.calls.append(("DELETE", path, row_id))
        self.tables[path] = [r for r in self.tables[path] if r[".id"] != row_id]

    def call(self, method, path, body):
        self.calls.append((method, path, copy.deepcopy(body)))
        if path == WIFI + "/scan":
            return copy.deepcopy(self.scan)
        if path != "execute":
            raise AssertionError(path)
        if "as-string" not in body:
            self.tables["system/script/job"].append({".id": "*J", "owner": "panel", "started": "2026-10-29 10:00:00"})
            return {"ret": "*J"}
        if self.fail_recover:
            raise RouterError("Injected recovery failure")
        for line in body["script"].splitlines():
            if line.startswith("/ip/dhcp-client/remove"):
                self.tables["ip/dhcp-client"] = [r for r in self.tables["ip/dhcp-client"] if r.get("comment") != "shelly-panel-station"]
                continue
            match = re.fullmatch(r'/([a-z/]+)/set \[/[a-z/]+/find where (name|interface)=("[^"]*")\] (.*?);?', line)
            if not match:
                raise AssertionError("Unexpected recovery command: " + line)
            path, field, value, assignment = match.groups()
            for row in self.tables[path]:
                if row.get(field) == unquote(value):
                    for key, val in re.findall(r'([a-z.\-]+)=("[^"]*")', assignment):
                        if unquote(val) == "":
                            row.pop(key, None)
                        else:
                            row[key] = unquote(val)
        return {"ret": ""}

    def rpc(self, address, method, params=None):
        self.rpc_calls.append((address, method, copy.deepcopy(params)))
        if method == "Shelly.GetDeviceInfo":
            return {"id": "shellyplus1-441793abcd01", "gen": 2, "mac": "441793ABCD01", "auth_en": False}
        if method == "WiFi.GetStatus":
            return {"ssid": self.target_ssid, "sta_ip": address, "status": "got ip"}
        return {}


class ServiceTests(unittest.TestCase):
    def setUp(self):
        root = Path(__file__).resolve().parents[1] / "data" / "tests"
        root.mkdir(parents=True, exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(dir=root)
        assert Path(self.temp.name).resolve().is_relative_to(root.resolve())
        self.addCleanup(self.temp.cleanup)
        self.router = FakeRouter()
        self.store = Store(self.temp.name)
        self.config = {"expected_board": "Chateau LTE7 ax", "radio_2g": "wifi2", "protected_5g": "wifi1", "allow_changes": True, "lan_bridge": "bridge", "allowed_existing": ["site1", "site2", "wifi1"]}
        self.service = Service(self.router, self.store, self.config, sleep=lambda _: None)

    def test_management_radio_cannot_be_toggled(self):
        with self.assertRaises(ValueError):
            self.service.switch(["wifi1"], False)
        self.assertEqual(self.router.calls, [])

    def test_bulk_switch_manages_2ghz_master_in_safe_order(self):
        self.service.switch(["site1", "wifi2", "site2"], False)
        self.assertEqual(self.router.calls[-1][2], "*2")
        self.router.calls.clear()
        self.service.switch(["site1", "wifi2", "site2"], True)
        self.assertEqual(self.router.calls[0][2], "*2")

    def test_wrong_board_or_band_rejected_before_any_write(self):
        self.config["expected_board"] = "Another router"
        with self.assertRaises(ValueError):
            self.service.switch(["site1"], False)
        self.config["expected_board"] = "Chateau LTE7 ax"
        self.config["radio_2g"], self.config["protected_5g"] = "wifi1", "wifi2"
        with self.assertRaises(ValueError):
            self.service.switch(["site1"], False)
        self.assertEqual(self.router.calls, [])

    def test_only_allowlisted_virtual_networks_toggle(self):
        self.service.switch(["site1", "site2"], False)
        self.assertTrue(all(c[2] in ("*3", "*4") for c in self.router.calls))

    def test_station_exception_restores_original_state_and_never_touches_5ghz(self):
        before = copy.deepcopy(self.router.tables)
        with self.assertRaisesRegex(RuntimeError, "failure"):
            with self.service.station():
                self.assertEqual(self.router.tables[WIFI][1]["configuration.mode"], "station")
                self.assertIsNotNone(Store(self.temp.name).data["recovery"])
                raise RuntimeError("failure")
        self.assertEqual(self.router.tables, before)
        self.assertIsNone(Store(self.temp.name).data["recovery"])
        for call in self.router.calls:
            if call[0] == "PATCH":
                self.assertNotIn(call[2], ("*1", "*Q"))
            if call[1] == "execute":
                self.assertNotIn(ros_string("wifi1"), call[2]["script"])

    def test_failed_restore_keeps_journal_and_blocks_new_changes(self):
        self.router.fail_recover = True
        with self.assertRaises(RouterError):
            with self.service.station():
                pass
        self.assertIsNotNone(Store(self.temp.name).data["recovery"])
        with self.assertRaises(ValueError):
            self.service.switch(["site1"], False)
        self.router.fail_recover = False
        self.service.recover()
        self.assertIsNone(self.store.data["recovery"])

    def test_startup_recovery_does_not_cancel_reused_job_id(self):
        with self.service.station():
            snapshot = copy.deepcopy(self.store.data["recovery"])
        self.store.data["recovery"] = snapshot
        self.router.tables["system/script/job"] = [{".id": "*J", "owner": "someone", "started": "tomorrow"}]
        self.service.recover()
        self.assertEqual(len(self.router.tables["system/script/job"]), 1)

    def test_scan_deduplicates_and_filters_non_shelly_networks(self):
        self.router.scan += [self.router.scan[0], {"ssid": "CoffeeShop"}, {"ssid": "ShellyPlus1-441793ABCD02", "security": "WPA2"}]
        self.service.lock.acquire()
        self.service.run("scan", {})
        self.assertEqual(len(self.service.found), 1)
        self.assertEqual(self.service.job["state"], "done")

    def setup_project(self):
        self.store.data["projects"] = [{"id": "p", "title": "Обект & тест", "ssid": "Project 1", "password": 'test"& pass', "interface": "site1"}]
        self.service.found = self.router.scan
        self.service.wait_bound = lambda _: None

    def test_unverified_device_keeps_factory_ap_enabled_and_preserves_cloud(self):
        self.setup_project()
        self.service.provision(self.store.data["projects"][0], {"devices": [self.router.scan[0]["ssid"]], "disable_ap": True})
        self.assertEqual(self.service.job["results"][0]["state"], "pending")
        for _, method, params in self.router.rpc_calls:
            self.assertNotEqual(method, "Cloud.SetConfig")
            self.assertNotIn("ap", (params or {}).get("config", {}))
        sent = next(p for _, m, p in self.router.rpc_calls if m == "WiFi.SetConfig")
        self.assertEqual(sent["config"]["sta"]["pass"], 'test"& pass')

    def test_verified_identity_allows_ap_shutdown_only_on_target_ip(self):
        self.setup_project()
        self.router.tables["ip/dhcp-server/lease"] = [{"mac-address": "44:17:93:AB:CD:01", "status": "bound", "address": "192.168.88.100"}]
        self.service.provision(self.store.data["projects"][0], {"devices": [self.router.scan[0]["ssid"]], "disable_ap": True})
        self.assertEqual(self.service.job["results"][0]["state"], "verified")
        self.assertEqual(self.router.rpc_calls[-1], ("192.168.88.100", "WiFi.SetConfig", {"config": {"ap": {"enable": False}}}))

    def test_readonly_mode_and_pending_journal_prevent_writes(self):
        self.config["allow_changes"] = False
        with self.assertRaises(ValueError):
            self.service.switch(["site1"], False)
        self.assertEqual(self.router.calls, [])

    def test_device_on_wrong_ssid_is_not_verified_and_ap_stays_on(self):
        self.setup_project()
        self.router.target_ssid = "Another network"
        self.router.tables["ip/dhcp-server/lease"] = [{"mac-address": "44:17:93:AB:CD:01", "status": "bound", "address": "192.168.88.100"}]
        self.service.provision(self.store.data["projects"][0], {"devices": [self.router.scan[0]["ssid"]], "disable_ap": True})
        self.assertEqual(self.service.job["results"][0]["state"], "pending")
        self.assertFalse(any("ap" in (p or {}).get("config", {}) for _, _, p in self.router.rpc_calls))

    def test_existing_profile_does_not_modify_router_and_redacts_password(self):
        self.service.save_project({"title": "Обект", "ssid": "Project 1", "password": "12345678", "existing_interface": "site1"})
        self.assertEqual(self.router.calls, [])
        self.assertNotIn("password", self.service.overview()["projects"][0])

    def test_ros_string_cannot_interpolate_code(self):
        value = 'Тест"; /system reboot; $password\\\n'
        encoded = ros_string(value)
        self.assertNotIn("$", encoded)
        self.assertNotIn("reboot", encoded)
        self.assertEqual(unquote(encoded), value)


if __name__ == "__main__":
    unittest.main()
