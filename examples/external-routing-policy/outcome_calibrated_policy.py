#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Outcome-calibrated external routing reference for Metrum AI Router.

The router calls ``serve`` before an upstream request. ``collect`` drives
synthetic calibration cases through that same router path, while ``calibrate``
turns human-reviewed JSONL records into a reviewable policy profile and YAML
weight patch. This example intentionally never writes router configuration.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import re
import sys
import threading
import urllib.error
import urllib.parse
import urllib.request
from collections import defaultdict
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any


def read_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError(f"{path} must contain a JSON object")
    return value


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def read_jsonl(path: Path) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        value = json.loads(line)
        if not isinstance(value, dict):
            raise ValueError(f"{path}:{number} must contain a JSON object")
        rows.append(value)
    return rows


def selector_key(value: dict[str, Any]) -> str:
    provider = str(value.get("provider", "")).strip()
    model = str(value.get("model", value.get("modelRef", ""))).strip()
    if not provider or not model:
        raise ValueError("candidate selector requires provider and model")
    return f"{provider}:{model}"


def selector_from_target(target: dict[str, Any]) -> dict[str, str]:
    return {"provider": str(target.get("provider", "")), "model": str(target.get("model", target.get("modelRef", "")))}


def request_text(request: dict[str, Any]) -> str:
    pieces = [str(request.get("system", "")), str(request.get("input", ""))]
    for message in request.get("messages", []) or []:
        if isinstance(message, dict):
            parts = [part for part in message.get("parts", []) or [] if isinstance(part, dict)]
            if parts:
                pieces.extend(str(part.get("text", "")) for part in parts)
            else:
                pieces.append(str(message.get("content", "")))
    return "\n".join(part for part in pieces if part).strip()


MARKER_ANCHOR = "[[outcome-"
MARKER_KINDS = ("calibration", "live-demo")
MARKER_VALUE_STOP = re.compile(r"[\]\r\n]")


def find_markers(text: str, kinds: tuple[str, ...] = MARKER_KINDS) -> list[tuple[int, int, str]]:
    """Return ``(start, end, value)`` for each ``[[outcome-<kind>:<value>]]`` marker.

    Matches the leftmost, non-overlapping semantics of
    ``\\[\\[outcome-(?:kind):[^\\]\\r\\n]+\\]\\]`` in linear time (Metrum AI
    hardening against polynomial backtracking). A value runs until the first
    ``]``, CR or LF; a failed candidate resumes scanning at that stop, because
    every marker starting inside the same value would fail at the same stop.
    """
    markers: list[tuple[int, int, str]] = []
    position = 0
    while True:
        start = text.find(MARKER_ANCHOR, position)
        if start < 0:
            return markers
        kind_start = start + len(MARKER_ANCHOR)
        kind = next((kind for kind in kinds if text.startswith(kind + ":", kind_start)), None)
        if kind is None:
            position = start + 1
            continue
        value_start = kind_start + len(kind) + 1
        stop = MARKER_VALUE_STOP.search(text, value_start)
        value_end = stop.start() if stop else len(text)
        if value_end > value_start and text.startswith("]]", value_end):
            markers.append((start, value_end + 2, text[value_start:value_end]))
            position = value_end + 2
        else:
            position = value_end


def strip_markers(text: str) -> str:
    pieces: list[str] = []
    position = 0
    for start, end, _ in find_markers(text):
        pieces.append(text[position:start])
        position = end
    pieces.append(text[position:])
    return "".join(pieces)


def marker_value(text: str, kind: str) -> str:
    markers = find_markers(text, (kind,))
    return markers[0][2].strip() if markers else ""


def classification_text(request: dict[str, Any]) -> str:
    text = strip_markers(request_text(request))
    return re.sub(r"\s+", " ", text).strip()


def calibration_case_id(request: dict[str, Any]) -> str:
    metadata = request.get("metadata", {}) if isinstance(request.get("metadata"), dict) else {}
    identifier = str(metadata.get("calibration_case_id", "")).strip()
    if identifier:
        return identifier
    return marker_value(request_text(request), "calibration")


def live_demo_case_id(request: dict[str, Any]) -> str:
    metadata = request.get("metadata", {}) if isinstance(request.get("metadata"), dict) else {}
    identifier = str(metadata.get("live_demo_case_id", "")).strip()
    if identifier:
        return identifier
    return marker_value(request_text(request), "live-demo")


def cosine(left: list[float], right: list[float]) -> float:
    if len(left) != len(right) or not left or not right:
        return -1.0
    numerator = sum(a * b for a, b in zip(left, right))
    left_norm = math.sqrt(sum(a * a for a in left))
    right_norm = math.sqrt(sum(b * b for b in right))
    if left_norm == 0 or right_norm == 0:
        return -1.0
    return numerator / (left_norm * right_norm)


class Embeddings:
    def __init__(self, base_url: str, model: str, api_key_env: str) -> None:
        self.base_url = base_url.rstrip("/")
        self.model = model
        self.api_key = os.environ.get(api_key_env, "")

    def embed(self, texts: list[str]) -> list[list[float]]:
        body = json.dumps({"model": self.model, "input": texts}).encode("utf-8")
        request = urllib.request.Request(self.base_url + "/embeddings", body, {"Content-Type": "application/json", "Accept": "application/json"})
        if self.api_key:
            request.add_header("Authorization", "Bearer " + self.api_key)
        try:
            with urllib.request.urlopen(request, timeout=5) as response:
                payload = json.loads(response.read())
        except (urllib.error.URLError, urllib.error.HTTPError, json.JSONDecodeError) as err:
            raise RuntimeError("embedding-request-failed") from err
        data = payload.get("data", []) if isinstance(payload, dict) else []
        vectors = [row.get("embedding") for row in data if isinstance(row, dict)]
        if len(vectors) != len(texts) or any(not isinstance(vector, list) for vector in vectors):
            raise RuntimeError("embedding-response-invalid")
        return [[float(item) for item in vector] for vector in vectors]


def class_vectors(profile: dict[str, Any], embeddings: Embeddings) -> dict[str, list[float]]:
    classes = profile.get("classes", [])
    vectors: dict[str, list[float]] = {}
    missing_text: list[str] = []
    missing_ids: list[str] = []
    for item in classes:
        if not isinstance(item, dict):
            continue
        identifier = str(item.get("id", ""))
        vector = item.get("embedding")
        if isinstance(vector, list):
            vectors[identifier] = [float(number) for number in vector]
        else:
            exemplars = item.get("exemplars", [])
            if not identifier or not isinstance(exemplars, list) or not exemplars:
                raise ValueError("each class requires id and exemplars or embedding")
            missing_ids.append(identifier)
            missing_text.append("\n".join(str(text) for text in exemplars))
    if missing_text:
        embedded = embeddings.embed(missing_text)
        vectors.update(dict(zip(missing_ids, embedded)))
    return vectors


def choose_class(profile: dict[str, Any], text: str, vectors: dict[str, list[float]], embeddings: Embeddings) -> tuple[dict[str, Any] | None, float]:
    if not text:
        return None, -1.0
    vector = embeddings.embed([text])[0]
    candidates: list[tuple[float, dict[str, Any]]] = []
    for item in profile.get("classes", []):
        if isinstance(item, dict) and str(item.get("id", "")) in vectors:
            candidates.append((cosine(vector, vectors[str(item["id"])]), item))
    if not candidates:
        return None, -1.0
    score, selected = max(candidates, key=lambda item: item[0])
    if score < float(selected.get("minSimilarity", profile.get("minSimilarity", 0.8))):
        return None, score
    return selected, score


def eligible_index(targets: list[dict[str, Any]], selector: dict[str, Any]) -> int | None:
    wanted = selector_key(selector)
    for index, target in enumerate(targets):
        try:
            if selector_key(selector_from_target(target)) == wanted:
                return index
        except ValueError:
            continue
    return None


def weighted_selector(class_profile: dict[str, Any], text: str) -> dict[str, Any]:
    weighted = [item for item in class_profile.get("weights", []) if isinstance(item, dict) and isinstance(item.get("candidate"), dict)]
    if not weighted:
        primary = class_profile.get("primary")
        if isinstance(primary, dict):
            return primary
        raise ValueError("approved class has no weighted candidate")
    total = sum(max(0, int(item.get("weight", 0))) for item in weighted)
    if total <= 0:
        raise ValueError("approved class has invalid weights")
    bucket = int(hashlib.sha256(text.encode("utf-8")).hexdigest()[:16], 16) % total
    for item in sorted(weighted, key=lambda candidate: selector_key(candidate["candidate"])):
        weight = max(0, int(item.get("weight", 0)))
        if bucket < weight:
            return item["candidate"]
        bucket -= weight
    return weighted[-1]["candidate"]


def policy_decision(profile: dict[str, Any], payload: dict[str, Any], vectors: dict[str, list[float]], embeddings: Embeddings) -> dict[str, Any]:
    targets = [item for item in payload.get("targets", []) if isinstance(item, dict)]
    if not targets:
        raise ValueError("policy request has no eligible targets")
    raw_request = payload.get("request")
    if not isinstance(raw_request, dict):
        raise ValueError("outcome policy requires external_policy.include_request=true")
    override = (profile.get("calibrationOverrides", {}) or {}).get(calibration_case_id(raw_request))
    selected_class: dict[str, Any] | None = None
    label = "outcome-policy:unclassified"
    if isinstance(override, dict):
        selector = override
        label = "outcome-policy:calibration"
    else:
        text = classification_text(raw_request)
        selected_class, _score = choose_class(profile, text, vectors, embeddings)
        selector = weighted_selector(selected_class, text) if selected_class else profile.get("strongDefault")
        if selected_class:
            label = "outcome-policy:" + str(selected_class.get("id", "unknown"))
    if not isinstance(selector, dict):
        raise ValueError("profile requires strongDefault selector")
    index = eligible_index(targets, selector)
    if index is None:
        raise ValueError("profile selector is not eligible for this request")
    fallback_indexes = [candidate for candidate in range(len(targets)) if candidate != index]
    return {"targetIndex": index, "fallbackIndexes": fallback_indexes, "classLabel": label}


def reviewed_profile(dataset: dict[str, Any], reviews: list[dict[str, Any]]) -> dict[str, Any]:
    candidates = {selector_key(item): item for item in dataset.get("candidates", []) if isinstance(item, dict)}
    case_classes = {str(item.get("id")): str(item.get("class")) for item in dataset.get("cases", []) if isinstance(item, dict)}
    class_case_counts: dict[str, int] = defaultdict(int)
    for class_id in case_classes.values():
        class_case_counts[class_id] += 1
    grouped: dict[tuple[str, str], list[dict[str, Any]]] = defaultdict(list)
    for review in reviews:
        case_id = str(review.get("caseId", ""))
        candidate = review.get("candidate")
        if case_id not in case_classes or not isinstance(candidate, dict):
            continue
        key = selector_key(candidate)
        if key in candidates and isinstance(review.get("passed"), bool):
            grouped[(case_classes[case_id], key)].append(review)
    profile_classes: list[dict[str, Any]] = []
    promotable = True
    for item in dataset.get("classes", []):
        if not isinstance(item, dict):
            continue
        class_id = str(item.get("id", ""))
        minimum = int(item.get("minReviewed", dataset.get("minReviewed", 10)))
        required_rate = float(item.get("minPassRate", 1.0))
        qualified: list[tuple[float, dict[str, Any], dict[str, Any]]] = []
        evidence: list[dict[str, Any]] = []
        for key, candidate in candidates.items():
            rows = grouped[(class_id, key)]
            passed = sum(1 for row in rows if row["passed"])
            count = len(rows)
            rate = passed / count if count else 0.0
            costs = [float(row.get("costUsd", candidate.get("estimatedCostUsd", 0.0))) for row in rows]
            cost = sum(costs) / len(costs) if costs else float(candidate.get("estimatedCostUsd", 0.0))
            accepted = count >= minimum and rate >= required_rate and cost > 0
            evidence.append({"candidate": {"provider": candidate["provider"], "model": candidate["model"]}, "reviewed": count, "passed": passed, "passRate": rate, "meanCostUsd": cost, "qualified": accepted})
            if accepted:
                qualified.append((rate / cost, candidate, evidence[-1]))
        if not qualified:
            promotable = False
            profile_classes.append({"id": class_id, "exemplars": item.get("exemplars", []), "minSimilarity": item.get("minSimilarity", dataset.get("minSimilarity", 0.8)), "trafficWeight": float(item.get("trafficWeight", class_case_counts[class_id])), "status": "insufficient-evidence", "evidence": evidence})
            continue
        total_score = sum(score for score, _candidate, _evidence in qualified)
        raw_weights = [(score * 100 / total_score, candidate, evidence_row) for score, candidate, evidence_row in qualified]
        weights = largest_remainder(raw_weights)
        primary = max(weights, key=lambda row: row["weight"])["candidate"]
        profile_classes.append({"id": class_id, "exemplars": item.get("exemplars", []), "minSimilarity": item.get("minSimilarity", dataset.get("minSimilarity", 0.8)), "trafficWeight": float(item.get("trafficWeight", class_case_counts[class_id])), "status": "promotable", "primary": primary, "weights": weights, "evidence": evidence})
    return {"schemaVersion": 1, "group": dataset.get("group"), "strongDefault": dataset.get("strongDefault"), "minSimilarity": dataset.get("minSimilarity", 0.8), "promotable": promotable, "classes": profile_classes}


def largest_remainder(rows: list[tuple[float, dict[str, Any], dict[str, Any]]]) -> list[dict[str, Any]]:
    floors = [(math.floor(raw), raw - math.floor(raw), candidate) for raw, candidate, _evidence in rows]
    remaining = 100 - sum(value for value, _fraction, _candidate in floors)
    for index in sorted(range(len(floors)), key=lambda item: (-floors[item][1], selector_key(floors[item][2])))[:remaining]:
        value, fraction, candidate = floors[index]
        floors[index] = (value + 1, fraction, candidate)
    return [{"candidate": {"provider": candidate["provider"], "model": candidate["model"]}, "weight": weight} for weight, _fraction, candidate in sorted(floors, key=lambda row: selector_key(row[2]))]


def render_yaml_patch(profile: dict[str, Any]) -> str:
    aggregate: dict[str, float] = defaultdict(float)
    for item in profile.get("classes", []):
        for weighted in item.get("weights", []) if isinstance(item, dict) else []:
            aggregate[selector_key(weighted["candidate"])] += int(weighted["weight"]) * float(item.get("trafficWeight", 1))
    total = sum(aggregate.values()) or 1
    lines = ["models:", f"  {profile.get('group', 'outcome-calibrated')}:", "    strategy: external", "    # Generated for operator review; apply only after validating the profile.", "    targets:"]
    for key in sorted(aggregate):
        provider, model = key.split(":", 1)
        lines.extend([f"      - provider: {provider}", f"        model: {model}", f"        weight: {round(aggregate[key] * 100 / total)}"])
    return "\n".join(lines) + "\n"


def command_calibrate(args: argparse.Namespace) -> int:
    profile = reviewed_profile(read_json(Path(args.dataset)), read_jsonl(Path(args.reviews)))
    write_json(Path(args.out_profile), profile)
    Path(args.out_yaml).write_text(render_yaml_patch(profile), encoding="utf-8")
    print("promotable" if profile["promotable"] else "insufficient-evidence")
    return 0 if profile["promotable"] else 2


class PolicyServer(ThreadingHTTPServer):
    def __init__(self, address: tuple[str, int], profile: dict[str, Any], embeddings: Embeddings, audit_log: Path | None, audit_token: str, request_token: str) -> None:
        super().__init__(address, PolicyHandler)
        self.profile = profile
        self.embeddings = embeddings
        self.vectors = class_vectors(profile, embeddings)
        self.audit_log = audit_log
        self.audit_token = audit_token
        self.request_token = request_token
        self.audit_lock = threading.Lock()
        self.audit_events: dict[str, dict[str, Any]] = {}

    def record(self, payload: dict[str, Any], decision: dict[str, Any]) -> None:
        raw = payload.get("request") if isinstance(payload.get("request"), dict) else {}
        metadata = raw.get("metadata") if isinstance(raw.get("metadata"), dict) else {}
        targets = payload.get("targets") if isinstance(payload.get("targets"), list) else []
        index = int(decision.get("targetIndex", -1))
        target = selector_from_target(targets[index]) if 0 <= index < len(targets) and isinstance(targets[index], dict) else {}
        event = {"caseId": live_demo_case_id(raw) or calibration_case_id(raw) or metadata.get("calibration_case_id", ""), "classLabel": decision.get("classLabel", ""), "selectedTarget": target}
        with self.audit_lock:
            case_id = str(event["caseId"])
            if case_id:
                self.audit_events[case_id] = event
            if self.audit_log is not None:
                self.audit_log.parent.mkdir(parents=True, exist_ok=True)
                with self.audit_log.open("a", encoding="utf-8") as file:
                    file.write(json.dumps(event, sort_keys=True) + "\n")

    def audit_event(self, case_id: str) -> dict[str, Any] | None:
        with self.audit_lock:
            return self.audit_events.get(case_id)


class PolicyHandler(BaseHTTPRequestHandler):
    server_version = "outcome-calibrated-policy/1.0"

    def do_POST(self) -> None:
        if self.path != "/route":
            self.respond(404, {"error": "not found"})
            return
        server = self.server
        assert isinstance(server, PolicyServer)
        if server.request_token and self.headers.get("X-Outcome-Policy-Request") != server.request_token:
            self.respond(403, {"error": "policy-forbidden"})
            return
        try:
            length = int(self.headers.get("content-length", "0"))
            payload = json.loads(self.rfile.read(length) or b"{}")
            if not isinstance(payload, dict):
                raise ValueError("invalid policy request")
            decision = policy_decision(server.profile, payload, server.vectors, server.embeddings)
            server.record(payload, decision)
            self.respond(200, decision)
        except (ValueError, RuntimeError, json.JSONDecodeError) as err:
            self.respond(400, {"error": str(err)})

    def do_GET(self) -> None:
        path, separator, query = self.path.partition("?")
        if path != "/audit":
            self.respond(404, {"error": "not found"})
            return
        server = self.server
        assert isinstance(server, PolicyServer)
        if not server.audit_token or self.headers.get("X-Outcome-Policy-Audit") != server.audit_token:
            self.respond(403, {"error": "audit-forbidden"})
            return
        case_id = ""
        if separator:
            for field in query.split("&"):
                key, equals, value = field.partition("=")
                if key == "case_id" and equals:
                    case_id = urllib.parse.unquote_plus(value)
                    break
        event = server.audit_event(case_id)
        if event is None:
            self.respond(404, {"error": "audit-not-found"})
            return
        self.respond(200, event)

    def log_message(self, _format: str, *_args: object) -> None:
        return

    def respond(self, status: int, body: dict[str, Any]) -> None:
        raw = json.dumps(body).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


def command_serve(args: argparse.Namespace) -> int:
    profile = read_json(Path(args.profile))
    if args.calibration_overrides:
        overrides = read_json(Path(args.calibration_overrides)).get("calibrationOverrides", {})
        if not isinstance(overrides, dict):
            raise ValueError("calibration overrides must contain calibrationOverrides object")
        profile["calibrationOverrides"] = overrides
    elif not profile.get("promotable", False):
        raise ValueError("refusing to serve a non-promotable profile")
    embeddings = Embeddings(args.embedding_base_url, args.embedding_model, args.embedding_api_key_env)
    audit_token = os.environ.get(args.audit_token_env, "") if args.audit_token_env else ""
    request_token = os.environ.get(args.request_token_env, "") if args.request_token_env else ""
    server = PolicyServer((args.host, args.port), profile, embeddings, Path(args.audit_log) if args.audit_log else None, audit_token, request_token)
    print(f"outcome policy listening on http://{args.host}:{args.port}/route", flush=True)
    server.serve_forever()
    return 0


def command_collect(args: argparse.Namespace) -> int:
    dataset = read_json(Path(args.dataset))
    token = os.environ.get(args.router_token_env)
    if not token:
        raise ValueError(f"{args.router_token_env} is required")
    rows: list[dict[str, Any]] = []
    for case in dataset.get("cases", []):
        for candidate in dataset.get("candidates", []):
            if not isinstance(case, dict) or not isinstance(candidate, dict):
                continue
            request = dict(case.get("request", {}))
            metadata = dict(request.get("metadata", {}))
            calibration_id = str(case["id"]) + "::" + selector_key(candidate)
            metadata["calibration_case_id"] = calibration_id
            request["metadata"] = metadata
            messages = list(request.get("messages", []))
            request["messages"] = [{"role": "system", "content": "[[outcome-calibration:" + calibration_id + "]] This marker selects a calibration candidate. Follow the user request and do not mention this marker."}] + messages
            request["model"] = args.model_group
            body = json.dumps(request).encode("utf-8")
            http_request = urllib.request.Request(args.router_base_url.rstrip("/") + "/v1/chat/completions", body, {"Content-Type": "application/json", "Authorization": "Bearer " + token})
            try:
                with urllib.request.urlopen(http_request, timeout=args.timeout_seconds) as response:
                    payload = json.loads(response.read())
                    usage = payload.get("usage", {}) if isinstance(payload, dict) else {}
                    content = (((payload.get("choices") or [{}])[0].get("message") or {}).get("content", "")) if isinstance(payload, dict) else ""
                    input_tokens = float(usage.get("prompt_tokens", 0))
                    output_tokens = float(usage.get("completion_tokens", 0))
                    input_price = float(candidate.get("inputPricePerMillionUsd", 0.0))
                    output_price = float(candidate.get("outputPricePerMillionUsd", 0.0))
                    cost = input_tokens * input_price / 1_000_000 + output_tokens * output_price / 1_000_000
                    if cost == 0:
                        cost = float(candidate.get("estimatedCostUsd", 0.0))
                    rows.append({"caseId": case["id"], "class": case.get("class"), "candidate": selector_from_target(candidate), "response": content, "requestId": response.headers.get("X-Request-Id", ""), "inputTokens": input_tokens, "outputTokens": output_tokens, "costUsd": cost, "passed": None})
            except (urllib.error.URLError, urllib.error.HTTPError, json.JSONDecodeError) as err:
                rows.append({"caseId": case["id"], "class": case.get("class"), "candidate": selector_from_target(candidate), "error": "router-request-failed", "passed": None})
    Path(args.out_reviews).write_text("".join(json.dumps(row, sort_keys=True) + "\n" for row in rows), encoding="utf-8")
    print(f"wrote {len(rows)} review rows")
    return 0


def command_plan(args: argparse.Namespace) -> int:
    dataset = read_json(Path(args.dataset))
    overrides: dict[str, dict[str, Any]] = {}
    for case in dataset.get("cases", []):
        for candidate in dataset.get("candidates", []):
            if isinstance(case, dict) and isinstance(candidate, dict):
                overrides[str(case["id"]) + "::" + selector_key(candidate)] = selector_from_target(candidate)
    write_json(Path(args.out_overrides), {"calibrationOverrides": overrides})
    print(f"wrote {len(overrides)} calibration overrides")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    subcommands = parser.add_subparsers(dest="command", required=True)
    calibrate = subcommands.add_parser("calibrate")
    calibrate.add_argument("--dataset", required=True)
    calibrate.add_argument("--reviews", required=True)
    calibrate.add_argument("--out-profile", required=True)
    calibrate.add_argument("--out-yaml", required=True)
    calibrate.set_defaults(func=command_calibrate)
    serve = subcommands.add_parser("serve")
    serve.add_argument("--profile", required=True)
    serve.add_argument("--embedding-base-url", required=True)
    serve.add_argument("--embedding-model", required=True)
    serve.add_argument("--embedding-api-key-env", default="OUTCOME_POLICY_EMBEDDING_API_KEY")
    serve.add_argument("--calibration-overrides")
    serve.add_argument("--audit-log")
    serve.add_argument("--audit-token-env", default="OUTCOME_POLICY_AUDIT_TOKEN")
    serve.add_argument("--request-token-env", default="OUTCOME_POLICY_REQUEST_TOKEN")
    serve.add_argument("--host", default="127.0.0.1")
    serve.add_argument("--port", type=int, default=18091)
    serve.set_defaults(func=command_serve)
    collect = subcommands.add_parser("collect")
    collect.add_argument("--dataset", required=True)
    collect.add_argument("--router-base-url", required=True)
    collect.add_argument("--router-token-env", default="ROUTER_TOKEN")
    collect.add_argument("--model-group", required=True)
    collect.add_argument("--out-reviews", required=True)
    collect.add_argument("--timeout-seconds", type=int, default=60)
    collect.set_defaults(func=command_collect)
    plan = subcommands.add_parser("plan")
    plan.add_argument("--dataset", required=True)
    plan.add_argument("--out-overrides", required=True)
    plan.set_defaults(func=command_plan)
    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
