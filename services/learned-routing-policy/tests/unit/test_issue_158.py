# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Regression tests for LRP correctness gaps (#158)."""

from __future__ import annotations

# ruff: noqa: F811 -- pytest fixtures are imported from the owned training test module.
import random
import threading
import time
from pathlib import Path
from typing import ClassVar

import numpy as np
from fastapi.testclient import TestClient
from lrp.bundle import generate_keypair, load_bundle, sign_bundle, write_trust_keys
from lrp.decision_path import (
    DecisionEvidence,
    compose_decision,
    evaluation_applicability,
)
from lrp.drift import evaluate_drift, population_stability_index
from lrp.features import SCALAR_NAMES
from lrp.policy import Decision, Prediction
from lrp.schemas import GroupConfig, ServiceConfig, Target
from lrp.serve import create_apps
from lrp.thompson import decide_with_exploration
from lrp.uncertainty import (
    UncertaintyAvailability,
    UncertaintyEstimate,
    apply_abstention,
    should_abstain,
)
from test_train import dataset, trained  # noqa: F401


def _target(name: str, price: float = 1.0) -> Target:
    return Target(provider="synthetic", model=name, input_price=price, output_price=price)


AUTH = {"X-LRP-Auth": "synthetic-test-auth"}


class CountingBundle:
    version = "synthetic-test"
    manifest: ClassVar[dict] = {}
    delay = 0.0
    ensemble_delay = 0.0
    builds = 0
    lock = threading.Lock()

    def build(self, payload):
        with self.lock:
            self.builds += 1
        time.sleep(self.delay)
        return np.zeros(3, dtype=np.float32)

    def predict(self, vector, targets=None):
        return self.bt_predictions()

    def bt_predictions(self):
        return {
            ("synthetic", "cheap"): Prediction(0.9, 10),
            ("synthetic", "strong"): Prediction(0.99, 100),
        }

    def explain(self, vector, keys=None):
        return {key: [] for key in self.bt_predictions()}


def _body():
    return {
        "group": "test-staging",
        "text": "SYNTHETIC PRIVATE CANARY",
        "context": {"estimatedTokens": 10},
        "caller": {"project": "demo", "tokenId": "discard-me"},
        "request": {"messages": [{"role": "user", "content": "synthetic first"}]},
        "targets": [
            {"provider": "synthetic", "model": "unknown"},
            {
                "provider": "synthetic",
                "model": "strong",
                "inputPricePerMillionUsd": 10,
                "outputPricePerMillionUsd": 10,
            },
            {
                "provider": "synthetic",
                "model": "cheap",
                "inputPricePerMillionUsd": 1,
                "outputPricePerMillionUsd": 1,
            },
        ],
    }


# --- Item 1: deadline-bounded uncertainty ---------------------------------


def test_uncertainty_uses_single_build_under_deadline():
    bundle = CountingBundle()
    bundle.delay = 0.02
    config = ServiceConfig.model_validate(
        {
            "groups": {
                "test-staging": {
                    "uncertainty_abstention": True,
                    "abstention_anchor": ["synthetic", "strong"],
                }
            },
            "deadline_ms": 80,
            "inference_workers": 1,
        }
    )
    app, _, runtime = create_apps(config, bundle, auth="synthetic-test-auth", enable_admin=True)
    client = TestClient(app)
    try:
        result = client.post("/route", json=_body(), headers=AUTH)
        assert result.status_code == 200
        assert bundle.builds == 1
    finally:
        runtime.pool.shutdown()


def test_timeout_does_not_start_second_build_for_uncertainty():
    bundle = CountingBundle()
    bundle.delay = 0.08
    config = ServiceConfig.model_validate(
        {
            "groups": {
                "test-staging": {
                    "uncertainty_abstention": True,
                    "abstention_anchor": ["synthetic", "strong"],
                }
            },
            "deadline_ms": 20,
            "inference_workers": 1,
        }
    )
    app, _, runtime = create_apps(config, bundle, auth="synthetic-test-auth", enable_admin=True)
    client = TestClient(app)
    try:
        started = time.monotonic()
        result = client.post("/route", json=_body(), headers=AUTH).json()
        elapsed = time.monotonic() - started
        assert result["classLabel"] == "lrp:latency-fallback"
        # Primary build may still be finishing in the worker; no second build.
        assert bundle.builds <= 1
        assert elapsed < 0.2
    finally:
        time.sleep(0.1)
        runtime.pool.shutdown()


# --- Item 2: eval/serve composed parity -----------------------------------


def test_compose_decision_parity_project_latency_cache_pins():
    targets = [_target("cheap", 1), _target("strong", 10)]
    preds = {
        targets[0].key: Prediction(0.85, 10),
        targets[1].key: Prediction(0.99, 100),
    }
    cfg = GroupConfig(
        quality_floor=0.8,
        floors_by_project={"demo-strict": 0.95},
        latency_p95_ms_max=1000,
        latency_evidence={"synthetic/cheap": 5000},
    )
    evidence = DecisionEvidence(
        project="demo-strict",
        input_tokens=10,
        latency_evidence={targets[0].key: 5000.0},
        exploration_allowed=False,
    )
    decision = compose_decision(targets, preds, cfg, random.Random(1), evidence)
    assert decision.primary == 1
    assert evaluation_applicability(
        cfg,
        has_project=False,
        has_latency_evidence=True,
        has_cache_evidence=False,
        has_pin_ordering=False,
        has_uncertainty=False,
    ).supported is False


def test_unsupported_uncertainty_config_blocks_applicability():
    cfg = GroupConfig(uncertainty_abstention=True)
    report = evaluation_applicability(
        cfg,
        has_project=True,
        has_latency_evidence=True,
        has_cache_evidence=True,
        has_pin_ordering=True,
        has_uncertainty=False,
    )
    assert not report.supported
    assert "uncertainty_abstention_requires_ensemble" in report.unsupported


# --- Item 3: PSI overflow and constant features ---------------------------


def test_psi_overflow_and_constant_feature_drift():
    reference = np.arange(100, dtype=float)
    observed = np.concatenate([reference, np.full(9900, 1e6)])
    assert population_stability_index(reference, observed) > 0.25
    assert population_stability_index(np.zeros(100), np.zeros(100)) < 0.01
    assert population_stability_index(np.zeros(100), np.full(100, 100.0)) > 0.25
    identical = np.random.default_rng(0).normal(0, 1, size=500)
    assert population_stability_index(identical, identical.copy()) < 0.05

    rng = np.random.default_rng(1)
    ref = np.zeros((200, len(SCALAR_NAMES)))
    obs = np.full((200, len(SCALAR_NAMES)), 50.0)
    emb = rng.normal(0, 1, size=(200, 32)).astype(np.float32)
    emb /= np.linalg.norm(emb, axis=1, keepdims=True)
    report = evaluate_drift(
        reference_scalars=ref,
        observed_scalars=obs,
        reference_embeddings=emb,
        observed_embeddings=emb,
        auto_shadow=True,
    )
    assert report.above_threshold
    assert report.recommend_shadow


# --- Item 4: Thompson single gate -----------------------------------------


class ScriptedRNG(random.Random):
    def __init__(self, draws: list[float]):
        super().__init__(0)
        self._draws = list(draws)
        self._i = 0

    def random(self) -> float:
        if self._i >= len(self._draws):
            return 0.0
        value = self._draws[self._i]
        self._i += 1
        return value


def test_thompson_rejected_gate_does_not_epsilon():
    targets = [_target("a", 1)]
    preds = {targets[0].key: Prediction(0.9, 10)}
    estimates = {targets[0].key: UncertaintyEstimate(0.9, 0.1, 5)}
    cfg = GroupConfig(explore_rate=0.1)
    # First draw rejects Thompson (0.5 >= 0.1). No further random() for epsilon.
    rng = ScriptedRNG([0.5])
    decision = decide_with_exploration(
        targets,
        preds,
        cfg,
        rng,
        estimates=estimates,
        strategy="thompson",
        exploration_allowed=True,
    )
    assert decision.label != "lrp:explore"
    assert decision.label != "lrp:explore-thompson"
    assert rng._i == 1


def test_thompson_explore_rate_near_p_not_double_gate():
    targets = [_target("a", 1)]
    preds = {targets[0].key: Prediction(0.9, 10)}
    estimates = {targets[0].key: UncertaintyEstimate(0.9, 0.1, 5)}
    cfg = GroupConfig(explore_rate=0.1)
    th = ep = other = 0
    draws = 20_000
    for i in range(draws):
        decision = decide_with_exploration(
            targets,
            preds,
            cfg,
            random.Random(i),
            estimates=estimates,
            strategy="thompson",
            exploration_allowed=True,
        )
        if decision.label == "lrp:explore-thompson":
            th += 1
        elif decision.label == "lrp:explore":
            ep += 1
        else:
            other += 1
    rate = th / draws
    assert ep == 0
    assert abs(rate - 0.1) < 0.02


def test_thompson_missing_estimates_no_epsilon():
    targets = [_target("a", 1), _target("b", 2)]
    preds = {t.key: Prediction(0.9, 10) for t in targets}
    cfg = GroupConfig(explore_rate=1.0)
    decision = decide_with_exploration(
        targets,
        preds,
        cfg,
        random.Random(1),
        estimates={},
        strategy="thompson",
        exploration_allowed=True,
        uncertainty_availability=UncertaintyAvailability.UNAVAILABLE,
    )
    assert decision.label != "lrp:explore"
    assert decision.label != "lrp:explore-thompson"


# --- Item 5: signed serve/reload ------------------------------------------


def test_serve_reload_monkeypatch_accepts_trust_kwargs(monkeypatch):
    from lrp.policy import Prediction as Pred
    from lrp.serve import Runtime

    class FakeBundle:
        version = "next"
        manifest: ClassVar[dict] = {}

        def build(self, payload):
            return np.zeros(3, dtype=np.float32)

        def predict(self, vector, targets=None):
            return self.bt_predictions()

        def bt_predictions(self):
            return {("synthetic", "cheap"): Pred(0.9, 10)}

        def explain(self, vector, keys=None):
            return {}

    config = ServiceConfig.model_validate({"groups": {"test-staging": {}}})
    runtime = Runtime(config, FakeBundle(), "synthetic-test-auth", Path("/synthetic"))
    replacement = FakeBundle()
    import lrp.bundle

    monkeypatch.setattr(
        lrp.bundle,
        "load_bundle",
        lambda path, threads=1, **kwargs: replacement,
    )
    runtime.reload()
    assert runtime.bundle is replacement
    runtime.pool.shutdown()


def test_signed_serve_startup_and_reload(trained, tmp_path, monkeypatch):
    import base64
    import shutil

    from lrp.cli import execute, parser

    root = tmp_path / "bundle"
    shutil.copytree(trained, root)
    root.chmod(0o700)
    for path in root.rglob("*"):
        path.chmod(0o700 if path.is_dir() else 0o600)
    public, seed = generate_keypair()
    key_path = tmp_path / "seed.b64"
    key_path.write_text(base64.b64encode(seed).decode())
    key_path.chmod(0o600)
    trust_path = tmp_path / "trust.json"
    write_trust_keys(trust_path, {"lrp-test-158": public})
    sign_bundle(root, key_path, "lrp-test-158")

    seen: dict = {}

    def fake_serve(**kwargs):
        seen.update(kwargs)
        loaded = load_bundle(
            kwargs["bundle"],
            threads=1,
            require_signed=kwargs.get("require_signed", False),
            trusted_keys=kwargs.get("trusted_keys"),
        )
        assert loaded.signature_key_id == "lrp-test-158"
        settings = ServiceConfig.model_validate({"groups": {"demo": {}}})
        app, admin, runtime = create_apps(
            settings,
            loaded,
            auth="synthetic-test-auth",
            enable_admin=True,
            bundle_path=kwargs["bundle"],
            require_signed=True,
            trusted_keys=kwargs.get("trusted_keys"),
        )
        try:
            assert TestClient(admin).get("/readyz").status_code == 200
            body = {
                "group": "demo",
                "text": "synthetic",
                "context": {"estimatedTokens": 10},
                "caller": {"project": "demo"},
                "request": {"messages": [{"role": "user", "content": "hi"}]},
                "targets": [
                    {
                        "provider": "synthetic",
                        "model": "cheap/model",
                        "inputPricePerMillionUsd": 1,
                        "outputPricePerMillionUsd": 1,
                    },
                    {
                        "provider": "synthetic",
                        "model": "anchor/model",
                        "inputPricePerMillionUsd": 10,
                        "outputPricePerMillionUsd": 10,
                    },
                ],
            }
            assert TestClient(app).post("/route", json=body, headers=AUTH).status_code == 200
        finally:
            runtime.pool.shutdown()

    monkeypatch.setattr("lrp.serve.serve", fake_serve)
    cfg = tmp_path / "config.yaml"
    cfg.write_text("groups:\n  demo: {}\n")
    args = parser().parse_args(
        [
            "serve",
            "--bundle",
            str(root),
            "--config",
            str(cfg),
            "--trust",
            str(trust_path),
            "--require-signed",
        ]
    )
    assert execute(args) == 0
    assert seen["require_signed"] is True
    assert seen["trusted_keys"] == trust_path


# --- Item 6: missing ensemble fails closed --------------------------------


def test_missing_uncertainty_fails_closed_when_abstention_enabled():
    targets = [_target("cheap", 1), _target("anchor", 10)]
    decision = Decision(0, (1,), "lrp:caf:q0.90:c1")
    result = apply_abstention(
        decision,
        targets,
        {},
        anchor=targets[1].key,
        enabled=True,
        availability=UncertaintyAvailability.UNAVAILABLE,
    )
    assert result.primary == 1
    assert result.label == "lrp:uncertain-unavailable"
    assert should_abstain(None, enabled=True)
    assert not should_abstain(None, enabled=False)


def test_serve_unavailable_uncertainty_uses_anchor():
    bundle = CountingBundle()
    config = ServiceConfig.model_validate(
        {
            "groups": {
                "test-staging": {
                    "uncertainty_abstention": True,
                    "abstention_anchor": ["synthetic", "strong"],
                    "quality_floor": 0.8,
                }
            }
        }
    )
    app, _, runtime = create_apps(config, bundle, auth="synthetic-test-auth", enable_admin=True)
    client = TestClient(app)
    try:
        assert runtime.ensemble.empty
        result = client.post("/route", json=_body(), headers=AUTH).json()
        assert result["targetIndex"] == 1
        assert result["classLabel"] == "lrp:uncertain-unavailable"
    finally:
        runtime.pool.shutdown()


# --- Composed cases -------------------------------------------------------


def test_composed_timeout_plus_uncertainty_and_thompson_auth():
    bundle = CountingBundle()
    bundle.delay = 0.05
    config = ServiceConfig.model_validate(
        {
            "groups": {
                "test-staging": {
                    "uncertainty_abstention": True,
                    "exploration_strategy": "thompson",
                    "explore_rate": 0.5,
                    "exploration_projects": ["calib-only"],
                    "abstention_anchor": ["synthetic", "strong"],
                }
            },
            "deadline_ms": 15,
            "inference_workers": 1,
        }
    )
    app, _, runtime = create_apps(config, bundle, auth="synthetic-test-auth")
    client = TestClient(app)
    try:
        result = client.post("/route", json=_body(), headers=AUTH).json()
        assert result["classLabel"] == "lrp:latency-fallback"
        assert bundle.builds <= 1
    finally:
        time.sleep(0.08)
        runtime.pool.shutdown()
