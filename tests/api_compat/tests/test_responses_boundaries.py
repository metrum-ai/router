# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""RESP-11: Responses capability-boundary rejects (background / retrieve / cancel / …)."""

from __future__ import annotations

import json
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from conftest import PROMPT_CANARY, request
from harness import CALLER
from harness.manifest_loader import cases_by_id


def _body(raw: bytes) -> dict:
    return json.loads(raw)


def _request_method(base: str, method: str, path: str, payload=None, headers=None, token=CALLER):
    hdrs = {}
    if token is not None:
        hdrs["Authorization"] = f"Bearer {token}"
    data = None if payload is None else json.dumps(payload).encode()
    if data is not None:
        hdrs["Content-Type"] = "application/json"
    if headers:
        hdrs.update(headers)
    req = Request(base + path, data=data, headers=hdrs, method=method)
    try:
        with urlopen(req, timeout=5) as response:
            return response.status, response.headers, response.read()
    except HTTPError as error:
        return error.code, error.headers, error.read()


def test_resp_11_manifest_disposition():
    case = cases_by_id()["RESP-11"]
    assert case["disposition"] == "unsupported"
    assert case["test_id"] == "test_resp_11_capability_boundaries_reject"
    assert "test_responses_boundaries.py" in case["evidence_paths"][0]


def test_resp_11_capability_boundaries_reject(api, router):
    """RESP-11: unadvertised Responses surfaces reject with zero upstream calls."""
    base = router["base"]
    upstream = router["upstream"]

    # background=true must not silently succeed as unary HTTP 200.
    before = len(upstream.calls)
    status, _, raw = api(
        "/v1/responses",
        {"model": "responses", "input": PROMPT_CANARY, "background": True},
    )
    assert status == 400, raw
    assert _body(raw)["error"]["type"] == "responses-background-unsupported"
    assert len(upstream.calls) == before

    # background=false remains a normal unary create.
    status, _, raw = api(
        "/v1/responses",
        {"model": "responses", "input": PROMPT_CANARY, "background": False},
    )
    assert status == 200, raw
    assert len(upstream.calls) == before + 1

    # Known unsupported endpoints return 501; unregistered paths remain 404/405.
    for method, path, expected in (
        ("GET", "/v1/responses/resp_synthetic", 501),
        ("POST", "/v1/responses/resp_synthetic/cancel", 501),
        ("DELETE", "/v1/responses/resp_synthetic", 501),
        ("GET", "/v1/responses/resp_synthetic/input_items", 501),
        ("POST", "/v1/conversations", 501),
        ("GET", "/v1/conversations/conv_synthetic", 501),
        ("POST", "/v1/responses/resp_synthetic/compact", None),
        ("GET", "/v1/realtime", 501),
    ):
        before = len(upstream.calls)
        status, _, raw = _request_method(base, method, path, payload={"model": "responses"} if method == "POST" else None)
        assert status in ({expected} if expected else {404, 405}), f"{method} {path} -> {status} {raw!r}"
        if expected == 501:
            error = _body(raw)["error"]
            assert error["type"] == "invalid_request_error" and error["code"] == "unsupported_endpoint"
        assert len(upstream.calls) == before

    # WebSocket-style Upgrade on /v1/responses must not become unary success.
    before = len(upstream.calls)
    status, _, raw = _request_method(
        base,
        "POST",
        "/v1/responses",
        payload={"model": "responses", "input": PROMPT_CANARY},
        headers={"Upgrade": "websocket", "Connection": "Upgrade"},
    )
    assert status == 400, raw
    assert _body(raw)["error"]["type"] == "responses-websocket-unsupported"
    assert len(upstream.calls) == before

    # Healthy unary create still works after boundary rejects.
    status, _, raw = request(base, "/v1/responses", {"model": "responses", "input": PROMPT_CANARY})
    assert status == 200, raw
