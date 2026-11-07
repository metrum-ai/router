# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Fail-closed source-scan/advisory gate; stdlib only, no network in parser."""

import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import sys

CASBIN = "github.com/casbin/casbin/v2"


class GateError(Exception):
    def __init__(self, reason, report=None):
        super().__init__(reason)
        self.report = report or {}


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


def evaluate(raw, affected, upstream, expected_version):
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
    for payload in (affected, upstream):
        if not isinstance(payload, list) or any(not isinstance(item, dict) for item in payload):
            raise GateError("advisory API did not return an advisory array", report)
    report.update(affected_version_advisories_returned=len(affected), upstream_advisories_returned=len(upstream))
    if imported:
        raise GateError("imported-package or symbol vulnerability requires review", report)
    if affected or upstream:
        raise GateError("published Casbin advisory requires review; no automatic exception", report)
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
    args = parser.parse_args()
    try:
        report = evaluate(args.scan.read_text(), json.loads(args.affected.read_text()), json.loads(args.upstream.read_text()), args.casbin_version)
        report.update(evaluate_inventory(args.modules.read_text(), args.module_graph.read_text(), args.casbin_version))
        report["module_only_triage"] = {osv: "No affected package/symbol in this selected source build; retain the module advisory for release review." for osv in report["module_only_findings"]}
        report["evidence_evaluated_at_utc"] = datetime.now(timezone.utc).isoformat()
    except GateError as exc:
        print(json.dumps(dict(exc.report, status="blocked", reason=str(exc)), sort_keys=True))
        return 1
    except (OSError, ValueError, UnicodeError):
        print(json.dumps({"status": "blocked", "reason": "missing or malformed evidence file"}))
        return 1
    print(json.dumps(dict(report, status="passed"), sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
