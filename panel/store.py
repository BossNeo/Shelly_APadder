import json
import os
from pathlib import Path
import threading


class Store:
    """Atomic state file; contains project Wi-Fi credentials, never served as an asset."""
    def __init__(self, directory):
        self.directory = Path(directory)
        self.directory.mkdir(parents=True, exist_ok=True)
        self.path = self.directory / "state.json"
        self.lock = threading.RLock()
        self.data = json.loads(self.path.read_text("utf-8")) if self.path.exists() else {
            "projects": [], "history": [], "recovery": None}

    def save(self):
        with self.lock:
            temporary = self.path.with_suffix(".tmp")
            fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
            with os.fdopen(fd, "w", encoding="utf-8") as stream:
                json.dump(self.data, stream, ensure_ascii=False, indent=2)
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(temporary, self.path)
