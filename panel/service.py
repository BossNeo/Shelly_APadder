import copy
import ipaddress
import re
import threading
import time
import uuid
from contextlib import contextmanager

from .router import RouterError

WIFI = "interface/wifi"
MARKER = "shelly-panel:"
TEMP = "shelly-panel-station"


def enabled(row):
    return str(row.get("disabled", "false")).lower() not in ("true", "yes")


def ros_string(value):
    # Byte escapes prevent RouterOS script interpolation, including $, quotes and newlines.
    return '"' + ''.join(f"\\{byte:02X}" for byte in str(value).encode("utf-8")) + '"'


def selector(path, field, value):
    return f"[/{path}/find where {field}={ros_string(value)}]"


def wifi_settings(row):
    groups = ("configuration", "security", "datapath", "channel")
    return {key: value for key, value in row.items()
            if key in groups or any(key.startswith(group + ".") for group in groups)}


class Service:
    def __init__(self, router, store, config, sleep=time.sleep):
        self.router, self.store, self.config, self.sleep = router, store, config, sleep
        self.lock = threading.Lock()
        self.status_lock = threading.RLock()
        self.job = {"state": "idle", "message": "Готов за работа", "results": []}
        self.found = []
        self.cancel = threading.Event()

    def topology(self):
        expected = self.config.get("expected_board")
        if not expected or self.router.rows("system/resource")[0].get("board-name") != expected:
            raise ValueError("Потвърди точния board-name чрез expected_board в config.json.")
        rows = self.router.rows(WIFI, detail="")
        by_name = {row["name"]: row for row in rows}
        radio = by_name.get(self.config.get("radio_2g"))
        protected = by_name.get(self.config.get("protected_5g"))
        if not radio or not protected or radio[".id"] == protected[".id"]:
            raise ValueError("Задай различни и съществуващи radio_2g и protected_5g в config.json.")
        if any(row.get("mld-interface") not in (None, "", "none") for row in (radio, protected)):
            raise ValueError("Радиата участват в MLD/MLO. Нужна е отделна проверена конфигурация за независим 5 GHz достъп преди промени от панела.")
        if not str(radio.get("channel.band", "")).startswith("2ghz"):
            raise ValueError("Радиото за Shelly трябва да има потвърден channel.band=2ghz-…")
        if not str(protected.get("channel.band", "")).startswith("5ghz"):
            raise ValueError("Защитеният интерфейс трябва да има потвърден channel.band=5ghz-…")
        if radio.get("master-interface") or protected.get("master-interface"):
            raise ValueError("Избери физическите радиа, а не виртуални интерфейси.")
        if not enabled(protected) or protected.get("configuration.mode", "ap") != "ap":
            raise ValueError("Защитеното 5 GHz радио трябва да е включено в AP режим.")
        if radio.get("configuration.manager", "local") != "local":
            raise ValueError("CAPsMAN радио не се променя от панела.")
        return rows, radio, protected

    def writable(self):
        if not self.config.get("allow_changes", False):
            raise ValueError("Панелът е само за преглед. Завърши проверката и задай allow_changes=true.")
        if self.store.data.get("recovery"):
            raise ValueError("Има невъзстановена сесия. Първо използвай Възстанови рутера.")

    def managed(self, row, radio):
        allowed = self.config.get("allowed_existing", [])
        return row.get("name") == radio.get("name") or (row.get("master-interface") == radio["name"] and
                (row.get("comment", "").startswith(MARKER) or row["name"] in allowed))

    def overview(self):
        rows = self.router.rows(WIFI, detail="")
        resource = self.router.rows("system/resource")[0]
        try:
            self.topology()
            warning = ""
        except ValueError as exc:
            warning = str(exc)
        projects = [{key: value for key, value in p.items() if key != "password"}
                    for p in self.store.data["projects"]]
        radio = next((r for r in rows if r["name"] == self.config.get("radio_2g")), {})
        return {"resource": {k: resource.get(k) for k in
                ("board-name", "version", "free-memory", "free-hdd-space", "cpu-load", "uptime")},
                "interfaces": [{"name": r["name"], "ssid": r.get("configuration.ssid", ""),
                                "band": r.get("channel.band", ""), "enabled": enabled(r),
                                "protected": r["name"] == self.config.get("protected_5g"),
                                "managed": self.managed(r, radio)} for r in rows],
                "projects": projects, "warning": warning,
                "allow_changes": self.config.get("allow_changes", False),
                "recovery": bool(self.store.data.get("recovery")), "job": self.job_view(),
                "history": self.store.data["history"][-100:]}

    def job_view(self):
        with self.status_lock:
            return copy.deepcopy({**self.job, "found": self.found})

    def message(self, message):
        with self.status_lock:
            self.job["message"] = message

    @contextmanager
    def mutation(self):
        if not self.lock.acquire(blocking=False):
            raise ValueError("Изчакай текущата операция или я спри.")
        try:
            self.writable()
            yield
        finally:
            self.lock.release()

    def save_project(self, body):
        ssid, password = body.get("ssid", ""), body.get("password", "")
        title = body.get("title", "").strip()
        if not title or len(title) > 80 or not 1 <= len(ssid.encode()) <= 32:
            raise ValueError("Име до 80 знака и SSID от 1 до 32 UTF-8 байта са задължителни.")
        if not 8 <= len(password) <= 63 or any(ord(c) < 32 or ord(c) > 126 for c in password):
            raise ValueError("WPA2 паролата трябва да е 8–63 печатни ASCII знака.")
        with self.mutation():
            rows, radio, _ = self.topology()
            existing = body.get("existing_interface")
            if existing:
                target = next((r for r in rows if r["name"] == existing), None)
                if not target or not self.managed(target, radio) or target.get("configuration.ssid") != ssid:
                    raise ValueError("Съществуващата мрежа трябва да е разрешена виртуална 2.4 GHz мрежа с този SSID.")
                if any(p["interface"] == existing for p in self.store.data["projects"]):
                    raise ValueError("Мрежата вече има профил.")
                project = {"id": uuid.uuid4().hex[:12], "title": title, "ssid": ssid, "password": password, "interface": existing}
                self.store.data["projects"].append(project)
                self.store.save()
                return {"id": project["id"]}
            bridges = self.router.rows("interface/bridge")
            bridge = self.config.get("lan_bridge")
            if not any(r["name"] == bridge for r in bridges):
                raise ValueError("LAN bridge не съществува.")
            project = {"id": uuid.uuid4().hex[:12], "title": title, "ssid": ssid, "password": password}
            project["interface"] = "sp-" + project["id"]
            created = self.router.add(WIFI, {
                "name": project["interface"], "master-interface": radio["name"], "disabled": "yes",
                "configuration.mode": "ap", "configuration.ssid": ssid,
                "security.authentication-types": "wpa2-psk", "security.passphrase": password,
                "security.wps": "disable", "datapath.bridge": bridge,
                "comment": MARKER + project["id"]})
            try:
                self.store.data["projects"].append(project)
                self.store.save()
            except Exception:
                self.store.data["projects"] = [p for p in self.store.data["projects"] if p["id"] != project["id"]]
                self.router.remove(WIFI, created[".id"])
                raise
            return {"id": project["id"]}

    def switch(self, names, on):
        if type(on) is not bool:
            raise ValueError("enabled трябва да е true или false")
        with self.mutation():
            rows, radio, _ = self.topology()
            targets = [r for r in rows if r["name"] in names]
            if len(targets) != len(set(names)) or not targets or any(not self.managed(r, radio) for r in targets):
                raise ValueError("Може да се управляват само разрешени 2.4 GHz мрежи.")
            if on and (radio.get("configuration.mode", "ap") != "ap" or (not enabled(radio) and radio["name"] not in names)):
                raise ValueError("Първо включи физическото 2.4 GHz радио в AP режим през WebFig.")
            targets.sort(key=lambda row: (row["name"] != radio["name"]) if on else (row["name"] == radio["name"]))
            changed = []
            try:
                for row in targets:
                    changed.append(row)
                    self.router.patch(WIFI, row[".id"], {"disabled": "no" if on else "yes"})
            except Exception:
                for row in reversed(changed):
                    self.router.patch(WIFI, row[".id"], {"disabled": row.get("disabled", "false")})
                raise
        return {"changed": len(targets)}

    def recovery_script(self, snapshot):
        commands = []
        # Clear properties introduced during station mode, then restore direct configuration.
        radio = snapshot["radio"]
        address = selector(WIFI, "name", radio["name"])
        for key in snapshot["temporary_keys"]:
            commands.append(f"/{WIFI}/set {address} {key}={ros_string('')}")
        values = {**radio["settings"], "disabled": radio["disabled"]}
        commands.append(f"/{WIFI}/set {address} " + ' '.join(f"{k}={ros_string(v)}" for k, v in values.items()))
        commands.insert(0, f"/ip/dhcp-client/remove [find where comment={ros_string(TEMP)}]")
        for row in snapshot["ports"]:
            commands.append(f"/interface/bridge/port/set {selector('interface/bridge/port', 'interface', row['interface'])} disabled={ros_string(row['disabled'])}")
        for row in snapshot["children"]:
            commands.append(f"/{WIFI}/set {selector(WIFI, 'name', row['name'])} disabled={ros_string(row['disabled'])}")
        return ";\n".join(commands)

    def recover(self):
        snapshot = self.store.data.get("recovery")
        if not snapshot:
            return
        # This is the exact locally generated snapshot, never caller-supplied script text.
        self.router.call("POST", "execute", {"script": self.recovery_script(snapshot), "as-string": ""})
        watchdog = snapshot.get("watchdog")
        if watchdog:
            jobs = self.router.rows("system/script/job")
            current = next((r for r in jobs if r[".id"] == watchdog), None)
            fingerprint = snapshot.get("watchdog_fingerprint")
            if current and not fingerprint:
                raise ValueError("Изчакай 3 минути за независимото възстановяване и опитай отново.")
            if current and all(current.get(k) == v for k, v in fingerprint.items()):
                self.router.remove("system/script/job", watchdog)
        self.store.data["recovery"] = None
        self.store.save()

    @contextmanager
    def station(self):
        rows, radio, _ = self.topology()
        name = radio["name"]
        direct = next(r for r in self.router.rows(WIFI, config="", **{"show-sensitive": ""}) if r["name"] == name)
        original = wifi_settings(direct)
        if any(v in ("***", "*****", "********") for v in original.values()):
            raise ValueError("Не могат да се прочетат настройките за възстановяване (sensitive policy).")
        clients = self.router.rows("ip/dhcp-client")
        if any(r.get("interface") == name for r in clients):
            raise ValueError("2.4 GHz радиото вече има DHCP client; необходим е преглед на конфигурацията.")
        addresses = self.router.rows("ip/address")
        if any(r.get("interface") == name for r in addresses):
            raise ValueError("2.4 GHz радиото има директен IP адрес; използвай LAN bridge преди сканиране.")
        patch = {key: "" for key in original}
        patch.update({"configuration.mode": "station", "configuration.ssid": "shelly-panel-idle",
                      "configuration.country": radio.get("configuration.country", "Bulgaria"),
                      "channel.band": radio["channel.band"], "disabled": "no"})
        snapshot = {"radio": {"name": name, "settings": original, "disabled": radio.get("disabled", "false")},
                    "temporary_keys": sorted(set(patch) - {"disabled"}),
                    "children": [{"name": r["name"], "disabled": r.get("disabled", "false")}
                                 for r in rows if r.get("master-interface") == name],
                    "ports": [{"interface": r["interface"], "disabled": r.get("disabled", "false")}
                              for r in self.router.rows("interface/bridge/port")
                              if r.get("interface") == name and str(r.get("dynamic", "false")) == "false"]}
        self.store.data["recovery"] = snapshot
        self.store.save()  # Persist BEFORE the first router mutation.
        try:
            self.station_deadline = time.monotonic() + 100
            # Independent native RouterOS job restores AP mode if Python/container dies.
            watchdog = self.router.call("POST", "execute", {"script": ":delay 3m;\n" + self.recovery_script(snapshot)})
            snapshot["watchdog"] = watchdog["ret"]
            self.store.save()
            job = next((r for r in self.router.rows("system/script/job") if r[".id"] == snapshot["watchdog"]), None)
            if not job or not job.get("started") or not job.get("owner"):
                raise ValueError("RouterOS не потвърди независимия таймер за възстановяване.")
            snapshot["watchdog_fingerprint"] = {k: job[k] for k in ("started", "owner")}
            self.store.save()
            for child in snapshot["children"]:
                self.check_station_window()
                row = next(r for r in rows if r["name"] == child["name"])
                self.router.patch(WIFI, row[".id"], {"disabled": "yes"})
            for port in snapshot["ports"]:
                for row in self.router.rows("interface/bridge/port"):
                    if row.get("interface") == port["interface"]:
                        self.check_station_window()
                        self.router.patch("interface/bridge/port", row[".id"], {"disabled": "yes"})
            self.check_station_window()
            self.router.patch(WIFI, radio[".id"], patch)
            # No default route or DNS learned from an unconfigured Shelly.
            self.check_station_window()
            self.router.add("ip/dhcp-client", {"interface": name, "add-default-route": "no",
                "use-peer-dns": "no", "use-peer-ntp": "no", "disabled": "no", "comment": TEMP})
            self.check_station_window()
            yield radio
        finally:
            self.recover()

    def start(self, kind, body):
        if kind not in ("scan", "provision"):
            raise ValueError("Unknown job")
        if not self.lock.acquire(blocking=False):
            raise ValueError("Вече има активна операция.")
        try:
            self.writable()
            self.topology()
            if kind == "provision":
                project = next((p for p in self.store.data["projects"] if p["id"] == body.get("project")), None)
                devices = [r for r in self.found if r["ssid"] in body.get("devices", [])]
                if not project or not devices or len(devices) != len(set(body.get("devices", []))):
                    raise ValueError("Избери обект и устройства от последното сканиране.")
            self.cancel.clear()
            with self.status_lock:
                self.job = {"state": "running", "kind": kind, "message": "Подготовка…", "results": []}
            threading.Thread(target=self.run, args=(kind, copy.deepcopy(body)), daemon=True).start()
        except Exception:
            self.lock.release()
            raise
        return {"started": True}

    def run(self, kind, body):
        try:
            if kind == "scan":
                self.message("Сканиране на 2.4 GHz — мрежите временно прекъсват…")
                with self.station() as radio:
                    result = self.router.call("POST", WIFI + "/scan", {".id": radio[".id"], "duration": "12s"})
                    found = {}
                    for row in result:
                        ssid = row.get("ssid", "")
                        if (re.fullmatch(r"[Ss][Hh][Ee][Ll][Ll][Yy][A-Za-z0-9_-]*-[A-Fa-f0-9]{12}", ssid)
                                and row.get("security", "").lower() in ("none", "open", "")):
                            found[ssid] = {"ssid": ssid, "signal": row.get("signal", row.get("signal-strength", ""))}
                with self.status_lock:
                    self.found = sorted(found.values(), key=lambda r: r["ssid"])
                self.message(f"Открити {len(found)} Shelly AP. Режимът на рутера е възстановен.")
            else:
                project = next(p for p in self.store.data["projects"] if p["id"] == body["project"])
                self.provision(project, body)
            with self.status_lock:
                self.job["state"] = "cancelled" if self.cancel.is_set() else "done"
        except Exception as exc:
            with self.status_lock:
                self.job.update(state="error", message=str(exc) if isinstance(exc, (ValueError, RouterError)) else "Вътрешна грешка. Провери възстановяването на рутера.")
        finally:
            self.lock.release()

    def wait_bound(self, name):
        for _ in range(30):
            self.check_station_window()
            if self.cancel.is_set():
                raise ValueError("Операцията е спряна.")
            clients = self.router.rows("ip/dhcp-client")
            if any(r.get("interface") == name and r.get("status") == "bound"
                   and r.get("address", "").startswith("192.168.33.") for r in clients):
                return
            self.sleep(1)
        raise RouterError("Shelly не даде DHCP адрес до 30 секунди.")

    def check_station_window(self):
        if time.monotonic() > self.station_deadline:
            raise RouterError("Сесията надвиши безопасното време. Възстановяване на рутера.")

    def provision(self, project, body):
        rows, radio, _ = self.topology()
        target = next((r for r in rows if r["name"] == project["interface"]), None)
        if not target or not enabled(target) or not enabled(radio):
            raise ValueError("Първо включи мрежата на избрания обект и физическото 2.4 GHz радио.")
        if target.get("configuration.ssid") != project["ssid"]:
            raise ValueError("SSID в RouterOS е променен извън панела. Създай актуален профил.")
        devices = [d for d in self.found if d["ssid"] in body["devices"]]
        for number, device in enumerate(devices, 1):
            if self.cancel.is_set():
                break
            result = {"ssid": device["ssid"], "state": "error"}
            try:
                self.message(f"{number}/{len(devices)}: {device['ssid']}")
                with self.station() as current:
                    self.router.patch(WIFI, current[".id"], {"configuration.ssid": device["ssid"]})
                    self.wait_bound(current["name"])
                    info = self.router.rpc("192.168.33.1", "Shelly.GetDeviceInfo")
                    if info.get("id", "").lower() != device["ssid"].lower() or int(info.get("gen", 0)) < 2 or info.get("auth_en"):
                        raise ValueError("Устройството не е избраният Shelly Gen2+ или има включена автентикация.")
                    result.update(device_id=info["id"], mac=info["mac"])
                    self.check_station_window()
                    if body.get("rename"):
                        label = f"{project['title']} {project.get('next_number', 1)}"
                        self.router.rpc("192.168.33.1", "Sys.SetConfig", {"config": {"device": {"name": label}}})
                    self.check_station_window()
                    # AP stays on until the identity is verified over the project LAN.
                    self.router.rpc("192.168.33.1", "WiFi.SetConfig", {"config": {"sta": {
                        "ssid": project["ssid"], "pass": project["password"], "enable": True, "ipv4mode": "dhcp"}}})
                result["state"] = "pending"
                self.message("Проверка на свързването към мрежата на обекта…")
                project["next_number"] = project.get("next_number", 1) + 1
                address = self.verify(info, project["ssid"])
                if address:
                    result.update(state="verified", ip=address)
                    if body.get("disable_ap"):
                        self.router.rpc(address, "WiFi.SetConfig", {"config": {"ap": {"enable": False}}})
                        result["ap_disabled"] = True
                else:
                    result["message"] = "Настройките са изпратени, но свързването не е потвърдено. AP остава включен."
            except (ValueError, RouterError) as exc:
                result["message"] = str(exc)
                if self.store.data.get("recovery"):
                    raise
            with self.status_lock:
                self.job["results"].append(result)
            self.store.data["history"] = (self.store.data["history"] + [{**result, "time": int(time.time()), "project": project["title"]}])[-500:]
            self.store.save()
        self.message("Обработката приключи. Режимът на рутера е възстановен.")

    def verify(self, info, target_ssid):
        mac = re.sub("[^a-f0-9]", "", info["mac"].lower())
        for _ in range(12):
            if self.cancel.is_set():
                return None
            for row in self.router.rows("ip/dhcp-server/lease"):
                if re.sub("[^a-f0-9]", "", row.get("mac-address", "").lower()) != mac or row.get("status") != "bound":
                    continue
                address = str(ipaddress.IPv4Address(row["address"]))
                try:
                    check = self.router.rpc(address, "Shelly.GetDeviceInfo")
                    if check.get("id") == info["id"]:
                        status = self.router.rpc(address, "WiFi.GetStatus")
                        if status.get("ssid") == target_ssid and status.get("sta_ip") == address and status.get("status") in ("connected", "got ip"):
                            return address
                except RouterError:
                    pass
            self.sleep(2)
        return None
