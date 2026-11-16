# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Exercise a pushed Crane image without a container daemon."""

from __future__ import annotations

import argparse
import json
import os
import socket
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path


def free_port() -> int:
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def run(crane: str, image: str, version: str) -> None:
    config = json.loads(subprocess.check_output([crane, "config", image], text=True))
    runtime = config.get("config", {})
    if runtime.get("Entrypoint") != ["/app/bin/metrum-ai-router"]:
        raise RuntimeError("image entrypoint differs from release router binary")
    if runtime.get("User") != "65532:65532":
        raise RuntimeError("image does not run as the unprivileged release user")
    if runtime.get("Cmd") != ["--config", "/app/config/config.yaml"]:
        raise RuntimeError("image command differs from the release runtime command")
    labels = runtime.get("Labels", {})
    if labels.get("org.opencontainers.image.version") != version or labels.get("org.opencontainers.image.licenses") != "Apache-2.0":
        raise RuntimeError("image release labels are missing or incorrect")
    if config.get("architecture") != "amd64" or config.get("os") != "linux":
        raise RuntimeError("smoke image must be linux/amd64")
    with tempfile.TemporaryDirectory(prefix="router-image-smoke-") as temporary:
        root = Path(temporary)
        exported = root / "rootfs.tar"
        subprocess.run([crane, "export", image, str(exported)], check=True)
        with tarfile.open(exported) as archive:
            archive.extractall(root / "rootfs", filter="data")
        binary = root / "rootfs/app/bin/metrum-ai-router"
        version = subprocess.run([binary, "--version"], capture_output=True, text=True, check=True)
        if not version.stdout.strip() or f"version={version}" not in version.stdout:
            raise RuntimeError("release image binary version output does not match the release")
        port = free_port()
        cfg = root / "config.yaml"
        cfg.write_text(
            f"server:\n  listen: 127.0.0.1:{port}\n"
            "  identifiers:\n    mode: passthrough\n"
            "  usage_db:\n    enabled: false\n"
            f"  logging:\n    path: {root / 'requests.jsonl'}\n"
            f"state_path: {root / 'state.json'}\n"
            "providers:\n  mock:\n    base_url: http://127.0.0.1:1/v1\n"
            "    dialect: openai\n    api_key: test-key\n"
            "models:\n  default:\n    strategy: static\n    targets:\n"
            "      - provider: mock\n        model: mock-model\n"
            "callers:\n  - id: startup-smoke\n"
            "    token_sha256: 0000000000000000000000000000000000000000000000000000000000000000\n"
            "    allow: [default]\n"
            "admin:\n  enabled: false\n",
            encoding="utf-8",
        )
        env = dict(os.environ)
        env["ROUTER_ID_TRANSFORM_KEY"] = "ab" * 64
        process = subprocess.Popen([binary, "--config", str(cfg)], cwd=root, env=env,
                                   stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
        try:
            deadline = time.monotonic() + 20
            last_error = "router did not become healthy"
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise RuntimeError("router exited before healthz: " + process.stderr.read()[-2000:])
                try:
                    with urllib.request.urlopen(f"http://127.0.0.1:{port}/healthz", timeout=1) as response:
                        if response.status == 200:
                            return
                        last_error = f"/healthz returned HTTP {response.status}"
                except (OSError, urllib.error.URLError) as exc:
                    last_error = str(exc)
                time.sleep(0.2)
            raise RuntimeError(last_error)
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
            if process.stderr:
                process.stderr.close()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--crane", required=True)
    parser.add_argument("--image", required=True)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    run(args.crane, args.image, args.version)
    print("crane-built image version and /healthz smoke passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
