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
                   "--accelerator", "amd", "--data-dir", str(amd_dir),
            "--port", "8099"], env={"EDGE_RENDER_GID": "992", "EDGE_VIDEO_GID": "44", "EDGE_TEST_HANDOVER": "1"})
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
        # A normal data-directory parent is searchable; mkdtemp creates 0700, which
        # the traversal preflight correctly refuses.
        self.tmp.chmod(0o755)
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

    def test_config_only_upgrade_keeps_the_installed_image(self) -> None:
        """A --config-only upgrade must migrate with the image the install recorded.

        Gate 2's Halo run hit `Error: parsing reference "": repository name must have at
        least one component` because upgrade.sh passed its empty --router-image variable
        to the migration helper, and the dry-run re-render rewrote install.env.
        """
        install = run(["bash", str(EDGE / "install.sh"), "--mode", "docker", "--data-dir", str(self.data),
                       "--router-image", "fake/router:1", "--accelerator", "nvidia"],
                      env=self.env(), timeout=300)
        self.assertEqual(install.returncode, 0, install.stdout + install.stderr)
        install_env = self.data / "install.env"
        self.assertIn("EDGE_ROUTER_IMAGE=fake/router:1", install_env.read_text(encoding="utf-8"))
        self.log.unlink()
        upgrade = run(["bash", str(EDGE / "upgrade.sh"), "--data-dir", str(self.data), "--config-only"],
                      env=self.env(), timeout=300)
        self.assertEqual(upgrade.returncode, 0, upgrade.stdout + upgrade.stderr)
        migrate_calls = [line for line in self.lines() if "metrum-ai-router-migrate" in line and " run " in line]
        self.assertTrue(migrate_calls, "the migration helper never ran")
        for line in migrate_calls:
            self.assertIn("fake/router:1", line, "the migration ran without the installed image: " + line)
        self.assertIn("EDGE_ROUTER_IMAGE=fake/router:1", install_env.read_text(encoding="utf-8"),
                      "the dry-run re-render must not clear the recorded image")

    def test_config_only_upgrade_failure_text_matches_reality(self) -> None:
        """--config-only never stops the router, so the failure must not say it is stopped."""
        install = run(["bash", str(EDGE / "install.sh"), "--mode", "docker", "--data-dir", str(self.data),
                       "--router-image", "fake/router:1", "--accelerator", "nvidia"],
                      env=self.env(), timeout=300)
        self.assertEqual(install.returncode, 0, install.stdout + install.stderr)
        docker = self.bin / "docker"
        docker.write_text(
            "#!/usr/bin/env bash\n"
            "printf 'docker %s\\n' \"$*\" >> \"$EDGE_TEST_LOG\"\n"
            "case \"$*\" in *metrum-ai-router-migrate*) echo 'podman: boom' >&2; exit 125;; esac\n"
            "exit 0\n", encoding="utf-8")
        docker.chmod(0o755)
        upgrade = run(["bash", str(EDGE / "upgrade.sh"), "--data-dir", str(self.data), "--config-only"],
                      env=self.env(), timeout=300)
        self.assertNotEqual(upgrade.returncode, 0, upgrade.stdout + upgrade.stderr)
        message = upgrade.stderr + upgrade.stdout
        self.assertIn("still serving", message, "the message must say the old router keeps serving: " + message[-400:])
        self.assertNotIn("router stays stopped", message,
                         "--config-only does not stop the router, so that message is false")

    def test_non_root_docker_mode_is_refused(self) -> None:
        """Not root means no handover is possible, so the installer must stop before
        it builds a stack the router can never read (Gate 2 run 1 timed out 360 s on
        exactly that). --dry-run is exempt: it starts nothing."""
        env = self.env()
        env.pop("EDGE_TEST_HANDOVER", None)
        refused = run(["bash", str(EDGE / "install.sh"), "--mode", "docker", "--data-dir", str(self.data),
                       "--router-image", "fake/router:1", "--accelerator", "nvidia"], env=env, timeout=120)
        self.assertNotEqual(refused.returncode, 0, "non-root docker mode must fail instead of carrying on")
        self.assertIn("must run as root", refused.stderr + refused.stdout)

        dry = run(["bash", str(EDGE / "install.sh"), "--mode", "docker", "--dry-run", "--data-dir", str(self.data),
                   "--router-image", "fake/router:1", "--accelerator", "nvidia",
                   "--routerctl", str(self.bin / "metrum-ai-routerctl")], env=env, timeout=120)
        self.assertEqual(dry.returncode, 0, "--dry-run must still work without root: " + dry.stdout + dry.stderr)

    def test_install_hands_over_after_writes_and_before_up(self) -> None:
        result = run(["bash", str(EDGE / "install.sh"), "--mode", "docker", "--data-dir", str(self.data),
                      "--router-image", "fake/router:1", "--accelerator", "nvidia"],
                     env=self.env(), timeout=300)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        lines = self.lines()
        self.assert_handover(lines)
        # migrations ran as root before the handover; a run that records none would
        # otherwise pass this check silently.
        runs = [i for i, line in enumerate(lines) if line.startswith("docker ") and " run " in line]
        chowns = [i for i, line in enumerate(lines) if line.startswith("chown ")]
        self.assertTrue(runs, "no `docker run` (migration) was recorded, so the ordering check would be vacuous")
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


class _FakeDockerHarness(unittest.TestCase):
    """Shared fake docker/chown/curl harness for the behavioural installer tests."""

    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp(prefix="edge-harness-"))
        self.tmp.chmod(0o755)  # a normal parent; mkdtemp's 0700 is correctly refused
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.bin = Path(tempfile.mkdtemp(prefix="edge-harness-bin-"))
        self.addCleanup(shutil.rmtree, self.bin, ignore_errors=True)
        for name in ROUTER_BINARIES:
            source = built_binaries() / name
            (self.bin / name).write_bytes(source.read_bytes())
            (self.bin / name).chmod(0o755)
        for name, body in {
            "docker": "#!/usr/bin/env bash\nprintf 'docker %s\\n' \"$*\" >> \"$EDGE_TEST_LOG\"\nexit 0\n",
            "chown": "#!/usr/bin/env bash\nexit 0\n",
            "curl": "#!/usr/bin/env bash\nprintf '{\"data\":[{\"id\":\"local-chat\"}]}'\nexit 0\n",
        }.items():
            path = self.bin / name
            path.write_text(body, encoding="utf-8")
            path.chmod(0o755)

    def installer_env(self, extra: dict[str, str] | None = None) -> dict[str, str]:
        env = os.environ.copy()
        env.update({
            "PATH": f"{self.bin}:{env.get('PATH', '')}",
            "ROUTERCTL_OVERRIDE": str(self.bin / "metrum-ai-routerctl"),
            "EDGE_TEST_LOG": str(self.tmp / "calls.log"),
        })
        if extra:
            env.update(extra)
        for key in ("LOCAL_VLLM_API_KEY", "ROUTER_ID_TRANSFORM_KEY", "METRUM_ADMIN_PASSWORD_HASH"):
            env.pop(key, None)
        return env


class AmdGroupIdTest(_FakeDockerHarness):
    """AMD compose files must name numeric host GIDs, and install.sh must produce them.

    The pinned ROCm image has no `render` entry in its /etc/group, so
    `group_add: [video, render]` fails with "Unable to find group render" - on Docker
    as well as Podman, because both resolve group names inside the container.
    """

    AMD_COMPOSE = (
        EDGE / "compose.amd.yaml",
        ROOT / "deploy" / "reference" / "amd-mi350p-datacenter" / "compose.yaml",
        ROOT / "deploy" / "reference" / "amd-strix-halo-edge" / "compose.yaml",
    )

    def setUp(self) -> None:
        super().setUp()
        try:
            import yaml
        except ImportError:  # pragma: no cover
            self.skipTest("PyYAML is not installed")
        self.yaml = yaml

    def run_install(self, env: dict[str, str]) -> subprocess.CompletedProcess[str]:
        return run(["bash", str(EDGE / "install.sh"), "--mode", "docker", "--accelerator", "amd",
                    "--data-dir", str(self.tmp / "data"), "--router-image", "fake/router:1"],
                   env=self.installer_env(env), timeout=300)

    def test_no_amd_compose_uses_group_names(self) -> None:
        for path in self.AMD_COMPOSE:
            compose = self.yaml.safe_load(path.read_text(encoding="utf-8"))
            for name, service in (compose.get("services") or {}).items():
                for entry in service.get("group_add") or []:
                    entry = str(entry)
                    numeric = entry.isdigit() or entry.startswith("${")
                    self.assertTrue(numeric, f"{path.relative_to(ROOT)}: {name} group_add uses a name ({entry!r}); "
                                             "the ROCm image cannot resolve it, use a numeric host GID")

    def test_install_writes_numeric_gpu_group_ids(self) -> None:
        result = self.run_install({"EDGE_TEST_HANDOVER": "1", "EDGE_RENDER_GID": "992", "EDGE_VIDEO_GID": "44"})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        env_text = (self.tmp / "data" / "secrets" / "env").read_text(encoding="utf-8")
        self.assertIn("RENDER_GID='992'", env_text)
        self.assertIn("VIDEO_GID='44'", env_text)

    def test_install_without_devices_or_override_fails_clearly(self) -> None:
        if os.path.exists("/dev/dri/renderD128"):
            self.skipTest("this host has real GPU device nodes, so detection succeeds")
        result = self.run_install({"EDGE_TEST_HANDOVER": "1"})
        self.assertNotEqual(result.returncode, 0, "AMD docker mode without device nodes must not continue")
        self.assertIn("render node", result.stderr + result.stdout)


class DataDirTraversalTest(_FakeDockerHarness):
    """Docker mode renders container paths, and the data directory must be reachable.

    Gate 2 docker run 2 died with "initialize router: mkdir /root/metrum-edge:
    permission denied" after the ownership handover had worked: the configuration
    carried the host data directory while the container mounts it at /app, and the
    default path under sudo is /root/metrum-edge, which uid 65532 cannot traverse.
    """

    def install(self, data_dir: Path, extra_env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
        return run(["bash", str(EDGE / "install.sh"), "--mode", "docker", "--accelerator", "nvidia",
                    "--data-dir", str(data_dir), "--router-image", "fake/router:1"],
                   env=self.installer_env(extra_env), timeout=300)

    def test_docker_config_renders_container_paths(self) -> None:
        open_dir = self.tmp / "open"
        open_dir.mkdir()
        open_dir.chmod(0o755)
        result = self.install(open_dir / "edge", {"EDGE_TEST_HANDOVER": "1"})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        config = (open_dir / "edge" / "config" / "config.yaml").read_text(encoding="utf-8")
        self.assertIn("/app/state/usage.sqlite", config)
        self.assertIn("/app/logs/requests.jsonl", config)
        self.assertIn("/app/state/router-state.json", config)
        self.assertNotIn(str(open_dir), config, "the docker-mode config must not carry host paths")

    def test_unsearchable_parent_is_refused(self) -> None:
        locked = self.tmp / "locked"
        locked.mkdir()
        locked.chmod(0o700)
        result = self.install(locked / "edge", {"EDGE_TEST_HANDOVER": "1"})
        self.assertNotEqual(result.returncode, 0, "an unsearchable data-directory parent must be refused")
        self.assertIn("not traversable", result.stderr + result.stdout)
        self.assertIn("--data-dir", result.stderr + result.stdout)

    def test_searchable_parent_is_accepted(self) -> None:
        open_dir = self.tmp / "open2"
        open_dir.mkdir()
        open_dir.chmod(0o755)
        result = self.install(open_dir / "edge", {"EDGE_TEST_HANDOVER": "1"})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


class BundleScriptAgreementTest(_FakeDockerHarness):
    """The four bundle scripts must agree on the data directory and on paths.

    install.sh defaults a root install to /var/lib/metrum-edge (because /root is 0700
    and the router runs as uid 65532); demo.sh, upgrade.sh and uninstall.sh inherited
    the old $HOME default, so `sudo bash demo.sh` looked in /root/metrum-edge and
    exited with "install.env not found". The docker-mode demo also passed the host
    config path to `exec router`, where only /app/config/config.yaml exists.
    """

    def setUp(self) -> None:
        super().setUp()
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.home.chmod(0o755)
        self.data = self.tmp / "data"
        self.data.mkdir()
        self.data.chmod(0o755)

    def base_env(self, extra: dict[str, str] | None = None) -> dict[str, str]:
        env = self.installer_env({"HOME": str(self.home)})
        if extra:
            env.update(extra)
        return env

    def install(self) -> subprocess.CompletedProcess[str]:
        return run(["bash", str(EDGE / "install.sh"), "--mode", "docker", "--accelerator", "nvidia",
                    "--data-dir", str(self.data), "--router-image", "fake/router:1"],
                   env=self.base_env({"EDGE_TEST_HANDOVER": "1", "EDGE_RENDER_GID": "44", "EDGE_VIDEO_GID": "44"}),
                   timeout=300)

    def test_lifecycle_scripts_find_the_install_without_a_data_dir(self) -> None:
        """demo/upgrade/uninstall must default to the same rule install.sh uses."""
        installed = self.install()
        self.assertEqual(installed.returncode, 0, installed.stdout + installed.stderr)
        install_env = (self.data / "install.env").read_text(encoding="utf-8")
        self.assertIn("EDGE_MODE=docker", install_env)
        for script, extra in (("demo.sh", {"HOME": str(self.home)}),
                              ("upgrade.sh", {"HOME": str(self.home)}),
                              ("uninstall.sh", {"HOME": str(self.home)})):
            result = run(["bash", str(EDGE / script), "--data-dir", str(self.data)],
                         env=self.base_env(extra), timeout=300)
            self.assertNotIn("install.env not found", result.stdout + result.stderr,
                             f"{script} did not find the install: " + (result.stdout + result.stderr)[:200])

    def test_docker_demo_passes_container_paths_to_exec(self) -> None:
        installed = self.install()
        self.assertEqual(installed.returncode, 0, installed.stdout + installed.stderr)
        demo = run(["bash", str(EDGE / "demo.sh"), "--data-dir", str(self.data)],
                   env=self.base_env({"EDGE_TEST_HANDOVER": "1"}), timeout=300)
        calls = (self.tmp / "calls.log").read_text(encoding="utf-8")
        exec_lines = [line for line in calls.splitlines() if " exec " in line and "router" in line]
        self.assertTrue(exec_lines, "the demo never invoked the router container: " + calls[-400:])
        for line in exec_lines:
            # Only the command after `exec -T router` runs inside the container; the
            # --env-file/-f arguments before it are host-side docker CLI paths.
            container_command = line.split("exec -T router", 1)[1]
            self.assertNotIn(str(self.data), container_command,
                             "a host path reached the router container: " + line)
        self.assertTrue(any("/app/config/config.yaml" in line for line in exec_lines),
                        "the usage report must read the in-container config path: " + "\n".join(exec_lines))
        self.assertIn(demo.returncode, (0, 1), demo.stdout + demo.stderr)

    def test_docker_mounts_listen_on_all_interfaces_but_publish_loopback(self) -> None:
        """A container process bound to 127.0.0.1 never answers the published port.

        Gate 2 docker run 3 sat out the full 360 s readiness wait while the router
        logged `listening on 127.0.0.1:8080` inside its container; the published
        127.0.0.1:8080 answered nothing. Docker mode must render 0.0.0.0 into the
        container while the compose publish stays on the operator's --listen address.
        """
        installed = self.install()
        self.assertEqual(installed.returncode, 0, installed.stdout + installed.stderr)
        config = (self.data / "config" / "config.yaml").read_text(encoding="utf-8")
        self.assertIn("listen: 0.0.0.0:8080", config,
                      "docker mode must bind all interfaces inside the container")
        self.assertNotIn("listen: 127.0.0.1:8080", config)
        env_text = (self.data / "secrets" / "env").read_text(encoding="utf-8")
        self.assertIn("EDGE_BIND_ADDRESS='127.0.0.1'", env_text,
                      "host exposure must stay loopback by default")
        compose = (ROOT / "deploy" / "edge" / "compose.yaml").read_text(encoding="utf-8")
        self.assertIn("${EDGE_BIND_ADDRESS:-127.0.0.1}:${EDGE_ROUTER_PORT:-8080}:8080", compose)

    def test_native_mode_keeps_the_operator_listen_address(self) -> None:
        binary = self.tmp / "bin"
        binary.mkdir()
        for name in ("metrum-ai-router", "metrum-ai-routerctl", "metrum-ai-router-migrate"):
            stub = binary / name
            stub.write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
            stub.chmod(0o755)
        # --dry-run renders the whole configuration and starts nothing: a real native
        # start would bind the fixed mock ports (18081-18083) and leave a router and
        # three mocks behind with this install's keys, which then answer the *next*
        # install's traffic with 401 (seen in CI, where the port collision made the
        # later demo test fail).
        result = run(["bash", str(EDGE / "install.sh"), "--mode", "native", "--accelerator", "nvidia",
                      "--bin-dir", str(binary), "--data-dir", str(self.data),
                      "--listen", "127.0.0.1", "--dry-run"],
                     env=self.base_env({"EDGE_TEST_HANDOVER": "1"}), timeout=300)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        config = (self.data / "config" / "config.yaml").read_text(encoding="utf-8")
        self.assertIn("listen: 127.0.0.1:8080", config,
                      "native mode listens where the operator asked, on the host itself")
        self.assertNotIn("listen: 0.0.0.0", config, result.stdout + result.stderr)

    def test_model_servers_start_in_sequence(self) -> None:
        """Each server sizes its KV cache against the device's free memory.

        Measured on an A6000 48 GB: with both servers loading in parallel the tiny slot
        reported "Available KV cache memory: -6.75 GiB" and died, then came up healthy on
        the retry once the primary had settled. vllm-primary must therefore wait for
        vllm-tiny to be healthy, and the router must still wait for both.
        """
        import yaml
        compose = yaml.safe_load((EDGE / "compose.yaml").read_text(encoding="utf-8"))
        services = compose["services"]
        self.assertEqual(services["vllm-primary"].get("depends_on"),
                         {"vllm-tiny": {"condition": "service_healthy"}},
                         "the primary must not start while the tiny slot is still loading")
        router_deps = services["router"].get("depends_on") or {}
        self.assertEqual(set(router_deps), {"vllm-tiny", "vllm-primary"},
                         "the router must wait for both model servers")
        # The AMD path must be sequenced the same way, and say so where a reader looks.
        amd = yaml.safe_load((EDGE / "compose.amd.yaml").read_text(encoding="utf-8"))
        self.assertEqual(amd["services"]["vllm-primary"].get("depends_on"),
                         {"vllm-tiny": {"condition": "service_healthy"}},
                         "the AMD overlay must sequence the ROCm servers too")

    def test_cold_start_wait_covers_a_first_pull(self) -> None:
        """360 s was shorter than a measured cold first boot (6 min 18 s on an A6000)."""
        import re
        install = (EDGE / "install.sh").read_text(encoding="utf-8")
        match = re.search(r'timeout="\$\{EDGE_READY_TIMEOUT:-(\d+)\}"', install)
        self.assertIsNotNone(match, "the readiness wait must be configurable")
        self.assertGreaterEqual(int(match.group(1)), 600,
                                "a cold pull plus two model servers takes longer than 10 minutes")
        self.assertIn("waiting for ${BASE_URL}/readyz", install, "the wait must say it is waiting")
        readme = (EDGE / "README.md").read_text(encoding="utf-8")
        self.assertIn("EDGE_READY_TIMEOUT", readme)
        # Both measurements the default has to cover, named in the docs.
        for measured in ("6 min 18 s", "516 s"):
            self.assertIn(measured, readme, f"the README must state the measured cold boot ({measured})")

    def test_demo_reports_not_ready_as_a_failure_not_a_skip(self) -> None:
        """Run 4's demo printed four HTTP 000 lines and said nothing about readiness.

        The demo now waits for /readyz with a bounded timeout and then fails loudly, so a
        router that is still starting can never look like a skipped or passing step.
        """
        installed = self.install()  # docker mode, fake docker: nothing answers /readyz
        self.assertEqual(installed.returncode, 0, installed.stdout + installed.stderr)
        # This curl answers JSON for API calls but fails /readyz, which is the state run 4
        # observed while the model servers were reloading.
        curl = self.bin / "curl"
        curl.write_text(
            "#!/usr/bin/env bash\n"
            "for arg in \"$@\"; do case \"$arg\" in */readyz) exit 7;; esac; done\n"
            "printf '{\"data\":[{\"id\":\"local-chat\"}]}'\n",
            encoding="utf-8")
        curl.chmod(0o755)
        demo = run(["bash", str(EDGE / "demo.sh"), "--data-dir", str(self.data)],
                   env=self.base_env({"EDGE_TEST_HANDOVER": "1", "EDGE_DEMO_READY_TIMEOUT": "2",
                                      "EDGE_BASE_URL": "http://127.0.0.1:9"}),
                   timeout=120)
        output = demo.stdout + demo.stderr
        self.assertNotEqual(demo.returncode, 0, "an unready router must fail the demo: " + output[-300:])
        self.assertIn("NOT READY", output)
        self.assertNotIn("SKIP", output.upper().replace("SKIPPED", ""))
        self.assertIn("1 step(s) failed", output)

    def test_48gb_profile_satisfies_the_stated_headroom_rule(self) -> None:
        """The documented profile must satisfy the rule the README states, not a literal.

        A real A6000 run at 0.26 + 0.70 (0.96 of a 47.40 GiB card) put the pair in a
        restart loop: "No available memory for the cache blocks" on the tiny slot and
        "torch.OutOfMemoryError: CUDA out of memory ... 352.31 MiB is free" on the
        primary, because each server grows into graphs and activations beyond its
        fraction of total capacity. The rule is published in the README and read from
        there, so the numbers may change as long as the rule holds.
        """
        import re
        # Hard-coded on purpose: reading the bound out of the README it checks would let a
        # README edit raise the rule to 0.99 and pass 0.70 + 0.26 again. 0.96 is the sum
        # that put a real A6000 into a restart loop in Gate 2 run 4 (see the citation
        # asserts below), so no more than 0.90 may be claimed.
        HEADROOM_BOUND = 0.90
        readme = (EDGE / "README.md").read_text(encoding="utf-8")
        rule = re.search(r"sum\(EDGE_PRIMARY_UTILIZATION, EDGE_TINY_UTILIZATION\) <= (0\.\d+)", readme)
        self.assertIsNotNone(rule, "the README must state the utilization-sum rule")
        self.assertEqual(float(rule.group(1)), HEADROOM_BOUND,
                         f"the README states {rule.group(1)}; this test enforces {HEADROOM_BOUND}")
        utils = [float(m) for m in re.findall(r"export EDGE_(?:PRIMARY|TINY)_UTILIZATION=(0\.\d+)", readme)]
        self.assertEqual(len(utils), 2, f"the 48 GB profile must set both utilizations: {utils}")
        self.assertLessEqual(sum(utils), HEADROOM_BOUND,
                             f"utilizations {utils} sum to {sum(utils):.2f}, above {HEADROOM_BOUND}")
        # The derivation has to stay traceable to the run-4 measurements.
        for evidence in ("47.40 GiB", "Available KV cache memory: 8.75 GiB", "352.31 MiB is free"):
            self.assertIn(evidence, readme, f"the README must cite the measured {evidence!r}")

    def test_tiny_slot_caps_concurrency_and_context(self) -> None:
        """The 48 GB cold-start starvation is fixed at its cause, not by retrying.

        vLLM sizes CUDA graphs and activation buffers per concurrent sequence, so the
        default 256 starved the KV allocation at 0.22 utilization on an A6000. The tiny
        slot must expose both caps and the documented profile must set them.
        """
        compose = (EDGE / "compose.yaml").read_text(encoding="utf-8")
        for knob in ("--max-num-seqs", "EDGE_TINY_MAX_NUM_SEQS", "EDGE_TINY_MAX_MODEL_LEN"):
            self.assertIn(knob, compose, f"compose must expose {knob} for the tiny slot")
        readme = (EDGE / "README.md").read_text(encoding="utf-8")
        for line in ("export EDGE_TINY_MAX_NUM_SEQS=8", "export EDGE_TINY_MAX_MODEL_LEN=4096"):
            self.assertIn(line, readme, f"the 48 GB profile must set {line!r}")
        self.assertIn("computed, not measured", readme)

    def test_gpu_gid_detection_edge_cases(self) -> None:
        """A missing render node is fatal even when /dev/kfd exists, and the video id
        never comes from kfd (which belongs to the render group)."""
        dev = self.tmp / "dev"
        (dev / "dri").mkdir(parents=True)
        (dev / "kfd").write_text("", encoding="utf-8")
        missing_render = run(["bash", str(EDGE / "install.sh"), "--mode", "docker", "--accelerator", "amd",
                              "--data-dir", str(self.tmp / "d1"), "--router-image", "fake/router:1"],
                             env=self.base_env({"EDGE_TEST_HANDOVER": "1", "EDGE_DEV_ROOT": str(dev)}), timeout=120)
        self.assertNotEqual(missing_render.returncode, 0, "no render node must be fatal")
        self.assertIn("render node", missing_render.stderr + missing_render.stdout)

        # renderD128 with no card0: the video id must come from the group database.
        (dev / "dri" / "renderD128").write_text("", encoding="utf-8")
        getent = self.bin / "getent"
        getent.write_text("#!/usr/bin/env bash\nprintf 'video:x:44:\n'\n", encoding="utf-8")
        getent.chmod(0o755)
        falls_back = run(["bash", str(EDGE / "install.sh"), "--mode", "docker", "--accelerator", "amd",
                          "--data-dir", str(self.tmp / "d2"), "--router-image", "fake/router:1"],
                         env=self.base_env({"EDGE_TEST_HANDOVER": "1", "EDGE_DEV_ROOT": str(dev)}), timeout=120)
        self.assertEqual(falls_back.returncode, 0, falls_back.stdout + falls_back.stderr)
        env_text = (self.tmp / "d2" / "secrets" / "env").read_text(encoding="utf-8")
        self.assertIn("VIDEO_GID='44'", env_text, "the video group id must come from the group database, not kfd")


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
