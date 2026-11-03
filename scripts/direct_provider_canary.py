#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Direct-provider differential canary (metrum-ai/router#330, ARCH-05).

The layering in ARCH-05 ends with a *direct-provider differential canary*: the
same fixed workload is sent once to the exact provider/model and once to the
same model pinned through the router, and the two results are compared. It
catches adapter-fidelity regressions that ordinary router-only tests cannot see,
because a divergence needs a reference from outside the router.

Properties:

- The probes and their checks are fixed and local (``canary-probes.json``); no
  model grades another model. A per-run nonce stops a canned answer from
  passing.
- The emitted report uses the same schema as the other evaluation cells
  (``scripts/eval_report_schema.py``), with one ``canary-direct`` cell and one
  ``canary-router-pinned`` cell, so latency and cost gates apply unchanged.
- The hard gate is a **router regression**: a probe the direct call passed and
  the router call failed. Provider-only failures are reported as such, not
  counted against the router.

Usage (live, paid, opt-in):

  CANARY_DIRECT_BASE_URL=... CANARY_DIRECT_API_KEY=... CANARY_DIRECT_MODEL=... \
  CANARY_ROUTER_BASE_URL=... CANARY_ROUTER_API_KEY=... CANARY_ROUTER_MODEL_GROUP=... \
  python3 scripts/direct_provider_canary.py run --report-out canary.json

  python3 scripts/direct_provider_canary.py validate canary.json

Exit codes: 0 pass, 1 gate violation, 2 configuration or schema error.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
import uuid
from dataclasses import dataclass, field
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_PROBES = ROOT / "evaluators" / "eval-infra" / "canary-probes.json"
DEFAULT_MATRIX = ROOT / "evaluators" / "eval-infra" / "preregistered-matrix.yaml"
SCHEMA_VERSION = 1
DIRECT_CELL = "canary-direct"
ROUTER_CELL = "canary-router-pinned"
# Preregistered allowed error rate per side (matrix cells canary-*).
ALLOWED_ERROR_RATE = 0.10
REQUEST_TIMEOUT_S = 90


class CanaryError(ValueError):
    """Configuration or input error; never a provider failure."""


@dataclass
class SideConfig:
    name: str
    base_url: str
    api_key: str
    model: str
    input_price_per_million: float
    output_price_per_million: float


@dataclass
class ProbeResult:
    probe_id: str
    outcome: str  # pass | fail | error
    latency_ms: float
    cost_usd: float
    detail: str = ""  # bounded, value-free reason only


@dataclass
class RunResult:
    direct: dict[str, ProbeResult] = field(default_factory=dict)
    router: dict[str, ProbeResult] = field(default_factory=dict)


def load_probes(path: Path = DEFAULT_PROBES) -> list[dict]:
    doc = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(doc, dict) or doc.get("version") != 1:
        raise CanaryError("probe file must be a version 1 object")
    probes = doc.get("probes")
    if not isinstance(probes, list) or len(probes) < 2:
        raise CanaryError("probe file needs at least two probes")
    seen = set()
    for probe in probes:
        for key in ("id", "kind", "prompt", "max_tokens"):
            if key not in probe:
                raise CanaryError(f"probe {probe.get('id', '?')!r} is missing {key!r}")
        if probe["id"] in seen:
            raise CanaryError(f"duplicate probe id {probe['id']!r}")
        seen.add(probe["id"])
        if probe["kind"] not in ("exact_text", "json_int_field", "max_words", "contains_text"):
            raise CanaryError(f"probe {probe['id']!r} has unknown kind {probe['kind']!r}")
        if not isinstance(probe["max_tokens"], int) or probe["max_tokens"] < 1:
            raise CanaryError(f"probe {probe['id']!r} max_tokens must be a positive integer")
    return probes


def probe_prompt(probe: dict, nonce: str) -> str:
    try:
        return str(probe["prompt"]).format(nonce=nonce, a=probe.get("a", 0), b=probe.get("b", 0))
    except KeyError as exc:  # pragma: no cover - guarded by the probe file
        raise CanaryError(f"probe {probe['id']!r} uses an unknown placeholder {exc}") from exc


def check_probe(probe: dict, nonce: str, text: str) -> tuple[bool, str]:
    """Pure local check. Returns (passed, bounded reason)."""
    kind = probe["kind"]
    body = (text or "").strip()
    if not body:
        return False, "empty-response"
    if kind == "exact_text":
        return (body == f"CANARY-{nonce}"), ("check-mismatch" if body != f"CANARY-{nonce}" else "")
    if kind == "contains_text":
        return (f"CANARY-{nonce}" in text), ("check-mismatch" if f"CANARY-{nonce}" not in text else "")
    if kind == "max_words":
        words = [w for w in re.split(r"\s+", body) if re.search(r"[A-Za-z0-9]", w)]
        limit = int(probe.get("max_words", 3))
        return (len(words) <= limit), ("" if len(words) <= limit else "too-many-words")
    if kind == "json_int_field":
        match = re.search(r"\{.*\}", body, re.DOTALL)
        if not match:
            return False, "not-json"
        try:
            parsed = json.loads(match.group(0))
        except json.JSONDecodeError:
            return False, "not-json"
        want = int(probe.get("a", 0)) + int(probe.get("b", 0))
        got = parsed.get(probe["field"]) if isinstance(parsed, dict) else None
        return (got == want), ("" if got == want else "wrong-field-value")
    return False, "unknown-probe-kind"


def side_from_env(prefix: str, *, default_prices: tuple[float, float]) -> SideConfig:
    base = os.environ.get(f"{prefix}_BASE_URL", "").strip()
    key = os.environ.get(f"{prefix}_API_KEY", "").strip()
    model = (os.environ.get(f"{prefix}_MODEL") or os.environ.get(f"{prefix}_MODEL_GROUP") or "").strip()
    if not base or not key or not model:
        raise CanaryError(f"{prefix}_BASE_URL, {prefix}_API_KEY and {prefix}_MODEL(_GROUP) are required")
    in_price = float(os.environ.get(f"{prefix}_INPUT_PRICE_PER_MILLION_USD", default_prices[0]))
    out_price = float(os.environ.get(f"{prefix}_OUTPUT_PRICE_PER_MILLION_USD", default_prices[1]))
    return SideConfig(prefix.lower(), base.rstrip("/"), key, model, in_price, out_price)


def _post_chat(cfg: SideConfig, prompt: str, max_tokens: int) -> tuple[str, dict, float]:
    payload = json.dumps(
        {
            "model": cfg.model,
            "messages": [{"role": "user", "content": prompt}],
            "max_tokens": max_tokens,
            "temperature": 0,
            "stream": False,
        }
    ).encode()
    request = urllib.request.Request(
        f"{cfg.base_url}/chat/completions",
        data=payload,
        headers={"Content-Type": "application/json", "Authorization": f"Bearer {cfg.api_key}"},
        method="POST",
    )
    started = time.monotonic()
    try:
        with urllib.request.urlopen(request, timeout=REQUEST_TIMEOUT_S) as response:
            first_byte_ms = (time.monotonic() - started) * 1000.0
            raw = response.read()
    except urllib.error.HTTPError as exc:
        raise CanaryError(f"{cfg.name}: http-{exc.code}") from None
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        raise CanaryError(f"{cfg.name}: transport-error") from None
    try:
        doc = json.loads(raw)
    except json.JSONDecodeError:
        raise CanaryError(f"{cfg.name}: non-json-response") from None
    choices = doc.get("choices") or []
    message = (choices[0].get("message") if choices else {}) or {}
    text = message.get("content")
    if isinstance(text, list):
        text = "".join(str(part.get("text", "")) for part in text if isinstance(part, dict))
    usage = doc.get("usage") or {}
    usage = {
        "input_tokens": int(usage.get("prompt_tokens") or usage.get("input_tokens") or 0),
        "output_tokens": int(usage.get("completion_tokens") or usage.get("output_tokens") or 0),
    }
    return str(text or ""), usage, first_byte_ms


def run_side(cfg: SideConfig, probes: list[dict], nonce: str) -> dict[str, ProbeResult]:
    results: dict[str, ProbeResult] = {}
    for probe in probes:
        prompt = probe_prompt(probe, nonce)
        try:
            text, usage, first_byte_ms = _post_chat(cfg, prompt, int(probe["max_tokens"]))
        except CanaryError as exc:
            results[probe["id"]] = ProbeResult(probe["id"], "error", 0.0, 0.0, str(exc).split(": ")[-1])
            continue
        passed, reason = check_probe(probe, nonce, text)
        cost = (
            usage["input_tokens"] * cfg.input_price_per_million
            + usage["output_tokens"] * cfg.output_price_per_million
        ) / 1_000_000.0
        results[probe["id"]] = ProbeResult(
            probe["id"], "pass" if passed else "fail", round(first_byte_ms, 3), round(cost, 9), reason
        )
    return results


def classify(direct: dict[str, ProbeResult], router: dict[str, ProbeResult]) -> dict[str, int]:
    counts = {"both_pass": 0, "both_fail": 0, "router_regression": 0, "provider_only_fail": 0}
    for probe_id, direct_result in direct.items():
        router_result = router[probe_id]
        if direct_result.outcome == "pass" and router_result.outcome == "pass":
            counts["both_pass"] += 1
        elif direct_result.outcome == "pass" and router_result.outcome != "pass":
            counts["router_regression"] += 1
        elif direct_result.outcome != "pass" and router_result.outcome == "pass":
            counts["provider_only_fail"] += 1
        else:
            counts["both_fail"] += 1
    return counts


def _cell(cell_id: str, mode: str, results: dict[str, ProbeResult], provenance: dict) -> dict:
    tasks = []
    for probe_id, result in results.items():
        tasks.append(
            {
                "task_id": probe_id,
                "outcome": result.outcome,
                "time_to_first_event_ms": result.latency_ms,
                "completion_latency_ms": result.latency_ms,
                "tool_turns": 0,
                "cost_usd": result.cost_usd,
                "detail": result.detail,
            }
        )
    passes = sum(1 for r in results.values() if r.outcome == "pass")
    errors = sum(1 for r in results.values() if r.outcome == "error")
    return {
        "cell_id": cell_id,
        "mode": mode,
        "attempts": len(results),
        "passes": passes,
        "observed_error_rate": round(errors / max(len(results), 1), 4),
        "provenance": provenance,
        "task_results": tasks,
    }


def build_report(run: RunResult, probes: list[dict], provenance: dict, run_id: str, started_utc: str) -> dict:
    counts = classify(run.direct, run.router)
    total_cost = sum(r.cost_usd for r in list(run.direct.values()) + list(run.router.values()))
    return {
        "schema_version": SCHEMA_VERSION,
        "run_id": run_id,
        "started_utc": started_utc,
        "canary": {
            "probes": len(probes),
            "classification": counts,
            "allowed_router_regressions": 0,
            "allowed_error_rate": ALLOWED_ERROR_RATE,
            "cost_usd_total": round(total_cost, 9),
            "cost_basis": "provider usage x listed per-million prices (not invoice-reconciled)",
        },
        "cells": [
            _cell(DIRECT_CELL, "direct", run.direct, provenance),
            _cell(ROUTER_CELL, "fixed-router", run.router, provenance),
        ],
    }


def gate_violations(report: dict) -> list[str]:
    violations: list[str] = []
    canary = report["canary"]
    counts = canary["classification"]
    if counts["router_regression"] > canary["allowed_router_regressions"]:
        violations.append(
            f"router regressions: {counts['router_regression']} > allowed {canary['allowed_router_regressions']}"
        )
    for cell in report["cells"]:
        observed = cell.get("observed_error_rate", 0.0)
        if observed > canary["allowed_error_rate"]:
            violations.append(f"{cell['cell_id']}: observed error rate {observed} over allowed {canary['allowed_error_rate']}")
    return violations


def validate_report(report: dict, matrix_path: Path = DEFAULT_MATRIX, *, require_baseline_link: bool = True) -> list[str]:
    """Validate the schema with the shared validator, then the canary gate."""
    sys.path.insert(0, str(ROOT / "scripts"))
    import eval_report_schema  # noqa: PLC0415 - local import keeps the offline path explicit

    matrix_doc = None
    if matrix_path.is_file():
        try:
            import yaml  # noqa: PLC0415
        except ImportError:  # pyyaml is optional for a live run without uv
            print(
                "note: pyyaml is unavailable, so preregistered sample-size and task-gate checks were skipped; "
                "run via 'uv run --with pyyaml' to include them",
                file=sys.stderr,
            )
        else:
            matrix_doc = yaml.safe_load(matrix_path.read_text(encoding="utf-8"))
    violations = eval_report_schema.validate_report(report, matrix_doc, require_baseline_link=require_baseline_link)
    violations.extend(gate_violations(report))
    return violations


def _provenance(
    direct: SideConfig,
    router: SideConfig,
    nonce_seed: int,
    nonce: str,
    task_image_digests: dict,
    baseline_kind: str = "",
    baseline_of: str = "",
) -> dict:
    out = {
        "client_versions": {
            "direct_provider_canary": "v1",
            "python": f"{sys.version_info.major}.{sys.version_info.minor}.{sys.version_info.micro}",
        },
        "canary_probe_version": 1,
        "direct_model": direct.model,
        "router_model_group": router.model,
        "seed": nonce_seed,
        "nonce": nonce,
        "task_image_digests": task_image_digests,
    }
    if baseline_kind:
        out["baseline_kind"] = baseline_kind
    if baseline_of:
        out["baseline_of"] = baseline_of
    return out


def _validate_baseline_flags(kind: str, of: str) -> None:
    if kind and kind not in ("pinned", "current-stable"):
        raise CanaryError("--baseline-kind must be 'pinned' or 'current-stable'")
    if kind == "current-stable" and of:
        raise CanaryError("a current-stable run must not set --baseline-of; merge sets it")
    if of and kind != "pinned":
        raise CanaryError("--baseline-of is only valid with --baseline-kind pinned")


def run_command(args) -> int:
    _validate_baseline_flags(args.baseline_kind, args.baseline_of)
    probes = load_probes(args.probes)
    direct_prices = _price_pair(args.direct_prices)
    router_prices = _price_pair(args.router_prices)
    direct = side_from_env("CANARY_DIRECT", default_prices=direct_prices)
    router = side_from_env("CANARY_ROUTER", default_prices=router_prices)
    seed = int(os.environ.get("CANARY_SEED", "330"))
    started_utc = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    nonce = f"{seed}-{uuid.uuid4().hex[:8]}"
    budget = float(os.environ.get("CANARY_MAX_COST_USD", "0.50"))
    if budget <= 0:
        raise CanaryError("CANARY_MAX_COST_USD must be positive")

    run = RunResult()
    run.direct = run_side(direct, probes, nonce)
    spent = sum(r.cost_usd for r in run.direct.values())
    if spent > budget:
        raise CanaryError(f"direct side spent ${spent:.4f} over the ${budget:.2f} budget before the router side ran")
    run.router = run_side(router, probes, nonce)
    spent += sum(r.cost_usd for r in run.router.values())
    if spent > budget:
        raise CanaryError(f"canary spent ${spent:.4f} over the ${budget:.2f} budget")

    report = build_report(
        run,
        probes,
        _provenance(direct, router, seed, nonce, json.loads(args.task_image_digests), args.baseline_kind, args.baseline_of),
        f"canary-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}",
        started_utc,
    )
    # Write the evidence before validating, so a gate failure still leaves the run recorded.
    if args.report_out:
        Path(args.report_out).write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    if args.markdown_out:
        Path(args.markdown_out).write_text(render_markdown(report), encoding="utf-8")
    # A current-stable run is an intermediate artifact: its per-cell baseline
    # link is established by `merge`, which validates the pair.
    violations = validate_report(report, args.matrix, require_baseline_link=args.baseline_kind != "current-stable")
    counts = report["canary"]["classification"]
    print(
        f"canary: {report['canary']['probes']} probes; both_pass={counts['both_pass']} "
        f"router_regression={counts['router_regression']} provider_only_fail={counts['provider_only_fail']} "
        f"both_fail={counts['both_fail']}; cost ${report['canary']['cost_usd_total']:.6f}"
    )
    if violations:
        for violation in violations:
            print(f"canary gate violation: {violation}", file=sys.stderr)
        return 1
    print("canary ok: no router regression, report schema valid, gates pass")
    return 0


def _price_pair(text: str) -> tuple[float, float]:
    try:
        left, right = text.split(",")
        return float(left), float(right)
    except ValueError as exc:
        raise CanaryError("prices must be two comma-separated per-million USD numbers") from exc


def render_markdown(report: dict) -> str:
    counts = report["canary"]["classification"]
    rows = ["| Cell | Probes | Passes | Error rate |", "| --- | ---: | ---: | ---: |"]
    for cell in report["cells"]:
        rows.append(f"| `{cell['cell_id']}` | {cell['attempts']} | {cell['passes']} | {cell['observed_error_rate']:.2%} |")
    return (
        f"# Direct-provider differential canary\n\n"
        f"- run: `{report['run_id']}` at {report['started_utc']}\n"
        f"- classification: both pass {counts['both_pass']}, router regression {counts['router_regression']}, "
        f"provider-only fail {counts['provider_only_fail']}, both fail {counts['both_fail']}\n"
        f"- cost: ${report['canary']['cost_usd_total']:.6f} "
        f"({report['canary']['cost_basis']})\n\n"
        + "\n".join(rows)
        + "\n"
    )


def _probe_ids(cell: dict) -> list[str]:
    return sorted(str(task.get("task_id")) for task in cell.get("task_results") or [])


def merge_reports(pinned: dict, current_stable: dict, run_id: str, started_utc: str) -> dict:
    """Combine a pinned-baseline run and a current-stable canary run (AGENT-01).

    The current-stable cells are suffixed and linked to their pinned baseline, so
    the canary is reported next to the baseline, never instead of it.
    """
    for label, report in (("pinned", pinned), ("current-stable", current_stable)):
        if not isinstance(report, dict) or report.get("schema_version") != SCHEMA_VERSION:
            raise CanaryError(f"{label} report must be a version {SCHEMA_VERSION} object")
        if not report.get("cells"):
            raise CanaryError(f"{label} report has no cells")
    pinned_kinds = {(c.get("provenance") or {}).get("baseline_kind") for c in pinned["cells"]}
    if pinned_kinds != {"pinned"}:
        raise CanaryError("pinned report cells must all declare provenance.baseline_kind: pinned")
    for cell in current_stable["cells"]:
        if (cell.get("provenance") or {}).get("baseline_kind") != "current-stable":
            raise CanaryError("current-stable report cells must declare provenance.baseline_kind: current-stable")
        if (cell.get("provenance") or {}).get("baseline_of"):
            raise CanaryError("current-stable report already carries baseline_of; merge sets it from the pinned run")

    pinned_ids = {c["cell_id"]: c for c in pinned["cells"]}
    stable_ids = {c["cell_id"]: c for c in current_stable["cells"]}
    if set(pinned_ids) != set(stable_ids):
        raise CanaryError(f"cell ids differ between runs: pinned={sorted(pinned_ids)} current-stable={sorted(stable_ids)}")
    for cell_id, cell in pinned_ids.items():
        if _probe_ids(cell) != _probe_ids(stable_ids[cell_id]):
            raise CanaryError(f"probe sets differ for {cell_id!r}; refusing to compare different workloads")

    cells: list[dict] = []
    for cell_id in pinned_ids:
        cells.append(pinned_ids[cell_id])
    for cell_id in sorted(stable_ids):
        cell = json.loads(json.dumps(stable_ids[cell_id]))
        cell["cell_id"] = f"{cell_id}-current-stable"
        cell["provenance"]["baseline_of"] = cell_id
        cells.append(cell)

    def classification_sum(reports: tuple[dict, ...]) -> dict[str, int]:
        out: dict[str, int] = {}
        for report in reports:
            for name, value in ((report.get("canary") or {}).get("classification") or {}).items():
                out[name] = out.get(name, 0) + int(value)
        return out

    merged = {
        "schema_version": SCHEMA_VERSION,
        "run_id": run_id,
        "started_utc": started_utc,
        "canary": {
            "probes": sum(int((r.get("canary") or {}).get("probes") or 0) for r in (pinned, current_stable)),
            "runs": 2,
            "classification": classification_sum((pinned, current_stable)),
            "allowed_router_regressions": 0,
            "allowed_error_rate": ALLOWED_ERROR_RATE,
            "cost_usd_total": round(
                float((pinned.get("canary") or {}).get("cost_usd_total") or 0)
                + float((current_stable.get("canary") or {}).get("cost_usd_total") or 0),
                9,
            ),
            "cost_basis": "provider usage x listed per-million prices (not invoice-reconciled)",
            "baseline_of": "pinned cells in this report",
        },
        "cells": cells,
    }
    return merged


def merge_command(args) -> int:
    pinned = json.loads(Path(args.pinned).read_text(encoding="utf-8"))
    stable = json.loads(Path(args.current_stable).read_text(encoding="utf-8"))
    started = args.started_utc or time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    run_id = args.run_id or f"canary-baseline-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}"
    merged = merge_reports(pinned, stable, run_id, started)
    if args.report_out:
        Path(args.report_out).write_text(json.dumps(merged, indent=2) + "\n", encoding="utf-8")
    if args.markdown_out:
        Path(args.markdown_out).write_text(render_markdown(merged), encoding="utf-8")
    violations = validate_report(merged, args.matrix)
    counts = merged["canary"]["classification"]
    print(
        f"merged canary: {len(merged['cells'])} cells; both_pass={counts.get('both_pass', 0)} "
        f"router_regression={counts.get('router_regression', 0)} cost ${merged['canary']['cost_usd_total']:.6f}"
    )
    if violations:
        for violation in violations:
            print(f"canary gate violation: {violation}", file=sys.stderr)
        return 1
    print("merged canary ok: pinned and current-stable rows, schema valid, no router regression")
    return 0


def validate_command(args) -> int:
    report = json.loads(Path(args.report).read_text(encoding="utf-8"))
    violations = validate_report(report, args.matrix)
    if violations:
        for violation in violations:
            print(f"canary gate violation: {violation}", file=sys.stderr)
        return 1
    print("canary report ok: schema valid, no router regression, gates pass")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Direct-provider differential canary (ARCH-05, #330)")
    sub = parser.add_subparsers(dest="command", required=True)
    run_parser = sub.add_parser("run", help="run the canary against a direct provider and the pinned router target")
    run_parser.add_argument("--probes", type=Path, default=DEFAULT_PROBES)
    run_parser.add_argument("--matrix", type=Path, default=DEFAULT_MATRIX)
    run_parser.add_argument("--report-out", type=Path)
    run_parser.add_argument("--markdown-out", type=Path)
    run_parser.add_argument(
        "--task-image-digests",
        default="{}",
        help='JSON object of sha256 task/container digests for provenance, e.g. \'{"sandbox":"sha256:..."}\'',
    )
    run_parser.add_argument(
        "--baseline-kind",
        default="",
        choices=["", "pinned", "current-stable"],
        help="AGENT-01 baseline role recorded in provenance (pinned baseline or current-stable canary)",
    )
    run_parser.add_argument(
        "--baseline-of",
        default="",
        help="cell id this pinned cell is the baseline of (recorded in provenance)",
    )
    run_parser.add_argument("--direct-prices", default="0.20,1.25", help="direct input,output USD per million tokens")
    run_parser.add_argument("--router-prices", default="0.20,1.25", help="router input,output USD per million tokens")
    run_parser.set_defaults(func=run_command)
    merge_parser = sub.add_parser("merge", help="combine a pinned-baseline run and a current-stable canary run")
    merge_parser.add_argument("--pinned", type=Path, required=True)
    merge_parser.add_argument("--current-stable", type=Path, required=True)
    merge_parser.add_argument("--matrix", type=Path, default=DEFAULT_MATRIX)
    merge_parser.add_argument("--run-id", default="")
    merge_parser.add_argument("--started-utc", default="")
    merge_parser.add_argument("--report-out", type=Path)
    merge_parser.add_argument("--markdown-out", type=Path)
    merge_parser.set_defaults(func=merge_command)
    validate_parser = sub.add_parser("validate", help="validate a recorded canary report")
    validate_parser.add_argument("report", type=Path)
    validate_parser.add_argument("--matrix", type=Path, default=DEFAULT_MATRIX)
    validate_parser.set_defaults(func=validate_command)
    args = parser.parse_args(argv)
    try:
        return args.func(args)
    except CanaryError as exc:
        print(f"canary error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
