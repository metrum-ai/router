# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import concurrent.futures
import json
import time
from typing import ClassVar

import numpy as np
import pytest
from fastapi.testclient import TestClient
from lrp.policy import Prediction
from lrp.schemas import Payload, ServiceConfig
from lrp.serve import create_apps, session_key


class FakeBundle:
    version = "synthetic-test"
    manifest: ClassVar[dict] = {}
    delay = 0

    def build(self, payload):
        time.sleep(self.delay)
        assert "tokenId" not in payload["caller"]
        return np.zeros(3, dtype=np.float32)

    def predict(self, vector, targets=None):
        return self.bt_predictions()

    def bt_predictions(self):
        return {
            ("synthetic", "cheap"): Prediction(0.9, 10),
            ("synthetic", "strong"): Prediction(0.99, 100),
        }

    def explain(self, vector, keys=None):
        return {
            key: [{"feature": "textChars", "contribution": 0.1}] for key in self.bt_predictions()
        }


def body():
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


def clients(bundle=None, **kwargs):
    config = ServiceConfig.model_validate({"groups": {"test-staging": {}}, **kwargs})
    app, admin, runtime = create_apps(config, bundle, auth="synthetic-test-auth", enable_admin=True)
    return TestClient(app), TestClient(admin), runtime


AUTH = {"X-LRP-Auth": "synthetic-test-auth"}


def test_api_contract_auth_unknown_content_images_logs(caplog):
    client, admin, runtime = clients(FakeBundle())
    assert client.post("/route", json=body()).status_code == 401
    assert client.post("/route", json=body(), headers={"X-LRP-Auth": "wrong"}).content == b""
    result = client.post("/route", json=body(), headers=AUTH)
    assert result.status_code == 200
    assert result.json()["targetIndex"] == 2
    assert result.json()["fallbackIndexes"] == [1]
    missing = body()
    del missing["request"], missing["text"]
    assert (
        client.post("/route", json=missing, headers=AUTH).json()["classLabel"]
        == "lrp:no-request-content"
    )
    missing["text"] = "synthetic"
    missing["context"]["imageCount"] = 1
    assert (
        client.post("/route", json=missing, headers=AUTH).json()["classLabel"]
        == "lrp:image-passthrough"
    )
    assert "lrp_route_degraded_total" in admin.get("/metrics").text
    assert client.get("/metrics").status_code == 404
    assert admin.get("/readyz").status_code == 200
    explanation = client.post("/explain", json=body(), headers=AUTH).json()
    assert explanation["targets"][0]["excluded_reason"] == "unknown_or_undertrained"
    assert "SYNTHETIC PRIVATE CANARY" not in json.dumps(explanation)
    assert "synthetic-test-auth" not in caplog.text
    assert "SYNTHETIC PRIVATE CANARY" not in caplog.text
    assert "discard-me" not in caplog.text
    runtime.pool.shutdown()


def test_invalid_body_ready_and_large_request():
    client, admin, runtime = clients()
    assert admin.get("/readyz").status_code == 503
    assert client.post("/route", json=body(), headers=AUTH).status_code == 503
    runtime.bundle = FakeBundle()
    assert client.post("/route", content="{", headers=AUTH).status_code == 400
    assert client.post("/route", json={"text": "secret"}, headers=AUTH).json() == {
        "error": "invalid_request"
    }
    large = body()
    large["text"] = "x" * 1_000_000
    assert client.post("/route", json=large, headers=AUTH).status_code == 200
    assert client.post("/route", content=b"x" * 2_100_000, headers=AUTH).status_code == 413
    runtime.pool.shutdown()


def test_bounded_deadline_no_queued_work():
    bundle = FakeBundle()
    bundle.delay = 0.2
    client, _, runtime = clients(bundle, deadline_ms=15, inference_workers=1)
    started = time.monotonic()
    first = client.post("/route", json=body(), headers=AUTH)
    second = client.post("/route", json=body(), headers=AUTH)
    assert first.json()["classLabel"] == second.json()["classLabel"] == "lrp:latency-fallback"
    assert time.monotonic() - started < 0.15
    runtime.pool.shutdown()


def test_reload_midtraffic_and_native_shadow(monkeypatch):
    bundle = FakeBundle()
    client, admin, runtime = clients(bundle)
    runtime.config.groups["test-staging"].mode = "shadow"
    # LRP returns real recommendation; router owns shadow serving.
    assert client.post("/route", json=body(), headers=AUTH).json()["targetIndex"] == 2

    def request(_):
        return client.post("/route", json=body(), headers=AUTH).status_code

    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
        futures = [pool.submit(request, i) for i in range(100)]
        replacement = FakeBundle()
        replacement.version = "synthetic-next"
        from pathlib import Path

        import lrp.bundle
        runtime.bundle_path = Path("/synthetic-bundle")
        monkeypatch.setattr(
            lrp.bundle, "load_bundle", lambda path, threads=1, **kwargs: replacement
        )
        assert admin.post("/admin/reload", headers=AUTH).status_code == 200
        assert runtime.bundle is replacement
        assert all(f.result() == 200 for f in futures)
    runtime.pool.shutdown()


def test_operator_deadline_allows_slow_learning_but_default_falls_back():
    bundle = FakeBundle()
    bundle.delay = 0.3
    default_client, _, default_runtime = clients(bundle, inference_workers=1)
    longer_client, _, longer_runtime = clients(bundle, inference_workers=1, deadline_ms=900)
    try:
        assert default_runtime.config.deadline_ms == 200
        default = default_client.post("/route", json=body(), headers=AUTH).json()
        longer = longer_client.post("/route", json=body(), headers=AUTH).json()
        assert default["classLabel"] == "lrp:latency-fallback"
        assert longer["classLabel"] != "lrp:latency-fallback"
        assert longer["targetIndex"] == 2
    finally:
        default_runtime.pool.shutdown()
        longer_runtime.pool.shutdown()


@pytest.mark.parametrize("deadline", [0, -1, 4501])
def test_deadline_range_rejects_unbounded_values(deadline):
    with pytest.raises(ValueError):
        ServiceConfig(groups={}, deadline_ms=deadline)


def test_pin_canonical_key_and_auth_required():
    payload = Payload.model_validate(body())
    key = session_key(payload)
    payload.request["messages"].append({"role": "user", "content": "turn two"})
    assert session_key(payload) == key
    payload.group = "different-staging"
    assert session_key(payload) != key
    with pytest.raises(ValueError):
        create_apps(ServiceConfig(groups={}), None, auth="")


def test_conversation_key_prefers_router_context():
    payload = Payload.model_validate(body())
    without = session_key(payload)
    payload.context["conversationKey"] = "ck_0123456789abcdef0123456789abcdef"
    with_key = session_key(payload)
    assert with_key is not None and with_key != without
    payload.request["messages"][0]["content"] = "completely different first turn"
    assert session_key(payload) == with_key


def test_feedback_endpoint_validates_bounded_payload():
    client, _, runtime = clients(FakeBundle())
    try:
        ok = client.post(
            "/feedback",
            json={
                "schemaVersion": "external_policy.feedback.v1",
                "requestId": "req_example",
                "group": "demo",
                "status": 200,
                "selectedTarget": {"provider": "synthetic", "model": "cheap"},
                "usage": {"inputTokens": 1, "outputTokens": 1, "totalTokens": 2},
                "latencyMs": 12,
            },
            headers=AUTH,
        )
        assert ok.status_code == 204
        assert client.post("/feedback", json={"requestId": "x"}, headers=AUTH).status_code == 400
        assert client.post("/feedback", json={"requestId": "x"}).status_code == 401
    finally:
        runtime.pool.shutdown()


def test_undertrained_abstains_and_ambiguous_skins_rejected():
    bundle = FakeBundle()
    bundle.manifest = {"targets": [{"provider": "synthetic", "model": "cheap", "n_train": 10}]}
    client, _, runtime = clients(bundle)
    response = client.post("/route", json=body(), headers=AUTH)
    assert response.status_code == 503
    assert response.json() == {"error": "no_trained_target"}
    ambiguous = body()
    ambiguous["targets"].append({**ambiguous["targets"][2], "dialect": "anthropic"})
    assert client.post("/route", json=ambiguous, headers=AUTH).status_code == 400
    runtime.pool.shutdown()


def test_payload_drops_raw_credentials_before_features():
    value = body()
    value["request"]["raw"] = {"secret": "discard-me"}
    normalized = Payload.model_validate(value).model_dump()
    assert "discard-me" not in json.dumps(normalized)


def test_project_floor_latency_cache_and_bounded_labels():
    config = ServiceConfig.model_validate(
        {
            "groups": {
                "test-staging": {
                    "quality_floor": 0.8,
                    "floors_by_project": {"demo-strict": 0.95},
                    "latency_p95_ms_max": 1000,
                    "latency_metric": "duration",
                    "unknown_latency": "allow",
                    "latency_evidence": {"synthetic/cheap": 5000},
                }
            }
        }
    )
    app, _, runtime = create_apps(config, FakeBundle(), auth="synthetic-test-auth", enable_admin=True)
    client = TestClient(app)
    try:
        # Static evidence excludes cheap (p95 5000 > 1000); strong remains.
        result = client.post("/route", json=body(), headers=AUTH).json()
        assert result["targetIndex"] == 1
        assert result["classLabel"].startswith("lrp:caf")
        assert len(result["classLabel"]) <= 64

        strict = body()
        strict["caller"]["project"] = "demo-strict"
        # Strong quality 0.99 meets 0.95; cheap excluded by latency anyway.
        assert client.post("/route", json=strict, headers=AUTH).json()["targetIndex"] == 1

        # Feedback updates ledger; after many fast samples for cheap, still need
        # p95 under the cap. Seed enough low samples to pull p95 down.
        for _ in range(64):
            assert (
                client.post(
                    "/feedback",
                    json={
                        "schemaVersion": "external_policy.feedback.v1",
                        "requestId": "req_example",
                        "group": "test-staging",
                        "status": 200,
                        "selectedTarget": {"provider": "synthetic", "model": "cheap"},
                        "latencyMs": 10,
                        "ttfbMs": 5,
                    },
                    headers=AUTH,
                ).status_code
                == 204
            )
        after = client.post("/route", json=body(), headers=AUTH).json()
        assert after["targetIndex"] == 2

        cached = body()
        cached["targets"][2]["cachedInputPricePerMillionUsd"] = 0.1
        cached["context"]["promptCacheState"] = "hit"
        explanation = client.post("/explain", json=cached, headers=AUTH).json()
        assert explanation["cache_state"] == "hit"
        assert explanation["floor"] == 0.8
        assert explanation["group_floor"] == 0.8
        assert explanation["project_floor_override"] is False
        assert "SYNTHETIC PRIVATE CANARY" not in json.dumps(explanation)

        # Pin context alone does not invent cache hits.
        pinned_only = body()
        pinned_only["targets"][2]["cachedInputPricePerMillionUsd"] = 0.1
        pinned_only["context"]["systemChars"] = 80000
        assert client.post("/explain", json=pinned_only, headers=AUTH).json()["cache_state"] == "unknown"
    finally:
        runtime.pool.shutdown()

