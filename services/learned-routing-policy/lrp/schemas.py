# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Bounded public contract; caller identifiers and unknown inventory are discarded."""

from __future__ import annotations

import json
import math
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

TargetKey = tuple[str, str]


class Model(BaseModel):
    model_config = ConfigDict(extra="ignore", allow_inf_nan=False, populate_by_name=True)


class Caller(Model):
    project: str = Field(default="", max_length=256)
    environment: str = Field(default="", max_length=256)


class Target(Model):
    provider: str = Field(min_length=1, max_length=256)
    model: str = Field(min_length=1, max_length=512)
    model_ref: str = Field(default="", alias="modelRef", max_length=512)
    dialect: str = Field(default="openai-chat", max_length=64)
    tier: str = Field(default="", max_length=64)
    weight: float = Field(default=1, ge=0)
    input_price: float | None = Field(default=None, alias="inputPricePerMillionUsd", ge=0)
    output_price: float | None = Field(default=None, alias="outputPricePerMillionUsd", ge=0)
    # Optional catalog cached-input price. Used for selection estimates only when
    # trustworthy prompt-cache state is also present; never invents savings.
    cached_input_price: float | None = Field(
        default=None, alias="cachedInputPricePerMillionUsd", ge=0
    )

    @property
    def key(self) -> TargetKey:
        return self.provider, self.model


class Payload(Model):
    group: str = Field(min_length=1, max_length=256)
    targets: list[Target] = Field(min_length=1, max_length=128)
    context: dict[str, Any] = Field(default_factory=dict)
    request: dict[str, Any] | None = None
    text: str | None = Field(default=None, max_length=1_100_000)
    caller: Caller = Field(default_factory=Caller)
    input_modalities: list[str] = Field(default_factory=list, alias="inputModalities")
    verifier_hint: VerifierHint | None = Field(default=None, alias="verifierHint")

    @field_validator("request")
    @classmethod
    def discard_raw_inventory(cls, value: dict[str, Any] | None) -> dict[str, Any] | None:
        if value is None:
            return None
        allowed = {
            "system",
            "messages",
            "input",
            "input_parts",
            "tools",
            "max_tokens",
            "temperature",
            "stream",
            "stop",
            "reasoning",
            "response_format",
        }
        return {key: item for key, item in value.items() if key in allowed}

    @model_validator(mode="after")
    def reject_ambiguous_skins(self) -> Payload:
        seen: dict[TargetKey, tuple[str, str]] = {}
        for target in self.targets:
            skin = target.dialect, target.model_ref
            if target.key in seen and seen[target.key] != skin:
                raise ValueError("ambiguous target identity across dialect or catalog aliases")
            seen[target.key] = skin
        return self


class VerifierHint(Model):
    """Caller-supplied offline evaluation metadata. Never an executable command."""

    kind: Literal["none", "exact", "regex", "json_schema"] = "none"
    version: str = Field(default="", max_length=64)
    spec: dict[str, Any] = Field(default_factory=dict)

    @model_validator(mode="after")
    def bounded_spec(self) -> VerifierHint:
        encoded = json.dumps(self.spec, separators=(",", ":"), sort_keys=True)
        if len(encoded) > 65536:
            raise ValueError("verifier hint spec exceeds size limit")
        if self.kind == "none" and self.spec:
            raise ValueError("verifier hint kind none must omit spec")
        required = {
            "none": set(),
            "exact": {"expected"},
            "regex": {"pattern"},
            "json_schema": {"schema"},
        }[self.kind]
        if set(self.spec) != required:
            raise ValueError("verifier hint spec keys are invalid")
        return self


class FeedbackPayload(Model):
    """Bounded post-completion callback body from the router."""

    schema_version: Literal["external_policy.feedback.v1"] = Field(
        default="external_policy.feedback.v1", alias="schemaVersion"
    )
    request_id: str = Field(alias="requestId", min_length=1, max_length=128)
    group: str = Field(default="", max_length=256)
    conversation_key: str = Field(default="", alias="conversationKey", max_length=128)
    status: int = Field(ge=0, le=599)
    error_class: str = Field(default="", alias="errorClass", max_length=128)
    selected_target: dict[str, Any] | None = Field(default=None, alias="selectedTarget")
    usage: dict[str, Any] | None = None
    cost_usd: dict[str, float] | None = Field(default=None, alias="costUsd")
    latency_ms: float = Field(default=0, alias="latencyMs", ge=0)
    ttfb_ms: float | None = Field(default=None, alias="ttfbMs", ge=0)
    class_label: str = Field(default="", alias="classLabel", max_length=64)
    completed_at: str = Field(default="", alias="completedAt", max_length=64)


class GroupConfig(Model):
    quality_floor: float = Field(default=0.8, ge=0, le=1)
    # Deployment-defined project → floor overrides. Unknown projects fall back
    # to quality_floor. Keys are operator-authored; never hardcode production IDs.
    floors_by_project: dict[str, float] = Field(default_factory=dict)
    explore_rate: float = Field(default=0, ge=0, le=1)
    pin_ttl_s: float = Field(default=0, ge=0, le=86400)
    min_train_rows: int = Field(default=200, ge=200)
    mode: Literal["enforce", "shadow"] = "enforce"
    # Explicit operator evidence can distinguish omitted known-free prices from unknown.
    zero_price_targets: list[tuple[str, str]] = Field(default_factory=list)
    exploration_projects: list[str] = Field(default_factory=list)
    # Optional upstream latency gate from stored TTFB/duration evidence (p95 ms).
    # None disables the constraint. Cold-start/unknown targets follow unknown_latency.
    latency_p95_ms_max: float | None = Field(default=None, ge=0)
    latency_metric: Literal["duration", "ttfb"] = "duration"
    unknown_latency: Literal["allow", "exclude"] = "allow"
    # Optional static p95 evidence keyed as "provider/model" (operator/eval seeded).
    latency_evidence: dict[str, float] = Field(default_factory=dict)
    # Wave-2 uncertainty / explore (defaults preserve epsilon-greedy behavior).
    uncertainty_abstention: bool = False
    uncertainty_threshold: float = Field(default=0.15, ge=0, le=1)
    exploration_strategy: Literal["epsilon_greedy", "thompson"] = "epsilon_greedy"
    auto_shadow_on_drift: bool = False
    psi_threshold: float = Field(default=0.25, ge=0)
    embedding_drift_threshold: float = Field(default=0.15, ge=0)
    cold_start_exploration: bool = False
    # Optional (provider, model) abstention anchor; when unset, first fallback is used.
    abstention_anchor: tuple[str, str] | None = None

    @field_validator("floors_by_project")
    @classmethod
    def validate_project_floors(cls, value: dict[str, float]) -> dict[str, float]:
        cleaned: dict[str, float] = {}
        for raw_key, raw_floor in value.items():
            key = str(raw_key).strip()
            if not key or len(key) > 256:
                raise ValueError("project floor keys must be nonempty and ≤256 characters")
            floor = float(raw_floor)
            if not (0.0 <= floor <= 1.0) or not math.isfinite(floor):
                raise ValueError("project floor values must be finite in [0, 1]")
            cleaned[key] = floor
        return cleaned

    @field_validator("latency_evidence")
    @classmethod
    def validate_latency_evidence(cls, value: dict[str, float]) -> dict[str, float]:
        cleaned: dict[str, float] = {}
        for raw_key, raw_ms in value.items():
            key = str(raw_key).strip()
            if not key or "/" not in key or len(key) > 768:
                raise ValueError("latency evidence keys must be provider/model identities")
            ms = float(raw_ms)
            if not math.isfinite(ms) or ms < 0:
                raise ValueError("latency evidence values must be finite and ≥0")
            cleaned[key] = ms
        return cleaned

    @field_validator("exploration_projects")
    @classmethod
    def validate_exploration_projects(cls, value: list[str]) -> list[str]:
        cleaned: list[str] = []
        for item in value:
            key = str(item).strip()
            if not key or len(key) > 256:
                raise ValueError("exploration project names must be nonempty and ≤256 characters")
            cleaned.append(key)
        return cleaned


class EmbeddingServiceConfig(Model):
    """Serve-time embedding defaults. Bundle artifacts remain authoritative."""

    backend: Literal["onnxruntime", "sentence-transformers"] = "onnxruntime"
    model: str = Field(default="", max_length=2048)
    max_seq_len: int = Field(default=512, ge=1, le=8192)
    batch_size: int = Field(default=1, ge=1, le=64)


class ComputeConfig(Model):
    device: str = Field(default="cpu", max_length=32)
    strict_device: bool = False

    @field_validator("device")
    @classmethod
    def validate_device(cls, value: str) -> str:
        from lrp.device import parse_device_request

        parse_device_request(value)
        return str(value).strip().lower()


class ServiceConfig(Model):
    groups: dict[str, GroupConfig]
    max_pins: int = Field(default=10000, ge=1, le=100000)
    inference_workers: int = Field(default=2, ge=1, le=4)
    # Leave transport headroom below the router's 5000 ms policy timeout ceiling.
    deadline_ms: int = Field(default=200, ge=1, le=4500)
    embedding: EmbeddingServiceConfig = Field(default_factory=EmbeddingServiceConfig)
    compute: ComputeConfig = Field(default_factory=ComputeConfig)


class RequestRow(Model):
    schema_version: Literal["lrp.request.v1"] = "lrp.request.v1"
    request_id: str
    captured_at: str
    source: str
    group: str
    dialect: str = "openai-chat"
    caller: Caller = Field(default_factory=Caller)
    session_key: str = ""
    turn_index: int = Field(default=0, ge=0)
    messages: list[dict[str, Any]] = Field(default_factory=list)
    system: str = ""
    input: str = ""
    tools: list[dict[str, Any]] = Field(default_factory=list)
    response_format: dict[str, Any] | None = None
    max_tokens: int | None = Field(default=None, ge=0)
    context: dict[str, Any] = Field(default_factory=dict)
    verifier: dict[str, Any] = Field(default_factory=lambda: {"kind": "none"})
    # Provider-scoped upstream extras (for example OpenAI prompt_cache_options).
    # Fanout applies only the map entry matching the selected target provider.
    provider_request_fields: dict[str, dict[str, Any]] = Field(default_factory=dict)


class OfflineTarget(Model):
    provider: str
    model: str
    model_ref: str = ""


class Usage(Model):
    input_tokens: int = Field(ge=0)
    output_tokens: int = Field(ge=0)


class Pricing(Model):
    input_per_m_usd: float = Field(ge=0)
    output_per_m_usd: float = Field(ge=0)
    source: str
    fetched_at: str


class ResponseAttempt(Model):
    sequence: int = Field(ge=1)
    status: str
    error_class: str | None = None
    http_status: int | None = None
    duration_ms: float = Field(ge=0)
    ttfb_ms: float | None = Field(default=None, ge=0)
    usage: Usage | None = None
    cost_usd: float | None = Field(default=None, ge=0)
    billed_cost_usd: float | None = Field(default=None, ge=0)


class ResponseRow(Model):
    schema_version: Literal["lrp.response.v1"] = "lrp.response.v1"
    request_id: str
    target: OfflineTarget
    source: str
    started_at: str
    ttfb_ms: float | None = Field(default=None, ge=0)
    duration_ms: float = Field(ge=0)
    status: Literal["ok", "upstream_error", "timeout", "refused", "ineligible"]
    error_class: str | None = None
    content: str = ""
    tool_calls: list[dict[str, Any]] = Field(default_factory=list)
    finish_reason: str = ""
    usage: Usage | None = None
    pricing: Pricing | None = None
    cost_usd: float | None = Field(default=None, ge=0)
    billed_cost_usd: float | None = Field(default=None, ge=0)
    router_request_id: str | None = None
    serving_provider: str | None = None
    request_hash: str = ""
    config_hash: str = ""
    attempts: list[ResponseAttempt] = Field(default_factory=list)

    @model_validator(mode="after")
    def cost_matches(self) -> ResponseRow:
        if self.usage is not None and self.pricing is not None and self.cost_usd is not None:
            expected = (
                self.usage.input_tokens * self.pricing.input_per_m_usd
                + self.usage.output_tokens * self.pricing.output_per_m_usd
            ) / 1e6
            if abs(expected - self.cost_usd) > 1e-9:
                raise ValueError("stored cost does not match stored usage and pricing")
        return self


class JudgmentRow(Model):
    schema_version: Literal["lrp.judgment.v1"] = "lrp.judgment.v1"
    request_id: str
    target: OfflineTarget
    source: str
    method: str
    quality: float | None = Field(default=None, ge=0, le=1)
    detail: dict[str, Any] = Field(default_factory=dict)
    judge_model: str = ""
    judge_cost_usd: float | None = Field(default=None, ge=0)
    judged_at: str
    cache_key: str = ""
