#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Exercise E01-E10 and native shadow with a router binary, real LRP, and loopback mocks.

Run using the service environment, or pass --lrp-python explicitly. A synthetic
bundle must contain two trained targets: --cheap-model and --strong-model select
their actual model IDs (otherwise the first two trained manifest entries apply).
Only the supplied bundle is read. Test credentials, signed license, configuration,
logs and SQLite database are temporary and deleted; exported evidence is scalar.
Synthetic embedding latency is wiring evidence, never real-ONNX performance.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import secrets
import socket
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from contextlib import ExitStack
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
SHORT = "Calculate 3 + 1. Return the integer only."
LONG = ("Review this multi-file code and identify the invariant. "
        + "module synthetic: function preserves counter invariant. " * 180)
GROUPS = ("lrp-e2e-staging", "lrp-e2e-no-content-staging", "lrp-e2e-pin-staging",
          "lrp-e2e-down-staging", "lrp-e2e-fallback-staging", "lrp-e2e-invalid-staging",
          "lrp-e2e-shadow-staging", "lrp-e2e-explain-staging")
# Identifiers are fixed by this program, not supplied by a caller or a bundle.
DECISION_TABLES = (
    "request_decision_shape_features", "request_target_candidates",
    "request_target_filter_reasons", "request_routing_decisions",
    "request_routing_signals", "request_dynamic_score_terms",
    "request_policy_executions", "request_fallback_transitions", "request_cache_reasons",
)


class HarnessError(Exception):
    """Messages are fixed scalar diagnostics, never subprocess or HTTP output."""


def write_json(path: Path, value: Any) -> None:
    path.write_text(json.dumps(value, indent=2, allow_nan=False) + "\n", encoding="utf-8")
    path.chmod(0o600)


def run(command: list[str], stage: str, *, cwd: Path = ROOT) -> None:
    try:
        result = subprocess.run(command, cwd=cwd, stdout=subprocess.DEVNULL,
                                stderr=subprocess.DEVNULL, timeout=300, check=False)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise HarnessError(stage + "_execution_failed") from exc
    if result.returncode:
        raise HarnessError(stage + "_nonzero_exit")


def stop(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)


def start(stack: ExitStack, command: list[str], log: Path, env: dict[str, str],
          cpus: set[int]) -> subprocess.Popen[bytes]:
    with log.open("wb") as output:
        process = subprocess.Popen(command, cwd=log.parent, env=env,
                                   stdout=output, stderr=subprocess.STDOUT)
    stack.callback(stop, process)
    if cpus and hasattr(os, "sched_setaffinity"):
        os.sched_setaffinity(process.pid, cpus)
    return process


def reserved_port(stack: ExitStack) -> socket.socket:
    sock = stack.enter_context(socket.socket())
    sock.bind(("127.0.0.1", 0))
    return sock


def http(url: str, payload: Any = None, headers: dict[str, str] | None = None,
         timeout: float = 10) -> tuple[int, bytes, str]:
    # Ignore ambient proxy configuration: this harness only addresses loopback.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    data = json.dumps(payload).encode() if payload is not None else None
    request = urllib.request.Request(url, data=data, headers={
        "Content-Type": "application/json", **(headers or {})})
    try:
        response = opener.open(request, timeout=timeout)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.code, response.read(4 * 1024 * 1024), response.headers.get("X-Request-Id", "")


def ready(url: str, process: subprocess.Popen[bytes], stage: str) -> None:
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise HarnessError(stage + "_exited_before_ready")
        try:
            if http(url, timeout=.5)[0] == 200:
                return
        except OSError:
            pass
        time.sleep(.1)
    raise HarnessError(stage + "_readiness_timeout")


class MockHandler(BaseHTTPRequestHandler):
    def log_message(self, _format: str, *_args: object) -> None:
        pass

    def do_POST(self) -> None:
        server: Any = self.server
        raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        if self.path == "/bad-index":
            status, body = 200, {"targetIndex": 999, "fallbackIndexes": [], "classLabel": "lrp:test-invalid"}
        elif self.path == "/observe-policy":
            # Only the optional two worked examples use this observer. Forward
            # the real router's normalized payload unchanged, then explain that
            # same payload against the same immutable bundle. Never retain raw.
            payload = json.loads(raw)
            headers = {"X-LRP-Auth": server.policy_auth}
            status, result, _ = http(server.policy_url + "/route", payload, headers)
            body = json.loads(result)
            explanation_status, explanation_raw, _ = http(server.policy_url + "/explain", payload, headers)
            explanation = json.loads(explanation_raw)
            context = payload.get("context", {})
            observed = {
                "policy_http_status": status, "explain_http_status": explanation_status,
                "estimated_input_tokens": context.get("estimatedTokens", 0),
                "text_bytes": context.get("textChars", 0),
                "selected_index": body.get("targetIndex"), "class_label": body.get("classLabel"),
                "explain_selected_index": explanation.get("targetIndex"),
                "quality_floor": explanation.get("floor"),
                "targets": [{key: target.get(key) for key in (
                    "index", "provider", "model", "quality", "out_tokens", "est_cost", "excluded_reason")}
                    for target in explanation.get("targets", [])],
            }
            # Feature contributions are local model explanations, not raw
            # features or prompt fragments. Keep only fixed feature identifiers
            # and finite scalar contributions, with a maximum of ten per target.
            for target, source in zip(observed["targets"], explanation.get("targets", [])):
                target["feature_importances"] = [
                    {"feature": item["feature"], "contribution": item["contribution"]}
                    for item in source.get("feature_importances", [])[:10]
                    if isinstance(item.get("feature"), str)
                    and item["feature"].replace("_", "").isalnum()
                    and isinstance(item.get("contribution"), (int, float))
                    and math.isfinite(item["contribution"])
                ]
            with server.lock:
                server.observations.append(observed)
        else:
            # Consume content without retaining or logging it. Only model IDs and
            # statuses survive in the in-memory mock observation list.
            model = str(json.loads(raw).get("model", ""))
            with server.lock:
                status = 500 if model == server.fail_model else 200
                server.calls.append((model, status))
            if status == 500:
                body = {"error": {"type": "server_error", "message": "synthetic failure"}}
            else:
                body = {"id": "synthetic-lrp-e2e", "object": "chat.completion", "model": model,
                        "choices": [{"index": 0, "message": {"role": "assistant", "content": "4"},
                                     "finish_reason": "stop"}],
                        "usage": {"prompt_tokens": 10, "completion_tokens": 1, "total_tokens": 11}}
        encoded = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)


class MockServer(ThreadingHTTPServer):
    def __init__(self) -> None:
        super().__init__(("127.0.0.1", 0), MockHandler)
        self.calls: list[tuple[str, int]] = []
        self.fail_model: str | None = None
        self.lock = threading.Lock()
        self.policy_url = ""
        self.policy_auth = ""
        self.observations: list[dict[str, Any]] = []


def mock_server(stack: ExitStack) -> MockServer:
    server = MockServer()
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    stack.callback(server.server_close)
    stack.callback(server.shutdown)
    return server


def rows(database: Path, query: str, params: tuple[Any, ...] = ()) -> list[dict[str, Any]]:
    with sqlite3.connect(f"{database.as_uri()}?mode=ro", uri=True, timeout=5) as connection:
        connection.row_factory = sqlite3.Row
        return [dict(row) for row in connection.execute(query, params)]


def telemetry(database: Path, request_id: str) -> dict[str, Any]:
    if not request_id:
        raise HarnessError("router_request_id_missing")
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        found = rows(database, "SELECT * FROM request_policy_executions WHERE request_id = ? ORDER BY seq", (request_id,))
        if found:
            return found[-1]
        time.sleep(.02)
    raise HarnessError("policy_telemetry_missing")


def safe_telemetry(database: Path, forbidden: list[str], logs: list[Path]) -> bool:
    # Check every persisted decision value and its relational type. Never emit
    # rows, raw prompts, model output, credentials or configuration on failure.
    for table in DECISION_TABLES:
        schema = rows(database, f'PRAGMA table_info("{table}")')
        if not schema or any(any(kind in str(column["type"]).upper() for kind in ("JSON", "BLOB", "[]"))
                             for column in schema):
            return False
        for row in rows(database, f'SELECT * FROM "{table}"'):
            for value in row.values():
                if isinstance(value, bytes) or (isinstance(value, str) and any(secret in value for secret in forbidden)):
                    return False
                if isinstance(value, str) and value.lstrip().startswith(("{", "[")):
                    return False
    return all(not any(secret in path.read_text(errors="replace") for secret in forbidden)
               for path in logs if path.exists())


def execute(args: argparse.Namespace, checks: list[dict[str, Any]]) -> dict[str, Any]:
    manifest = json.loads((args.bundle / "manifest.json").read_text())
    if not manifest.get("synthetic") or manifest.get("embedding", {}).get("kind") != "synthetic":
        raise HarnessError("synthetic_bundle_required")
    targets = [entry for entry in manifest["targets"] if not entry.get("skipped") and entry.get("n_train", 0) >= 200]
    if len(targets) < 2:
        raise HarnessError("two_trained_targets_required")

    def choose(model: str | None, default: int) -> dict[str, Any]:
        matches = [entry for entry in targets if entry["model"] == model] if model else [targets[default]]
        if len(matches) != 1:
            raise HarnessError("ambiguous_or_missing_target")
        return dict(matches[0])

    cheap, strong = choose(args.cheap_model, 0), choose(args.strong_model, 1)
    if cheap["model"] == strong["model"]:
        raise HarnessError("distinct_mock_models_required")
    selected = [cheap, strong]
    cpus = set(sorted(os.sched_getaffinity(0))[:4]) if hasattr(os, "sched_getaffinity") else set()
    evidence: dict[str, Any] = {"embedding_kind": "synthetic", "real_onnx_performance_evidence": False,
                                "cpu_affinity_count": len(cpus), "latency_requests": args.requests}
    with tempfile.TemporaryDirectory(prefix="metrum-lrp-e2e-") as temp_name, ExitStack() as stack:
        temp = Path(temp_name)
        binary = args.router_binary.resolve() if args.router_binary else temp / "metrum-genai-smartrouter"
        if args.router_binary is None:
            run(["go", "build", "-buildvcs=false", "-o", str(binary), "./cmd/metrum-ai-router"], "router_build")
        upstream = mock_server(stack)
        router_sock, policy_sock, admin_sock, down_sock = [reserved_port(stack) for _ in range(4)]
        router_port, policy_port, admin_port, down_port = [sock.getsockname()[1] for sock in
                                                         (router_sock, policy_sock, admin_sock, down_sock)]
        token, auth, provider_key = [secrets.token_urlsafe(32) for _ in range(3)]
        upstream.policy_url = f"http://127.0.0.1:{policy_port}"
        upstream.policy_auth = auth
        token_digest = hashlib.sha256(token.encode()).hexdigest()
        db = temp / "usage.sqlite"
        service_config = {"groups": {group: {"quality_floor": .8, "explore_rate": 0,
                          "pin_ttl_s": 1800 if group == GROUPS[2] else 0, "min_train_rows": 200}
                          for group in GROUPS}}
        write_json(temp / "lrp.json", service_config)
        providers = {entry["provider"]: {"base_url": f"http://127.0.0.1:{upstream.server_port}/v1",
                     "dialect": "openai-chat", "api_key": provider_key} for entry in selected}
        router_targets = [{"provider": entry["provider"], "model": entry["model"],
                           "tier": "cheap" if index == 0 else "strong",
                           "input_price_per_million_usd": .1 if index == 0 else 1.0,
                           "output_price_per_million_usd": .1 if index == 0 else 1.0}
                          for index, entry in enumerate(selected)]
        models = {}
        for group in GROUPS:
            port = down_port if group in GROUPS[3:5] else policy_port
            url = f"http://127.0.0.1:{port}/route"
            if group == GROUPS[5]:
                url = f"http://127.0.0.1:{upstream.server_port}/bad-index"
            if group == GROUPS[7]:
                url = f"http://127.0.0.1:{upstream.server_port}/observe-policy"
            models[group] = {"strategy": "external", "targets": router_targets, "external_policy": {
                "url": url, "allow_hosts": ["127.0.0.1"], "timeout_ms": 500,
                "max_response_bytes": 65536, "include_request": group != GROUPS[1],
                "mode": "shadow" if group == GROUPS[6] else "enforce",
                "on_error": "fallback" if group == GROUPS[4] else "fail_closed",
                "headers": {"X-LRP-Auth": auth}}}
        write_json(temp / "router.json", {"server": {
            "listen": f"127.0.0.1:{router_port}", "default_model_group": GROUPS[0],
            "logging": {"path": str(temp / "requests.jsonl")},
            "cache": {"enabled": False}, "decision_telemetry": {"enabled": True},
            "diagnostics": {"enabled": True}, "upstream": {"timeout_ms": 5000},
            "identifiers": {"mode": "passthrough"},
            "usage_db": {"enabled": True, "driver": "sqlite", "path": str(db), "migration_policy": "auto-safe"}},
            "state_path": str(temp / "state.json"), "providers": providers, "models": models,
            "callers": [{"id": "lrp-e2e-test-caller", "user": "test-user", "project": "test-project",
                         "environment": "test", "status": "active", "token_sha256": token_digest,
                         "token_id": "rtr_metrum_lrp_e2e_test", "allow": list(GROUPS),
                         "rate": {"rpm": 100000, "tpm": 100000000, "concurrent": 8}}]})
        # Do not pass operator provider credentials or proxy settings into test children.
        env = {key: os.environ[key] for key in ("PATH", "HOME", "TMPDIR", "SYSTEMROOT", "LD_LIBRARY_PATH")
               if key in os.environ}
        env.update({"LRP_POLICY_AUTH_HEADER": auth, "PYTHONPATH": str(args.service_dir.resolve()),
                    "PYTHONUNBUFFERED": "1", "OMP_NUM_THREADS": "1", "OPENBLAS_NUM_THREADS": "1",
                    "MKL_NUM_THREADS": "1", "GOMAXPROCS": str(len(cpus) or 4)})
        policy_sock.close()
        admin_sock.close()
        # Preserve a virtualenv interpreter symlink: resolving it selects the
        # base interpreter and loses the installed service dependencies.
        policy = start(stack, [str(args.lrp_python.absolute()), "-m", "lrp.cli", "serve", "--bundle",
                       str(args.bundle.resolve()), "--config", str(temp / "lrp.json"), "--port", str(policy_port),
                       "--admin-port", str(admin_port), *(["--enable-admin"] if args.explain_evidence else [])],
                       temp / "lrp.log", env, cpus)
        ready(f"http://127.0.0.1:{admin_port}/readyz", policy, "lrp")
        router_sock.close()
        router = start(stack, [str(binary), "-config", str(temp / "router.json")], temp / "router.log", env, cpus)
        base = f"http://127.0.0.1:{router_port}"
        ready(base + "/readyz", router, "router")
        sensitive = [token, auth, provider_key, token_digest, SHORT, LONG,
                     "Review this multi-file code and identify the invariant."]

        def chat(group: str = GROUPS[0], text: str = SHORT,
                 messages: list[dict[str, str]] | None = None) -> tuple[int, bytes, str, dict[str, Any]]:
            status, body, rid = http(base + "/v1/chat/completions", {"model": group, "messages": messages or
                                    [{"role": "user", "content": text}], "max_tokens": 128},
                                    {"Authorization": "Bearer " + token})
            return status, body, rid, telemetry(db, rid)

        def label_kind(label: str, *prefixes: str) -> bool:
            """Match legacy exact classLabels or Wave-2 bounded lrp:<kind>… labels (#36)."""
            value = str(label or "")
            return any(value == prefix or value.startswith(prefix + ":") for prefix in prefixes)

        def record(case: str, passed: bool, **scalars: Any) -> None:
            checks.append({"case": case, "passed": bool(passed), **scalars})
            print(json.dumps(checks[-1], allow_nan=False), flush=True)

        def model_for(rid: str) -> str:
            attempts = rows(db, "SELECT model FROM request_attempts WHERE request_id = ? AND selected = 1", (rid,))
            return str(attempts[0]["model"]) if len(attempts) == 1 else ""

        status, _, rid, event = chat()
        record("E01", status == 200 and model_for(rid) == cheap["model"] and
               label_kind(event["class_label"], "lrp:caf", "lrp:cheapest-above-floor"), http_status=status)
        status, _, rid, event = chat(text=LONG)
        record("E02", status == 200 and model_for(rid) == strong["model"] and
               label_kind(event["class_label"], "lrp:caf", "lrp:cheapest-above-floor"), http_status=status)
        for case, group in (("E03", GROUPS[3]), ("E05", GROUPS[5])):
            before = len(upstream.calls)
            status, body, _, event = chat(group)
            record(case, status == 502 and b"routing-policy-error" in body and
                   len(upstream.calls) == before and event["outcome"] != "selected", http_status=status)
        status, _, rid, event = chat(GROUPS[4])
        record("E04", status == 200 and model_for(rid) == cheap["model"] and event["outcome"] == "fallback",
               http_status=status, policy_outcome=event["outcome"])
        status, _, rid, event = chat(GROUPS[1])
        record("E06", status == 200 and model_for(rid) == cheap["model"] and
               event["class_label"] == "lrp:no-request-content", http_status=status)
        with upstream.lock:
            upstream.fail_model = cheap["model"]
        try:
            status, _, rid, event = chat()
        finally:
            with upstream.lock:
                upstream.fail_model = None
        attempts = rows(db, "SELECT model, status_code, selected FROM request_attempts WHERE request_id = ? ORDER BY attempt_index", (rid,))
        transitions = rows(db, "SELECT fallback_succeeded FROM request_fallback_transitions WHERE request_id = ?", (rid,))
        record("E07", status == 200 and len(attempts) == 2 and attempts[0]["model"] == cheap["model"] and
               attempts[0]["status_code"] == 500 and attempts[1]["model"] == strong["model"] and
               attempts[1]["status_code"] == 200 and bool(attempts[1]["selected"]) and
               len(transitions) == 1 and bool(transitions[0]["fallback_succeeded"]),
               http_status=status, attempt_count=len(attempts))
        first_status, _, first_rid, _ = chat(GROUPS[2])
        status, _, rid, event = chat(GROUPS[2], messages=[{"role": "user", "content": SHORT},
                                  {"role": "assistant", "content": "4"}, {"role": "user", "content": LONG}])
        record("E08", first_status == status == 200 and model_for(first_rid) == model_for(rid) == cheap["model"]
               and label_kind(event["class_label"], "lrp:pinned"), http_status=status)
        status, _, rid, event = chat(GROUPS[6], text=LONG)
        recommended = rows(db, "SELECT c.provider, c.model FROM request_routing_signals s "
                           "JOIN request_target_candidates c ON c.request_id = s.request_id "
                           "AND c.candidate_index = s.candidate_index "
                           "WHERE s.request_id = ? AND s.signal_name = 'shadow_recommended_candidate'",
                           (rid,))
        record("native-shadow", status == 200 and model_for(rid) == cheap["model"] and
               event["outcome"] == "shadow_recommended" and len(recommended) == 1 and
               recommended[0]["provider"] == strong["provider"] and recommended[0]["model"] == strong["model"],
               http_status=status, policy_outcome=event["outcome"])
        if args.explain_evidence:
            examples = []
            for workload, text, expected in (("short-arithmetic", SHORT, cheap), ("long-code-review", LONG, strong)):
                before = len(upstream.observations)
                status, _, rid, event = chat(GROUPS[7], text=text)
                if len(upstream.observations) != before + 1:
                    raise HarnessError("explain_observation_missing")
                observed = upstream.observations[-1]
                if (status != 200 or observed["policy_http_status"] != 200 or observed["explain_http_status"] != 200
                        or observed["selected_index"] != observed["explain_selected_index"]
                        or model_for(rid) != expected["model"]):
                    raise HarnessError("explain_does_not_match_routing")
                examples.append({"workload": workload, "router_request_id": rid,
                                 "actual_upstream_provider": expected["provider"],
                                 "actual_upstream_model": model_for(rid),
                                 "router_policy_outcome": event["outcome"], **observed})
            evidence["inference_examples"] = examples
            evidence["bundle_version"] = manifest["version"]
            evidence["explanation_scope"] = "same-router-payload-separate-authenticated-explain-call"
        # Warmup is excluded. Every measured request must have a fresh policy row
        # and a normal learned decision; BT/cache paths cannot fake a fast p99.
        for _ in range(10):
            chat()
        measured_ids: set[str] = set()
        durations = []
        valid = True
        for index in range(args.requests):
            status, _, rid, event = chat(text=SHORT if index % 2 == 0 else LONG)
            valid &= status == 200 and event["outcome"] == "selected" and label_kind(event["class_label"], "lrp:caf", "lrp:cheapest-above-floor")
            measured_ids.add(rid)
            durations.append(float(event["duration_ms"]))
            if (index + 1) % 100 == 0:
                print(json.dumps({"stage": "E09", "completed": index + 1}), flush=True)
        p99 = sorted(durations)[math.ceil(.99 * len(durations)) - 1]
        record("E09", valid and len(measured_ids) == args.requests and p99 < 40,
               request_count=args.requests, policy_p99_ms=p99, cpu_affinity_count=len(cpus),
               embedding_kind="synthetic", real_onnx_performance_evidence=False)
        # Stop children to flush logs and close SQLite before checking persistence.
        stop(router)
        stop(policy)
        safe = safe_telemetry(db, sensitive, [temp / "requests.jsonl", temp / "router.log", temp / "lrp.log"])
        # Apply the same content/credential checks to optional exported evidence,
        # in addition to checking the persistent router tables and process logs.
        serialized_evidence = json.dumps(evidence, allow_nan=False)
        safe = safe and not any(value in serialized_evidence for value in sensitive)
        record("E10", safe, relational_scalar_telemetry=safe, content_and_credentials_absent=safe)
    return evidence


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--lrp-python", type=Path, default=Path(sys.executable))
    parser.add_argument("--service-dir", type=Path, default=ROOT / "services/learned-routing-policy")
    parser.add_argument("--router-binary", type=Path, help="Normal licensed binary; built locally if omitted")
    parser.add_argument("--cheap-model", help="Actual trained model ID assigned the cheap test tier")
    parser.add_argument("--strong-model", help="Actual trained model ID assigned the strong test tier")
    parser.add_argument("--requests", type=int, default=500, help="E09 request count, at least 500")
    parser.add_argument("--explain-evidence", action="store_true",
                        help="Include two safe same-payload inference explanations; E09 remains direct")
    parser.add_argument("--output", type=Path, help="Optional safe scalar JSON report")
    args = parser.parse_args()
    if args.requests < 500:
        parser.error("--requests must be at least 500")
    checks: list[dict[str, Any]] = []
    report: dict[str, Any] = {"schema": "metrum.lrp.e2e.v1", "evidence_scope": "synthetic-local-binary-http",
                              "checks": checks, "production_promotion_evidence": False}
    try:
        report.update(execute(args, checks))
    except HarnessError as exc:
        report["error_class"] = str(exc)
    except KeyboardInterrupt:
        report["error_class"] = "interrupted"
    except Exception:  # noqa: BLE001 -- exceptions can contain protected input; emit a fixed class only.
        report["error_class"] = "harness_internal_error"
    passed = len(checks) == 11 and all(check["passed"] for check in checks) and "error_class" not in report
    report["result"] = "passed" if passed else "failed"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        write_json(args.output, report)
    print(json.dumps(report, allow_nan=False, sort_keys=True), flush=True)
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
