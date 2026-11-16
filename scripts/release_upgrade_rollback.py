# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Exercise a prior package upgrade, migration gate, serving smoke and rollback."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import pwd
import re
import shutil
import subprocess
import tarfile
import tempfile
import time
from pathlib import Path


def run(command: list[str], *, timeout: int, check: bool = True) -> subprocess.CompletedProcess[str]:
    try:
        return subprocess.run(command, text=True, capture_output=True, timeout=timeout, check=check)
    except subprocess.TimeoutExpired:
        raise RuntimeError("release check subprocess exceeded its timeout") from None
    except subprocess.CalledProcessError:
        raise RuntimeError("release check subprocess failed") from None


def unpack(archive: Path, destination: Path) -> Path:
    destination.mkdir(parents=True)
    with tarfile.open(archive, "r:gz") as bundle:
        root = destination.resolve()
        for member in bundle.getmembers():
            target = (destination / member.name).resolve()
            if target != root and root not in target.parents:
                raise RuntimeError("package archive contains an unsafe path")
        bundle.extractall(destination, filter="data")
    return destination


def find_binary(root: Path, name: str) -> Path:
    matches = list(root.rglob(name))
    if len(matches) != 1:
        raise RuntimeError(f"expected one {name} binary in package, found {len(matches)}")
    return matches[0]


def run_as(user: str, command: list[str], timeout: int = 120, check: bool = True) -> subprocess.CompletedProcess[str]:
    if os.geteuid() == pwd.getpwnam(user).pw_uid:
        return run(command, timeout=timeout, check=check)
    return run(["runuser", "--user", user, "--", *command], timeout=timeout, check=check)


def parse_single_json(output: str) -> dict[str, object]:
    parsed = json.loads(output)
    if not isinstance(parsed, dict):
        raise ValueError("migration output was not a JSON object")
    return parsed


def contains_sql_text(output: str) -> bool:
    return bool(re.search(r"\b(?:SELECT|INSERT|UPDATE|DELETE|PRAGMA|CREATE\s+TABLE|ALTER\s+TABLE|DROP\s+TABLE)\b", output, re.IGNORECASE))


def previous_json_status(version: str, output: str) -> bool:
    try:
        parse_single_json(output)
    except (json.JSONDecodeError, ValueError):
        return False
    if version == "v4.8.0":
        raise RuntimeError("v4.8.0 did not reproduce the known migrate --json regression")
    return True


def strict_json(binary: Path, action: str, database: Path, user: str, *extra: str) -> dict[str, object]:
    result = run_as(user, [str(binary), "--action=" + action, "--driver=sqlite", "--db=" + str(database), *extra, "--json"])
    if contains_sql_text(result.stderr):
        raise RuntimeError(f"{action} wrote SQL text to stderr")
    try:
        return parse_single_json(result.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"{action} stdout was not exactly one JSON document") from exc


def migration_job_example_script() -> str:
    """Return the shell script stored in the checked-in PostgreSQL Job args."""
    manifest = Path(__file__).parents[1] / "deploy/kubernetes/examples/postgres-migration-plan-job.yaml"
    try:
        text = manifest.read_text(encoding="utf-8")
    except OSError:
        raise RuntimeError("PostgreSQL migration Job example is missing") from None
    if not re.search(r'^\s+command:\s*\["/bin/sh", "-ec"\]\s*$', text, re.MULTILINE):
        raise RuntimeError("PostgreSQL migration Job must invoke its checked-in shell contract")
    match = re.search(r'^\s+args:\s*\n\s+- \|\n((?:\s{14,}.*\n)+)', text, re.MULTILINE)
    if not match:
        raise RuntimeError("PostgreSQL migration Job args are missing")
    script = "\n".join(line[14:] for line in match.group(1).splitlines()).rstrip()
    required = ("apply_scope()", "apply_scope usage ROUTER_USAGE_DB_DSN", "apply_scope router-config ROUTER_CONFIG_DB_DSN",
                "--action=plan", "--action=maintenance", "--action=verify")
    if not all(fragment in script for fragment in required):
        raise RuntimeError("PostgreSQL migration Job must plan/apply/verify both scopes")
    return script


def run_migration_job_example(binary: Path, usage_db: Path, config_db: Path, user: str, action: str, workspace: Path) -> list[dict[str, object]]:
    """Execute the Job's checked-in shell script, mapping its PostgreSQL DSNs to isolated SQLite files."""
    if action not in {"plan", "apply"}:
        raise ValueError("migration Job action must be plan or apply")
    workspace.mkdir(parents=True, exist_ok=True)
    workspace.chmod(0o755)
    wrapper = workspace / "metrum-ai-router-migrate"
    wrapper.write_text(
        "#!/usr/bin/env python3\n"
        "import os, sys\n"
        f"binary = {str(binary)!r}\n"
        f"databases = {{'ROUTER_USAGE_DB_DSN': {str(usage_db)!r}, 'ROUTER_CONFIG_DB_DSN': {str(config_db)!r}}}\n"
        "args = sys.argv[1:]\n"
        "dsn_name = next((arg.split('=', 1)[1] for arg in args if arg.startswith('--dsn-env=')), None)\n"
        "if dsn_name not in databases:\n    raise SystemExit('Job used an unexpected DSN environment variable')\n"
        "args = [arg for arg in args if not arg.startswith('--dsn-env=') and arg != '--driver=postgres']\n"
        "args += ['--driver=sqlite', '--db=' + databases[dsn_name]]\n"
        "os.execv(binary, [binary, *args])\n",
        encoding="utf-8",
    )
    wrapper.chmod(0o755)
    script = migration_job_example_script()
    if "/app/bin/metrum-ai-router-migrate" not in script:
        raise RuntimeError("PostgreSQL migration Job does not invoke the packaged CLI")
    script = script.replace("/app/bin/metrum-ai-router-migrate", str(wrapper))
    command = ["/usr/bin/env", f"MIGRATION_ACTION={action}",
               "BACKUP_EVIDENCE_REF=release-check-approved-snapshot", "/bin/sh", "-ec", script]
    result = run_as(user, command, timeout=1800, check=False)
    if contains_sql_text(result.stderr):
        raise RuntimeError("Kubernetes migration Job wrote SQL text to stderr")
    if result.returncode != 0:
        raise RuntimeError("Kubernetes migration Job shell contract failed")
    try:
        return [parse_single_json(line) for line in result.stdout.splitlines() if line.strip()]
    except json.JSONDecodeError as exc:
        raise RuntimeError("Kubernetes migration Job output was not a sequence of JSON documents") from exc


def smoke(binary_root: Path, version: str, commit: str, output: Path, user: str, database: Path) -> None:
    router = find_binary(binary_root, "metrum-ai-router")
    report = find_binary(binary_root, "metrum-ai-router-usage-report")
    result = run_as(user, ["python3", str(Path(__file__).with_name("local_launch_path_smoke.py")),
                           "--router-binary", str(router), "--usage-report-binary", str(report),
                           "--expected-version", version, "--expected-commit", commit, "--output", str(output),
                           "--usage-db", str(database)], timeout=180, check=False)
    if result.returncode != 0:
        raise RuntimeError(smoke_failure_summary(output))


def smoke_failure_summary(output: Path) -> str:
    """Summarize only static check names from the smoke's safe JSON evidence."""
    try:
        payload = json.loads(output.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return "serving smoke failed before writing safe evidence"
    checks = payload.get("checks") if isinstance(payload, dict) else None
    if not isinstance(checks, list):
        return "serving smoke failed without a valid check list"
    failed = sorted({
        item["check"] for item in checks
        if isinstance(item, dict)
        and item.get("passed") is False
        and isinstance(item.get("check"), str)
        and re.fullmatch(r"[a-z0-9-]{1,80}", item["check"])
    })
    if not failed:
        return "serving smoke process failed without a reported failed check"
    return "serving smoke checks failed: " + ", ".join(failed)


def package_commit(binary: Path, user: str) -> str:
    version_text = run_as(user, [str(binary), "--version"], timeout=10).stdout
    match = re.search(r"\bcommit=([0-9a-f]{7,40})\b", version_text)
    if not match:
        raise RuntimeError("package binary did not report a commit")
    return match.group(1)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--previous-archive", required=True, type=Path)
    parser.add_argument("--candidate-archive", required=True, type=Path)
    parser.add_argument("--previous-version", required=True)
    parser.add_argument("--candidate-version", required=True)
    parser.add_argument("--candidate-commit", required=True)
    parser.add_argument("--service-user", required=True)
    parser.add_argument("--report", required=True, type=Path)
    args = parser.parse_args()
    if pwd.getpwnam(args.service_user).pw_uid == 0:
        raise RuntimeError("release checks must use an unprivileged service user")

    started = time.monotonic()
    checks: list[dict[str, object]] = []
    with tempfile.TemporaryDirectory(prefix="router-release-upgrade-") as raw:
        work = Path(raw)
        work.chmod(0o755)
        service = pwd.getpwnam(args.service_user)
        if os.geteuid() == 0:
            os.chown(work, service.pw_uid, service.pw_gid)
        evidence = work / "evidence"
        evidence.mkdir(mode=0o777)
        previous_root = unpack(args.previous_archive, work / "previous")
        candidate_root = unpack(args.candidate_archive, work / "candidate")
        previous_migrate = find_binary(previous_root, "metrum-ai-router-migrate")
        candidate_migrate = find_binary(candidate_root, "metrum-ai-router-migrate")
        database = work / "usage.sqlite"
        config_database = work / "router-config.sqlite"
        old_result = run_as(args.service_user, [str(previous_migrate), "--action=apply", "--driver=sqlite",
                                                 "--db=" + str(database), "--json"])
        old_json_valid = previous_json_status(args.previous_version, old_result.stdout)
        checks.append({"check": "previous-release-json", "strict_json": old_json_valid})
        run_as(args.service_user, [str(previous_migrate), "--action=resume", "--job=historical-usage-validation-v1",
                                   "--checkpoint-ordinal=0", "--driver=sqlite", "--db=" + str(database)])
        backup = work / "usage.sqlite.backup"
        shutil.copy2(database, backup)
        owner, group = service.pw_uid, service.pw_gid
        if os.geteuid() == 0:
            os.chown(backup, owner, group)

        previous_router = find_binary(previous_root, "metrum-ai-router")
        previous_commit = package_commit(previous_router, args.service_user)
        smoke(previous_root, args.previous_version, previous_commit, evidence / "previous-serving-smoke.json", args.service_user, database)
        checks.append({"check": "previous-serving-smoke", "passed": True})
        gate_started = time.monotonic()
        for action in ("plan", "apply"):
            statuses = run_migration_job_example(candidate_migrate, database, config_database, args.service_user,
                                                 action, evidence / "migration-job")
            checks.append({"check": "kubernetes-migration-job-" + action, "strict_json": True,
                           "scope_count": len(statuses),
                           "compatible": all(status.get("Compatible", status.get("compatible", True)) for status in statuses)})
        config_status = strict_json(candidate_migrate, "status", config_database, args.service_user, "--scope=router-config")
        if config_status.get("SchemaVersion", config_status.get("schemaVersion")) != 18:
            raise RuntimeError("Kubernetes migration Job did not apply router-config schema 18")
        checks.append({"check": "kubernetes-migration-job-router-config-schema-18", "passed": True})
        status = strict_json(candidate_migrate, "resume", database, args.service_user,
                             "--job=historical-usage-validation-v1", "--checkpoint-ordinal=0")
        checks.append({"check": "candidate-resume", "strict_json": True,
                       "compatible": status.get("Compatible", status.get("compatible"))})
        status = strict_json(candidate_migrate, "verify-serving", database, args.service_user)
        checks.append({"check": "candidate-verify-serving", "strict_json": True,
                       "compatible": status.get("Compatible", status.get("compatible"))})
        smoke(candidate_root, args.candidate_version, args.candidate_commit, evidence / "candidate-smoke.json", args.service_user, database)
        checks.append({"check": "candidate-serving-smoke", "passed": True})
        upgrade_downtime_seconds = round(time.monotonic() - gate_started, 3)

        # Rollback restores both the previous package and its matching database snapshot.
        rollback_started = time.monotonic()
        shutil.copy2(backup, database)
        if os.geteuid() == 0:
            os.chown(database, owner, group)
        previous_status = run_as(args.service_user, [str(previous_migrate), "--action=status", "--driver=sqlite", "--db=" + str(database), "--json"])
        json.loads(previous_status.stdout)
        smoke(previous_root, args.previous_version, previous_commit, evidence / "rollback-smoke.json", args.service_user, database)
        checks.append({"check": "rollback-serving-smoke", "passed": True})
        rollback_downtime_seconds = round(time.monotonic() - rollback_started, 3)

    args.report.parent.mkdir(parents=True, exist_ok=True)
    args.report.write_text(json.dumps({
        "schema": "router.release-upgrade-rollback/v1",
        "generated_at": dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z"),
        "previous_version": args.previous_version,
        "candidate_version": args.candidate_version,
        "candidate_commit": args.candidate_commit,
        "upgrade_downtime_seconds": upgrade_downtime_seconds,
        "rollback_downtime_seconds": rollback_downtime_seconds,
        "total_upgrade_rollback_seconds": round(time.monotonic() - started, 3),
        "checks": checks,
    }, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
