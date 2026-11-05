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
import shutil
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
_BUILT_BINARIES: Path | None = None


def built_binaries() -> Path:
    """Build the three binaries once per test process and return their directory."""
    global _BUILT_BINARIES
    if _BUILT_BINARIES is not None:
        return _BUILT_BINARIES
    target = Path(tempfile.mkdtemp(prefix="edge-bins-"))
    result = subprocess.run(
        ["go", "build", "-buildvcs=false", "-o", str(target) + os.sep, *[f"./cmd/{name}" for name in ROUTER_BINARIES]],
        cwd=ROOT, capture_output=True, text=True, check=False,
    )
    if result.returncode != 0:
        raise unittest.SkipTest(f"cannot build router binaries: {result.stderr[-400:]}")
    _BUILT_BINARIES = target
    return target


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
        self.assertTrue(any(":/app/config:" in mount for mount in router["volumes"]),
                        "the router must mount the configuration directory")
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
        self.assertIn("ROUTER_CONFIG_MOUNT_MODE=rw", edge, "the edge README must document the mount-mode switch for a late mint")
        self.assertIn("not yet verified on hardware", " ".join(edge.lower().split()),
                      "the rootless remedies must be marked unverified until a hardware run proves them")
        for tier in ("amd-strix-halo-edge", "amd-mi350p-datacenter", "nvidia-datacenter", "nvidia-gb10-edge"):
            text = (ROOT / "deploy" / "reference" / tier / "README.md").read_text(encoding="utf-8")
            self.assertIn("Rootless Podman", text, tier)
            self.assertIn("podman unshare chown", text, tier)
            self.assertIn("ROUTER_CONFIG_MOUNT_MODE=rw", text, tier)
            self.assertIn("not yet verified on hardware", " ".join(text.lower().split()), tier)


class ReferenceMintStepTest(unittest.TestCase):
    """The documented caller-mint step must switch the mount mode, and nothing else.

    A hardware run failed at this step because the configuration is mounted
    read-only. The first attempt at a fix used `compose run -v <dir>:/app/config:rw`,
    which does *not* replace a service mount of the same target - verified on Docker
    Compose v5.0.1 (read-only) as well as Compose 2.26.1. The supported form is the
    `ROUTER_CONFIG_MOUNT_MODE` variable, so a `-v ...:/app/config` in the docs is a
    defect rather than a legacy alternative.
    """

    TIERS = ("amd-mi350p-datacenter", "amd-strix-halo-edge", "nvidia-datacenter", "nvidia-gb10-edge")

    def setUp(self) -> None:
        try:
            import yaml
        except ImportError:  # pragma: no cover
            self.skipTest("PyYAML is not installed")
        self.yaml = yaml

    def router_config_mount(self, compose_path) -> str:
        import re

        compose = self.yaml.safe_load(compose_path.read_text(encoding="utf-8"))
        router = compose["services"]["router"]
        matches = [re.match(r"^(?P<source>.+?):/app/config:(?P<mode>.+)$", mount) for mount in router["volumes"]]
        matches = [match for match in matches if match]
        self.assertEqual(len(matches), 1, f"{compose_path}: exactly one /app/config mount expected")
        mode = matches[0].group("mode")
        self.assertTrue(mode.startswith("${ROUTER_CONFIG_MOUNT_MODE") and mode.endswith(":-ro}"),
                        f"{compose_path}: the mount mode must be a variable defaulting to ro, got {mode!r}")
        return matches[0].group("source")

    def mint_command(self, readme_path) -> str:
        text = readme_path.read_text(encoding="utf-8")
        for block in text.split("```"):
            if "callers generate" in block and "--write" in block:
                return block
        self.fail(f"{readme_path}: no documented `callers generate ... --write` command found")

    def assert_mint_uses_the_mode_variable(self, label: str, command: str) -> None:
        self.assertIn("ROUTER_CONFIG_MOUNT_MODE=rw", command,
                      f"{label}: the mint command must set ROUTER_CONFIG_MOUNT_MODE=rw")
        self.assertNotIn(":/app/config:", command,
                         f"{label}: a `-v ...:/app/config` override cannot replace a service mount (verified on Compose v5.0.1)")

    def test_reference_tiers_mint_through_the_mount_mode(self) -> None:
        for tier in self.TIERS:
            base = ROOT / "deploy" / "reference" / tier
            self.router_config_mount(base / "compose.yaml")
            self.assert_mint_uses_the_mode_variable(tier, self.mint_command(base / "README.md"))

    def test_edge_bundle_mints_through_the_mount_mode(self) -> None:
        source = self.router_config_mount(EDGE / "compose.yaml")
        self.assertIn("EDGE_DATA_DIR", source, "edge compose should mount ${EDGE_DATA_DIR}/config")
        command = self.mint_command(EDGE / "README.md")
        self.assert_mint_uses_the_mode_variable("edge", command)
        self.assertIn("--env-file", command, "edge: the documented command must pass the env file like the other compose calls")
        self.assertIn("install.env", command, "edge: the documented command must source install.env for EDGE_ACCELERATOR")


class ComposeIpcTest(unittest.TestCase):
    """Podman refuses a container that sets `shm_size` while `ipc: host` is in effect
    ("cannot set shmsize when running in the {host } IPC Namespace"), and shm_size is
    inert there anyway."""

    def setUp(self) -> None:
        try:
            import yaml
        except ImportError:  # pragma: no cover
            self.skipTest("PyYAML is not installed")
        self.yaml = yaml

    def test_no_service_sets_shm_size_with_host_ipc(self) -> None:
        files = sorted((ROOT / "deploy" / "reference").glob("*/compose.yaml")) + sorted(EDGE.glob("compose*.yaml"))
        problems = []
        for path in files:
            compose = self.yaml.safe_load(path.read_text(encoding="utf-8"))
            for name, service in (compose.get("services") or {}).items():
                if str(service.get("ipc")) == "host" and "shm_size" in service:
                    problems.append(f"{path.relative_to(ROOT)}: {name}")
        self.assertEqual(problems, [], "ipc: host makes shm_size inert and Podman refuses it: " + ", ".join(problems))


class DockerModeHandoverTest(unittest.TestCase):
    """Behavioural check of the ownership handover, with fake docker/chown/curl.

    The installer must hand config, state and logs to the runtime image's uid 65532
    (the router is a scratch image running as that user), must *not* touch secrets,
    and must do it after every write - config, tokens, migrations - and immediately
    before the stack starts. A grep of the script cannot see that ordering; running
    it against instrumented fakes can.
    """

    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp(prefix="edge-handover-"))
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.bin = Path(tempfile.mkdtemp(prefix="edge-handover-bin-"))
        self.addCleanup(shutil.rmtree, self.bin, ignore_errors=True)
        for name in ROUTER_BINARIES:
            source = built_binaries() / name
            if not source.is_file():
                self.skipTest(f"{name} was not built for this test run")
            (self.bin / name).write_bytes(source.read_bytes())
            (self.bin / name).chmod(0o755)
        self.log = self.tmp / "calls.log"
        self.data = self.tmp / "data"

        self._fake("docker", """#!/usr/bin/env bash
printf 'docker %s\\n' "$*" >> "$EDGE_TEST_LOG"
exit 0
""")
        self._fake("chown", """#!/usr/bin/env bash
printf 'chown %s\\n' "$*" >> "$EDGE_TEST_LOG"
d="$EDGE_TEST_DATA"
for f in config/config.yaml secrets/demo.token secrets/env state/usage.sqlite; do
  if [ -e "$d/$f" ]; then printf 'present %s=yes\\n' "$f" >> "$EDGE_TEST_LOG"; else printf 'present %s=no\\n' "$f" >> "$EDGE_TEST_LOG"; fi
done
exit 0
""")
        self._fake("curl", """#!/usr/bin/env bash
printf 'curl %s\\n' "$*" >> "$EDGE_TEST_LOG"
printf '{"object":"list","data":[{"id":"local-chat"}]}'
exit 0
""")

    def _fake(self, name: str, body: str) -> None:
        path = self.bin / name
        path.write_text(body, encoding="utf-8")
        path.chmod(0o755)

    def env(self) -> dict[str, str]:
        environment = os.environ.copy()
        environment.update({
            "PATH": f"{self.bin}:{environment.get('PATH', '')}",
            "EDGE_TEST_LOG": str(self.log),
            "EDGE_TEST_DATA": str(self.data),
            "ROUTERCTL_OVERRIDE": str(self.bin / "metrum-ai-routerctl"),
            "EDGE_TEST_HANDOVER": "1",
        })
        for key in ("LOCAL_VLLM_API_KEY", "ROUTER_ID_TRANSFORM_KEY", "METRUM_ADMIN_PASSWORD_HASH"):
            environment.pop(key, None)
        return environment

    def lines(self) -> list[str]:
        return self.log.read_text(encoding="utf-8").splitlines() if self.log.is_file() else []

    def assert_handover(self, lines: list[str]) -> None:
        chowns = [i for i, line in enumerate(lines) if line.startswith("chown ")]
        self.assertTrue(chowns, "no chown was recorded; the installer never handed the data directories over")
        for index in chowns:
            args = lines[index].split()
            self.assertTrue(any(arg.endswith("/config") for arg in args), f"chown must cover the config directory: {lines[index]}")
            self.assertTrue(any(arg.endswith("/state") for arg in args), f"chown must cover state: {lines[index]}")
            self.assertTrue(any(arg.endswith("/logs") for arg in args), f"chown must cover logs: {lines[index]}")
            self.assertFalse(any(arg.endswith("/secrets") for arg in args),
                             f"the router does not mount secrets; it must stay root-owned: {lines[index]}")
            window = lines[index:index + 5]
            self.assertIn("present config/config.yaml=yes", window, "the chown ran before the configuration was written")
            self.assertIn("present secrets/demo.token=yes", window, "the chown ran before the caller tokens were written")
            # ordering against the stack start
            ups = [j for j, line in enumerate(lines) if line.startswith("docker ") and "compose" in line and " up" in line]
            self.assertTrue(ups, "no `compose up` was recorded")
            self.assertLess(index, min(ups), "the chown must happen before the stack starts")

    def test_install_hands_over_after_writes_and_before_up(self) -> None:
        result = run(["bash", str(EDGE / "install.sh"), "--mode", "docker", "--data-dir", str(self.data),
                      "--router-image", "fake/router:1", "--accelerator", "nvidia"],
                     env=self.env(), timeout=300)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        lines = self.lines()
        self.assert_handover(lines)
        # migrations ran as root before the handover
        runs = [i for i, line in enumerate(lines) if line.startswith("docker ") and " run " in line]
        chowns = [i for i, line in enumerate(lines) if line.startswith("chown ")]
        if runs:
            self.assertLess(max(runs), min(chowns), "the chown must come after every docker run (migrations)")

    def test_upgrade_hands_over_before_recreating_the_router(self) -> None:
        install = run(["bash", str(EDGE / "install.sh"), "--mode", "docker", "--data-dir", str(self.data),
                       "--router-image", "fake/router:1", "--accelerator", "nvidia"],
                      env=self.env(), timeout=300)
        self.assertEqual(install.returncode, 0, install.stdout + install.stderr)
        self.log.unlink()
        upgrade = run(["bash", str(EDGE / "upgrade.sh"), "--data-dir", str(self.data), "--config-only"],
                      env=self.env(), timeout=300)
        self.assertEqual(upgrade.returncode, 0, upgrade.stdout + upgrade.stderr)
        self.assert_handover(self.lines())


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
