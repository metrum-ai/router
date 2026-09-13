# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Authenticated policy service with bounded inference and atomic bundle reload."""

from __future__ import annotations

import asyncio
import hashlib
import hmac
import json
import logging
import math
import os
import random
import signal
import stat
import threading
import time
from collections import OrderedDict
from collections.abc import AsyncIterator, Mapping, Sequence
from concurrent.futures import ThreadPoolExecutor
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any, Protocol

import numpy as np
import numpy.typing as npt
import uvicorn
import yaml
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, Response
from prometheus_client import CollectorRegistry, Counter, Gauge, Histogram, generate_latest
from pydantic import ValidationError

from lrp.decision_path import DecisionEvidence, compose_decision
from lrp.policy import (
    Decision,
    Prediction,
    effective_quality_floor,
    estimated_cost,
    metric_label,
    resolve_cache_estimate,
    safe_label,
)
from lrp.schemas import FeedbackPayload, Payload, ServiceConfig, TargetKey
from lrp.thompson import inject_cold_start_predictions, parse_cold_start_targets
from lrp.uncertainty import (
    BundleEnsemble,
    UncertaintyAvailability,
    UncertaintyEstimate,
    load_bundle_ensemble,
)

LOG = logging.getLogger("lrp")
MAX_BODY = 2 * 1024 * 1024


class Bundle(Protocol):
    @property
    def version(self) -> str: ...
    @property
    def manifest(self) -> Mapping[str, Any]: ...

    def build(self, payload: Mapping[str, Any]) -> npt.NDArray[np.float32]: ...
    def predict(
        self, vector: npt.NDArray[np.float32], targets: Any = None
    ) -> Mapping[TargetKey, Prediction]: ...
    def bt_predictions(self) -> Mapping[TargetKey, Prediction]: ...
    def explain(
        self, vector: npt.NDArray[np.float32], keys: Any = None
    ) -> Mapping[TargetKey, list[dict[str, Any]]]: ...


class Pins:
    def __init__(self, limit: int) -> None:
        self.limit = limit
        self.rows: OrderedDict[str, tuple[float, TargetKey]] = OrderedDict()
        self.lock = threading.Lock()

    def get(self, key: str) -> TargetKey | None:
        with self.lock:
            value = self.rows.pop(key, None)
            if value is None or value[0] <= time.monotonic():
                return None
            self.rows[key] = value
            return value[1]

    def put(self, key: str, target: TargetKey, ttl: float) -> None:
        with self.lock:
            self.rows.pop(key, None)
            self.rows[key] = time.monotonic() + ttl, target
            while len(self.rows) > self.limit:
                self.rows.popitem(last=False)


class LatencyLedger:
    """Bounded in-memory rolling latency samples from authenticated feedback.

    Stores sanitized (group, provider, model) → recent duration/TTFB milliseconds.
    Used only for optional selection constraints; never stores prompts or tokens.
    """

    def __init__(self, limit_keys: int = 4096, window_per_key: int = 64) -> None:
        self.limit_keys = limit_keys
        self.window_per_key = window_per_key
        self.duration: OrderedDict[tuple[str, str, str], list[float]] = OrderedDict()
        self.ttfb: OrderedDict[tuple[str, str, str], list[float]] = OrderedDict()
        self.lock = threading.Lock()

    def observe(
        self,
        group: str,
        provider: str,
        model: str,
        *,
        duration_ms: float | None,
        ttfb_ms: float | None,
    ) -> None:
        key = (group.strip(), provider.strip(), model.strip())
        if not all(key) or max(len(part) for part in key) > 512:
            return
        with self.lock:
            if duration_ms is not None and math.isfinite(duration_ms) and duration_ms >= 0:
                self._append(self.duration, key, float(duration_ms))
            if ttfb_ms is not None and math.isfinite(ttfb_ms) and ttfb_ms >= 0:
                self._append(self.ttfb, key, float(ttfb_ms))

    def _append(
        self,
        store: OrderedDict[tuple[str, str, str], list[float]],
        key: tuple[str, str, str],
        value: float,
    ) -> None:
        samples = store.pop(key, [])
        samples.append(value)
        if len(samples) > self.window_per_key:
            samples = samples[-self.window_per_key :]
        store[key] = samples
        while len(store) > self.limit_keys:
            store.popitem(last=False)

    def p95(
        self, group: str, provider: str, model: str, *, metric: str
    ) -> float | None:
        key = (group.strip(), provider.strip(), model.strip())
        with self.lock:
            store = self.ttfb if metric == "ttfb" else self.duration
            samples = store.get(key)
            if not samples:
                return None
            ordered = sorted(samples)
            index = max(0, min(len(ordered) - 1, math.ceil(0.95 * len(ordered)) - 1))
            return ordered[index]


def parse_latency_evidence_key(raw: str) -> TargetKey | None:
    text = raw.strip()
    if "/" not in text:
        return None
    provider, model = text.split("/", 1)
    provider, model = provider.strip(), model.strip()
    if not provider or not model:
        return None
    return provider, model


def merge_latency_evidence(
    group: str,
    cfg: Any,
    targets: Sequence[Any],
    ledger: LatencyLedger,
) -> dict[TargetKey, float]:
    """Combine static config evidence with rolling feedback p95 values."""
    merged: dict[TargetKey, float] = {}
    for raw_key, ms in cfg.latency_evidence.items():
        parsed = parse_latency_evidence_key(raw_key)
        if parsed is not None:
            merged[parsed] = float(ms)
    for target in targets:
        observed = ledger.p95(
            group, target.provider, target.model, metric=cfg.latency_metric
        )
        if observed is not None:
            # Live feedback supersedes static seed for the same identity.
            merged[target.key] = observed
    return merged


def session_key(payload: Payload) -> str | None:
    context_key = ""
    if isinstance(payload.context, dict):
        raw = payload.context.get("conversationKey") or payload.context.get("conversation_key")
        if isinstance(raw, str):
            context_key = raw.strip()
    if context_key:
        return hashlib.sha256(context_key.encode()).hexdigest()
    req = payload.request or {}
    first = ""
    for message in req.get("messages", []):
        if isinstance(message, dict) and message.get("role") == "user":
            content = message.get("content", "")
            if isinstance(content, str):
                first = content
            if not first:
                first = "".join(
                    str(part.get("text", ""))
                    for part in message.get("parts", [])
                    if isinstance(part, dict)
                )
            break
    if not first and isinstance(req.get("input"), str):
        first = req["input"]
    if not first:
        return None
    encoded = json.dumps(
        [
            payload.group,
            payload.caller.project,
            payload.caller.environment,
            hashlib.sha256(first.encode()).hexdigest(),
        ],
        separators=(",", ":"),
    )
    return hashlib.sha256(encoded.encode()).hexdigest()


class Runtime:
    def __init__(
        self,
        config: ServiceConfig,
        bundle: Bundle | None,
        auth: str,
        bundle_path: Path | None = None,
        *,
        require_signed: bool = False,
        trusted_keys: Path | None = None,
    ) -> None:
        if not auth or auth.strip() != auth or "\n" in auth or "\r" in auth:
            raise ValueError("LRP_POLICY_AUTH_HEADER must contain a nonempty header value")
        self.auth = auth.encode()
        self.config, self.bundle, self.bundle_path = config, bundle, bundle_path
        self.require_signed = require_signed
        self.trusted_keys = trusted_keys
        self.ensemble: BundleEnsemble = BundleEnsemble({})
        self.pins = Pins(config.max_pins)
        self.latency_ledger = LatencyLedger()
        self.pool = ThreadPoolExecutor(
            max_workers=config.inference_workers, thread_name_prefix="lrp-inference"
        )
        self.admission = threading.BoundedSemaphore(config.inference_workers)
        self.reload_lock = threading.Lock()
        self.warned_targets: set[str] = set()
        self.registry = CollectorRegistry()
        self.latency = Histogram(
            "lrp_route_latency_seconds", "Whole route handler latency", registry=self.registry
        )
        self.total = Counter(
            "lrp_route_total", "Policy decisions", ["label"], registry=self.registry
        )
        self.degraded = Counter(
            "lrp_route_degraded_total", "Degraded decisions", ["reason"], registry=self.registry
        )
        self.drift_alert = Counter(
            "lrp_drift_alert", "Drift threshold crossings", ["reason"], registry=self.registry
        )
        self.quality = Histogram(
            "lrp_predicted_quality",
            "Predicted quality",
            ["target"],
            buckets=(0, 0.25, 0.5, 0.75, 0.8, 0.9, 1),
            registry=self.registry,
        )
        self.info = Gauge("lrp_bundle_info", "Loaded bundle", ["version"], registry=self.registry)
        self._set_version()
        self._load_ensemble_into(self)

    def requires_uncertainty_evidence(self) -> bool:
        return any(
            group.uncertainty_abstention or group.exploration_strategy == "thompson"
            for group in self.config.groups.values()
        )

    def _load_ensemble(self, bundle: Bundle, bundle_path: Path | None) -> BundleEnsemble:
        if bundle_path is None:
            return BundleEnsemble({})
        try:
            return load_bundle_ensemble(bundle_path, bundle.manifest)
        except Exception:  # noqa: BLE001 - surface as empty; callers decide fail-closed
            LOG.warning("ensemble load failed")
            return BundleEnsemble({})

    @staticmethod
    def _load_ensemble_into(runtime: Runtime) -> None:
        if runtime.bundle is None:
            runtime.ensemble = BundleEnsemble({})
            return
        runtime.ensemble = runtime._load_ensemble(runtime.bundle, runtime.bundle_path)

    def _set_version(self) -> None:
        self.info.clear()
        if self.bundle is not None:
            self.info.labels(safe_label(self.bundle.version)).set(1)

    def reload(self) -> None:
        from lrp.bundle import load_bundle

        if self.bundle_path is None:
            raise ValueError("bundle path unavailable")
        with self.reload_lock:
            previous_bundle = self.bundle
            previous_ensemble = self.ensemble
            candidate = load_bundle(
                self.bundle_path,
                threads=1,
                require_signed=self.require_signed,
                trusted_keys=self.trusted_keys,
            )
            ensemble = self._load_ensemble(candidate, self.bundle_path)
            if self.requires_uncertainty_evidence() and ensemble.empty:
                # Keep the last valid complete snapshot; do not publish a partial reload.
                self.bundle = previous_bundle
                self.ensemble = previous_ensemble
                raise ValueError("ensemble_required")
            self.bundle = candidate
            self.ensemble = ensemble
            self._set_version()

    async def infer(
        self,
        bundle: Bundle,
        payload: Payload,
        remaining: float,
        *,
        explain: bool = False,
        need_uncertainty: bool = False,
    ) -> tuple[
        Mapping[TargetKey, Prediction],
        str | None,
        Mapping[TargetKey, list[dict[str, Any]]],
        dict[TargetKey, UncertaintyEstimate],
        UncertaintyAvailability,
    ]:
        """Build features once; run predict, ensemble, and explain under one deadline."""
        unavailable = (
            UncertaintyAvailability.UNAVAILABLE
            if need_uncertainty
            else UncertaintyAvailability.DISABLED
        )
        if remaining <= 0 or not self.admission.acquire(blocking=False):
            return bundle.bt_predictions(), "latency", {}, {}, unavailable

        ensemble = self.ensemble

        def run() -> tuple[
            Mapping[TargetKey, Prediction],
            Mapping[TargetKey, list[dict[str, Any]]],
            dict[TargetKey, UncertaintyEstimate],
            UncertaintyAvailability,
        ]:
            try:
                vector = bundle.build(payload.model_dump(by_alias=True))
                keys = [target.key for target in payload.targets]
                predictions = bundle.predict(vector, keys)
                contributions = bundle.explain(vector, keys) if explain else {}
                estimates: dict[TargetKey, UncertaintyEstimate] = {}
                availability = UncertaintyAvailability.DISABLED
                if need_uncertainty:
                    if ensemble.empty:
                        availability = UncertaintyAvailability.UNAVAILABLE
                    else:
                        try:
                            estimates = ensemble.estimates_for(vector, keys)
                            if not estimates:
                                availability = UncertaintyAvailability.UNAVAILABLE
                            elif any(
                                estimate.n_members < 2 for estimate in estimates.values()
                            ):
                                availability = UncertaintyAvailability.INSUFFICIENT
                            else:
                                availability = UncertaintyAvailability.AVAILABLE
                        except Exception:  # noqa: BLE001 - never expose model internals
                            estimates = {}
                            availability = UncertaintyAvailability.UNAVAILABLE
                return predictions, contributions, estimates, availability
            finally:
                self.admission.release()

        # Timed-out work keeps its admission slot until native compute finishes.
        # Do not start a second build/ensemble after timeout or admission rejection.
        future = asyncio.wrap_future(self.pool.submit(run))
        try:
            predictions, contributions, estimates, availability = await asyncio.wait_for(
                asyncio.shield(future), remaining
            )
            return predictions, None, contributions, estimates, availability
        except TimeoutError:
            future.add_done_callback(
                lambda done: done.exception() if not done.cancelled() else None
            )
            return bundle.bt_predictions(), "latency", {}, {}, unavailable
        except Exception:  # noqa: BLE001 - inference failure degrades without exposing content
            return bundle.bt_predictions(), "embedding", {}, {}, unavailable


def create_apps(
    config: ServiceConfig,
    bundle: Bundle | None,
    *,
    auth: str,
    enable_admin: bool = False,
    bundle_path: Path | None = None,
    require_signed: bool = False,
    trusted_keys: Path | None = None,
) -> tuple[FastAPI, FastAPI, Runtime]:
    runtime = Runtime(
        config,
        bundle,
        auth,
        bundle_path,
        require_signed=require_signed,
        trusted_keys=trusted_keys,
    )

    @asynccontextmanager
    async def lifespan(app: FastAPI) -> AsyncIterator[None]:
        yield
        runtime.pool.shutdown(wait=False, cancel_futures=True)

    app = FastAPI(docs_url=None, redoc_url=None, openapi_url=None, lifespan=lifespan)
    admin = FastAPI(docs_url=None, redoc_url=None, openapi_url=None)

    def authenticated(request: Request) -> bool:
        value = request.headers.get("X-LRP-Auth", "").encode()
        return hmac.compare_digest(value, runtime.auth)

    async def route(request: Request, explain: bool = False) -> Response:
        started = time.monotonic()
        try:
            if not authenticated(request):
                return Response(status_code=401)
            current = runtime.bundle
            if current is None:
                return JSONResponse({"error": "not_ready"}, status_code=503)
            raw = bytearray()
            try:
                async with asyncio.timeout(config.deadline_ms / 1000):
                    async for chunk in request.stream():
                        raw.extend(chunk)
                        if len(raw) > MAX_BODY:
                            return JSONResponse({"error": "body_too_large"}, status_code=413)
            except TimeoutError:
                return JSONResponse({"error": "body_timeout"}, status_code=408)
            try:
                payload = Payload.model_validate_json(raw)
            except (ValidationError, ValueError):
                return JSONResponse({"error": "invalid_request"}, status_code=400)
            cfg = config.groups.get(payload.group)
            if cfg is None:
                return JSONResponse({"error": "unknown_group"}, status_code=400)
            predictions: Mapping[TargetKey, Prediction] = {}
            contributions: Mapping[TargetKey, list[dict[str, Any]]] = {}
            key = session_key(payload) if cfg.pin_ttl_s else None
            pin = runtime.pins.get(key) if key else None
            if not payload.request and not payload.text:
                decision = Decision(
                    0, tuple(range(1, len(payload.targets))), "lrp:no-request-content"
                )
                runtime.degraded.labels("no_request").inc()
            elif payload.context.get("imageCount", 0) > 0:
                decision = Decision(
                    0, tuple(range(1, len(payload.targets))), "lrp:image-passthrough"
                )
            else:
                need_uncertainty = (
                    cfg.uncertainty_abstention or cfg.exploration_strategy == "thompson"
                )
                predictions, degradation, contributions, estimates, availability = (
                    await runtime.infer(
                        current,
                        payload,
                        config.deadline_ms / 1000 - (time.monotonic() - started),
                        explain=explain,
                        need_uncertainty=need_uncertainty,
                    )
                )
                entries = current.manifest.get("targets", [])
                trained = {
                    (row["provider"], row["model"])
                    for row in entries
                    if row.get("n_train", 0) >= cfg.min_train_rows and not row.get("skipped")
                }
                if entries:
                    predictions = {
                        key: value for key, value in predictions.items() if key in trained
                    }
                    if not predictions and any((row["provider"], row["model"]) in
                            {target.key for target in payload.targets} for row in entries):
                        cold = parse_cold_start_targets(current.manifest)
                        if not cold or not cfg.cold_start_exploration:
                            runtime.degraded.labels("undertrained").inc()
                            return JSONResponse({"error": "no_trained_target"}, status_code=503)
                cold_starts = parse_cold_start_targets(current.manifest)
                if cfg.cold_start_exploration and cold_starts:
                    predictions, cold_estimates = inject_cold_start_predictions(
                        predictions,
                        payload.targets,
                        cold_starts,
                        min_train_rows=cfg.min_train_rows,
                        trained_keys=trained if entries else set(),
                    )
                    estimates = {**estimates, **cold_estimates}
                    if cold_estimates and availability in (
                        UncertaintyAvailability.UNAVAILABLE,
                        UncertaintyAvailability.DISABLED,
                    ):
                        availability = UncertaintyAvailability.AVAILABLE
                for target in payload.targets:
                    if target.key not in predictions:
                        identity = hashlib.sha256(json.dumps(target.key).encode()).hexdigest()[:16]
                        if identity not in runtime.warned_targets and len(runtime.warned_targets) < 1024:
                            runtime.warned_targets.add(identity)
                            LOG.warning("unknown or excluded model identity: %s", identity)
                pin_active = pin is not None
                cache = resolve_cache_estimate(payload.context, pinned=pin_active)
                latency_map = (
                    merge_latency_evidence(
                        payload.group, cfg, payload.targets, runtime.latency_ledger
                    )
                    if cfg.latency_p95_ms_max is not None
                    else {}
                )
                exploration_allowed = payload.caller.project in cfg.exploration_projects
                rng = random.SystemRandom()
                cold_keys = {c.key for c in cold_starts}
                input_tokens = float(payload.context.get("estimatedTokens", 0))
                cold_start_only = bool(
                    cfg.cold_start_exploration
                    and cold_keys
                    and any(k in predictions for k in cold_keys)
                    and not any(k in trained for k in predictions)
                )
                if availability == UncertaintyAvailability.UNAVAILABLE and need_uncertainty:
                    runtime.degraded.labels("uncertainty_unavailable").inc()
                decision = compose_decision(
                    payload.targets,
                    predictions,
                    cfg,
                    rng,
                    DecisionEvidence(
                        project=payload.caller.project,
                        input_tokens=input_tokens,
                        pin=pin,
                        exploration_allowed=exploration_allowed,
                        latency_evidence=latency_map or None,
                        cache=cache,
                        estimates=estimates,
                        uncertainty_availability=availability,
                        strategy=cfg.exploration_strategy,
                        cold_keys=frozenset(cold_keys),
                        cold_start_only=cold_start_only,
                    ),
                )
                if degradation:
                    runtime.degraded.labels(degradation).inc()
                    decision = Decision(
                        decision.primary, decision.fallbacks, f"lrp:{degradation}-fallback"
                    )
                if key and not explain:
                    runtime.pins.put(key, payload.targets[decision.primary].key, cfg.pin_ttl_s)
            runtime.total.labels(metric_label(decision.label)).inc()
            for target in payload.targets:
                prediction = predictions.get(target.key)
                if prediction is not None:
                    # Stable manifest target identity only, never caller data.
                    metric_key = hashlib.sha256(json.dumps(target.key).encode()).hexdigest()[:16]
                    runtime.quality.labels(metric_key).observe(prediction.quality)
            if not explain:
                return JSONResponse(decision.response())
            floor = effective_quality_floor(cfg, payload.caller.project)
            cache_for_explain = resolve_cache_estimate(payload.context, pinned=pin is not None)
            latency_for_explain = (
                merge_latency_evidence(
                    payload.group, cfg, payload.targets, runtime.latency_ledger
                )
                if cfg.latency_p95_ms_max is not None
                else {}
            )
            explained = []
            for index, target in enumerate(payload.targets):
                pred = predictions.get(target.key)
                explained.append(
                    {
                        "index": index,
                        "provider": target.provider,
                        "model": target.model,
                        "quality": pred.quality if pred else None,
                        "out_tokens": pred.out_tokens if pred else None,
                        "est_cost": estimated_cost(
                            target,
                            pred,
                            float(payload.context.get("estimatedTokens", 0)),
                            cfg,
                            cache=cache_for_explain,
                        )
                        if pred
                        else None,
                        "excluded_reason": None if pred else "unknown_or_undertrained",
                        "feature_importances": contributions.get(target.key, []),
                        "latency_p95_ms": latency_for_explain.get(target.key),
                    }
                )
            return JSONResponse(
                {
                    **decision.response(),
                    "targets": explained,
                    "floor": floor,
                    "group_floor": cfg.quality_floor,
                    "project_floor_override": payload.caller.project in cfg.floors_by_project,
                    "cache_state": cache_for_explain.state,
                    "latency_p95_ms_max": cfg.latency_p95_ms_max,
                }
            )
        except Exception:  # noqa: BLE001 - sanitize all exceptions at the HTTP boundary
            LOG.error("policy request failed: internal_error")
            return JSONResponse({"error": "internal_error"}, status_code=500)
        finally:
            runtime.latency.observe(time.monotonic() - started)

    @app.post("/route")
    async def route_endpoint(request: Request) -> Response:
        return await route(request)

    @app.post("/feedback")
    async def feedback_endpoint(request: Request) -> Response:
        # Authenticated completion feedback updates the optional latency ledger
        # and acknowledges. Selection still does not persist prompts or tokens.
        if not authenticated(request):
            return Response(status_code=401)
        raw = bytearray()
        async for chunk in request.stream():
            raw.extend(chunk)
            if len(raw) > MAX_BODY:
                return JSONResponse({"error": "body_too_large"}, status_code=413)
        try:
            feedback = FeedbackPayload.model_validate_json(raw)
        except (ValidationError, ValueError):
            return JSONResponse({"error": "invalid_request"}, status_code=400)
        selected = feedback.selected_target or {}
        provider = selected.get("provider") if isinstance(selected, dict) else None
        model = selected.get("model") if isinstance(selected, dict) else None
        if (
            feedback.group
            and isinstance(provider, str)
            and isinstance(model, str)
            and provider.strip()
            and model.strip()
        ):
            runtime.latency_ledger.observe(
                feedback.group,
                provider,
                model,
                duration_ms=feedback.latency_ms if feedback.latency_ms > 0 else None,
                ttfb_ms=feedback.ttfb_ms,
            )
        return Response(status_code=204)

    @admin.get("/healthz")
    async def health() -> Response:
        return JSONResponse({"status": "ok"})

    @admin.get("/readyz")
    async def ready() -> Response:
        ready_ok = runtime.bundle is not None
        payload = {
            "ready": ready_ok,
            "uncertainty": (
                "unavailable"
                if runtime.requires_uncertainty_evidence() and runtime.ensemble.empty
                else "ok"
                if runtime.requires_uncertainty_evidence()
                else "disabled"
            ),
        }
        return JSONResponse(payload, status_code=200 if ready_ok else 503)

    @admin.get("/metrics")
    async def metrics() -> Response:
        return Response(
            generate_latest(runtime.registry), media_type="text/plain; version=0.0.4; charset=utf-8"
        )

    if enable_admin:

        @app.post("/explain")
        async def explain_endpoint(request: Request) -> Response:
            return await route(request, explain=True)

        @admin.post("/admin/reload")
        async def reload_endpoint(request: Request) -> Response:
            if not authenticated(request):
                return Response(status_code=401)
            try:
                await asyncio.to_thread(runtime.reload)
            except Exception:  # noqa: BLE001 - preserve old bundle without exposing artifact data
                return JSONResponse({"error": "reload_failed"}, status_code=400)
            return JSONResponse({"version": runtime.bundle.version if runtime.bundle else None})

    return app, admin, runtime


def load_settings(config: Path, deadline_ms: int | None = None) -> ServiceConfig:
    # Configs may be public examples, but must be bounded regular files. Open
    # nonblocking so an accidental FIFO cannot hang startup before validation.
    with os.fdopen(os.open(config, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as stream:
        metadata = os.fstat(stream.fileno())
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_size > 1_048_576:
            raise ValueError("invalid_config_file")
        data = stream.read(1_048_577)
    if len(data) > 1_048_576:
        raise ValueError("invalid_config_file")
    settings = ServiceConfig.model_validate(yaml.safe_load(data))
    if deadline_ms is not None:
        settings = ServiceConfig.model_validate({**settings.model_dump(), "deadline_ms": deadline_ms})
    return settings


def serve(
    *,
    bundle: Path,
    config: Path,
    port: int = 18093,
    admin_port: int = 18094,
    enable_admin: bool = False,
    deadline_ms: int | None = None,
    require_signed: bool = False,
    trusted_keys: Path | None = None,
) -> None:
    from lrp.bundle import load_bundle

    settings = load_settings(config, deadline_ms)
    loaded = load_bundle(
        bundle,
        threads=1,
        require_signed=require_signed,
        trusted_keys=trusted_keys,
    )
    app, admin, runtime = create_apps(
        settings,
        loaded,
        auth=os.environ.get("LRP_POLICY_AUTH_HEADER", ""),
        enable_admin=enable_admin,
        bundle_path=bundle,
        require_signed=require_signed,
        trusted_keys=trusted_keys,
    )
    if enable_admin and hasattr(signal, "SIGHUP"):

        def reload_signal(signum: int, frame: Any) -> None:
            def reload_safe() -> None:
                try:
                    runtime.reload()
                except Exception:  # noqa: BLE001 - signal-thread errors must not expose artifact data
                    LOG.error("bundle reload failed")

            if not runtime.reload_lock.locked():
                threading.Thread(target=reload_safe, daemon=True).start()

        signal.signal(signal.SIGHUP, reload_signal)
    threading.Thread(
        target=uvicorn.run,
        args=(admin,),
        kwargs={
            "host": "127.0.0.1",
            "port": admin_port,
            "access_log": False,
            "log_level": "warning",
        },
        daemon=True,
    ).start()
    uvicorn.run(app, host="127.0.0.1", port=port, access_log=False, log_level="warning")
