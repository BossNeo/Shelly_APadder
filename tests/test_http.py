import http.client
import json
from pathlib import Path
import tempfile
import threading
import unittest

from panel.demo import DemoService, demo_config
from panel.server import AppServer
from panel.store import Store


class HTTPTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        root = Path(__file__).resolve().parents[1] / "data" / "tests"
        root.mkdir(parents=True, exist_ok=True)
        cls.temp = tempfile.TemporaryDirectory(dir=root)
        assert Path(cls.temp.name).resolve().is_relative_to(root.resolve())
        cls.service = DemoService(Store(cls.temp.name), demo_config)
        cls.server = AppServer(("127.0.0.1", 0), cls.service, "a-strong-test-password", [])
        cls.port = cls.server.server_address[1]
        cls.host = f"127.0.0.1:{cls.port}"
        cls.server.hosts = {cls.host}
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join()
        cls.temp.cleanup()

    def request(self, path, data=None, **headers):
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=5)
        body = None if data is None else json.dumps(data)
        defaults = {"Host": self.host, "Origin": "http://" + self.host, "Content-Type": "application/json"}
        connection.request("GET" if data is None else "POST", path, body, {**defaults, **headers})
        response = connection.getresponse()
        result = (response.status, dict(response.getheaders()), response.read())
        connection.close()
        return result

    def test_unauthenticated_state_and_data_files_are_not_exposed(self):
        self.assertEqual(self.request("/api/state")[0], 401)
        self.assertEqual(self.request("/state.json")[0], 401)
        self.assertEqual(self.request("/../data/state.json")[0], 401)

    def test_host_and_origin_guard_against_rebinding_and_cross_site_posts(self):
        self.assertEqual(self.request("/", **{"Host": "attacker.test"})[0], 403)
        self.assertEqual(self.request("/api/login", {"password": "a-strong-test-password"}, Origin="http://attacker.test")[0], 403)

    def test_login_csrf_logout_and_local_assets(self):
        status, headers, raw = self.request("/api/login", {"password": "a-strong-test-password"})
        self.assertEqual(status, 200)
        cookie, csrf = headers["Set-Cookie"].split(";", 1)[0], json.loads(raw)["csrf"]
        self.assertIn("HttpOnly", headers["Set-Cookie"])
        self.assertEqual(self.request("/api/scan", {"acknowledge_disconnect": True}, Cookie=cookie)[0], 403)
        status, _, raw = self.request("/api/state", Cookie=cookie)
        self.assertEqual(status, 200)
        self.assertNotIn("demo-password", raw.decode())
        self.assertEqual(self.request("/api/logout", {}, Cookie=cookie, **{"X-CSRF-Token": csrf})[0], 200)
        self.assertEqual(self.request("/api/state", Cookie=cookie)[0], 401)
        for asset in ("/", "/style.css", "/app.js"):
            status, headers, _ = self.request(asset)
            self.assertEqual(status, 200)
            self.assertIn("default-src 'self'", headers["Content-Security-Policy"])


if __name__ == "__main__":
    unittest.main()
