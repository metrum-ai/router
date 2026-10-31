#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Credential-free unit tests for the d3 external routing policy (issue #278).

Runs the adapter against the scripted fake d3 (`fake_d3.py`) over loopback.
Stdlib unittest only; no GPU, provider network or credentials.

Copyright 2026 Metrum AI.
"""
from __future__ import annotations

import copy
import importlib.util
import io
import json
import sys
import threading
import unittest
from unittest import mock
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
EXAMPLES = ROOT / "examples" / "external-routing-policy"


def _load(name: str) -> Any:
    spec = importlib.util.spec_from_file_location(name, EXAMPLES / f"{name}.py")
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


d3p = _load("d3_policy")
fake = _load("fake_d3")

SECRET_PROMPT = "Prove the lock-free queue invariant for customer ZEBRA-7731 under concurrent dequeue."
EASY_PROMPT = "Say hello to WOMBAT-4410 in one short sentence."
TARGETS = [
    {"provider": "fireworks", "model": "deepseek-v4p1-flash", "tier": "cheap", "keyConfigured": True, "weight": 1},
    {"provider": "openai", "model": "gpt-6-luna", "tier": "strong", "keyConfigured": True, "weight": 1},
]


def cfg_for(url: str, **d3: Any) -> dict[str, Any]:
    cfg = copy.deepcopy(d3p.DEFAULT_CONFIG)
    cfg["d3"]["url"] = url
    cfg["d3"].update(d3)
    return d3p.validate_config(cfg)


def answers_body(difficulty_probs: list[float], reasoning: float, kind: str = "code",
                 expected: float | None = None) -> dict[str, Any]:
    kinds = ["code", "math", "chat", "agentic"]
    return {
        "model": "fake",
        "answers": {
            "difficulty": {
                "type": "score",
                "score": expected if expected is not None else sum(i * p for i, p in enumerate(difficulty_probs)),
                "probabilities": {str(i): p for i, p in enumerate(difficulty_probs)},
            },
            "reasoning": {"type": "noul", "noul": reasoning},
            "kind": {"type": "choice", "choice": kind,
                     "probabilities": {k: (0.7 if k == kind else 0.1) for k in kinds}},
        },
    }


class StubClient:
    """Returns a scripted d3 body without network."""

    def __init__(self, body: Any) -> None:
        self.body = body
        self.model = "vllm-sr/d3-lite"
        self.states: list[str] = []

    def ask(self, state: str) -> tuple[Any, float]:
        self.states.append(state)
        return self.body, 1.0


class MappingTests(unittest.TestCase):
    def setUp(self) -> None:
        self.cfg = cfg_for("http://127.0.0.1:9")

    def decide(self, body: Any, targets: list[dict[str, Any]] | None = None,
               text: str = "x") -> dict[str, Any]:
        policy = d3p.Policy(self.cfg, client=StubClient(body))
        return policy.route({"text": text, "targets": targets or TARGETS, "context": {}})

    def test_easy_goes_cheap(self) -> None:
        out = self.decide(answers_body([0.7, 0.2, 0.05, 0.05], 0.2, "chat"))
        self.assertEqual(out["targetIndex"], 0)
        self.assertEqual(out["fallbackIndexes"], [1])
        self.assertEqual(out["classLabel"], "d3:trivial:chat:r0.20")
        self.assertEqual(out["metadata"]["rule"], "cheap-default")

    def test_hard_goes_strong(self) -> None:
        out = self.decide(answers_body([0.05, 0.15, 0.6, 0.2], 0.3, "code"))
        self.assertEqual(out["targetIndex"], 1)
        self.assertEqual(out["fallbackIndexes"], [0])
        self.assertEqual(out["classLabel"], "d3:hard:code:r0.30")

    def test_expert_goes_strong(self) -> None:
        out = self.decide(answers_body([0.0, 0.1, 0.2, 0.7], 0.1, "math"))
        self.assertEqual(out["targetIndex"], 1)

    def test_noul_threshold_edges(self) -> None:
        easy = [0.7, 0.2, 0.05, 0.05]
        self.assertEqual(self.decide(answers_body(easy, 0.6))["targetIndex"], 1)  # gte is inclusive
        self.assertEqual(self.decide(answers_body(easy, 0.5999))["targetIndex"], 0)
        self.assertEqual(self.decide(answers_body(easy, 1.0))["targetIndex"], 1)
        self.assertEqual(self.decide(answers_body(easy, 0.0))["targetIndex"], 0)

    def test_score_tie_goes_to_higher_level(self) -> None:
        out = self.decide(answers_body([0.0, 0.5, 0.5, 0.0], 0.1))
        self.assertTrue(out["classLabel"].startswith("d3:hard:"))
        self.assertEqual(out["targetIndex"], 1)

    def test_score_probabilities_by_level_name_accepted(self) -> None:
        body = answers_body([0.7, 0.2, 0.05, 0.05], 0.1)
        body["answers"]["difficulty"]["probabilities"] = {
            "trivial": 0.7, "moderate": 0.2, "hard": 0.05, "expert": 0.05}
        self.assertEqual(self.decide(body)["targetIndex"], 0)

    def test_missing_strong_tier_falls_through(self) -> None:
        targets = [TARGETS[0], {**TARGETS[1], "tier": "frontier"}]
        out = self.decide(answers_body([0.0, 0.1, 0.8, 0.1], 0.9), targets)
        self.assertEqual(out["targetIndex"], 0)
        self.assertEqual(out["metadata"]["rule"], "cheap-default")

    def test_no_matching_selector_uses_first_eligible(self) -> None:
        targets = [{**TARGETS[0], "tier": "a"}, {**TARGETS[1], "tier": "b"}]
        out = self.decide(answers_body([0.7, 0.2, 0.05, 0.05], 0.1), targets)
        self.assertEqual((out["targetIndex"], out["fallbackIndexes"]), (0, [1]))
        self.assertEqual(out["metadata"]["rule"], "no-preferred-target")

    def test_key_unconfigured_targets_skipped(self) -> None:
        targets = [TARGETS[0], {**TARGETS[1], "keyConfigured": False}, {**TARGETS[1], "model": "b"}]
        out = self.decide(answers_body([0.0, 0.1, 0.8, 0.1], 0.9), targets)
        self.assertEqual(out["targetIndex"], 2)
        self.assertNotIn(1, out["fallbackIndexes"])

    def test_target_order_independent(self) -> None:
        out = self.decide(answers_body([0.7, 0.2, 0.05, 0.05], 0.1), [TARGETS[1], TARGETS[0]])
        self.assertEqual(out["targetIndex"], 1)

    def test_malformed_answers_raise(self) -> None:
        bad_bodies = [
            {},
            {"answers": []},
            {"answers": {"difficulty": {}}},
            answers_body([0.7, 0.2, 0.05], 0.1),  # wrong level count
            answers_body([0.7, 0.2, 0.05, 0.05], 1.7),  # noul out of range
            answers_body([0.7, 0.2, 0.05, 0.05], float("nan")),
            answers_body([0.7, 0.2, 0.05, 0.05], True),
            answers_body([0.7, 0.2, 0.05, 0.05], 0.1, kind="poetry"),
        ]
        no_score = answers_body([0.7, 0.2, 0.05, 0.05], 0.1)
        del no_score["answers"]["difficulty"]["score"]
        bad_bodies.append(no_score)
        q_err = answers_body([0.7, 0.2, 0.05, 0.05], 0.1)
        q_err["answers"]["kind"] = {"type": "choice", "error": "max_length_exceeded", "message": "x"}
        for body in bad_bodies:
            with self.subTest(body=json.dumps(body)[:80]):
                with self.assertRaises(d3p.D3Error):
                    self.decide(body)
        with self.assertRaises(d3p.D3Error) as ctx:
            self.decide(q_err)
        self.assertEqual(ctx.exception.kind, "d3_question_error")

    def test_no_text_skips_d3(self) -> None:
        client = StubClient(None)
        policy = d3p.Policy(self.cfg, client=client)
        for payload in ({"targets": TARGETS, "context": {"estimatedTokens": 12}},
                        {"text": "   ", "targets": TARGETS, "context": {}}):
            out = policy.route(payload)
            self.assertEqual(out["classLabel"], "d3:no-request-content")
            self.assertEqual(out["targetIndex"], 0)
        big = policy.route({"targets": TARGETS, "context": {"estimatedTokens": 9000}})
        self.assertEqual((big["targetIndex"], big["classLabel"]), (1, "d3:no-request-content"))
        self.assertEqual(client.states, [])

    def test_left_truncation_keeps_tail(self) -> None:
        cfg = copy.deepcopy(self.cfg)
        cfg["text"]["max_chars"] = 10
        client = StubClient(answers_body([0.7, 0.2, 0.05, 0.05], 0.1))
        d3p.Policy(cfg, client=client).route({"text": "HEAD-" + "x" * 20 + "TAIL", "targets": TARGETS})
        self.assertEqual(client.states, ["x" * 6 + "TAIL"])

    def test_label_is_bounded_and_safe(self) -> None:
        cfg = copy.deepcopy(self.cfg)
        long_key = "k" * 70
        cfg["questions"]["kind"]["criteria"] = {long_key: None, "other": None}
        cfg = d3p.validate_config(cfg)
        label = d3p.build_label(cfg, {"kind": {"type": "choice", "value": long_key, "p": 0.9}})
        self.assertLessEqual(len(label), 64)
        self.assertRegex(label, r"^[A-Za-z0-9_.:-]{1,64}$")

    def test_custom_rules_by_provider_and_choice(self) -> None:
        cfg = copy.deepcopy(self.cfg)
        cfg["rules"] = [
            {"name": "agentic-openai", "when": {"question": "kind", "in": ["agentic"]},
             "prefer": [{"provider": "openai"}]},
            {"name": "rest", "prefer": [{"tier": "cheap"}]},
        ]
        cfg = d3p.validate_config(cfg)
        policy = d3p.Policy(cfg, client=StubClient(answers_body([0.7, 0.2, 0.05, 0.05], 0.1, "agentic")))
        self.assertEqual(policy.route({"text": "x", "targets": TARGETS})["targetIndex"], 1)

    def test_config_validation(self) -> None:
        bad = [
            lambda c: c["d3"].update(url="ftp://x"),
            lambda c: c["d3"].update(timeout_ms=0),
            lambda c: c["d3"].update(max_concurrency=0),
            lambda c: c["rules"][0]["when"]["any"].append({"question": "nope", "gte": 1}),
            lambda c: c["rules"][0]["when"]["any"].append({"question": "difficulty", "in": ["legendary"]}),
            lambda c: c["rules"][0]["when"]["any"].append({"question": "reasoning", "in": ["yes"]}),
            lambda c: c["rules"][0].update(prefer=[{"color": "red"}]),
            lambda c: c["no_content_rules"][0].update(when={"question": "kind", "in": ["code"]}),
            lambda c: c.update(label=["nope"]),
            lambda c: c["questions"]["kind"]["criteria"].update({"bad key!": None}),
        ]
        for mutate in bad:
            cfg = copy.deepcopy(d3p.DEFAULT_CONFIG)
            mutate(cfg)
            with self.assertRaises(d3p.ConfigError):
                d3p.validate_config(cfg)

    def test_example_yaml_config_is_valid(self) -> None:
        try:
            import yaml  # type: ignore  # noqa: F401
        except ImportError:
            self.skipTest("PyYAML not installed; JSON example covers the same config")
        path = EXAMPLES / "d3_policy.example.yaml"
        cfg = d3p.validate_config(d3p._merge(d3p.DEFAULT_CONFIG, d3p.load_config_file(path)))
        self.assertEqual(cfg["d3"]["model"], "vllm-sr/d3-lite")

    def test_example_json_config_matches_defaults(self) -> None:
        loaded = d3p.load_config_file(EXAMPLES / "d3_policy.example.json")
        merged = d3p.validate_config(d3p._merge(d3p.DEFAULT_CONFIG, loaded))
        self.assertEqual(merged["questions"], d3p.DEFAULT_CONFIG["questions"])
        self.assertEqual(merged["rules"], d3p.DEFAULT_CONFIG["rules"])


class FakeClassifierTests(unittest.TestCase):
    """The fake d3 keyword heuristic must stay linear on adversarial text."""

    KIND = {"type": "choice", "criteria": {"code": None, "math": None, "chat": None}}

    def kind_of(self, text: str) -> str:
        return fake.answer(self.KIND, fake.classify(text))["choice"]

    def test_adversarial_inputs_have_bounded_scan_work(self) -> None:
        # Guard the audited patterns, including the bounded arithmetic branch:
        # CodeQL py/polynomial-redos #22 replaced quadratic unbounded digit and
        # whitespace runs. A pattern change requires renewed complexity review.
        patterns = {
            "HARD_RE": (r"\b(prove|proof|theorem|distributed|consensus|concurren\w*|lock-free|optimi[sz]\w*|"
                        r"complexity|derive|formal|invariant|race condition|compiler|scheduler|asymptotic|"
                        r"np-hard|cryptograph\w*|architecture)\b"),
            "CODE_RE": r"\b(code|function|python|go|rust|bug|class|compile|refactor|test|api|sql|regex)\b",
            "MATH_RE": (r"\b(integral|equation|prove|theorem|sum|probability|matrix|calculate|derivative|prime)\b|"
                        r"\d\s{0,8}[-+*/^]\s{0,8}\d"),
            "AGENT_RE": r"\b(tool|write the file|read the file|create the file|run the command|proof\.txt)\b",
        }
        regexes = {name: getattr(fake, name) for name in patterns}
        for name, pattern in patterns.items():
            self.assertEqual(regexes[name].pattern, pattern, f"review regex complexity before changing {name}")
        self.assertEqual(fake.MAX_SCAN_CHARS, 32768)
        inputs = (
            "0" * 200000 + "x",
            "1 " * 100000 + "x",
            "9" * 100000 + " " * 100000 + "+",
            ("12" + " " * 50) * 4000,
            "7" * 100000 + "+" + " " * 100000,
        )
        for text in inputs:
            spies = {name: mock.Mock(wraps=regex) for name, regex in regexes.items()}
            with mock.patch.multiple(fake, **spies):
                fake.classify(text)
            for name, spy in spies.items():
                spy.search.assert_called_once_with(text[-32768:])
                # Exercise each reviewed regex on the full adversarial text too.
                regexes[name].search(text)

    def test_arithmetic_prompts_still_math(self) -> None:
        for text in ("What is 12 + 7?", "compute 3*4 please", "2^10 = ?", "100 - 58",
                     "Calculate the area of a circle", "x = 4 / 2",
                     "1" * 40 + " + 1"):
            self.assertEqual(fake.classify(text)["math"], 1.0, text)
            self.assertEqual(self.kind_of(text), "math", text)
        for text in (EASY_PROMPT, "Order 3 apples and 5 pears", "Room 101"):
            self.assertEqual(fake.classify(text)["math"], 0.0, text)
            self.assertEqual(self.kind_of(text), "chat", text)


class HTTPTests(unittest.TestCase):
    """Adapter HTTP service against the fake d3 over loopback."""

    def setUp(self) -> None:
        self.fake = fake.start_in_thread(api_key="unit-test-d3-key", slow_ms=600)
        self.addCleanup(self.fake.server_close)
        self.addCleanup(self.fake.shutdown)
        self.log = io.StringIO()
        self.start_policy(timeout_ms=300)

    def start_policy(self, **d3: Any) -> None:
        import os
        os.environ["D3_UNIT_TEST_KEY"] = "unit-test-d3-key"
        self.addCleanup(os.environ.pop, "D3_UNIT_TEST_KEY", None)
        cfg = cfg_for(f"http://127.0.0.1:{self.fake.server_port}", api_key_env="D3_UNIT_TEST_KEY", **d3)
        self.server = d3p.serve(cfg, "127.0.0.1", 0, log=self.log)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)

    def post(self, payload: Any, path: str = "/route") -> tuple[int, dict[str, Any]]:
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        req = urllib.request.Request(f"http://127.0.0.1:{self.server.server_port}{path}",
                                     data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"})
        try:
            with opener.open(req, timeout=10) as resp:
                return resp.status, json.loads(resp.read())
        except urllib.error.HTTPError as err:
            with err:
                return err.code, json.loads(err.read())

    def test_easy_and_hard_over_http(self) -> None:
        status, easy = self.post({"text": "user: " + EASY_PROMPT, "targets": TARGETS})
        self.assertEqual((status, easy["targetIndex"]), (200, 0))
        status, hard = self.post({"text": "user: " + SECRET_PROMPT, "targets": TARGETS})
        self.assertEqual((status, hard["targetIndex"]), (200, 1))
        self.assertTrue(hard["classLabel"].startswith("d3:hard:"))
        self.assertGreaterEqual(self.fake.calls, 2)

    def test_error_modes_return_502(self) -> None:
        expected = {
            "http500": "d3_http_5xx",
            "http400": "d3_http_4xx",
            "http422": "d3_http_422",
            "malformed": "d3_malformed",
            "question_error": "d3_question_error",
            "slow": "d3_timeout",
        }
        for mode, kind in expected.items():
            with self.subTest(mode=mode):
                self.fake.set_mode(mode)
                status, body = self.post({"text": SECRET_PROMPT, "targets": TARGETS})
                self.assertEqual((status, body), (502, {"error": "d3-policy-error", "class": kind}))
        self.fake.set_mode("ok")

    def test_unreachable_and_unauthorized(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        cfg = cfg_for("http://127.0.0.1:9")  # discard port: nothing listens
        self.server = d3p.serve(cfg, "127.0.0.1", 0, log=self.log)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        status, body = self.post({"text": SECRET_PROMPT, "targets": TARGETS})
        self.assertEqual((status, body["class"]), (502, "d3_unreachable"))
        self.server.shutdown()
        self.server.server_close()
        cfg = cfg_for(f"http://127.0.0.1:{self.fake.server_port}", api_key_env="D3_UNSET_KEY_FOR_TEST")
        self.server = d3p.serve(cfg, "127.0.0.1", 0, log=self.log)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        status, body = self.post({"text": SECRET_PROMPT, "targets": TARGETS})
        self.assertEqual((status, body["class"]), (502, "d3_http_4xx"))

    def test_concurrency_cap_rejects_when_busy(self) -> None:
        policy = self.server.RequestHandlerClass.policy
        policy.client._slots.acquire()  # simulate one d3 call in flight (max_concurrency 1)
        try:
            status, body = self.post({"text": "a", "targets": TARGETS})
        finally:
            policy.client._slots.release()
        self.assertEqual((status, body["class"]), (502, "policy_busy"))
        self.assertEqual(self.fake.calls, 0)

    def test_concurrent_calls_are_serialized(self) -> None:
        self.fake.set_mode("slow")  # 600 ms per call, adapter timeout 300 ms, max_concurrency 1
        results: list[tuple[int, dict[str, Any]]] = []
        threads = [threading.Thread(target=lambda: results.append(self.post({"text": "a", "targets": TARGETS})))
                   for _ in range(3)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        self.assertEqual(len(results), 3)
        self.assertTrue(all(status == 502 for status, _ in results))
        self.assertTrue({body["class"] for _, body in results} <= {"policy_busy", "d3_timeout"})
        # The cap holds: at most one call per timeout window reached d3, never all three at once.
        self.assertLessEqual(self.fake.calls, 2)

    def test_bad_policy_request(self) -> None:
        status, body = self.post({"text": SECRET_PROMPT, "targets": []})
        self.assertEqual((status, body["class"]), (502, "no_targets"))
        status, body = self.post([1, 2])
        self.assertEqual((status, body["class"]), (502, "bad_policy_request"))

    def test_no_prompt_text_in_logs_labels_or_responses(self) -> None:
        responses = []
        for text in (SECRET_PROMPT, EASY_PROMPT):
            responses.append(self.post({"text": "user: " + text, "targets": TARGETS})[1])
        self.fake.set_mode("http500")
        responses.append(self.post({"text": SECRET_PROMPT, "targets": TARGETS})[1])
        self.fake.set_mode("ok")
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(
                f"http://127.0.0.1:{self.server.server_port}/health", timeout=5) as resp:
            responses.append(json.loads(resp.read()))
        blob = self.log.getvalue() + json.dumps(responses)
        for needle in ("ZEBRA-7731", "WOMBAT-4410", "lock-free", "hello", "unit-test-d3-key"):
            self.assertNotIn(needle, blob)
        for line in self.log.getvalue().splitlines():
            record = json.loads(line)
            self.assertEqual(record["event"], "route")
            for value in record.values():
                self.assertTrue(isinstance(value, (int, float)) or len(str(value)) <= 64)


if __name__ == "__main__":
    unittest.main()
