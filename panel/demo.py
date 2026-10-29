"""Explicit simulation: never connects to a router or real Shelly."""
import copy
import threading
import time
import uuid
from .service import Service

demo_config = {"radio_2g": "wifi2", "protected_5g": "wifi1", "allow_changes": True,
               "allowed_hosts": ["localhost:8088", "127.0.0.1:8088"]}


class DemoService(Service):
    def __init__(self, store, config):
        super().__init__(None, store, config)
        if not store.data["projects"]:
            for i, name in enumerate(("Обект • София", "Работилница", "Тестова мрежа"), 1):
                store.data["projects"].append({"id": str(i), "title": name, "ssid": f"Project-{i}", "interface": f"sp-demo{i}", "password": "demo-password"})
        self.interfaces = [{"name": "wifi1", "ssid": "MikroTik • Управление", "band": "5ghz-ax", "enabled": True, "protected": True, "managed": False}] + [
            {"name": p["interface"], "ssid": p["ssid"], "band": "2ghz-ax", "enabled": False, "protected": False, "managed": True}
            for p in store.data["projects"]]

    def overview(self):
        return {"resource": {"board-name": "Chateau LTE7 ax · СИМУЛАЦИЯ", "version": "7.x", "free-memory": "805306368", "free-hdd-space": "73400320", "cpu-load": "3", "uptime": "2h"},
                "interfaces": copy.deepcopy(self.interfaces), "projects": [{k: v for k, v in p.items() if k != "password"} for p in self.store.data["projects"]],
                "warning": "Демонстрация — всички устройства и резултати са примерни.", "allow_changes": True, "recovery": False,
                "job": self.job_view(), "history": self.store.data["history"]}

    def save_project(self, body):
        if not body.get("ssid") or len(body.get("password", "")) < 8:
            raise ValueError("Попълни SSID и парола от поне 8 знака.")
        with self.mutation():
            project = {**body, "id": uuid.uuid4().hex[:8]}
            project["interface"] = "sp-" + project["id"]
            self.store.data["projects"].append(project)
            self.store.save()
            self.interfaces.append({"name": project["interface"], "ssid": project["ssid"], "band": "2ghz-ax", "enabled": False, "managed": True, "protected": False})
        return {"id": project["id"]}

    def switch(self, names, on):
        with self.mutation():
            targets = [r for r in self.interfaces if r["name"] in names]
            if not targets or any(not r["managed"] for r in targets):
                raise ValueError("Защитеният интерфейс не се променя.")
            for row in targets:
                row["enabled"] = on
        return {"changed": len(targets)}

    def start(self, kind, body):
        if not self.lock.acquire(blocking=False):
            raise ValueError("Изчакай текущата операция.")
        self.cancel.clear()
        self.job = {"state": "running", "kind": kind, "message": "Симулиране…", "results": []}
        def run():
            try:
                time.sleep(1)
                if kind == "scan":
                    self.found = [{"ssid": f"ShellyPlus1-441793ABCD0{i}", "signal": f"-{40+i*5}"} for i in range(1, 4)]
                else:
                    for i, ssid in enumerate(body.get("devices", []), 1):
                        self.job["results"].append({"ssid": ssid, "state": "verified", "ip": f"192.168.88.{100+i}"})
                self.job.update(state="cancelled" if self.cancel.is_set() else "done", message="Симулацията приключи. Няма промени по рутер.")
            finally:
                self.lock.release()
        threading.Thread(target=run, daemon=True).start()
        return {"started": True}
