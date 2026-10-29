"""Local HTTP server. Run with python -m panel.server --demo for a safe preview."""
import argparse
import hashlib
import hmac
import json
import os
from pathlib import Path
import secrets
import signal
import ssl
import threading
import time
from http.cookies import SimpleCookie
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

from .router import Router, RouterError
from .service import Service
from .store import Store

STATIC = Path(__file__).parent / "static"


class AppServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, address, service, password, hosts, demo=False, secure=False):
        super().__init__(address, Handler)
        self.service, self.hosts, self.demo, self.secure = service, set(hosts), demo, secure
        self.salt = secrets.token_bytes(32)
        self.password_hash = hashlib.pbkdf2_hmac("sha256", password.encode(), self.salt, 210_000)
        self.sessions, self.attempts = {}, {}
        self.auth_lock = threading.Lock()

    def password_matches(self, password):
        candidate = hashlib.pbkdf2_hmac("sha256", password.encode(), self.salt, 210_000)
        return hmac.compare_digest(candidate, self.password_hash)


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"

    def log_message(self, *args):
        pass  # No request bodies, tokens, URLs or Wi-Fi secrets in RouterOS logs.

    def answer(self, status, data, cookie=None, mime="application/json; charset=utf-8"):
        encoded = data if isinstance(data, bytes) else json.dumps(data, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", mime)
        self.send_header("Content-Length", str(len(encoded)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("X-Frame-Options", "DENY")
        self.send_header("Referrer-Policy", "no-referrer")
        self.send_header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
        if cookie:
            self.send_header("Set-Cookie", cookie)
        self.end_headers()
        self.wfile.write(encoded)

    def valid_host(self):
        return self.headers.get("Host", "") in self.server.hosts

    def session(self):
        try:
            cookie = SimpleCookie(self.headers.get("Cookie", ""))
            token = cookie["sp_session"].value
        except (KeyError, ValueError):
            return None
        with self.server.auth_lock:
            self.server.sessions = {k: v for k, v in self.server.sessions.items() if v["expires"] > time.monotonic()}
            return self.server.sessions.get(token)

    def do_GET(self):
        if not self.valid_host():
            return self.answer(403, {"error": "Host не е разрешен."})
        path = urlsplit(self.path).path
        assets = {"/": ("index.html", "text/html; charset=utf-8"),
                  "/app.js": ("app.js", "text/javascript; charset=utf-8"),
                  "/style.css": ("style.css", "text/css; charset=utf-8")}
        if path in assets:
            name, mime = assets[path]
            return self.answer(200, (STATIC / name).read_bytes(), mime=mime)
        if path == "/api/session":
            session = self.session()
            return self.answer(200, {"authenticated": bool(session), "csrf": session["csrf"] if session else None,
                                     "demo": self.server.demo})
        if not self.session():
            return self.answer(401, {"error": "Необходим е вход."})
        try:
            if path == "/api/state":
                return self.answer(200, self.server.service.overview())
            if path == "/api/job":
                return self.answer(200, self.server.service.job_view())
            return self.answer(404, {"error": "Няма такъв адрес."})
        except (ValueError, RouterError) as exc:
            return self.answer(502, {"error": str(exc)})
        except Exception:
            return self.answer(500, {"error": "Вътрешна грешка."})

    def do_POST(self):
        origin = f"{'https' if self.server.secure else 'http'}://{self.headers.get('Host', '')}"
        if not self.valid_host() or self.headers.get("Origin") != origin:
            return self.answer(403, {"error": "Невалиден Origin/Host."})
        if self.headers.get("Content-Type", "").split(";")[0] != "application/json":
            return self.answer(415, {"error": "Изисква се application/json."})
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= 16_384:
                return self.answer(413, {"error": "Невалиден размер на заявката."})
            body = json.loads(self.rfile.read(length))
            if not isinstance(body, dict):
                raise ValueError("Изисква се JSON обект.")
        except (ValueError, UnicodeError):
            return self.answer(400, {"error": "Невалиден JSON."})
        path = urlsplit(self.path).path
        if path == "/api/login":
            ip, now = self.client_address[0], time.monotonic()
            with self.server.auth_lock:
                recent = [t for t in self.server.attempts.get(ip, []) if now - t < 60]
                self.server.attempts[ip] = recent
                if len(recent) >= 5:
                    return self.answer(429, {"error": "Изчакай една минута преди нов опит."})
                recent.append(now)
            password = body.get("password", "")
            if not isinstance(password, str) or not self.server.password_matches(password):
                return self.answer(401, {"error": "Невалидна парола."})
            token, csrf = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
            with self.server.auth_lock:
                self.server.sessions[token] = {"expires": now + 8 * 3600, "csrf": csrf}
                self.server.attempts[ip] = []
            cookie = f"sp_session={token}; HttpOnly; SameSite=Strict; Path=/; Max-Age=28800"
            if self.server.secure:
                cookie += "; Secure"
            return self.answer(200, {"csrf": csrf}, cookie)
        session = self.session()
        if not session:
            return self.answer(401, {"error": "Необходим е вход."})
        if not hmac.compare_digest(self.headers.get("X-CSRF-Token", ""), session["csrf"]):
            return self.answer(403, {"error": "Невалиден CSRF token."})
        service = self.server.service
        try:
            if path == "/api/logout":
                with self.server.auth_lock:
                    self.server.sessions = {k: v for k, v in self.server.sessions.items() if v is not session}
                return self.answer(200, {}, "sp_session=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict")
            if path == "/api/projects":
                result = service.save_project(body)
            elif path == "/api/switch":
                names = body.get("interfaces", [])
                if not isinstance(names, list) or any(not isinstance(n, str) for n in names):
                    raise ValueError("Избери валидни интерфейси.")
                result = service.switch(names, body.get("enabled"))
            elif path in ("/api/scan", "/api/provision"):
                if body.get("acknowledge_disconnect") is not True:
                    raise ValueError("Потвърди временното прекъсване на 2.4 GHz.")
                result = service.start(path.rsplit("/", 1)[1], body)
            elif path == "/api/stop":
                service.cancel.set()
                result = {"stopping": True}
            elif path == "/api/recover":
                if not service.lock.acquire(blocking=False):
                    raise ValueError("Спри активната операция и изчакай възстановяването.")
                try:
                    service.recover()
                finally:
                    service.lock.release()
                result = {"recovered": True}
            else:
                return self.answer(404, {"error": "Няма такава операция."})
            return self.answer(200, result)
        except (ValueError, RouterError, TypeError, KeyError) as exc:
            return self.answer(400, {"error": str(exc) if isinstance(exc, (ValueError, RouterError)) else "Невалидни параметри."})
        except Exception:
            return self.answer(500, {"error": "Вътрешна грешка. Провери статуса на възстановяването."})


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--demo", action="store_true")
    parser.add_argument("--config", default="/data/config.json")
    parser.add_argument("--data", default=os.environ.get("DATA_DIR", "/data"))
    parser.add_argument("--bind", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=8088)
    args = parser.parse_args()
    store = Store(args.data)
    if args.demo:
        from .demo import DemoService, demo_config
        service = DemoService(store, demo_config)
        password = "demo-local"
    else:
        config = json.loads(Path(args.config).read_text("utf-8"))
        password = os.environ["PANEL_PASSWORD"]
        if len(password) < 12:
            raise ValueError("PANEL_PASSWORD must have at least 12 characters")
        router = Router(os.environ["ROUTER_URL"], os.environ["ROUTER_USER"], os.environ["ROUTER_PASSWORD"], os.environ.get("ROUTER_CA_FILE"))
        service = Service(router, store, config)
        if store.data.get("recovery"):
            try:
                service.recover()
            except (ValueError, RouterError):
                print("Pending recovery. Changes remain blocked until router is reachable.", flush=True)
    config = service.config
    hosts = config.get("allowed_hosts", [f"localhost:{args.port}", f"127.0.0.1:{args.port}"])
    cert, key = os.environ.get("PANEL_TLS_CERT"), os.environ.get("PANEL_TLS_KEY")
    server = AppServer((args.bind, args.port), service, password, hosts, args.demo, bool(cert))
    if cert:
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(cert, key)
        server.socket = context.wrap_socket(server.socket, server_side=True)
    def shutdown(*_):
        service.cancel.set()
        threading.Thread(target=server.shutdown, daemon=True).start()
    signal.signal(signal.SIGTERM, shutdown)
    print(f"Shelly panel: {'https' if cert else 'http'}://127.0.0.1:{args.port}" + (" [DEMO: demo-local]" if args.demo else ""), flush=True)
    try:
        server.serve_forever(poll_interval=0.2)
    except KeyboardInterrupt:
        service.cancel.set()
    finally:
        server.server_close()
        # Native RouterOS watchdog remains armed if a worker is interrupted at process exit.
        if service.lock.acquire(timeout=5):
            try:
                service.recover()
            finally:
                service.lock.release()


if __name__ == "__main__":
    main()
