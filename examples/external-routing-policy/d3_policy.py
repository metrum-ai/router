#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""vLLM Semantic Router Decision 3.0 (d3) external routing policy for Metrum AI Router.

The router POSTs an external-policy request (`strategy: external`). This
service asks a d3 System One endpoint (`POST /v1/systemone`) a small set of
typed questions about the redacted request text, then maps the answers onto an
eligible target with a declarative rule list. Metrum AI Router still owns
caller authorization, eligibility, fallback and telemetry: the policy can only
choose among the `targets` the router already sent.

Behaviour (see docs-site/docs/routing/d3-external-policy.md):

- Needs `external_policy.include_request: true` to classify content. The
  router's `text` field is left-truncated to `text.max_chars` (default 32,768)
  so the most recent turns are kept.
- Without request text it never calls d3: `no_content_rules` decide from the
  scalar `context`, and `classLabel` is `d3:no-request-content`.
- If d3 fails, times out, answers malformed JSON or rejects the input, the
  service returns HTTP 502 so the router's `on_error` decides. It never
  guesses a target.
- `classLabel` is built only from operator-configured answer keys and a
  probability, for example `d3:hard:code:r0.71`. Request text never appears in
  labels, metadata or logs.
- `d3_server.py` serves one request at a time, so concurrent d3 calls are
  capped by `d3.max_concurrency` (default 1). A request that cannot get a slot
  within the d3 timeout fails as `policy_busy`.

Stdlib only. YAML configs additionally need PyYAML
(`uv run --with pyyaml python d3_policy.py --config d3_policy.example.yaml`);
JSON configs need nothing extra.

Copyright 2026 Metrum AI. Part of the Metrum AI Router external-policy examples.
"""
from __future__ import annotations

import argparse
import copy
import json
import math
import os
import re
import socket
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

SAFE_PART_RE = re.compile(r"[^A-Za-z0-9_.-]+")
SAFE_LABEL_RE = re.compile(r"^[A-Za-z0-9_.:-]{1,64}$")
NO_CONTENT_LABEL = "d3:no-request-content"
MAX_LABEL_CHARS = 64
MAX_D3_RESPONSE_BYTES = 1 << 20
SELECTOR_KEYS = ("tier", "provider", "model", "modelRef")
CONDITION_OPS = ("in", "not_in", "gte", "lte", "gt", "lt", "eq")

# Defaults follow the plan in issue #278. Operators override them with --config.
DEFAULT_CONFIG: dict[str, Any] = {
    "d3": {
        "url": "http://127.0.0.1:8000",
        "model": "vllm-sr/d3-lite",
        "revision": "a8d25a91ef75552803f7009d25b7bd435991abd9",
        "timeout_ms": 250,
        "api_key_env": "D3_API_KEY",
        "max_concurrency": 1,
    },
    "text": {"max_chars": 32768},
    "questions": {
        "difficulty": {
            "type": "score",
            "instructions": "How difficult is it to answer this request well?",
            "criteria": ["trivial", "moderate", "hard", "expert"],
        },
        "reasoning": {
            "type": "noul",
            "instructions": "Does answering this need multi-step reasoning?",
        },
        "kind": {
            "type": "choice",
            "instructions": "What kind of work is this?",
            "criteria": {
                "code": "Writing, reviewing or fixing code",
                "math": "Mathematics or quantitative reasoning",
                "chat": "Conversation, writing or anything else",
                "agentic": "Multi-step agentic tool use such as editing files or running commands",
            },
        },
    },
    # Label parts, in order. choice/score -> answer key, noul -> <first letter><p>.
    "label": ["difficulty", "kind", "reasoning"],
    # First matching rule wins. A rule whose preferred targets are all missing
    # falls through to the next matching rule.
    "rules": [
        {
            "name": "strong-hard-or-reasoning",
            "when": {
                "any": [
                    {"question": "difficulty", "in": ["hard", "expert"]},
                    {"question": "reasoning", "gte": 0.6},
                ]
            },
            "prefer": [{"tier": "strong"}],
        },
        {"name": "cheap-default", "prefer": [{"tier": "cheap"}]},
    ],
    # Used only when the policy request has no text (include_request: false).
    "no_content_rules": [
        {
            "name": "strong-large-context",
            "when": {"context": "estimatedTokens", "gte": 8000},
            "prefer": [{"tier": "strong"}],
        },
        {"name": "cheap-default", "prefer": [{"tier": "cheap"}]},
    ],
}


class ConfigError(ValueError):
    """The policy configuration is invalid. Raised at startup only."""


class D3Error(Exception):
    """A d3 call failed. `kind` is a fixed scalar class, never request text."""

    def __init__(self, kind: str) -> None:
        super().__init__(kind)
        self.kind = kind


# --------------------------------------------------------------------------
# Configuration
# --------------------------------------------------------------------------


def _merge(base: dict[str, Any], override: dict[str, Any]) -> dict[str, Any]:
    out = copy.deepcopy(base)
    for key, value in override.items():
        if isinstance(value, dict) and isinstance(out.get(key), dict) and key not in (
            "questions",
        ):
            out[key] = _merge(out[key], value)
        else:
            out[key] = copy.deepcopy(value)
    return out


def load_config_file(path: Path) -> dict[str, Any]:
    raw = path.read_text(encoding="utf-8")
    if path.suffix.lower() in (".yaml", ".yml"):
        try:
            import yaml  # type: ignore
        except ImportError as exc:  # pragma: no cover - depends on environment
            raise ConfigError("YAML config needs PyYAML; use a .json config or uv run --with pyyaml") from exc
        data = yaml.safe_load(raw) or {}
    else:
        data = json.loads(raw)
    if not isinstance(data, dict):
        raise ConfigError("config must be an object")
    return data


def _validate_condition(cond: Any, questions: dict[str, Any], where: str) -> None:
    if not isinstance(cond, dict):
        raise ConfigError(f"{where}: condition must be an object")
    for group in ("any", "all"):
        if group in cond:
            items = cond[group]
            if not isinstance(items, list) or not items:
                raise ConfigError(f"{where}: {group} must be a non-empty list")
            for i, item in enumerate(items):
                _validate_condition(item, questions, f"{where}.{group}[{i}]")
            return
    if "not" in cond:
        _validate_condition(cond["not"], questions, f"{where}.not")
        return
    if ("question" in cond) == ("context" in cond):
        raise ConfigError(f"{where}: name exactly one of question or context")
    if "question" in cond and cond["question"] not in questions:
        raise ConfigError(f"{where}: unknown question {cond['question']!r}")
    ops = [op for op in CONDITION_OPS if op in cond]
    if len(ops) != 1:
        raise ConfigError(f"{where}: use exactly one of {', '.join(CONDITION_OPS)}")
    op = ops[0]
    if op in ("in", "not_in") and not isinstance(cond[op], list):
        raise ConfigError(f"{where}: {op} needs a list")
    if op in ("gte", "lte", "gt", "lt") and not isinstance(cond[op], (int, float)):
        raise ConfigError(f"{where}: {op} needs a number")
    if "question" in cond:
        q = questions[cond["question"]]
        if q["type"] in ("choice", "score") and op in ("in", "not_in", "eq"):
            allowed = set(_answer_keys(q))
            values = cond[op] if isinstance(cond[op], list) else [cond[op]]
            unknown = [v for v in values if v not in allowed]
            if unknown:
                raise ConfigError(f"{where}: {unknown} not in {cond['question']} options")
        if q["type"] == "noul" and op in ("in", "not_in", "eq"):
            raise ConfigError(f"{where}: noul answers are probabilities; use gte/lte/gt/lt")


def _answer_keys(question: dict[str, Any]) -> list[str]:
    if question["type"] == "choice":
        return list(question["criteria"])
    if question["type"] == "score":
        return [str(level) for level in question["criteria"]]
    return []


def validate_config(cfg: dict[str, Any]) -> dict[str, Any]:
    d3 = cfg.get("d3") or {}
    parsed = urlparse(str(d3.get("url") or ""))
    if parsed.scheme not in ("http", "https") or not parsed.hostname:
        raise ConfigError("d3.url must be an http(s) URL")
    timeout_ms = d3.get("timeout_ms")
    if not isinstance(timeout_ms, (int, float)) or not 1 <= timeout_ms <= 60000:
        raise ConfigError("d3.timeout_ms must be 1..60000")
    conc = d3.get("max_concurrency")
    if not isinstance(conc, int) or not 1 <= conc <= 256:
        raise ConfigError("d3.max_concurrency must be 1..256")
    max_chars = (cfg.get("text") or {}).get("max_chars")
    if not isinstance(max_chars, int) or max_chars < 1:
        raise ConfigError("text.max_chars must be a positive integer")

    questions = cfg.get("questions")
    if not isinstance(questions, dict) or not questions:
        raise ConfigError("questions must be a non-empty object")
    for name, q in questions.items():
        if not isinstance(q, dict) or q.get("type") not in ("choice", "noul", "score"):
            raise ConfigError(f"questions.{name}: type must be choice, noul or score")
        if SAFE_PART_RE.search(name):
            raise ConfigError(f"questions.{name}: name must match [A-Za-z0-9_.-]")
        if q["type"] == "choice":
            crit = q.get("criteria")
            if not isinstance(crit, dict) or not 2 <= len(crit) <= 255:
                raise ConfigError(f"questions.{name}: choice criteria needs 2..255 options")
        if q["type"] == "score":
            crit = q.get("criteria")
            if not isinstance(crit, list) or not 2 <= len(crit) <= 10:
                raise ConfigError(f"questions.{name}: score criteria needs 2..10 levels")
            if len({str(c) for c in crit}) != len(crit):
                raise ConfigError(f"questions.{name}: score levels must be distinct")
        for key in _answer_keys(q):
            if SAFE_PART_RE.search(key):
                raise ConfigError(f"questions.{name}: option {key!r} must match [A-Za-z0-9_.-]")

    for name in cfg.get("label") or []:
        if name not in questions:
            raise ConfigError(f"label: unknown question {name!r}")

    for section in ("rules", "no_content_rules"):
        rules = cfg.get(section)
        if not isinstance(rules, list) or not rules:
            raise ConfigError(f"{section} must be a non-empty list")
        for i, rule in enumerate(rules):
            where = f"{section}[{i}]"
            if not isinstance(rule, dict):
                raise ConfigError(f"{where}: rule must be an object")
            if "when" in rule:
                _validate_condition(rule["when"], questions, where + ".when")
            prefer = rule.get("prefer")
            if not isinstance(prefer, list) or not prefer:
                raise ConfigError(f"{where}: prefer must be a non-empty list of selectors")
            for sel in prefer:
                if not isinstance(sel, dict) or not sel or set(sel) - set(SELECTOR_KEYS):
                    raise ConfigError(f"{where}: selectors use only {', '.join(SELECTOR_KEYS)}")
            if section == "no_content_rules" and _uses_question(rule.get("when")):
                raise ConfigError(f"{where}: no_content_rules can only test context fields")
    return cfg


def _uses_question(cond: Any) -> bool:
    if not isinstance(cond, dict):
        return False
    if "question" in cond:
        return True
    return any(_uses_question(c) for key in ("any", "all") for c in cond.get(key) or []) or _uses_question(
        cond.get("not")
    )


def build_config(args: argparse.Namespace) -> dict[str, Any]:
    cfg = copy.deepcopy(DEFAULT_CONFIG)
    if args.config:
        cfg = _merge(cfg, load_config_file(Path(args.config)))
    d3 = cfg["d3"]
    for flag, key in (
        ("url", "url"),
        ("model", "model"),
        ("revision", "revision"),
        ("timeout_ms", "timeout_ms"),
        ("api_key_env", "api_key_env"),
        ("max_concurrency", "max_concurrency"),
    ):
        value = getattr(args, flag, None)
        if value is not None:
            d3[key] = value
    if getattr(args, "max_chars", None) is not None:
        cfg["text"]["max_chars"] = args.max_chars
    return validate_config(cfg)


# --------------------------------------------------------------------------
# d3 client and answer parsing
# --------------------------------------------------------------------------


def d3_questions(cfg: dict[str, Any]) -> dict[str, Any]:
    out: dict[str, Any] = {}
    for name, q in cfg["questions"].items():
        item: dict[str, Any] = {"type": q["type"]}
        if q.get("instructions"):
            item["instructions"] = q["instructions"]
        if "criteria" in q:
            item["criteria"] = q["criteria"]
        out[name] = item
    return out


def _finite_prob(value: Any) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise D3Error("d3_malformed")
    v = float(value)
    if not math.isfinite(v) or v < -1e-6 or v > 1 + 1e-6:
        raise D3Error("d3_malformed")
    return min(max(v, 0.0), 1.0)


def parse_answers(cfg: dict[str, Any], body: Any) -> dict[str, dict[str, Any]]:
    """Validate a d3 System One response. Returns normalized answers per question.

    choice -> {"type", "value": key, "p": probability of key}
    score  -> {"type", "value": level (argmax; ties go to the higher level),
               "index": int, "expected": float}
    noul   -> {"type", "p": P(yes)}
    """
    if not isinstance(body, dict) or not isinstance(body.get("answers"), dict):
        raise D3Error("d3_malformed")
    answers = body["answers"]
    out: dict[str, dict[str, Any]] = {}
    for name, q in cfg["questions"].items():
        ans = answers.get(name)
        if not isinstance(ans, dict):
            raise D3Error("d3_malformed")
        if "error" in ans:
            raise D3Error("d3_question_error")
        kind = q["type"]
        if ans.get("type") not in (None, kind):
            raise D3Error("d3_malformed")
        if kind == "noul":
            out[name] = {"type": kind, "p": _finite_prob(ans.get("noul"))}
        elif kind == "choice":
            keys = list(q["criteria"])
            probs = ans.get("probabilities")
            if not isinstance(probs, dict) or set(probs) != set(keys):
                raise D3Error("d3_malformed")
            values = {k: _finite_prob(probs[k]) for k in keys}
            choice = ans.get("choice")
            if choice not in values:
                raise D3Error("d3_malformed")
            out[name] = {"type": kind, "value": choice, "p": values[choice]}
        else:
            levels = [str(level) for level in q["criteria"]]
            probs = ans.get("probabilities")
            index_keys = [str(i) for i in range(len(levels))]
            # d3_server keys score probabilities by level index; accept level names too.
            if isinstance(probs, dict) and set(probs) == set(index_keys):
                values = [_finite_prob(probs[k]) for k in index_keys]
            elif isinstance(probs, dict) and set(probs) == set(levels):
                values = [_finite_prob(probs[k]) for k in levels]
            else:
                raise D3Error("d3_malformed")
            best = max(values)
            index = max(i for i, v in enumerate(values) if v == best)
            expected = ans.get("score")
            if isinstance(expected, bool) or not isinstance(expected, (int, float)) or not math.isfinite(
                float(expected)
            ):
                raise D3Error("d3_malformed")
            out[name] = {
                "type": kind,
                "value": levels[index],
                "index": index,
                "expected": float(expected),
            }
    return out


class D3Client:
    def __init__(self, cfg: dict[str, Any]) -> None:
        d3 = cfg["d3"]
        self.url = str(d3["url"]).rstrip("/") + "/v1/systemone"
        self.model = str(d3.get("model") or "")
        self.timeout_s = float(d3["timeout_ms"]) / 1000.0
        env_name = str(d3.get("api_key_env") or "")
        self._api_key = os.environ.get(env_name, "") if env_name else ""
        self._slots = threading.BoundedSemaphore(int(d3["max_concurrency"]))
        self._questions = d3_questions(cfg)
        # Only loopback/tunnel use is expected; never route d3 traffic through a proxy.
        self._opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def ask(self, state: str) -> tuple[dict[str, Any], float]:
        deadline = time.monotonic() + self.timeout_s
        if not self._slots.acquire(timeout=self.timeout_s):
            raise D3Error("policy_busy")
        try:
            remaining = deadline - time.monotonic()
            if remaining <= 0.005:
                raise D3Error("policy_busy")
            return self._post(state, remaining)
        finally:
            self._slots.release()

    def _post(self, state: str, timeout_s: float) -> tuple[dict[str, Any], float]:
        body = {"state": state, "questions": self._questions}
        if self.model:
            body["model"] = self.model
        headers = {"Content-Type": "application/json", "Accept": "application/json"}
        if self._api_key:
            headers["Authorization"] = "Bearer " + self._api_key
        req = urllib.request.Request(self.url, data=json.dumps(body).encode("utf-8"), headers=headers, method="POST")
        started = time.perf_counter()
        try:
            with self._opener.open(req, timeout=timeout_s) as resp:
                raw = resp.read(MAX_D3_RESPONSE_BYTES + 1)
        except urllib.error.HTTPError as err:
            err.close()
            if err.code == 422:
                raise D3Error("d3_http_422") from None
            raise D3Error("d3_http_4xx" if 400 <= err.code < 500 else "d3_http_5xx") from None
        except (socket.timeout, TimeoutError):
            raise D3Error("d3_timeout") from None
        except urllib.error.URLError as err:
            if isinstance(err.reason, (socket.timeout, TimeoutError)):
                raise D3Error("d3_timeout") from None
            raise D3Error("d3_unreachable") from None
        except OSError:
            raise D3Error("d3_unreachable") from None
        elapsed_ms = (time.perf_counter() - started) * 1000.0
        if len(raw) > MAX_D3_RESPONSE_BYTES:
            raise D3Error("d3_malformed")
        try:
            parsed = json.loads(raw.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise D3Error("d3_malformed") from None
        return parsed, elapsed_ms


# --------------------------------------------------------------------------
# Decision
# --------------------------------------------------------------------------


def request_text(payload: dict[str, Any], max_chars: int) -> str:
    """The router's redacted `text`, left-truncated so the latest turns stay."""
    text = payload.get("text")
    if not isinstance(text, str):
        return ""
    text = text.strip()
    if len(text) > max_chars:
        text = text[-max_chars:]
    return text


def _cond_value(cond: dict[str, Any], answers: dict[str, Any], context: dict[str, Any]) -> Any:
    if "context" in cond:
        return context.get(cond["context"])
    ans = answers.get(cond["question"])
    if ans is None:
        return None
    op = next(op for op in CONDITION_OPS if op in cond)
    if ans["type"] == "noul":
        return ans["p"]
    if ans["type"] == "score" and op in ("gte", "lte", "gt", "lt"):
        return ans["expected"]
    if ans["type"] == "choice" and op in ("gte", "lte", "gt", "lt"):
        return ans["p"]
    return ans["value"]


def matches(cond: Any, answers: dict[str, Any], context: dict[str, Any]) -> bool:
    if cond is None:
        return True
    if "any" in cond:
        return any(matches(c, answers, context) for c in cond["any"])
    if "all" in cond:
        return all(matches(c, answers, context) for c in cond["all"])
    if "not" in cond:
        return not matches(cond["not"], answers, context)
    value = _cond_value(cond, answers, context)
    if value is None:
        return False
    op = next(op for op in CONDITION_OPS if op in cond)
    want = cond[op]
    if op == "in":
        return value in want
    if op == "not_in":
        return value not in want
    if op == "eq":
        return value == want
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return False
    if op == "gte":
        return value >= want
    if op == "lte":
        return value <= want
    if op == "gt":
        return value > want
    return value < want


def _selector_matches(sel: dict[str, Any], target: dict[str, Any]) -> bool:
    for key, want in sel.items():
        have = str(target.get(key) or "")
        if have.lower() != str(want).lower():
            return False
    return True


def choose_target(
    rules: list[dict[str, Any]], answers: dict[str, Any], context: dict[str, Any], targets: list[dict[str, Any]]
) -> tuple[int, list[int], str]:
    """Return (targetIndex, fallbackIndexes, rule name) over eligible targets."""
    candidates = [i for i, t in enumerate(targets) if isinstance(t, dict) and t.get("keyConfigured", True)]
    if not candidates:
        candidates = list(range(len(targets)))
    if not candidates:
        raise ValueError("no targets")
    for rule in rules:
        if not matches(rule.get("when"), answers, context):
            continue
        preferred: list[int] = []
        for sel in rule["prefer"]:
            for i in candidates:
                if i not in preferred and _selector_matches(sel, targets[i]):
                    preferred.append(i)
        if not preferred:
            continue  # preferred tier/provider missing for this group: try the next rule
        chosen = preferred[0]
        fallbacks = preferred[1:] + [i for i in candidates if i not in preferred]
        return chosen, fallbacks, str(rule.get("name") or "rule")
    return candidates[0], candidates[1:], "no-preferred-target"


def build_label(cfg: dict[str, Any], answers: dict[str, Any]) -> str:
    parts = ["d3"]
    for name in cfg.get("label") or []:
        ans = answers.get(name)
        if ans is None:
            continue
        if ans["type"] == "noul":
            parts.append(f"{name[:1]}{ans['p']:.2f}")
        else:
            parts.append(str(ans["value"]))
    label = ":".join(SAFE_PART_RE.sub("_", p) for p in parts)[:MAX_LABEL_CHARS].rstrip(":")
    return label if SAFE_LABEL_RE.match(label) else "d3:unsafe"


class Policy:
    def __init__(self, cfg: dict[str, Any], client: D3Client | None = None, log: Any = None) -> None:
        self.cfg = cfg
        self.client = client or D3Client(cfg)
        self.log = log
        self.lock = threading.Lock()
        self.counters = {"routes": 0, "d3_calls": 0, "errors": 0, "no_content": 0}

    def _emit(self, record: dict[str, Any]) -> None:
        if self.log is None:
            return
        # Scalar fields only: never request text, prompts, keys or URLs with credentials.
        self.log.write(json.dumps(record, sort_keys=True) + "\n")
        self.log.flush()

    def _count(self, key: str) -> None:
        with self.lock:
            self.counters[key] += 1

    def route(self, payload: dict[str, Any]) -> dict[str, Any]:
        self._count("routes")
        targets = payload.get("targets")
        if not isinstance(targets, list) or not targets:
            raise D3Error("no_targets")
        context = payload.get("context") if isinstance(payload.get("context"), dict) else {}
        state = request_text(payload, int(self.cfg["text"]["max_chars"]))
        if not state:
            self._count("no_content")
            idx, fallbacks, rule = choose_target(self.cfg["no_content_rules"], {}, context, targets)
            self._emit({"event": "route", "outcome": "no_content", "rule": rule, "targetIndex": idx,
                        "classLabel": NO_CONTENT_LABEL})
            return {"targetIndex": idx, "fallbackIndexes": fallbacks, "classLabel": NO_CONTENT_LABEL,
                    "metadata": {"rule": rule, "d3": False}}
        self._count("d3_calls")
        body, d3_ms = self.client.ask(state)
        answers = parse_answers(self.cfg, body)
        idx, fallbacks, rule = choose_target(self.cfg["rules"], answers, context, targets)
        label = build_label(self.cfg, answers)
        metadata: dict[str, Any] = {"rule": rule, "d3": True, "d3Ms": round(d3_ms, 1),
                                    "d3Model": self.client.model}
        revision = str(self.cfg["d3"].get("revision") or "")
        if revision:
            metadata["d3Revision"] = revision[:12]
        self._emit({"event": "route", "outcome": "selected", "rule": rule, "targetIndex": idx,
                    "classLabel": label, "d3Ms": round(d3_ms, 1), "stateChars": len(state)})
        return {"targetIndex": idx, "fallbackIndexes": fallbacks, "classLabel": label, "metadata": metadata}

    def error(self, kind: str) -> None:
        self._count("errors")
        self._emit({"event": "route", "outcome": "error", "errorClass": kind})


# --------------------------------------------------------------------------
# HTTP service
# --------------------------------------------------------------------------


class Handler(BaseHTTPRequestHandler):
    server_version = "metrum-d3-policy/1.0"
    policy: Policy
    max_body_bytes = 16 * 1024 * 1024

    def log_message(self, *_args: object) -> None:
        return

    def _json(self, code: int, body: dict[str, Any]) -> None:
        raw = json.dumps(body).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self) -> None:  # noqa: N802
        if urlparse(self.path).path != "/health":
            return self._json(404, {"error": "not found"})
        with self.policy.lock:
            counters = dict(self.policy.counters)
        d3 = self.policy.cfg["d3"]
        return self._json(200, {"ok": True, "model": d3.get("model"), "revision": d3.get("revision"),
                                "maxConcurrency": d3["max_concurrency"], **counters})

    def do_POST(self) -> None:  # noqa: N802
        if urlparse(self.path).path != "/route":
            return self._json(404, {"error": "not found"})
        try:
            length = int(self.headers.get("content-length", "0"))
            if length < 0 or length > self.max_body_bytes:
                raise D3Error("bad_policy_request")
            payload = json.loads(self.rfile.read(length) or b"{}")
            if not isinstance(payload, dict):
                raise D3Error("bad_policy_request")
            decision = self.policy.route(payload)
        except D3Error as err:
            self.policy.error(err.kind)
            # Non-2xx: the router applies external_policy.on_error (fail_closed or fallback).
            return self._json(502, {"error": "d3-policy-error", "class": err.kind})
        except (ValueError, KeyError, TypeError):
            self.policy.error("policy_internal_error")
            return self._json(502, {"error": "d3-policy-error", "class": "policy_internal_error"})
        return self._json(200, decision)


def serve(cfg: dict[str, Any], host: str, port: int, log: Any = sys.stderr) -> ThreadingHTTPServer:
    handler = type("BoundHandler", (Handler,), {"policy": Policy(cfg, log=log)})
    server = ThreadingHTTPServer((host, port), handler)
    server.daemon_threads = True
    return server


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--config", help="JSON or YAML policy config (defaults are built in)")
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=18094)
    ap.add_argument("--url", help="d3 base URL, e.g. http://127.0.0.1:18300 (an SSH tunnel)")
    ap.add_argument("--model", help="d3 model name sent in the request, e.g. vllm-sr/d3-lite")
    ap.add_argument("--revision", help="pinned Hub revision of the served d3 model (recorded only)")
    ap.add_argument("--timeout-ms", dest="timeout_ms", type=int, help="d3 call timeout incl. queueing")
    ap.add_argument("--api-key-env", dest="api_key_env",
                    help="environment variable holding the d3 bearer key (DECISION_API_KEY on the server)")
    ap.add_argument("--max-concurrency", dest="max_concurrency", type=int,
                    help="concurrent d3 calls (d3_server.py serves one at a time)")
    ap.add_argument("--max-chars", dest="max_chars", type=int, help="left-truncation limit for request text")
    ap.add_argument("--quiet", action="store_true", help="do not write scalar decision logs to stderr")
    return ap.parse_args(argv)


def main(argv: list[str] | None = None) -> None:
    args = parse_args(argv)
    try:
        cfg = build_config(args)
    except (ConfigError, OSError, json.JSONDecodeError) as exc:
        print(f"d3 policy config error: {exc}", file=sys.stderr)
        raise SystemExit(2) from None
    server = serve(cfg, args.host, args.port, log=None if args.quiet else sys.stderr)
    d3 = cfg["d3"]
    print(
        json.dumps({"event": "listening", "route": f"http://{args.host}:{server.server_port}/route",
                    "d3Model": d3.get("model"), "d3Revision": d3.get("revision"),
                    "timeoutMs": d3["timeout_ms"], "maxConcurrency": d3["max_concurrency"]}),
        file=sys.stderr, flush=True,
    )
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
