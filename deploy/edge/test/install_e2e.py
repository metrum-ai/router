#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Install, demo, upgrade-plan and uninstall test for the edge bundle (#432).

Runs on a clean machine with no accelerator and no container runtime:

1. builds the three router binaries;
2. installs the bundle in native mode against three CPU mock upstreams;
3. checks the rendered configuration, secret file modes and the advertised groups;
4. runs demo.sh and requires every step to pass or skip with a reason;
5. re-installs to prove idempotency (tokens and secrets survive);
6. renders a docker-mode install with --dry-run and validates that configuration;
7. uninstalls without --purge (data kept), then with --purge (data gone).

The mock upstreams are not models. This test proves the install path, the router
configuration, group routing, the quota budget hit and the spend report; it proves
nothing about model quality, throughput or GPU behaviour.
"""

from __future__ import annotations

import json
import os
import socket
import stat
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
EDGE = ROOT / "deploy" / "edge"
INSTALL = EDGE / "install.sh"
DEMO = EDGE / "demo.sh"
UNINSTALL = EDGE / "uninstall.sh"

ROUTER_BINARIES = ("metrum-ai-router", "metrum-ai-routerctl", "metrum-ai-router-migrate")


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def run(args: list[str], env: dict[str, str] | None = None, timeout: int = 300,
        cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
    full_env = os.environ.copy()
    full_env.pop("OPENAI_API_KEY", None)
    full_env.pop("LOCAL_VLLM_API_KEY", None)
    full_env.pop("ROUTER_ID_TRANSFORM_KEY", None)
    full_env.pop("METRUM_ADMIN_PASSWORD_HASH", None)
    if env:
        full_env.update(env)
    return subprocess.run(args, capture_output=True, text=True, timeout=timeout, env=full_env, cwd=cwd, check=False)


def http(path: str, token: str | None = None, body: dict | None = None) -> tuple[int, str]:
    url = path
    data = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(url, data=data, method="POST" if data else "GET")
    if token:
        request.add_header("Authorization", f"Bearer {token}")
    if data:
        request.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            return response.status, response.read().decode()
    except urllib.error.HTTPError as err:
        return err.code, err.read().decode()
    except urllib.error.URLError as err:
        return 0, str(err.reason)


class EdgeBundleInstallTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.tmp = Path(tempfile.mkdtemp(prefix="edge-bundle-test-"))
        cls.bin_dir = cls.tmp / "bin"
        cls.bin_dir.mkdir()
        build = run(
            ["go", "build", "-buildvcs=false", "-o", str(cls.bin_dir) + os.sep, *[f"./cmd/{name}" for name in ROUTER_BINARIES]],
            cwd=ROOT,
            timeout=900,
        )
        if build.returncode != 0:
            raise unittest.SkipTest(f"cannot build router binaries for the edge bundle test: {build.stderr[-800:]}")
        for name in ROUTER_BINARIES:
            if not (cls.bin_dir / name).is_file():
                raise unittest.SkipTest(f"missing built binary {name}")
        cls.data_dir = cls.tmp / "data"
        cls.port = free_port()
        cls.base_url = f"http://127.0.0.1:{cls.port}"
        cls.install = run([
            "bash", str(INSTALL),
            "--mode", "native",
            "--bin-dir", str(cls.bin_dir),
            "--data-dir", str(cls.data_dir),
            "--port", str(cls.port),
            "--budget-daily-requests", "2",
        ])

    @classmethod
    def tearDownClass(cls) -> None:
        if hasattr(cls, "data_dir") and cls.data_dir.exists():
            run(["bash", str(UNINSTALL), "--data-dir", str(cls.data_dir), "--native-only"])
            subprocess.run(["rm", "-rf", str(cls.tmp)], check=False)

    def test_01_install_succeeds(self) -> None:
        self.assertEqual(self.install.returncode, 0, self.install.stdout + self.install.stderr)
        self.assertIn("router is ready", self.install.stderr)

    def test_02_no_secret_in_installer_output(self) -> None:
        env_file = self.data_dir / "secrets" / "env"
        self.assertTrue(env_file.is_file(), "secret environment was not written")
        secrets = {}
        for line in env_file.read_text(encoding="utf-8").splitlines():
            if "=" in line and not line.startswith("#"):
                key, value = line.split("=", 1)
                secrets[key] = value.strip("'")
        output = self.install.stdout + self.install.stderr
        for key in ("LOCAL_VLLM_API_KEY", "ROUTER_ID_TRANSFORM_KEY", "METRUM_ADMIN_PASSWORD_HASH"):
            self.assertTrue(secrets.get(key), f"{key} is empty in the secret environment")
            self.assertNotIn(secrets[key], output, f"installer output leaked {key}")
        token = (self.data_dir / "secrets" / "demo.token").read_text(encoding="utf-8").strip()
        self.assertNotIn(token, output, "installer output leaked the demo token")

    def test_03_secret_file_modes(self) -> None:
        for relative in ("secrets/env", "secrets/demo.token", "secrets/demo-budget.token", "config/config.yaml"):
            path = self.data_dir / relative
            self.assertTrue(path.is_file(), f"{relative} is missing")
            mode = stat.S_IMODE(path.stat().st_mode)
            self.assertEqual(mode & 0o077, 0, f"{relative} is readable by group or others: {oct(mode)}")

    def test_04_rendered_config_is_a_valid_router_config(self) -> None:
        config = self.data_dir / "config" / "config.yaml"
        text = config.read_text(encoding="utf-8")
        self.assertNotIn("@@", text, "rendered config still has a placeholder")
        for group in ("local-tiny:", "local-chat:", "escalate:"):
            self.assertIn(group, text)
        # The raw tokens must not be in the configuration; only verifiers.
        token = (self.data_dir / "secrets" / "demo.token").read_text(encoding="utf-8").strip()
        self.assertNotIn(token, text)
        self.assertIn("token_hash:", text)
        validate = run([str(self.bin_dir / "metrum-ai-routerctl"), "config", "validate", "--config", str(config)],
                       env=self.secret_env())
        self.assertEqual(validate.returncode, 0, validate.stderr)
        self.assertIn('"valid": true', validate.stdout)

    def secret_env(self) -> dict[str, str]:
        env: dict[str, str] = {}
        for line in (self.data_dir / "secrets" / "env").read_text(encoding="utf-8").splitlines():
            if "=" in line and not line.startswith("#"):
                key, value = line.split("=", 1)
                env[key] = value.strip("'")
        return env

    def test_05_groups_and_auth_surface(self) -> None:
        token = (self.data_dir / "secrets" / "demo.token").read_text(encoding="utf-8").strip()
        status, body = http(f"{self.base_url}/readyz")
        self.assertEqual(status, 200, body)
        status, body = http(f"{self.base_url}/v1/models", token=token)
        self.assertEqual(status, 200, body)
        served = {entry["id"] for entry in json.loads(body)["data"]}
        for group in ("local-tiny", "local-chat", "escalate"):
            self.assertIn(group, served)
        status, _ = http(f"{self.base_url}/v1/chat/completions", body={"model": "local-tiny", "messages": [{"role": "user", "content": "hi"}]})
        self.assertEqual(status, 401, "the router must require a caller token")
        status, _ = http(f"{self.base_url}/admin/reports/")
        self.assertIn(status, (401, 403), "the admin surface must require credentials")

    def test_06_demo_passes_every_step(self) -> None:
        demo = run(["bash", str(DEMO), "--data-dir", str(self.data_dir)], timeout=300)
        output = demo.stdout + demo.stderr
        self.assertEqual(demo.returncode, 0, output)
        self.assertIn("local-chat answered", output)
        self.assertIn("quota-exhausted", output)
        self.assertIn("report lists the local-chat group", output)
        self.assertNotIn("FAIL", output)

    def test_07_reinstall_is_idempotent(self) -> None:
        token_before = (self.data_dir / "secrets" / "demo.token").read_text(encoding="utf-8").strip()
        again = run(["bash", str(INSTALL), "--mode", "native", "--bin-dir", str(self.bin_dir),
                     "--data-dir", str(self.data_dir), "--port", str(self.port)])
        self.assertEqual(again.returncode, 0, again.stdout + again.stderr)
        token_after = (self.data_dir / "secrets" / "demo.token").read_text(encoding="utf-8").strip()
        self.assertEqual(token_before, token_after, "a re-install must not rotate caller tokens")
        self.assertIn("keeping existing", again.stderr)

    def test_08_docker_mode_renders_without_a_runtime(self) -> None:
        docker_dir = self.tmp / "docker-render"
        result = run([
            "bash", str(INSTALL), "--mode", "docker", "--dry-run",
            "--router-image", "metrum-ai-router:4.7.0-linux-amd64",
            "--routerctl", str(self.bin_dir / "metrum-ai-routerctl"),
            "--accelerator", "nvidia",
            "--data-dir", str(docker_dir),
        ])
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        config = docker_dir / "config" / "config.yaml"
        self.assertTrue(config.is_file())
        self.assertIn("http://vllm-primary:8000/v1", config.read_text(encoding="utf-8"))
        compose = docker_dir / "compose"
        self.assertTrue((compose / "compose.yaml").is_file())
        overlay = (compose / "compose.nvidia.yaml").read_text(encoding="utf-8")
        self.assertIn("vllm/vllm-openai:v0.33.0@sha256:", overlay)
        self.assertNotIn(":latest", overlay)
        install_env = (docker_dir / "install.env").read_text(encoding="utf-8")
        self.assertIn("EDGE_MODE=docker", install_env)
        amd_dir = self.tmp / "docker-render-amd"
        amd = run(["bash", str(INSTALL), "--mode", "docker", "--dry-run",
                   "--router-image", "metrum-ai-router:4.7.0-linux-amd64",
                   "--routerctl", str(self.bin_dir / "metrum-ai-routerctl"),
                   "--accelerator", "amd", "--data-dir", str(amd_dir)])
        self.assertEqual(amd.returncode, 0, amd.stdout + amd.stderr)
        amd_overlay = (amd_dir / "compose" / "compose.amd.yaml").read_text(encoding="utf-8")
        self.assertIn("rocm/vllm:", amd_overlay)
        # The ROCm image's default CMD is a wheel-pin helper, not the server.
        self.assertIn('entrypoint: ["vllm", "serve"]', amd_overlay)
        validate = run([str(self.bin_dir / "metrum-ai-routerctl"), "config", "validate", "--config", str(config)],
                       env=self.secret_env_of(docker_dir))
        self.assertEqual(validate.returncode, 0, validate.stderr)

    def secret_env_of(self, directory: Path) -> dict[str, str]:
        env: dict[str, str] = {}
        for line in (directory / "secrets" / "env").read_text(encoding="utf-8").splitlines():
            if "=" in line and not line.startswith("#"):
                key, value = line.split("=", 1)
                env[key] = value.strip("'")
        return env

    def test_09_uninstall_keeps_then_purges(self) -> None:
        kept = run(["bash", str(UNINSTALL), "--data-dir", str(self.data_dir), "--native-only"])
        self.assertEqual(kept.returncode, 0, kept.stdout + kept.stderr)
        self.assertTrue((self.data_dir / "config" / "config.yaml").is_file(), "uninstall must keep data by default")
        time.sleep(1)
        status, _ = http(f"{self.base_url}/readyz")
        self.assertEqual(status, 0, "the router kept serving after uninstall")
        refused = run(["bash", str(UNINSTALL), "--data-dir", str(self.data_dir), "--native-only", "--purge"])
        self.assertEqual(refused.returncode, 2, "purge without --yes must refuse")
        purged = run(["bash", str(UNINSTALL), "--data-dir", str(self.data_dir), "--native-only", "--purge", "--yes"])
        self.assertEqual(purged.returncode, 0, purged.stdout + purged.stderr)
        self.assertFalse(self.data_dir.exists(), "purge left files behind")


class EdgeBundleComposeTest(unittest.TestCase):
    """The compose files must be structurally sound before a GPU host sees them.

    CI has no container runtime, so `docker compose config` cannot run there; this
    parses the same files and asserts the keys the deployment depends on.
    """

    def setUp(self) -> None:
        try:
            import yaml
        except ImportError:  # pragma: no cover - the Makefile target installs PyYAML
            self.skipTest("PyYAML is not installed")
        self.yaml = yaml

    def load(self, name: str) -> dict:
        return self.yaml.safe_load((EDGE / name).read_text(encoding="utf-8"))

    def test_base_compose_shape(self) -> None:
        base = self.load("compose.yaml")
        services = base["services"]
        self.assertEqual(set(services), {"vllm-tiny", "vllm-primary", "router"})
        for name in ("vllm-tiny", "vllm-primary"):
            service = services[name]
            self.assertIn("command", service)
            self.assertIn("healthcheck", service)
            self.assertEqual(service["ports"][0].split(":")[0], "127.0.0.1", f"{name} must publish on loopback only")
            command = service["command"]
            self.assertIn("--served-model-name", command)
            self.assertIn("--api-key", command)
            self.assertIn("--gpu-memory-utilization", command)
        router = services["router"]
        self.assertTrue(router["image"].startswith("${ROUTER_IMAGE:?"))
        self.assertTrue(router["ports"][0].endswith(":8080"), router["ports"][0])
        self.assertIn("127.0.0.1", router["ports"][0])
        self.assertTrue(any(mount.rsplit(":", 2)[-2].startswith("/app/config") for mount in router["volumes"]),
                        "the router must mount the configuration")
        self.assertTrue(any("state" in mount for mount in router["volumes"]))
        for required in ("vllm-tiny", "vllm-primary"):
            self.assertIn(required, router["depends_on"])

    def test_served_names_are_stable_slots(self) -> None:
        base = self.load("compose.yaml")
        for service, served in (("vllm-tiny", "local-tiny"), ("vllm-primary", "local-chat")):
            command = base["services"][service]["command"]
            index = command.index("--served-model-name")
            self.assertEqual(command[index + 1], served)

    def test_overlays_only_add_accelerator_wiring(self) -> None:
        base = self.load("compose.yaml")
        for overlay_name in ("compose.amd.yaml", "compose.nvidia.yaml"):
            overlay = self.load(overlay_name)
            self.assertLessEqual(set(overlay["services"]), set(base["services"]))
            for name, service in overlay["services"].items():
                # An overlay may replace the image (digest per vendor) and add
                # device wiring; it must not shadow the serving command.
                self.assertNotIn("command", service, f"{overlay_name} shadows {name}'s command")
                self.assertIn("image", service, f"{overlay_name} must pin {name}'s image")


class EdgeBundleOverlayMergeTest(unittest.TestCase):
    """Compose merges sequences by appending, so an overlay that repeats a base entry
    makes the file invalid ("array items must be unique"). Compose 2.26 refuses it at
    `docker compose config`; CI has no container runtime, so the same rule is applied
    here to every overlay.

    Deliberately stricter than Compose for *override-type* lists (a short list such as
    `ports` or `entrypoint` that an operator means to replace): Compose replaces some
    of those rather than appending, while this check rejects any duplicate item
    anywhere. That is the safe direction - it fails on a file Compose might accept,
    never passes one Compose rejects."""

    def setUp(self) -> None:
        try:
            import yaml
        except ImportError:  # pragma: no cover
            self.skipTest("PyYAML is not installed")
        self.yaml = yaml

    def load(self, name: str) -> dict:
        return self.yaml.safe_load((EDGE / name).read_text(encoding="utf-8"))

    @staticmethod
    def merge(base: object, overlay: object) -> object:
        if isinstance(base, dict) and isinstance(overlay, dict):
            merged = dict(base)
            for key, value in overlay.items():
                merged[key] = EdgeBundleOverlayMergeTest.merge(merged[key], value) if key in merged else value
            return merged
        if isinstance(base, list) and isinstance(overlay, list):
            return list(base) + list(overlay)
        return overlay

    @staticmethod
    def duplicates(value: object, path: str = "") -> list[str]:
        found: list[str] = []
        if isinstance(value, dict):
            for key, item in value.items():
                found += EdgeBundleOverlayMergeTest.duplicates(item, f"{path}.{key}")
        elif isinstance(value, list):
            seen = {}
            for item in value:
                marker = repr(item)
                if marker in seen:
                    found.append(f"{path}: duplicate {marker}")
                seen[marker] = True
                found += EdgeBundleOverlayMergeTest.duplicates(item, path)
        return found

    def test_every_overlay_merges_into_valid_compose(self) -> None:
        base = self.load("compose.yaml")
        for overlay_name in ("compose.amd.yaml", "compose.nvidia.yaml"):
            merged = self.merge(base, self.load(overlay_name))
            problems = self.duplicates(merged)
            self.assertEqual(problems, [], f"{overlay_name} produces duplicate sequence entries: {problems}")


class EdgeBundleRuntimeDocsTest(unittest.TestCase):
    """The realistic runtime on AMD development platforms is rootless Podman, and a
    single-file read-only config mount breaks caller minting. Both were hit on
    hardware; the docs must carry the guidance."""

    def test_readmes_cover_rootless_podman_and_config_mount(self) -> None:
        edge = (EDGE / "README.md").read_text(encoding="utf-8")
        self.assertIn("Rootless Podman", edge)
        self.assertIn("podman unshare chown", edge)
        self.assertIn("/app/config:rw", edge, "the edge README must document the writable override for a late mint")
        self.assertIn("not yet verified on hardware", " ".join(edge.lower().split()),
                      "the rootless remedies must be marked unverified until a hardware run proves them")
        for tier in ("amd-strix-halo-edge", "amd-mi350p-datacenter", "nvidia-datacenter", "nvidia-gb10-edge"):
            text = (ROOT / "deploy" / "reference" / tier / "README.md").read_text(encoding="utf-8")
            self.assertIn("Rootless Podman", text, tier)
            self.assertIn("podman unshare chown", text, tier)
            self.assertIn("/app/config:rw", text, tier)
            self.assertIn("not yet verified on hardware", " ".join(text.lower().split()), tier)


class ReferenceMintStepTest(unittest.TestCase):
    """The documented caller-mint step must write the configuration through the same
    directory the compose file mounts.

    A hardware run failed at this step: the procedure ran `compose run ... callers
    generate --write` while `config.yaml` was mounted as a single read-only file, so
    the writer had nowhere to put its temp file and the rename could not land. A
    later revision repeated the mistake in a subtler form - an override that mounted
    a *different* source (`$PWD/config` for the edge bundle, whose compose mounts
    `${EDGE_DATA_DIR}/config`) - so the check compares the override's source with the
    compose mount's source instead of looking for a magic string.
    """

    TIERS = ("amd-mi350p-datacenter", "amd-strix-halo-edge", "nvidia-datacenter", "nvidia-gb10-edge")

    def setUp(self) -> None:
        try:
            import yaml
        except ImportError:  # pragma: no cover
            self.skipTest("PyYAML is not installed")
        self.yaml = yaml

    @staticmethod
    def compose_source(mount_source: str) -> str:
        """Drop the `${VAR:?message}` guard so it compares with the documented path."""
        if ":?" in mount_source:
            head, _, tail = mount_source.partition(":?")
            rest = tail.split("}", 1)[-1]
            return head + "}" + rest
        return mount_source

    def router_config_mount(self, compose_path) -> str:
        compose = self.yaml.safe_load(compose_path.read_text(encoding="utf-8"))
        router = compose["services"]["router"]
        mounts = [m for m in router["volumes"] if m.rsplit(":", 2)[-2].startswith("/app/config")]
        self.assertEqual(len(mounts), 1, f"{compose_path}: exactly one /app/config mount expected")
        mount = mounts[0]
        source, target, mode = mount.rsplit(":", 2)
        self.assertEqual(target, "/app/config", f"{compose_path}: mount the directory, not a file ({mount})")
        self.assertEqual(mode, "ro", f"{compose_path}: the router's own mount stays read-only ({mount})")
        return source

    def mint_command(self, readme_path) -> str:
        text = readme_path.read_text(encoding="utf-8")
        for block in text.split("```"):
            if "callers generate" in block and "--write" in block:
                return block
        self.fail(f"{readme_path}: no documented `callers generate ... --write` command found")

    @staticmethod
    def normalise(source: str) -> str:
        """Strip shell quoting and spell the working directory the same way."""
        source = source.strip().strip('"').strip("'")
        source = source.replace("${", "$").replace("}", "")
        return source.replace("./", "$PWD/", 1) if source.startswith("./") else source

    @classmethod
    def override_source(cls, command: str) -> str:
        for token in command.split():
            if ":/app/config:" in token:
                return cls.normalise(token.rsplit(":", 2)[0])
        return ""

    def test_reference_tiers_mint_through_the_mounted_directory(self) -> None:
        for tier in self.TIERS:
            base = ROOT / "deploy" / "reference" / tier
            mount_source = self.router_config_mount(base / "compose.yaml")
            command = self.mint_command(base / "README.md")
            override = self.override_source(command)
            self.assertEqual(override, self.normalise(mount_source),
                             f"{tier}: the mint command must mount {mount_source} read-write "
                             f"(it uses {override or 'no override'})")
            self.assertIn("/app/config:rw", command, f"{tier}: the override must be read-write")

    def test_edge_bundle_mints_through_the_mounted_directory(self) -> None:
        mount_source = self.router_config_mount(EDGE / "compose.yaml")
        self.assertIn("EDGE_DATA_DIR", mount_source, "edge compose should mount ${EDGE_DATA_DIR}/config")
        command = self.mint_command(EDGE / "README.md")
        override = self.override_source(command)
        expected = self.normalise(self.compose_source(mount_source))
        self.assertEqual(override, expected,
                         f"edge: the mint command must mount {expected} read-write (it uses {override or 'no override'})")
        self.assertIn("/app/config:rw", command, "edge: the override must be read-write")
        self.assertIn("--env-file", command, "edge: the documented command must pass the env file like the other compose calls")
        self.assertIn("install.env", command, "edge: the documented command must source install.env for EDGE_ACCELERATOR")


class EdgeBundlePinTest(unittest.TestCase):
    """The bundle must not drift from the reference configuration pins."""

    def test_overlay_pins_match_reference_tiers(self) -> None:
        pairs = {
            "compose.amd.yaml": ROOT / "deploy" / "reference" / "amd-strix-halo-edge" / "images.lock",
            "compose.nvidia.yaml": ROOT / "deploy" / "reference" / "nvidia-gb10-edge" / "images.lock",
        }
        missing = [str(path.relative_to(ROOT)) for path in pairs.values() if not path.is_file()]
        if missing:
            self.skipTest("reference tier locks not present in this tree: " + ", ".join(missing))
        for overlay_name, lock_path in pairs.items():
            overlay = (EDGE / overlay_name).read_text(encoding="utf-8")
            lock = lock_path.read_text(encoding="utf-8")
            references = {
                line.strip().removeprefix("image:").strip()
                for line in overlay.splitlines()
                if line.strip().startswith("image:")
            }
            self.assertTrue(references, f"{overlay_name} has no image pin")
            for reference in references:
                tag, digest = reference.split("@", 1)
                self.assertIn(f"image: {tag}\n", lock, f"{tag} in {overlay_name} is not pinned in {lock_path.name}")
                self.assertIn(digest, lock, f"{digest} in {overlay_name} is not pinned in {lock_path.name}")


if __name__ == "__main__":
    unittest.main(verbosity=2)
