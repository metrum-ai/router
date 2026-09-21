#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Self-test Compose clean-stack cutover fail-closed checks."""

from __future__ import annotations

import json
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import compose_clean_cutover as cutover
import compose_package_upgrade as upgrade


COMPOSE_JSON = json.dumps(
    {
        "name": "compose",
        "networks": {"default": {"name": "compose_default"}},
        "volumes": {
            "postgres_data": {"name": "compose_postgres_data"},
            "caddy_data": {"name": "compose_caddy_data"},
            "caddy_config": {"name": "compose_caddy_config"},
        },
    }
)


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def write(path: Path, text: str = "x\n") -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def make_package(root: Path, *, version: str = "a910827", arch: str = "amd64") -> Path:
    payload = root / f"metrum-ai-router-{version}-docker-linux-{arch}"
    write(payload / "compose" / "docker-compose.yml", "services: {}\n")
    write(payload / "compose" / "docker-compose.postgres-localhost.yml", "services:\n  postgres: {}\n")
    write(payload / "compose" / ".env", "SMART_LLMROUTER_VERSION=placeholder\n")
    write(payload / "images" / f"metrum-ai-router-{version}-linux-{arch}.tar", "image-bytes\n")
    archive = root / f"metrum-ai-router-{version}-docker-linux-{arch}.tar.gz"
    with tarfile.open(archive, "w:gz") as tar:
        tar.add(payload, arcname=payload.name)
    return archive


def make_install(root: Path) -> Path:
    install = root / "metrum-ai-router"
    write(install / "compose" / "config" / "config.yaml", "live: true\n")
    write(install / "compose" / "config" / "env.json", '{"k":"secret"}\n')
    write(install / "compose" / "state" / "quota.json", "{}\n")
    write(install / "compose" / "logs" / "requests.jsonl", "{}\n")
    write(
        install / "compose" / ".env",
        "SMART_LLMROUTER_VERSION=4175bdf-linux-amd64\n"
        "KEEP=yes\n"
        "ROUTER_USAGE_DB_DSN=host=postgres\n",
    )
    write(install / "compose" / "ROUTER_TOKEN.txt", "token-redacted\n")
    write(install / "compose" / "ROUTER_TOKEN_HARBOR.txt", "harbor-redacted\n")
    write(install / "compose" / "docker-compose.yml", "old: true\n")
    write(install / "compose" / "docker-compose.postgres-localhost.yml", "services:\n  postgres: {}\n")
    return install


def job_json(state: str) -> str:
    return json.dumps(
        {
            "Compatible": True,
            "State": "current" if state == "validated" else "pending",
            "Jobs": [{"Key": cutover.JOB_KEY, "State": state}],
        }
    )


class FakeRunner:
    def __init__(self, *, resume_states: list[str] | None = None) -> None:
        self.calls: list[list[str]] = []
        self.volume_rms: list[str] = []
        self.verify_index: int | None = None
        self.full_up_index: int | None = None
        self.resume_states = list(resume_states or ["validated"])
        self.resume_calls = 0

    def __call__(self, args, *, cwd=None) -> subprocess.CompletedProcess[str]:
        argv = [str(part) for part in args]
        self.calls.append(argv)
        joined = " ".join(argv)
        if "down" in argv:
            raise AssertionError(f"compose down was invoked: {argv}")
        if "volume" in argv and "rm" in argv:
            volume = argv[-1]
            require("caddy" not in volume.lower(), f"caddy volume removed: {volume}")
            self.volume_rms.append(volume)
            return subprocess.CompletedProcess(argv, 0, stdout="", stderr="")
        if "config" in argv and "--format" in argv:
            return subprocess.CompletedProcess(argv, 0, stdout=COMPOSE_JSON, stderr="")
        if "/app/bin/metrum-ai-router-migrate" in argv:
            action = ""
            for part in argv:
                if part.startswith("--action="):
                    action = part.split("=", 1)[1]
            text = "scope=usage schema_version=2 data_version=1 compatible=true state=current pending=0 entries=3\n"
            if action == "resume":
                state = self.resume_states[min(self.resume_calls, len(self.resume_states) - 1)]
                self.resume_calls += 1
                return subprocess.CompletedProcess(argv, 0, stdout=text, stderr="")
            if action == "verify-serving":
                self.verify_index = len(self.calls) - 1
                if self.resume_calls == 0:
                    return subprocess.CompletedProcess(argv, 1, stdout="", stderr="verify-serving pending")
                state = self.resume_states[min(self.resume_calls - 1, len(self.resume_states) - 1)]
                if state == "validated":
                    return subprocess.CompletedProcess(argv, 0, stdout=text, stderr="")
                return subprocess.CompletedProcess(argv, 1, stdout="", stderr="verify-serving running")
            if action in {"plan", "apply", "status"}:
                return subprocess.CompletedProcess(argv, 0, stdout=text, stderr="")
            raise AssertionError(f"unexpected migrate action {action}: {argv}")
        if argv[-2:] == ["up", "-d"]:
            self.full_up_index = len(self.calls) - 1
            return subprocess.CompletedProcess(argv, 0, stdout="", stderr="")
        if "ps" in argv:
            return subprocess.CompletedProcess(argv, 0, stdout="compose-router metrum-ai-router:a910827-linux-amd64 Up\n", stderr="")
        if "load" in argv:
            return subprocess.CompletedProcess(argv, 0, stdout="Loaded image\n", stderr="")
        if "pg_isready" in joined or "stop" in argv or (argv[-2:] == ["-d", "postgres"]):
            return subprocess.CompletedProcess(argv, 0, stdout="accepting connections\n", stderr="")
        return subprocess.CompletedProcess(argv, 0, stdout="", stderr="")


def test_missing_runtime_aborts() -> None:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        package = make_package(root)
        install = root / "metrum-ai-router"
        write(install / "compose" / "docker-compose.yml", "old: true\n")
        info = upgrade.inspect_package(package)
        try:
            cutover.apply_local(
                info,
                install,
                "clean",
                "20260818T180000Z",
                confirm=cutover.CONFIRM_RESET,
                skip_archive=True,
                skip_package=True,
                skip_serve=True,
                runner=FakeRunner(),
                sleeper=lambda _seconds: None,
            )
        except cutover.CutoverError as exc:
            require("missing required runtime" in str(exc), f"unexpected error: {exc}")
        else:
            raise AssertionError("missing runtime was accepted")


def test_missing_tokens_aborts() -> None:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        package = make_package(root)
        install = make_install(root)
        (install / "compose" / "ROUTER_TOKEN.txt").unlink()
        (install / "compose" / "ROUTER_TOKEN_HARBOR.txt").unlink()
        info = upgrade.inspect_package(package)
        try:
            cutover.plan_cutover(info, install, "clean", "20260818T180001Z")
        except cutover.CutoverError as exc:
            require("ROUTER_TOKEN" in str(exc), f"unexpected error: {exc}")
        else:
            raise AssertionError("missing tokens were accepted")


def test_forbidden_paths() -> None:
    for path in ("/", "/usr", "/bin", "/opt"):
        try:
            upgrade.reject_forbidden(Path(path), label="install-root")
        except upgrade.UpgradeError:
            continue
        raise AssertionError(f"forbidden path accepted: {path}")


def test_plan_is_read_only() -> None:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        package = make_package(root)
        install = make_install(root)
        before = {path.relative_to(install): path.read_bytes() for path in install.rglob("*") if path.is_file()}
        info = upgrade.inspect_package(package)
        plan = cutover.plan_cutover(info, install, "clean", "20260818T180002Z")
        after = {path.relative_to(install): path.read_bytes() for path in install.rglob("*") if path.is_file()}
        require(before == after, "plan mutated the install root")
        require(plan["unpack"] == "tar --strip-components=1", "plan missing strip unpack")
        require(plan["skip_instance_reboot"] is True, "plan should refuse instance reboot")
        require(plan["skip_instance_reboot"] is True, "plan should skip instance reboot")
        require(plan["compose_down_volumes"] is False, "plan should refuse compose down -v")
        require(plan["dsn_flag"] == "--dsn-env=ROUTER_USAGE_DB_DSN", "plan leaked a DSN flag")
        require("caddy_data" in plan["keep"], "plan dropped caddy keep")
        require(plan["reset"] == "postgres_data volume only", "plan reset target drifted")


def test_confirm_required() -> None:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        package = make_package(root)
        install = make_install(root)
        info = upgrade.inspect_package(package)
        try:
            cutover.apply_local(
                info,
                install,
                "clean",
                "20260818T180003Z",
                confirm="yes",
                skip_archive=True,
                skip_package=True,
                skip_serve=True,
                runner=FakeRunner(),
                sleeper=lambda _seconds: None,
            )
        except cutover.CutoverError as exc:
            require("confirm-reset-usage" in str(exc), f"unexpected error: {exc}")
        else:
            raise AssertionError("unconfirmed reset was accepted")


def test_archive_restic_argv_has_no_secrets() -> None:
    recorded: list[list[str]] = []

    def restic_fn(stage: Path, version: str, stamp: str) -> str:
        require((stage / "usage.dump").read_bytes() == b"FAKE-DUMP", "dump was not staged")
        require("secret" not in (stage / "MANIFEST.txt").read_text(encoding="utf-8"), "manifest leaked config")
        require("host=postgres" not in (stage / "MANIFEST.txt").read_text(encoding="utf-8"), "manifest leaked DSN")
        recorded.append(["restic", "backup", "--tag", "purpose:compose-usage-archive", "--tag", f"version:{version}", "--", str(stage)])
        return "abc123"

    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        install = make_install(root)
        dump = root / "usage.dump"

        def dump_fn(_install: Path, dest: Path) -> None:
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.write_bytes(b"FAKE-DUMP")

        result = cutover.archive_usage(
            install,
            "a910827",
            "20260818T180004Z",
            dump,
            dump_fn=dump_fn,
            restic_fn=restic_fn,
        )
        require(result["restic_snapshot"] == "abc123", "snapshot id not returned")
        require(recorded, "restic was not invoked")
        argv = recorded[0]
        require("--tag" in argv, "restic tags missing")
        require("purpose:compose-usage-archive" in argv, "purpose tag missing")
        require("version:a910827" in argv, "version tag missing")
        require(not any("BACKUP_" in part or "restic-pass" in part.lower() for part in argv), f"secret in restic argv: {argv}")
        require(not any("host=postgres" in part or "ROUTER_USAGE_DB_DSN" in part for part in argv), f"DSN in restic argv: {argv}")
        require("env.json" not in " ".join(argv), "env.json staged into restic")
        require("config.yaml" not in " ".join(argv), "config.yaml staged into restic")


def test_volume_reset_and_migrate_before_serve() -> None:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        package = make_package(root)
        install = make_install(root)
        info = upgrade.inspect_package(package)
        runner = FakeRunner()

        def dump_fn(_install: Path, dest: Path) -> None:
            dest.write_bytes(b"FAKE-DUMP")

        def restic_fn(_stage: Path, _version: str, _stamp: str) -> str:
            return "snap1"

        result = cutover.apply_local(
            info,
            install,
            "clean",
            "20260818T180005Z",
            confirm=cutover.CONFIRM_RESET,
            skip_archive=False,
            skip_package=False,
            skip_serve=True,
            dump_path=root / "usage.dump",
            runner=runner,
            dump_fn=dump_fn,
            restic_fn=restic_fn,
            sleeper=lambda _seconds: None,
        )
        require(result["postgres_volume"] == "compose_postgres_data", f"unexpected volume {result['postgres_volume']}")
        require(runner.volume_rms == ["compose_postgres_data"], f"unexpected volume rms {runner.volume_rms}")
        rm_postgres = [cmd for cmd in runner.calls if cmd[-3:] == ["rm", "-f", "postgres"] or ( "rm" in cmd and "-f" in cmd and "postgres" in cmd)]
        require(rm_postgres, "postgres container was not removed before volume rm")
        volume_rm_index = next(i for i, cmd in enumerate(runner.calls) if cmd[:3] == ["docker", "volume", "rm"])
        container_rm_index = next(i for i, cmd in enumerate(runner.calls) if "rm" in cmd and "postgres" in cmd and "-f" in cmd and "volume" not in cmd)
        require(container_rm_index < volume_rm_index, "volume rm ran before postgres container rm")
        require((install / "compose" / "config" / "config.yaml").read_text(encoding="utf-8") == "live: true\n", "live config was not copied")
        require((install / "compose" / "ROUTER_TOKEN.txt").exists(), "token was not copied")
        require("SMART_LLMROUTER_VERSION=a910827-linux-amd64" in (install / "compose" / ".env").read_text(encoding="utf-8"), "version not pinned")
        require(result["job_state"] == "validated", "migration gate did not validate")
        require(runner.verify_index is not None, "verify-serving was not run")
        require(runner.full_up_index is None, "compose up -d ran before skip_serve returned")
        migrate_cmds = [cmd for cmd in runner.calls if "/app/bin/metrum-ai-router-migrate" in cmd]
        require(any("--dsn-env=ROUTER_USAGE_DB_DSN" in cmd for cmd in migrate_cmds), "missing dsn-env")
        require(all(cmd[:2] == ["docker", "run"] for cmd in migrate_cmds), "migrate used compose run")
        require(all("--env-file" in cmd for cmd in migrate_cmds), "migrate missing env-file")
        require(not any(any(part.startswith("--dsn=") or part == "--dsn" for part in cmd) for cmd in migrate_cmds), "DSN passed on CLI")
        require(not any("down" in cmd for cmd in runner.calls), "compose down invoked")
        load_cmds = [cmd for cmd in runner.calls if cmd[:2] == ["docker", "load"]]
        require(load_cmds, "packaged image was not loaded")


def test_resume_loop_stops_at_validated_then_serves() -> None:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        package = make_package(root)
        install = make_install(root)
        info = upgrade.inspect_package(package)
        runner = FakeRunner(resume_states=["running", "validated"])
        cutover.apply_local(
            info,
            install,
            "clean",
            "20260818T180006Z",
            confirm=cutover.CONFIRM_RESET,
            skip_archive=True,
            skip_package=True,
            skip_serve=False,
            runner=runner,
            sleeper=lambda _seconds: None,
        )
        resume_cmds = [cmd for cmd in runner.calls if "--action=resume" in cmd]
        require(len(resume_cmds) == 2, f"expected two resume calls, got {len(resume_cmds)}")
        require(any("--checkpoint-ordinal=0" in cmd for cmd in resume_cmds), "missing ordinal 0")
        require(any("--checkpoint-ordinal=1" in cmd for cmd in resume_cmds), "missing ordinal 1")
        require(runner.verify_index is not None, "verify-serving missing")
        require(runner.full_up_index is not None, "compose up -d missing")
        require(runner.verify_index < runner.full_up_index, "compose up -d ran before verify-serving")


def test_refuses_caddy_volume_name() -> None:
    try:
        cutover.postgres_volume_name(
            {"name": "compose", "volumes": {"postgres_data": {"name": "compose_caddy_data"}}}
        )
    except cutover.CutoverError as exc:
        require("caddy" in str(exc), f"unexpected error: {exc}")
    else:
        raise AssertionError("caddy volume name was accepted as postgres_data")


def test_unsafe_commands() -> None:
    try:
        cutover.reject_unsafe_command(["docker", "compose", "down", "-v"])
    except cutover.CutoverError:
        pass
    else:
        raise AssertionError("down -v accepted")
    try:
        cutover.reject_unsafe_command(["docker", "volume", "rm", "compose_caddy_data"])
    except cutover.CutoverError:
        pass
    else:
        raise AssertionError("caddy volume rm accepted")


def test_remote_pg_dump_keeps_container_user() -> None:
    cmd = cutover.remote_pg_dump_command(Path("/opt/metrum-ai-router"))
    require("sh -c " in cmd, f"missing container sh -c: {cmd}")
    require('"$POSTGRES_USER"' in cmd, f"POSTGRES_USER would expand on the SSH host: {cmd}")
    require(cmd.startswith("sudo docker compose "), f"unexpected prefix: {cmd}")
    argv = ["ssh", "ubuntu@example", cmd]
    require(argv[-1] == cmd, "remote dump command must be a single SSH argument")


def test_remote_apply_forwards_package() -> None:
    cmd = cutover.remote_apply_command(
        "compose_clean_cutover.py",
        ["apply", "--install-root", "/opt/metrum-ai-router", "--skip-archive", "--confirm-reset-usage", "reset-postgres-data"],
        "metrum-ai-router-d73ac83-docker-linux-amd64.tar.gz",
    )
    require("--package" in cmd, f"missing --package: {cmd}")
    require("/tmp/metrum-ai-router-d73ac83-docker-linux-amd64.tar.gz" in cmd, f"package path missing: {cmd}")
    require(cmd[cmd.index("--package") + 1].startswith("/tmp/"), "package must be the uploaded /tmp path")


def test_source_has_no_star_move_or_dsn_flag() -> None:
    source = Path(cutover.__file__).read_text(encoding="utf-8")
    require("shell=True" not in source, "shell=True would allow glob expansion")
    require("mv ${" not in source, "shell mv with expansion is present")
    require("${INNER}" not in source, "INNER glob unpacker is present")
    require("add_argument(\"--dsn\"" not in source, "DSN CLI flag is present")
    require("--dsn-env=" in source, "dsn-env flag missing")
    require("compose_package_upgrade" in source, "upgrade helper was not reused")
    require("skip_compose" in source, "package apply should reuse skip-compose")


def main() -> int:
    test_missing_runtime_aborts()
    test_missing_tokens_aborts()
    test_forbidden_paths()
    test_plan_is_read_only()
    test_confirm_required()
    test_archive_restic_argv_has_no_secrets()
    test_volume_reset_and_migrate_before_serve()
    test_resume_loop_stops_at_validated_then_serves()
    test_refuses_caddy_volume_name()
    test_unsafe_commands()
    test_remote_pg_dump_keeps_container_user()
    test_remote_apply_forwards_package()
    test_source_has_no_star_move_or_dsn_flag()
    print("compose clean cutover self-test passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
