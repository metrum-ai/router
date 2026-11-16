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


def run_as(user: str, command: list[str], timeout: int = 120) -> subprocess.CompletedProcess[str]:
    if os.geteuid() == pwd.getpwnam(user).pw_uid:
        return run(command, timeout=timeout)
    return run(["runuser", "--user", user, "--", *command], timeout=timeout)


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


def smoke(binary_root: Path, version: str, commit: str, output: Path, user: str, database: Path) -> None:
    router = find_binary(binary_root, "metrum-ai-router")
    report = find_binary(binary_root, "metrum-ai-router-usage-report")
    run_as(user, ["python3", str(Path(__file__).with_name("local_launch_path_smoke.py")),
                  "--router-binary", str(router), "--usage-report-binary", str(report),
                  "--expected-version", version, "--expected-commit", commit, "--output", str(output),
                  "--usage-db", str(database)], timeout=180)


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
            status = strict_json(candidate_migrate, action, database, args.service_user)
            checks.append({"check": "candidate-" + action, "strict_json": True,
                           "compatible": status.get("Compatible", status.get("compatible"))})
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
