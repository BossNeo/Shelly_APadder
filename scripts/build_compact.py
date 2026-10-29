"""Build a static ARM64 binary and a Docker-compatible image archive without Docker.

Requires an official Go 1.24+ toolchain. No image pull, registry or third-party module.
The Linux binary is cross-compiled, not executed on the build computer.
"""
import argparse
from datetime import datetime, timezone
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[1]


def encoded(obj):
    return json.dumps(obj, sort_keys=True, separators=(",", ":")).encode()


def tar_entry(archive, name, data=b"", mode=0o644, directory=False):
    entry = tarfile.TarInfo(name)
    entry.mode, entry.uid, entry.gid, entry.mtime = mode, 0, 0, 0
    entry.type = tarfile.DIRTYPE if directory else tarfile.REGTYPE
    entry.size = 0 if directory else len(data)
    archive.addfile(entry, None if directory else io.BytesIO(data))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default=shutil.which("go"))
    parser.add_argument("--output", type=Path, default=ROOT / "output" / "compact")
    args = parser.parse_args()
    if not args.go:
        parser.error("Pass --go with the absolute path to the official Go compiler")
    go = str(Path(args.go).resolve())
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, "CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "arm64",
           "GOTOOLCHAIN": "local", "GOCACHE": str(ROOT / "output" / "go-cache")}
    binary = out / "shelly-panel-arm64"
    subprocess.run([go, "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid=",
                    "-o", str(binary), "./panel"], cwd=ROOT, env=env, check=True)
    executable = binary.read_bytes()
    if executable[:4] != b"\x7fELF" or executable[4:6] != b"\x02\x01":
        raise RuntimeError("Expected a 64-bit little-endian Linux ELF")
    if struct.unpack_from("<H", executable, 18)[0] != 183:
        raise RuntimeError("Expected ARM64 ELF machine")
    phoff = struct.unpack_from("<Q", executable, 32)[0]
    phsize, phcount = struct.unpack_from("<HH", executable, 54)
    if any(struct.unpack_from("<I", executable, phoff + i * phsize)[0] == 3
           for i in range(phcount)):
        raise RuntimeError("Binary has a dynamic ELF interpreter")
    go_root = Path(subprocess.check_output([go, "env", "GOROOT"], text=True).strip())
    files = {"shelly-panel": (executable, 0o755),
             "licenses/Shelly-APadder-LICENSE": ((ROOT / "LICENSE").read_bytes(), 0o644),
             "licenses/Go-LICENSE": ((go_root / "LICENSE").read_bytes(), 0o644)}
    layer_io = io.BytesIO()
    with tarfile.open(fileobj=layer_io, mode="w", format=tarfile.USTAR_FORMAT) as layer:
        for directory, mode in (("data", 0o700), ("tmp", 0o1777), ("licenses", 0o755)):
            tar_entry(layer, directory, mode=mode, directory=True)
        for name, (data, mode) in files.items():
            tar_entry(layer, name, data, mode)
    layer_bytes = layer_io.getvalue()
    layer_id = hashlib.sha256(layer_bytes).hexdigest()
    created = datetime.fromtimestamp(int(os.environ.get("SOURCE_DATE_EPOCH", "0")), timezone.utc).isoformat().replace("+00:00", "Z")
    config = encoded({"architecture": "arm64", "os": "linux", "created": created,
                      "config": {"Entrypoint": ["/shelly-panel"], "WorkingDir": "/",
                                 "Env": ["PATH=/"], "ExposedPorts": {"8088/tcp": {}},
                                 "Volumes": {"/data": {}}},
                      "rootfs": {"type": "layers", "diff_ids": ["sha256:" + layer_id]},
                      "history": [{"created": created, "created_by": "Shelly APadder compact ARM64"}]})
    config_id = hashlib.sha256(config).hexdigest()
    layer_path = layer_id + "/layer.tar"
    manifest = [{"Config": config_id + ".json", "RepoTags": ["shelly-panel:compact-arm64"],
                 "Layers": [layer_path]}]
    image = out / "shelly-panel-arm64.tar.gz"
    with image.open("wb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", filename="", mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as archive:
            tar_entry(archive, config_id + ".json", config)
            tar_entry(archive, layer_path, layer_bytes)
            tar_entry(archive, "manifest.json", encoded(manifest))
            tar_entry(archive, "repositories", encoded({"shelly-panel": {"compact-arm64": layer_id}}))
    # Verify the finished archive independently of its creation path.
    with tarfile.open(image, "r:gz") as archive:
        read_manifest = json.load(archive.extractfile("manifest.json"))
        read_config = archive.extractfile(read_manifest[0]["Config"]).read()
        read_layer = archive.extractfile(read_manifest[0]["Layers"][0]).read()
        assert hashlib.sha256(read_config).hexdigest() == config_id
        assert "sha256:" + hashlib.sha256(read_layer).hexdigest() == json.loads(read_config)["rootfs"]["diff_ids"][0]
    unpacked = sum(len(data) for data, _ in files.values())
    report = {"architecture": "linux/arm64", "static_elf_verified": True,
              "binary_bytes": len(executable), "unpacked_file_bytes": unpacked,
              "archive_bytes": image.stat().st_size,
              "minimum_free_bytes_estimate": 3 * unpacked + image.stat().st_size + 32 * 1024**2,
              "sha256": hashlib.sha256(image.read_bytes()).hexdigest(),
              "note": "Cross-compiled and archive-verified; RouterOS extraction/start and physical Shelly remain live checks."}
    (out / "image-size.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    (out / "SHA256SUMS.txt").write_text(report["sha256"] + "  " + image.name + "\n", encoding="ascii")
    for source, target in (("deploy/config.example.json", "config.json"),
                           ("deploy/chateau-network.rsc", "chateau-network.rsc"),
                           ("deploy/chateau-install.rsc", "chateau-install.rsc"),
                           ("deploy/routeros-network.rsc.example", "routeros-network.rsc.example"),
                           ("deploy/chateau-credentials.rsc.example", "chateau-credentials.rsc.example"),
                           ("deploy/upload-chateau.ps1", "upload-chateau.ps1"),
                           ("deploy/enable-chateau.ps1", "enable-chateau.ps1"),
                           ("docs/INSTALL.bg.md", "INSTALL.bg.md")):
        shutil.copyfile(ROOT / source, out / target)
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
