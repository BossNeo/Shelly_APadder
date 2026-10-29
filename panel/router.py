"""Small, bounded RouterOS REST client. No shell and no external dependencies."""
import base64
import json
import ssl
import urllib.error
import urllib.parse
import urllib.request


class RouterError(RuntimeError):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


class Router:
    def __init__(self, url, username, password, ca_file=None):
        parsed = urllib.parse.urlsplit(url)
        if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username:
            raise ValueError("Invalid ROUTER_URL")
        self.url = url.rstrip("/") + "/rest/"
        self.authorization = "Basic " + base64.b64encode(f"{username}:{password}".encode()).decode()
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), NoRedirect(),
            urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=ca_file)))

    def call(self, method, path, body=None):
        request = urllib.request.Request(
            self.url + path, method=method,
            data=None if body is None else json.dumps(body, ensure_ascii=False).encode(),
            headers={"Authorization": self.authorization, "Content-Type": "application/json"})
        try:
            with self.opener.open(request, timeout=55) as response:
                raw = response.read(2_000_000)
                return json.loads(raw) if raw else None
        except urllib.error.HTTPError as exc:
            # Error bodies can echo http-data (Wi-Fi passwords). Never log/return them.
            raise RouterError(f"RouterOS {method} {path.split('/')[0]}: HTTP {exc.code}") from None
        except (OSError, ValueError) as exc:
            raise RouterError("RouterOS connection/JSON error") from exc

    def rows(self, path, **params):
        result = self.call("POST", path + "/print", params)
        return result if isinstance(result, list) else [result]

    def patch(self, path, row_id, data):
        return self.call("PATCH", path + "/" + urllib.parse.quote(row_id, safe="*"), data)

    def add(self, path, data):
        return self.call("PUT", path, data)

    def remove(self, path, row_id):
        return self.call("DELETE", path + "/" + urllib.parse.quote(row_id, safe="*"))

    def rpc(self, address, method, params=None):
        # Addresses are constructed internally from the factory AP or validated DHCP leases.
        import ipaddress
        address = str(ipaddress.IPv4Address(address))
        result = self.call("POST", "tool/fetch", {
            "url": f"http://{address}/rpc", "http-method": "post",
            "http-header-field": "Content-Type:application/json", "output": "user",
            "duration": "8s", "http-data": json.dumps({"id": 1, "method": method, "params": params or {}})})
        rows = result if isinstance(result, list) else [result]
        data = next((row["data"] for row in reversed(rows) if "data" in row), None)
        if data is None:
            raise RouterError("Shelly response has no data")
        try:
            reply = json.loads(data)
        except ValueError:
            raise RouterError("Invalid Shelly JSON") from None
        if "error" in reply:
            raise RouterError("Shelly rejected " + method)
        if "result" not in reply:
            raise RouterError("Missing Shelly RPC result")
        return reply["result"]
