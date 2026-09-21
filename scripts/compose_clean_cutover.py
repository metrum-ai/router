#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Deterministic Compose usage-store reset for an existing install root.

Archives the live Postgres usage database, replaces the packaged files, removes
only the Compose postgres_data volume, runs the empty-DB migration gate, then
serves. It never glob-moves from `/`, never runs compose down with volumes,
never removes Caddy volumes, never stops or reboots the host, and never invokes
EKS tooling.

Callers, model groups, provider keys, license/quota state, and Caddy stay in
the preserved runtime files and volumes. Usage rows, reports, and the
migration ledger are discarded after the restic archive.
"""

from __future__ import annotations

import argparse
import json
import os
import shlex
import shutil
import subprocess
import sys
import time
from pathlib import Path
from typing import Callable, Sequence


SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))
import compose_package_upgrade as upgrade

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_ENV_JSON = ROOT / "env.json"
STABLE_ARCHIVE_DIR = ROOT / "tmp" / "restic-compose-usage-archive"
DUMP_NAME = "usage.dump"
MANIFEST_NAME = "MANIFEST.txt"
REMOTE_DUMP_PATH = Path("/tmp/metrum-ai-router-usage.dump")
JOB_KEY = "historical-usage-validation-v1"
DSN_ENV = "ROUTER_USAGE_DB_DSN"
CONFIRM_RESET = "reset-postgres-data"
MAX_CHECKPOINTS = 64
POSTGRES_READY_ATTEMPTS = 30
POSTGRES_VOLUME_KEY = "postgres_data"
MIGRATE_ENTRYPOINT = "/app/bin/metrum-ai-router-migrate"


class CutoverError(RuntimeError):
    pass


Runner = Callable[..., subprocess.CompletedProcess[str]]
DumpFn = Callable[[Path, Path], None]
ResticFn = Callable[[Path, str, str], str]


def utc_stamp() -> str:
    return upgrade.utc_stamp()


def default_runner(args: Sequence[str], *, cwd: str | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        list(args),
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        cwd=cwd,
    )


def reject_unsafe_command(args: Sequence[str]) -> None:
    tokens = [str(part) for part in args]
    if "down" in tokens:
        rest = tokens[tokens.index("down") :]
        if "-v" in rest or "--volumes" in rest:
            raise CutoverError("refusing docker compose down with volumes")
        raise CutoverError("refusing docker compose down")
    if "volume" in tokens and "rm" in tokens:
        for token in tokens:
            if "caddy" in token.lower():
                raise CutoverError(f"refusing to remove caddy volume {token}")


def require_cutover_runtime(root: Path) -> None:
    root = upgrade.reject_forbidden(root, label="install-root")
    required = (
        "compose/config/config.yaml",
        "compose/config/env.json",
        "compose/.env",
        "compose/docker-compose.yml",
        f"compose/{upgrade.POSTGRES_OVERRIDE}",
    )
    missing = [rel for rel in required if not (root / rel).is_file()]
    if missing:
        raise CutoverError(f"install-root {root} is missing required runtime paths: {', '.join(missing)}")
    if not upgrade.postgres_override_enabled(root / upgrade.RUNTIME_ENV):
        raise CutoverError("postgres override is required: uncommented ROUTER_USAGE_DB_DSN in compose/.env")
    if not upgrade.token_files(root / "compose"):
        raise CutoverError(f"install-root {root} is missing compose/ROUTER_TOKEN*.txt")


def restic_tags(version: str) -> list[str]:
    return [
        "purpose:compose-usage-archive",
        "cto",
        "metrum-ai-router",
        f"version:{version}",
    ]


def manifest_text(version: str, stamp: str, install_root: Path) -> str:
    return (
        f"purpose: compose-usage-archive\n"
        f"package_version: {version}\n"
        f"utc: {stamp}\n"
        f"install_root_name: {install_root.name}\n"
        f"note: config kept live; usage discarded after this snapshot\n"
    )


def parse_json_object(text: str) -> dict[str, object]:
    start = text.find("{")
    if start < 0:
        raise CutoverError("migration output was not JSON")
    data, _ = json.JSONDecoder().raw_decode(text[start:])
    if not isinstance(data, dict):
        raise CutoverError("migration JSON was not an object")
    return data


def job_state_from_status(status: dict[str, object], job_key: str = JOB_KEY) -> str:
    jobs = status.get("Jobs") or status.get("jobs") or []
    if not isinstance(jobs, list):
        return ""
    for job in jobs:
        if not isinstance(job, dict):
            continue
        key = str(job.get("Key") or job.get("key") or "")
        if key == job_key:
            return str(job.get("State") or job.get("state") or "").strip()
    return ""


def postgres_volume_name(compose_config: dict[str, object]) -> str:
    volumes = compose_config.get("volumes")
    if not isinstance(volumes, dict) or POSTGRES_VOLUME_KEY not in volumes:
        raise CutoverError("compose config is missing the postgres_data volume")
    spec = volumes[POSTGRES_VOLUME_KEY]
    name = ""
    if isinstance(spec, dict):
        raw = spec.get("name")
        if isinstance(raw, str):
            name = raw.strip()
    if not name:
        project = compose_config.get("name")
        if not isinstance(project, str) or not project.strip():
            raise CutoverError("compose config is missing postgres_data volume name")
        name = f"{project.strip()}_{POSTGRES_VOLUME_KEY}"
    if "caddy" in name.lower():
        raise CutoverError(f"refusing postgres volume name {name}")
    return name


def compose_run(
    run: Runner,
    install_root: Path,
    args: Sequence[str],
) -> subprocess.CompletedProcess[str]:
    cmd = ["docker", "compose", *upgrade.compose_file_args(install_root), *args]
    reject_unsafe_command(cmd)
    return run(cmd, cwd=str(install_root / "compose"))


def load_compose_config(run: Runner, install_root: Path) -> dict[str, object]:
    completed = compose_run(run, install_root, ["config", "--format", "json"])
    if completed.returncode != 0:
        raise CutoverError(
            f"docker compose config failed: {(completed.stderr or completed.stdout).strip()}"
        )
    try:
        data = json.loads(completed.stdout)
    except json.JSONDecodeError as exc:
        raise CutoverError("docker compose config JSON was invalid") from exc
    if not isinstance(data, dict):
        raise CutoverError("docker compose config JSON was not an object")
    return data


def discover_postgres_volume(run: Runner, install_root: Path) -> str:
    return postgres_volume_name(load_compose_config(run, install_root))


def compose_network_name(compose_config: dict[str, object]) -> str:
    networks = compose_config.get("networks")
    if isinstance(networks, dict):
        default = networks.get("default")
        if isinstance(default, dict):
            raw = default.get("name")
            if isinstance(raw, str) and raw.strip():
                return raw.strip()
    project = compose_config.get("name")
    if isinstance(project, str) and project.strip():
        return f"{project.strip()}_default"
    raise CutoverError("compose config is missing the default network name")


def router_image_tag(install_root: Path) -> str:
    env_path = install_root / upgrade.RUNTIME_ENV
    for line in env_path.read_text(encoding="utf-8").splitlines():
        if line.startswith("SMART_LLMROUTER_VERSION="):
            version = line.split("=", 1)[1].strip()
            if version:
                return f"metrum-ai-router:{version}"
    raise CutoverError("compose/.env is missing SMART_LLMROUTER_VERSION")


def default_dump(install_root: Path, dump_path: Path) -> None:
    cmd = [
        "docker",
        "compose",
        *upgrade.compose_file_args(install_root),
        "exec",
        "-T",
        "postgres",
        "sh",
        "-c",
        'pg_dump -Fc -U "$POSTGRES_USER" -d "$POSTGRES_DB"',
    ]
    reject_unsafe_command(cmd)
    dump_path.parent.mkdir(parents=True, exist_ok=True)
    with dump_path.open("wb") as handle:
        completed = subprocess.run(
            cmd,
            cwd=str(install_root / "compose"),
            stdout=handle,
            stderr=subprocess.PIPE,
            check=False,
        )
    if completed.returncode != 0:
        dump_path.unlink(missing_ok=True)
        err = completed.stderr.decode("utf-8", errors="replace").strip()
        raise CutoverError(f"pg_dump failed: {err}")
    if dump_path.stat().st_size == 0:
        dump_path.unlink(missing_ok=True)
        raise CutoverError("pg_dump produced an empty archive")


def stage_archive(dump_path: Path, version: str, stamp: str, install_root: Path) -> Path:
    if not dump_path.is_file() or dump_path.stat().st_size == 0:
        raise CutoverError(f"usage dump {dump_path} is missing or empty")
    if STABLE_ARCHIVE_DIR.exists():
        shutil.rmtree(STABLE_ARCHIVE_DIR)
    STABLE_ARCHIVE_DIR.mkdir(parents=True, exist_ok=True)
    staged = STABLE_ARCHIVE_DIR / DUMP_NAME
    shutil.copy2(dump_path, staged)
    (STABLE_ARCHIVE_DIR / MANIFEST_NAME).write_text(
        manifest_text(version, stamp, install_root),
        encoding="utf-8",
    )
    return STABLE_ARCHIVE_DIR


def default_restic(stage_dir: Path, version: str, env_json: Path) -> str:
    import backup_dist_restic as restic

    creds = restic.merge_credentials(restic.load_env_json(env_json))
    repository = restic.build_repository_url(creds)
    password = restic.require_credential(creds, "RESTIC_PASSWORD")
    tag_args: list[str] = []
    for tag in restic_tags(version):
        tag_args.extend(["--tag", tag])
    env = os.environ.copy()
    env["RESTIC_REPOSITORY"] = repository
    env["RESTIC_PASSWORD"] = password
    cmd = ["restic", "backup", *tag_args, "--", str(stage_dir)]
    completed = subprocess.run(
        cmd,
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=env,
    )
    if completed.returncode != 0:
        raise CutoverError(f"restic backup failed: {(completed.stderr or completed.stdout).strip()}")
    snapshot = ""
    for line in (completed.stdout or "").splitlines():
        stripped = line.strip()
        if stripped.startswith("snapshot ") and stripped.endswith(" saved"):
            snapshot = stripped.split()[1]
    return snapshot


def archive_usage(
    install_root: Path,
    version: str,
    stamp: str,
    dump_path: Path,
    *,
    env_json: Path = DEFAULT_ENV_JSON,
    dump_fn: DumpFn | None = None,
    restic_fn: ResticFn | None = None,
    require_local_runtime: bool = True,
) -> dict[str, object]:
    root = upgrade.reject_forbidden(install_root, label="install-root")
    if require_local_runtime:
        require_cutover_runtime(root)
    dump_path = Path(dump_path)
    if dump_path.exists() and dump_path.is_dir():
        raise CutoverError(f"dump path {dump_path} is a directory")
    config_dir = (root / "compose" / "config").resolve()
    try:
        dump_path.resolve().relative_to(config_dir)
    except ValueError:
        pass
    else:
        raise CutoverError("refusing to write a usage dump under compose/config")
    (dump_fn or default_dump)(root, dump_path)
    stage = stage_archive(dump_path, version, stamp, root)
    try:
        snapshot = (restic_fn or (lambda staged, ver, _stamp: default_restic(staged, ver, env_json)))(
            stage, version, stamp
        )
    finally:
        if STABLE_ARCHIVE_DIR.exists():
            shutil.rmtree(STABLE_ARCHIVE_DIR, ignore_errors=True)
    return {
        "dump": str(dump_path),
        "restic_snapshot": snapshot,
        "restic_tags": restic_tags(version),
        "restic_stage": str(stage),
    }


def load_packaged_image(run: Runner, install_root: Path, info: upgrade.PackageInfo) -> None:
    image_tar = upgrade.image_tar_in(install_root, info.arch)
    cmd = ["docker", "load", "-i", str(image_tar)]
    reject_unsafe_command(cmd)
    completed = run(cmd)
    if completed.returncode != 0:
        raise CutoverError(f"docker load failed: {(completed.stderr or completed.stdout).strip()}")


def wait_postgres_healthy(run: Runner, install_root: Path, *, sleeper: Callable[[float], None]) -> None:
    ready = [
        "exec",
        "-T",
        "postgres",
        "sh",
        "-c",
        'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"',
    ]
    last = ""
    for _ in range(POSTGRES_READY_ATTEMPTS):
        completed = compose_run(run, install_root, ready)
        if completed.returncode == 0:
            return
        last = (completed.stderr or completed.stdout).strip()
        sleeper(2)
    raise CutoverError(f"postgres did not become ready: {last}")


def migrate_argv(
    install_root: Path,
    image: str,
    network: str,
    action: str,
    extra: Sequence[str] = (),
) -> list[str]:
    args = [
        "docker",
        "run",
        "--rm",
        "--network",
        network,
        "--env-file",
        str(install_root / upgrade.RUNTIME_ENV),
        "--entrypoint",
        MIGRATE_ENTRYPOINT,
        image,
        f"--action={action}",
        "--driver=postgres",
        f"--dsn-env={DSN_ENV}",
        *extra,
    ]
    if any(part.startswith("--dsn=") or part == "--dsn" for part in args):
        raise CutoverError("refusing to pass a DSN on the CLI")
    reject_unsafe_command(args)
    return args


def migrate(
    run: Runner,
    install_root: Path,
    image: str,
    network: str,
    action: str,
    extra: Sequence[str] = (),
) -> subprocess.CompletedProcess[str]:
    completed = run(migrate_argv(install_root, image, network, action, extra))
    if completed.returncode != 0 and action != "verify-serving":
        raise CutoverError(
            f"router-migrate {action} failed: {(completed.stderr or completed.stdout).strip()}"
        )
    return completed


def run_migration_gate(run: Runner, install_root: Path) -> dict[str, object]:
    image = router_image_tag(install_root)
    network = compose_network_name(load_compose_config(run, install_root))
    migrate(run, install_root, image, network, "plan")
    migrate(run, install_root, image, network, "apply")
    ordinal = 0
    verify: subprocess.CompletedProcess[str] | None = None
    while ordinal <= MAX_CHECKPOINTS:
        extra = [f"--job={JOB_KEY}", f"--checkpoint-ordinal={ordinal}"]
        migrate(run, install_root, image, network, "resume", extra)
        verify = migrate(run, install_root, image, network, "verify-serving")
        if verify.returncode == 0:
            break
        ordinal += 1
    else:
        detail = ""
        if verify is not None:
            detail = (verify.stderr or verify.stdout or "").strip()
        raise CutoverError(
            f"data job {JOB_KEY} did not pass verify-serving within {MAX_CHECKPOINTS} checkpoints: {detail}"
        )
    final_status = migrate(run, install_root, image, network, "status")
    return {
        "job_state": "validated",
        "verify_compatible": True,
        "status_text": (final_status.stdout or "").strip(),
    }


def reset_postgres_volume(run: Runner, install_root: Path, *, sleeper: Callable[[float], None]) -> str:
    volume = discover_postgres_volume(run, install_root)
    stop = compose_run(run, install_root, ["stop", "postgres"])
    if stop.returncode != 0:
        raise CutoverError(f"stop postgres failed: {(stop.stderr or stop.stdout).strip()}")
    removed_container = compose_run(run, install_root, ["rm", "-f", "postgres"])
    if removed_container.returncode != 0:
        raise CutoverError(
            f"remove postgres container failed: {(removed_container.stderr or removed_container.stdout).strip()}"
        )
    rm_cmd = ["docker", "volume", "rm", volume]
    reject_unsafe_command(rm_cmd)
    removed = run(rm_cmd)
    if removed.returncode != 0:
        raise CutoverError(f"docker volume rm failed: {(removed.stderr or removed.stdout).strip()}")
    started = compose_run(run, install_root, ["up", "-d", "postgres"])
    if started.returncode != 0:
        raise CutoverError(f"postgres up failed: {(started.stderr or started.stdout).strip()}")
    wait_postgres_healthy(run, install_root, sleeper=sleeper)
    return volume


def plan_cutover(
    info: upgrade.PackageInfo,
    install_root: Path | None,
    suffix: str,
    stamp: str,
) -> dict[str, object]:
    planned: dict[str, object] = {
        "package": str(info.path),
        "version": info.version,
        "arch": info.arch,
        "image_tag": info.image_tag,
        "unpack": "tar --strip-components=1",
        "keep": [
            "compose/config",
            "compose/state",
            "compose/logs",
            "compose/.env",
            "compose/ROUTER_TOKEN*.txt",
            "caddy_data",
            "caddy_config",
        ],
        "reset": "postgres_data volume only",
        "migrate": f"plan apply resume {JOB_KEY} verify-serving status",
        "dsn_flag": f"--dsn-env={DSN_ENV}",
        "restic_tags": restic_tags(info.version),
        "restic_stage": str(STABLE_ARCHIVE_DIR),
        "confirm_reset_usage": CONFIRM_RESET,
        "skip_instance_reboot": True,
        "compose_down_volumes": False,
        "steps": [
            "archive pg_dump and restic",
            "stop router",
            "package apply --skip-compose",
            "docker load",
            "stop postgres",
            "docker volume rm postgres_data only",
            "up postgres",
            "empty-DB migration gate",
            "compose up -d",
        ],
    }
    if install_root is not None:
        root = upgrade.reject_forbidden(install_root, label="install-root")
        planned["install_root"] = str(root)
        planned["backup"] = str(upgrade.backup_path(root, suffix, stamp))
        if root.exists():
            require_cutover_runtime(root)
            planned["existing_tokens"] = [path.name for path in upgrade.token_files(root / "compose")]
            planned["postgres_override"] = True
    return planned


def apply_local(
    info: upgrade.PackageInfo,
    install_root: Path,
    suffix: str,
    stamp: str,
    *,
    confirm: str,
    skip_archive: bool = False,
    skip_package: bool = False,
    skip_serve: bool = False,
    dump_path: Path | None = None,
    env_json: Path = DEFAULT_ENV_JSON,
    runner: Runner | None = None,
    dump_fn: DumpFn | None = None,
    restic_fn: ResticFn | None = None,
    sleeper: Callable[[float], None] | None = None,
) -> dict[str, object]:
    if confirm != CONFIRM_RESET:
        raise CutoverError(f"--confirm-reset-usage must be {CONFIRM_RESET}")
    root = upgrade.reject_forbidden(install_root, label="install-root")
    require_cutover_runtime(root)
    run = runner or default_runner
    sleep = sleeper or time.sleep
    result: dict[str, object] = {
        "install_root": str(root),
        "version": info.version,
        "skip_instance_reboot": True,
    }
    if not skip_archive:
        dump = dump_path or REMOTE_DUMP_PATH
        archived = archive_usage(
            root,
            info.version,
            stamp,
            dump,
            env_json=env_json,
            dump_fn=dump_fn,
            restic_fn=restic_fn,
        )
        result.update(archived)
    stopped = compose_run(run, root, ["stop", "router"])
    if stopped.returncode != 0:
        raise CutoverError(f"stop router failed: {(stopped.stderr or stopped.stdout).strip()}")
    if not skip_package:
        upgraded = upgrade.apply_local(info, root, suffix, stamp, skip_compose=True)
        result["backup"] = upgraded["backup"]
        result["copied"] = upgraded["copied"]
        result["version_env"] = upgraded["version_env"]
        require_cutover_runtime(root)
        load_packaged_image(run, root, info)
    volume = reset_postgres_volume(run, root, sleeper=sleep)
    result["postgres_volume"] = volume
    gate = run_migration_gate(run, root)
    result.update(gate)
    if skip_serve:
        result["services"] = ["compose skipped"]
        return result
    if os.geteuid() == 0:
        upgrade.chown_runtime(root)
    up = compose_run(run, root, ["up", "-d"])
    if up.returncode != 0:
        raise CutoverError(f"docker compose up failed: {(up.stderr or up.stdout).strip()}")
    ps = compose_run(run, root, ["ps", "--format", "{{.Name}} {{.Image}} {{.Status}}"])
    if ps.returncode != 0:
        raise CutoverError(f"docker compose ps failed: {(ps.stderr or ps.stdout).strip()}")
    result["services"] = [line for line in ps.stdout.splitlines() if line.strip()]
    return result


def print_plan(plan: dict[str, object]) -> None:
    for key in (
        "package",
        "version",
        "arch",
        "image_tag",
        "unpack",
        "install_root",
        "backup",
        "keep",
        "reset",
        "migrate",
        "dsn_flag",
        "restic_tags",
        "restic_stage",
        "existing_tokens",
        "postgres_override",
        "confirm_reset_usage",
        "skip_instance_reboot",
        "compose_down_volumes",
        "steps",
    ):
        if key in plan:
            print(f"{key}: {plan[key]}")


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    sub = parser.add_subparsers(dest="command", required=True)

    def add_common(cmd: argparse.ArgumentParser, *, require_package: bool) -> None:
        if require_package:
            cmd.add_argument("--package", required=True, type=Path)
        cmd.add_argument("--install-root", type=Path, default=Path("/opt/metrum-ai-router"))
        cmd.add_argument("--backup-suffix", default="compose-clean-cutover")
        cmd.add_argument("--utc", default=None, help="UTC stamp override YYYYMMDDTHHMMSSZ")
        cmd.add_argument("--remote", default=None)
        cmd.add_argument("--ssh-identity", type=Path, default=None)
        cmd.add_argument("--env-json", type=Path, default=DEFAULT_ENV_JSON)
        cmd.add_argument("--dump-path", type=Path, default=REMOTE_DUMP_PATH)

    add_common(sub.add_parser("plan", help="inspect package and print the cutover plan"), require_package=True)
    add_common(sub.add_parser("archive", help="pg_dump the live usage DB and restic-archive it"), require_package=True)
    apply_cmd = sub.add_parser("apply", help="archive, reset postgres_data only, migrate empty DB, serve")
    add_common(apply_cmd, require_package=True)
    apply_cmd.add_argument("--confirm-reset-usage", required=True)
    apply_cmd.add_argument("--skip-archive", action="store_true")
    apply_cmd.add_argument("--skip-package", action="store_true")
    apply_cmd.add_argument("--skip-serve", action="store_true")
    apply_cmd.add_argument("--cleanup-remote-files", action="store_true")
    return parser.parse_args(argv)


def remote_pg_dump_command(install_root: Path) -> str:
    compose = install_root / "compose"
    inner = 'pg_dump -Fc -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
    return (
        "sudo docker compose "
        f"--project-directory {shlex.quote(str(compose))} "
        f"-f {shlex.quote(str(compose / 'docker-compose.yml'))} "
        f"-f {shlex.quote(str(compose / upgrade.POSTGRES_OVERRIDE))} "
        f"exec -T postgres sh -c {shlex.quote(inner)}"
    )


def remote_dump(remote: str, identity: Path | None, install_root: Path, dump_path: Path) -> None:
    remote_cmd = remote_pg_dump_command(install_root)
    reject_unsafe_command(shlex.split(remote_cmd))
    dump_path.parent.mkdir(parents=True, exist_ok=True)
    with dump_path.open("wb") as handle:
        completed = subprocess.run(
            [*upgrade.ssh_args(identity), remote, remote_cmd],
            stdout=handle,
            stderr=subprocess.PIPE,
            check=False,
        )
    if completed.returncode != 0:
        dump_path.unlink(missing_ok=True)
        err = completed.stderr.decode("utf-8", errors="replace").strip()
        raise CutoverError(f"remote pg_dump failed: {err}")
    if dump_path.stat().st_size == 0:
        dump_path.unlink(missing_ok=True)
        raise CutoverError("remote pg_dump produced an empty archive")


def remote_apply_command(script_name: str, argv: Sequence[str], package_name: str) -> list[str]:
    return [
        "sudo",
        "python3",
        f"/tmp/{script_name}",
        *list(argv),
        "--package",
        f"/tmp/{package_name}",
        "--cleanup-remote-files",
    ]


def run_remote_apply(
    remote: str,
    identity: Path | None,
    argv: Sequence[str],
    files: Sequence[Path],
) -> int:
    for path in files:
        completed = subprocess.run(
            [*upgrade.scp_args(identity), str(path), f"{remote}:/tmp/{path.name}"],
            check=False,
        )
        if completed.returncode != 0:
            raise CutoverError(f"scp {path.name} to {remote} failed")
    remote_cmd = remote_apply_command(Path(argv[0]).name, argv[1:], files[0].name)
    completed = subprocess.run([*upgrade.ssh_args(identity), remote, *remote_cmd], check=False)
    return completed.returncode


def cleanup_remote_files(paths: Sequence[Path]) -> None:
    for path in paths:
        try:
            path.unlink(missing_ok=True)
        except OSError:
            pass


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    stamp = args.utc or utc_stamp()
    try:
        info = upgrade.inspect_package(args.package)
        if args.command == "plan":
            print_plan(plan_cutover(info, args.install_root, args.backup_suffix, stamp))
            return 0
        if args.command == "archive":
            dump_path = args.dump_path
            if args.remote:
                dump_fn: DumpFn = lambda _root, dest: remote_dump(
                    args.remote, args.ssh_identity, args.install_root, dest
                )
            else:
                dump_fn = default_dump
            result = archive_usage(
                args.install_root,
                info.version,
                stamp,
                dump_path,
                env_json=args.env_json,
                dump_fn=dump_fn,
                require_local_runtime=not bool(args.remote),
            )
            print(f"dump: {result['dump']}")
            print(f"restic_snapshot: {result['restic_snapshot']}")
            print(f"restic_tags: {result['restic_tags']}")
            return 0
        if args.command == "apply" and args.remote:
            if not args.skip_archive:
                archived = archive_usage(
                    args.install_root,
                    info.version,
                    stamp,
                    args.dump_path,
                    env_json=args.env_json,
                    dump_fn=lambda _root, dest: remote_dump(
                        args.remote, args.ssh_identity, args.install_root, dest
                    ),
                    require_local_runtime=False,
                )
                print(f"restic_snapshot: {archived['restic_snapshot']}")
                print(f"restic_tags: {archived['restic_tags']}")
            script = Path(__file__).resolve()
            upgrade_script = Path(upgrade.__file__).resolve()
            remote_argv = [
                script.name,
                "apply",
                "--install-root",
                str(args.install_root),
                "--backup-suffix",
                args.backup_suffix,
                "--utc",
                stamp,
                "--confirm-reset-usage",
                args.confirm_reset_usage,
                "--skip-archive",
            ]
            if args.skip_package:
                remote_argv.append("--skip-package")
            if args.skip_serve:
                remote_argv.append("--skip-serve")
            return run_remote_apply(
                args.remote,
                args.ssh_identity,
                remote_argv,
                (upgrade.resolved(args.package), script, upgrade_script),
            )
        if args.command == "apply":
            result = apply_local(
                info,
                args.install_root,
                args.backup_suffix,
                stamp,
                confirm=args.confirm_reset_usage,
                skip_archive=args.skip_archive,
                skip_package=args.skip_package,
                skip_serve=args.skip_serve,
                dump_path=args.dump_path,
                env_json=args.env_json,
            )
            for key in (
                "backup",
                "install_root",
                "version",
                "version_env",
                "postgres_volume",
                "restic_snapshot",
                "job_state",
            ):
                if key in result:
                    print(f"{key}: {result[key]}")
            print("services:")
            for line in result.get("services", []):
                print(f"  {line}")
            if args.cleanup_remote_files:
                cleanup_remote_files(
                    [
                        upgrade.resolved(args.package),
                        Path("/tmp") / Path(__file__).name,
                        Path("/tmp") / Path(upgrade.__file__).name,
                    ]
                )
            return 0
        raise CutoverError(f"unknown command {args.command}")
    except (CutoverError, upgrade.UpgradeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
