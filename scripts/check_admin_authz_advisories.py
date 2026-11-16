# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Fail-closed source-scan/advisory gate; stdlib only, no network in parser."""

import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import re
import sys

CASBIN = "github.com/casbin/casbin/v2"
VERSION_RE = re.compile(r"^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?(?:\+([0-9A-Za-z.-]+))?$")
COMPARATOR_RE = re.compile(r"^(>=|<=|>|<|=)\s*(v?\S+)$")


class GateError(Exception):
    def __init__(self, reason, report=None):
        super().__init__(reason)
        self.report = report or {}


def _version(value):
    if not isinstance(value, str):
        raise GateError("missing or malformed advisory version")
    match = VERSION_RE.fullmatch(value)
    if not match:
        raise GateError("unsupported advisory version syntax")
    major, minor, patch, prerelease, build = match.groups()
    if build and any(not part for part in build.split(".")):
        raise GateError("unsupported advisory build metadata syntax")
    identifiers = []
    if prerelease:
        for part in prerelease.split("."):
            if not part or (part.isdigit() and len(part) > 1 and part.startswith("0")):
                raise GateError("unsupported advisory prerelease syntax")
            identifiers.append((0, int(part)) if part.isdigit() else (1, part))
    return (int(major), int(minor), int(patch), identifiers)


def _compare_versions(left, right):
    a, b = _version(left), _version(right)
    if a[:3] != b[:3]:
        return (a[:3] > b[:3]) - (a[:3] < b[:3])
    ap, bp = a[3], b[3]
    if not ap or not bp:
        return (bool(ap) < bool(bp)) - (bool(ap) > bool(bp))
    for x, y in zip(ap, bp):
        if x != y:
            return (x > y) - (x < y)
    return (len(ap) > len(bp)) - (len(ap) < len(bp))


def _range_matches(version, expression):
    if not isinstance(expression, str) or not expression.strip():
        raise GateError("missing or malformed advisory version range")
    for alternative in expression.split("||"):
        terms = [part.strip() for part in alternative.split(",")]
        if not terms or any(not part for part in terms):
            raise GateError("malformed advisory version range")
        matches = True
        for term in terms:
            match = COMPARATOR_RE.fullmatch(term)
            if not match:
                raise GateError("unsupported advisory version range syntax")
            operator, bound = match.groups()
            comparison = _compare_versions(version, bound)
            matches &= {">=": comparison >= 0, "<=": comparison <= 0, ">": comparison > 0, "<": comparison < 0, "=": comparison == 0}[operator]
        if matches:
            return True
    return False


def validate_exceptions(payload):
    expected = {"copyright", "license", "schema", "exceptions"}
    if not isinstance(payload, dict) or set(payload) != expected or payload["schema"] != "router-admin-authz-advisory-exceptions-v1" or payload["copyright"] != "Copyright 2026 Metrum AI" or payload["license"] != "Apache-2.0":
        raise GateError("missing or unsupported advisory exception schema")
    entries = payload["exceptions"]
    if not isinstance(entries, list):
        raise GateError("advisory exceptions must be an array")
    seen = set()
    normalized = []
    required = {"ghsa_id", "module", "version", "rationale", "evidence", "revalidation", "reviewer", "approved_at"}
    for item in entries:
        if not isinstance(item, dict) or set(item) != required:
            raise GateError("advisory exception fields are missing or unsupported")
        if item["module"] != CASBIN or not all(isinstance(item[field], str) and item[field].strip() for field in required):
            raise GateError("advisory exception must name an exact module, version, reason, evidence, review, and revalidation")
        _version(item["version"])
        if not re.fullmatch(r"GHSA-[0-9a-z]{4}-[0-9a-z]{4}-[0-9a-z]{4}", item["ghsa_id"], re.IGNORECASE):
            raise GateError("advisory exception has malformed GHSA identity")
        try:
            datetime.strptime(item["approved_at"], "%Y-%m-%d")
        except ValueError:
            raise GateError("advisory exception has malformed approval date")
        key = (item["ghsa_id"].upper(), item["module"], item["version"])
        if key in seen:
            raise GateError("duplicate advisory exception")
        seen.add(key)
        normalized.append(dict(item, ghsa_id=item["ghsa_id"].upper()))
    return normalized


def _evaluate_advisories(source, advisories, version, exceptions):
    if not isinstance(advisories, list) or any(not isinstance(item, dict) for item in advisories):
        raise GateError("advisory API did not return an advisory array")
    evaluations = []
    blocking = False
    seen_exceptions = set()
    for advisory in advisories:
        ghsa = advisory.get("ghsa_id")
        vulnerabilities = advisory.get("vulnerabilities")
        if not isinstance(ghsa, str) or not re.fullmatch(r"GHSA-[0-9a-z]{4}-[0-9a-z]{4}-[0-9a-z]{4}", ghsa, re.IGNORECASE):
            raise GateError("advisory identity is missing or malformed")
        if not isinstance(vulnerabilities, list) or not vulnerabilities:
            raise GateError("advisory is missing vulnerability package ranges")
        relevant = []
        for vulnerability in vulnerabilities:
            if not isinstance(vulnerability, dict) or not isinstance(vulnerability.get("package"), dict):
                raise GateError("advisory package identity is missing or malformed")
            package = vulnerability["package"]
            name, ecosystem = package.get("name"), package.get("ecosystem")
            if not isinstance(name, str) or not name or not isinstance(ecosystem, str) or not ecosystem:
                raise GateError("advisory package identity is missing or malformed")
            if name == CASBIN:
                if ecosystem.lower() != "go":
                    raise GateError("Casbin advisory uses an unsupported ecosystem")
                range_text = vulnerability.get("vulnerable_version_range")
                affected = _range_matches(version, range_text)
                record = {"source": source, "ghsa_id": ghsa.upper(), "module": name, "ecosystem": ecosystem, "range": range_text, "locked_version": version, "evaluation": "affected" if affected else "not_affected"}
                relevant.append(record)
                if affected:
                    exception = next((entry for entry in exceptions if (entry["ghsa_id"], entry["module"], entry["version"]) == (ghsa.upper(), name, version)), None)
                    if exception:
                        record["evaluation"] = "reviewed_exception"
                        record["exception"] = exception
                        seen_exceptions.add((exception["ghsa_id"], exception["module"], exception["version"]))
                    else:
                        blocking = True
            else:
                relevant.append({"source": source, "ghsa_id": ghsa.upper(), "module": name, "ecosystem": ecosystem, "range": vulnerability.get("vulnerable_version_range"), "locked_version": version, "evaluation": "unrelated_package"})
        if not relevant:
            raise GateError("advisory range is ambiguous for the selected Casbin module")
        evaluations.extend(relevant)
    return evaluations, blocking, seen_exceptions


def _events(raw):
    decoder = json.JSONDecoder()
    remaining = raw.strip()
    while remaining:
        try:
            event, end = decoder.raw_decode(remaining)
        except (ValueError, TypeError) as exc:
            raise GateError("malformed scan JSON") from exc
        if not isinstance(event, dict) or len(event) != 1:
            raise GateError("malformed scan event")
        yield next(iter(event.items()))
        remaining = remaining[end:].lstrip()


def evaluate(raw, affected, upstream, expected_version, exception_document=None):
    config = sbom = None
    findings = []
    osv_ids = set()
    for kind, value in _events(raw):
        if not isinstance(value, dict):
            raise GateError("malformed scan payload")
        if kind == "config" and config is None:
            config = value
        elif kind == "SBOM" and sbom is None:
            sbom = value
        elif kind == "finding":
            findings.append(value)
        elif kind == "osv" and isinstance(value.get("id"), str):
            osv_ids.add(value["id"])
        elif kind == "progress":
            continue
        else:
            raise GateError("unsupported or duplicate scan event")
    required = {"protocol_version": "v1.0.0", "scanner_name": "govulncheck", "scanner_version": "v1.9.0", "db": "https://vuln.go.dev", "scan_mode": "source", "scan_level": "symbol"}
    if config is None or sbom is None or any(config.get(k) != v for k, v in required.items()):
        raise GateError("missing or unsupported source/symbol scan configuration")
    if not isinstance(config.get("go_version"), str) or not config["go_version"] or sbom.get("go_version") != config["go_version"]:
        raise GateError("missing or inconsistent actual Go version")
    modules = sbom.get("modules")
    if not isinstance(modules, list) or any(not isinstance(m, dict) for m in modules):
        raise GateError("malformed scan SBOM")
    versions = [m.get("version") for m in modules if m.get("path") == CASBIN]
    if not expected_version or versions != [expected_version]:
        raise GateError("Casbin scan version does not match module lock")
    _version(expected_version)
    imported, module_only = set(), set()
    for finding in findings:
        osv, trace = finding.get("osv"), finding.get("trace")
        if not isinstance(osv, str) or not osv or osv not in osv_ids or not isinstance(trace, list) or not trace:
            raise GateError("incomplete finding or missing OSV evidence")
        relevant = False
        for frame in trace:
            if not isinstance(frame, dict) or not isinstance(frame.get("module"), str) or not frame["module"]:
                raise GateError("malformed finding trace")
            for field in ("package", "function", "receiver"):
                if field in frame and (not isinstance(frame[field], str) or not frame[field]):
                    raise GateError("malformed finding scope")
                relevant |= bool(frame.get(field))
            if "position" in frame:
                position = frame["position"]
                if not isinstance(position, dict) or not isinstance(position.get("filename"), str) or not position["filename"]:
                    raise GateError("malformed finding position")
                if type(position.get("line")) is not int or position["line"] < 1:
                    raise GateError("malformed finding position")
                for name, minimum in (("column", 1), ("offset", 0)):
                    if name in position and (type(position[name]) is not int or position[name] < minimum):
                        raise GateError("malformed finding position")
                relevant = True
        (imported if relevant else module_only).add(osv)
    report = {"casbin_version": expected_version, "go_version": config["go_version"], "package_or_symbol_findings": sorted(imported), "module_only_findings": sorted(module_only)}
    if exception_document is None:
        exception_document = {"copyright": "Copyright 2026 Metrum AI", "license": "Apache-2.0", "schema": "router-admin-authz-advisory-exceptions-v1", "exceptions": []}
    exceptions = validate_exceptions(exception_document)
    try:
        affected_results, affected_blocked, affected_exceptions = _evaluate_advisories("affected_version_api", affected, expected_version, exceptions)
        upstream_results, upstream_blocked, upstream_exceptions = _evaluate_advisories("upstream_api", upstream, expected_version, exceptions)
    except GateError as exc:
        exc.report = dict(report, advisory_evaluations=exc.report.get("advisory_evaluations", []), advisory_evaluation_error=str(exc))
        raise
    report.update(
        affected_version_advisories_returned=len(affected),
        upstream_advisories_returned=len(upstream),
        advisory_evaluations=affected_results + upstream_results,
    )
    used_exceptions = affected_exceptions | upstream_exceptions
    if any((entry["ghsa_id"], entry["module"], entry["version"]) not in used_exceptions for entry in exceptions):
        raise GateError("stale advisory exception does not match an affected returned advisory", report)
    if imported:
        raise GateError("imported-package or symbol vulnerability requires review", report)
    if affected_blocked or upstream_blocked:
        raise GateError("locked Casbin version is inside a published advisory range", report)
    return report


def evaluate_inventory(raw_modules, raw_graph, expected_version):
    versions, roots, replaced = {}, [], set()
    for line in raw_modules.splitlines():
        fields = line.split()
        if not fields:
            continue
        if len(fields) == 1:
            roots.append(fields[0])
        elif len(fields) == 2 or (len(fields) == 3 and fields[2] == "replaced"):
            if fields[0] in versions:
                raise GateError("duplicate module inventory entry")
            versions[fields[0]] = fields[1]
            if len(fields) == 3:
                replaced.add(fields[0])
        else:
            raise GateError("malformed module inventory")
    if len(roots) != 1 or versions.get(CASBIN) != expected_version:
        raise GateError("missing or inconsistent module inventory")
    related = {path: version for path, version in versions.items() if path.startswith("github.com/casbin/")}
    if replaced.intersection(related):
        raise GateError("Casbin-related replacement requires explicit source review")
    candidates, root_edge = set(), False
    lines = raw_graph.splitlines()
    if not lines:
        raise GateError("missing module dependency graph")
    for line in lines:
        fields = line.split()
        if len(fields) != 2 or "@" not in fields[1]:
            raise GateError("malformed module dependency graph")
        parent, dependency = fields
        target = dependency.split("@", 1)[0]
        engine = target == "github.com/casbin/casbin" or target.startswith("github.com/casbin/casbin/")
        if parent == roots[0]:
            root_edge |= target == CASBIN
            continue
        if "@" not in parent:
            raise GateError("malformed module graph parent")
        path, version = parent.split("@", 1)
        if engine and versions.get(path) == version and path != CASBIN:
            candidates.add(path)
    report = {"adapter_candidates": sorted(candidates), "casbin_related_modules": related}
    if not root_edge:
        raise GateError("module graph does not establish the Casbin dependency", report)
    if candidates:
        raise GateError("external Casbin consumers require adapter inventory and upstream coverage before acceptance", report)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scan", type=Path, required=True)
    parser.add_argument("--affected", type=Path, required=True)
    parser.add_argument("--upstream", type=Path, required=True)
    parser.add_argument("--casbin-version", required=True)
    parser.add_argument("--modules", type=Path, required=True)
    parser.add_argument("--module-graph", type=Path, required=True)
    parser.add_argument("--exceptions", type=Path, required=True)
    args = parser.parse_args()
    report = {}
    try:
        report = evaluate(args.scan.read_text(), json.loads(args.affected.read_text()), json.loads(args.upstream.read_text()), args.casbin_version, json.loads(args.exceptions.read_text()))
        report.update(evaluate_inventory(args.modules.read_text(), args.module_graph.read_text(), args.casbin_version))
        report["module_only_triage"] = {osv: "No affected package/symbol in this selected source build; retain the module advisory for release review." for osv in report["module_only_findings"]}
        report["evidence_evaluated_at_utc"] = datetime.now(timezone.utc).isoformat()
    except GateError as exc:
        print(json.dumps(dict(report, **exc.report, status="blocked", reason=str(exc)), sort_keys=True))
        return 1
    except (OSError, ValueError, UnicodeError):
        print(json.dumps({"status": "blocked", "reason": "missing or malformed evidence file"}))
        return 1
    print(json.dumps(dict(report, status="passed"), sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
