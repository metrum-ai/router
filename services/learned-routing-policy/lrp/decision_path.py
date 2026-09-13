# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Shared composed policy decision path for serve and offline eval (#158).

Transport, authentication, and I/O stay in callers. This module owns the
deterministic composition of exploration, abstention, and governed evidence
(project floors, latency, cache, pins) so promotion replay matches serving.
"""

from __future__ import annotations

import random
from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from typing import Any, Literal

from lrp.policy import CacheEstimate, Decision, Prediction
from lrp.schemas import GroupConfig, Target, TargetKey
from lrp.thompson import (
    ExplorationStrategy,
    cold_start_exploration_only,
    decide_with_exploration,
)
from lrp.uncertainty import (
    UncertaintyAvailability,
    UncertaintyEstimate,
    apply_abstention,
)

ExplorationMode = Literal["disabled", "seeded", "live"]


@dataclass(frozen=True)
class DecisionEvidence:
    """Governed evidence available for one composed decision."""

    project: str = ""
    input_tokens: float = 0.0
    pin: TargetKey | None = None
    exploration_allowed: bool = False
    latency_evidence: Mapping[TargetKey, float] | None = None
    cache: CacheEstimate | None = None
    estimates: Mapping[TargetKey, UncertaintyEstimate] | None = None
    uncertainty_availability: UncertaintyAvailability = UncertaintyAvailability.DISABLED
    strategy: ExplorationStrategy = "epsilon_greedy"
    cold_keys: frozenset[TargetKey] = field(default_factory=frozenset)
    cold_start_only: bool = False


@dataclass(frozen=True)
class ApplicabilityReport:
    """Whether a group configuration can be evaluated from supplied evidence."""

    supported: bool
    unsupported: tuple[str, ...]
    exploration_mode: ExplorationMode
    notes: tuple[str, ...] = ()

    def as_dict(self) -> dict[str, Any]:
        return {
            "supported": self.supported,
            "unsupported": list(self.unsupported),
            "exploration_mode": self.exploration_mode,
            "notes": list(self.notes),
        }


def compose_decision(
    targets: Sequence[Target],
    predictions: Mapping[TargetKey, Prediction],
    cfg: GroupConfig,
    rng: random.Random,
    evidence: DecisionEvidence,
) -> Decision:
    """Run the same exploration + abstention composition used at serve time."""
    estimates = dict(evidence.estimates or {})
    cold_keys = set(evidence.cold_keys)
    if evidence.cold_start_only and cold_keys:
        decision = cold_start_exploration_only(
            targets,
            predictions,
            estimates,
            cold_keys,
            cfg,
            rng,
            input_tokens=evidence.input_tokens,
            pin=evidence.pin,
            exploration_allowed=evidence.exploration_allowed,
            project=evidence.project,
            latency_evidence=evidence.latency_evidence,
            cache=evidence.cache,
        )
    else:
        decision = decide_with_exploration(
            targets,
            predictions,
            cfg,
            rng,
            estimates=estimates,
            input_tokens=evidence.input_tokens,
            pin=evidence.pin,
            exploration_allowed=evidence.exploration_allowed,
            strategy=evidence.strategy,
            project=evidence.project,
            latency_evidence=evidence.latency_evidence,
            cache=evidence.cache,
            uncertainty_availability=evidence.uncertainty_availability,
        )
    return apply_abstention(
        decision,
        targets,
        estimates,
        anchor=cfg.abstention_anchor,
        threshold=cfg.uncertainty_threshold,
        enabled=cfg.uncertainty_abstention,
        availability=evidence.uncertainty_availability,
    )


def evaluation_applicability(
    cfg: GroupConfig,
    *,
    has_project: bool,
    has_latency_evidence: bool,
    has_cache_evidence: bool,
    has_pin_ordering: bool,
    has_uncertainty: bool,
    exploration_mode: ExplorationMode = "disabled",
) -> ApplicabilityReport:
    """Report which configured behaviors cannot be validated from eval evidence."""
    unsupported: list[str] = []
    notes: list[str] = []
    if cfg.floors_by_project and not has_project:
        unsupported.append("project_floors_require_caller_project")
    if cfg.latency_p95_ms_max is not None and not has_latency_evidence:
        unsupported.append("latency_constraint_requires_evidence")
    if not has_cache_evidence:
        notes.append("cache_evidence_absent_defaults_unknown")
    if cfg.pin_ttl_s > 0 and not has_pin_ordering:
        unsupported.append("pins_require_ordered_session_evidence")
    if cfg.uncertainty_abstention and not has_uncertainty:
        unsupported.append("uncertainty_abstention_requires_ensemble")
    if cfg.exploration_strategy == "thompson" and not has_uncertainty:
        unsupported.append("thompson_requires_ensemble_estimates")
    if exploration_mode == "disabled" and cfg.explore_rate > 0:
        notes.append(
            "exploration_disabled_for_eval_does_not_prove_stochastic_production"
        )
    return ApplicabilityReport(
        supported=not unsupported,
        unsupported=tuple(unsupported),
        exploration_mode=exploration_mode,
        notes=tuple(notes),
    )


def static_latency_map(cfg: GroupConfig) -> dict[TargetKey, float]:
    """Parse operator-seeded provider/model latency evidence from group config."""
    merged: dict[TargetKey, float] = {}
    for raw_key, ms in cfg.latency_evidence.items():
        text = raw_key.strip()
        if "/" not in text:
            continue
        provider, model = text.split("/", 1)
        provider, model = provider.strip(), model.strip()
        if provider and model:
            merged[(provider, model)] = float(ms)
    return merged
