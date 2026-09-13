# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Thompson sampling exploration and cold-start helpers (#25, #30).

Thompson sampling draws quality from a Normal(mean, std) posterior derived from
the bootstrap ensemble, then runs the same cheapest-above-floor policy.
Exploration remains restricted to approved calibration traffic via
``exploration_allowed`` (caller project allow-list). Seeded epsilon-greedy stays
available for A/B comparison.

Cold start (#30): targets with a 200-prompt anchor seed and
``n_train < min_train_rows`` may contribute Bradley-Terry baseline predictions
and receive Thompson-only exploration. They never override router eligibility.
"""

from __future__ import annotations

import math
import random
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from typing import Any, Literal

from lrp.policy import CacheEstimate, Decision, Prediction, decide
from lrp.schemas import GroupConfig, Target, TargetKey
from lrp.uncertainty import (
    DEFAULT_UNCERTAINTY_THRESHOLD,
    UncertaintyAvailability,
    UncertaintyEstimate,
    ensemble_stats,
)

ExplorationStrategy = Literal["epsilon_greedy", "thompson"]

# Reviewed cold-start floor: same numeric minimum as ordinary training readiness.
COLD_START_ANCHOR_PROMPTS = 200
# Until a learned model exists, treat cold-start BT quality as maximally uncertain.
COLD_START_DEFAULT_STD = DEFAULT_UNCERTAINTY_THRESHOLD * 2


def _decide_kwargs(
    *,
    input_tokens: float = 0,
    pin: TargetKey | None = None,
    exploration_allowed: bool = True,
    project: str = "",
    latency_evidence: Mapping[TargetKey, float] | None = None,
    cache: CacheEstimate | None = None,
) -> dict[str, Any]:
    return {
        "input_tokens": input_tokens,
        "pin": pin,
        "exploration_allowed": exploration_allowed,
        "project": project,
        "latency_evidence": latency_evidence,
        "cache": cache,
    }


@dataclass(frozen=True)
class ColdStartTarget:
    provider: str
    model: str
    bt_strength: float
    n_anchor_prompts: int
    mean_out_tokens: float = 0.0

    @property
    def key(self) -> TargetKey:
        return self.provider, self.model


def sample_thompson_quality(
    mean: float,
    std: float,
    rng: random.Random,
    *,
    floor: float = 0.0,
    ceiling: float = 1.0,
) -> float:
    """Draw one posterior quality sample; clip to [0, 1]."""
    sigma = max(float(std), 0.0)
    if not math.isfinite(mean) or not math.isfinite(sigma):
        return 0.5
    if sigma <= 0:
        draw = mean
    else:
        # Box-Muller via Random.gauss for reproducibility with Random(seed).
        draw = rng.gauss(mean, sigma)
    return float(min(max(draw, floor), ceiling))


def thompson_predictions(
    predictions: Mapping[TargetKey, Prediction],
    estimates: Mapping[TargetKey, UncertaintyEstimate],
    rng: random.Random,
) -> dict[TargetKey, Prediction]:
    """Replace point estimates with Thompson samples where uncertainty exists."""
    sampled: dict[TargetKey, Prediction] = {}
    for key, prediction in predictions.items():
        estimate = estimates.get(key)
        if estimate is None or estimate.n_members < 1:
            sampled[key] = prediction
            continue
        quality = sample_thompson_quality(estimate.mean, estimate.std, rng)
        sampled[key] = Prediction(quality, prediction.out_tokens)
    return sampled


def decide_with_exploration(
    targets: Sequence[Target],
    predictions: Mapping[TargetKey, Prediction],
    cfg: GroupConfig,
    rng: random.Random,
    *,
    estimates: Mapping[TargetKey, UncertaintyEstimate] | None = None,
    input_tokens: float = 0,
    pin: TargetKey | None = None,
    exploration_allowed: bool = True,
    strategy: ExplorationStrategy = "epsilon_greedy",
    project: str = "",
    latency_evidence: Mapping[TargetKey, float] | None = None,
    cache: CacheEstimate | None = None,
    uncertainty_availability: UncertaintyAvailability | None = None,
) -> Decision:
    """Epsilon-greedy or Thompson sampling over calibrated uncertainty.

    Thompson uses a single exploration gate per request. A rejected Thompson
    draw (or missing estimates) reaches deterministic exploitation without a
    second epsilon draw. Ordinary epsilon-greedy is unchanged when selected.
    """
    base = _decide_kwargs(
        input_tokens=input_tokens,
        pin=pin,
        exploration_allowed=exploration_allowed,
        project=project,
        latency_evidence=latency_evidence,
        cache=cache,
    )
    if strategy != "thompson":
        return decide(targets, predictions, cfg, rng, **base)

    # Thompson path: one gate only. Never fall through to epsilon-greedy.
    frozen = cfg.model_copy(update={"explore_rate": 0.0})
    exploit = {**base, "exploration_allowed": False}
    if not exploration_allowed or cfg.explore_rate <= 0:
        return decide(targets, predictions, frozen, rng, **exploit)
    estimates = estimates or {}
    availability = uncertainty_availability
    if availability is None:
        availability = (
            UncertaintyAvailability.AVAILABLE
            if estimates
            else UncertaintyAvailability.UNAVAILABLE
        )
    if availability in (
        UncertaintyAvailability.UNAVAILABLE,
        UncertaintyAvailability.INSUFFICIENT,
    ) or not estimates:
        # Align with abstention fail-closed: missing estimates do not open epsilon.
        return decide(targets, predictions, frozen, rng, **exploit)
    if rng.random() >= cfg.explore_rate:
        return decide(targets, predictions, frozen, rng, **exploit)
    sampled = thompson_predictions(predictions, estimates, rng)
    decision = decide(targets, sampled, frozen, rng, **exploit)
    return Decision(decision.primary, decision.fallbacks, "lrp:explore-thompson")


def compare_exploration_strategies(
    targets: Sequence[Target],
    predictions: Mapping[TargetKey, Prediction],
    estimates: Mapping[TargetKey, UncertaintyEstimate],
    cfg: GroupConfig,
    *,
    seed: int = 42,
    draws: int = 1000,
) -> dict[str, Any]:
    """Synthetic wiring: seeded Thompson vs epsilon-greedy selection rates."""
    if draws < 1 or draws > 100_000:
        raise ValueError("invalid comparison draw count")
    epsilon_counts = [0] * len(targets)
    thompson_counts = [0] * len(targets)
    explore_cfg = cfg.model_copy(
        update={"explore_rate": 1.0 if cfg.explore_rate <= 0 else cfg.explore_rate}
    )
    for i in range(draws):
        eps = decide_with_exploration(
            targets,
            predictions,
            explore_cfg,
            random.Random(seed + i),
            estimates=estimates,
            exploration_allowed=True,
            strategy="epsilon_greedy",
        )
        th = decide_with_exploration(
            targets,
            predictions,
            explore_cfg,
            random.Random(seed + i),
            estimates=estimates,
            exploration_allowed=True,
            strategy="thompson",
        )
        epsilon_counts[eps.primary] += 1
        thompson_counts[th.primary] += 1
    return {
        "seed": seed,
        "draws": draws,
        "epsilon_greedy_rates": [c / draws for c in epsilon_counts],
        "thompson_rates": [c / draws for c in thompson_counts],
        "labels": {
            "epsilon_greedy": "lrp:explore",
            "thompson": "lrp:explore-thompson",
        },
    }


def cold_start_entry(
    *,
    provider: str,
    model: str,
    bt_strength: float,
    n_anchor_prompts: int,
    mean_out_tokens: float = 0.0,
    min_prompts: int = COLD_START_ANCHOR_PROMPTS,
) -> dict[str, Any] | None:
    """Build a manifest-safe cold-start record when the 200-prompt seed is met."""
    if n_anchor_prompts < min_prompts:
        return None
    if not math.isfinite(bt_strength) or not 0 <= bt_strength <= 1:
        return None
    return {
        "provider": provider,
        "model": model,
        "cold_start": True,
        "n_anchor_prompts": int(n_anchor_prompts),
        "bt_strength": float(bt_strength),
        "mean_out_tokens": float(max(0.0, mean_out_tokens)),
        "skipped": "cold_start_anchor_seed",
    }


def parse_cold_start_targets(manifest: Mapping[str, Any]) -> list[ColdStartTarget]:
    """Extract cold-start targets from a bundle manifest (skipped + seeded)."""
    result: list[ColdStartTarget] = []
    for entry in manifest.get("targets", []):
        if not isinstance(entry, dict) or not entry.get("cold_start"):
            continue
        strength = float(entry.get("bt_strength", 0.5))
        raw_prompts = entry.get("n_anchor_prompts")
        if raw_prompts is None:
            raw_prompts = entry.get("n_train", 0)
        prompts = int(raw_prompts if raw_prompts is not None else 0)
        if prompts < COLD_START_ANCHOR_PROMPTS:
            continue
        result.append(
            ColdStartTarget(
                provider=str(entry["provider"]),
                model=str(entry["model"]),
                bt_strength=strength,
                n_anchor_prompts=prompts,
                mean_out_tokens=float(entry.get("mean_out_tokens", 0.0)),
            )
        )
    return result


def inject_cold_start_predictions(
    predictions: Mapping[TargetKey, Prediction],
    eligible: Sequence[Target],
    cold_starts: Sequence[ColdStartTarget],
    *,
    min_train_rows: int = 200,
    trained_keys: set[TargetKey] | None = None,
) -> tuple[dict[TargetKey, Prediction], dict[TargetKey, UncertaintyEstimate]]:
    """Add BT predictions for cold-start targets that the router already made eligible.

    Never invents targets outside ``eligible``. Fully trained keys are left alone.
    """
    merged = dict(predictions)
    estimates: dict[TargetKey, UncertaintyEstimate] = {}
    eligible_keys = {target.key for target in eligible}
    trained = trained_keys or set()
    for cold in cold_starts:
        if cold.key not in eligible_keys:
            continue
        if cold.key in trained:
            continue
        if cold.key in merged:
            continue
        if cold.n_anchor_prompts < COLD_START_ANCHOR_PROMPTS:
            continue
        # Cold-start must remain below the normal training minimum.
        if cold.n_anchor_prompts >= min_train_rows and cold.key in trained:
            continue
        merged[cold.key] = Prediction(cold.bt_strength, cold.mean_out_tokens)
        estimates[cold.key] = ensemble_stats(
            [
                cold.bt_strength,
                min(1.0, cold.bt_strength + COLD_START_DEFAULT_STD),
                max(0.0, cold.bt_strength - COLD_START_DEFAULT_STD),
            ]
        )
    return merged, estimates


def cold_start_exploration_only(
    targets: Sequence[Target],
    predictions: Mapping[TargetKey, Prediction],
    estimates: Mapping[TargetKey, UncertaintyEstimate],
    cold_keys: set[TargetKey],
    cfg: GroupConfig,
    rng: random.Random,
    *,
    input_tokens: float = 0,
    pin: TargetKey | None = None,
    exploration_allowed: bool = True,
    project: str = "",
    latency_evidence: Mapping[TargetKey, float] | None = None,
    cache: CacheEstimate | None = None,
) -> Decision:
    """Restrict exploration to cold-start keys via Thompson; otherwise enforce."""
    base = _decide_kwargs(
        input_tokens=input_tokens,
        pin=pin,
        exploration_allowed=False,
        project=project,
        latency_evidence=latency_evidence,
        cache=cache,
    )
    frozen = cfg.model_copy(update={"explore_rate": 0.0})
    if not exploration_allowed or not cold_keys or cfg.explore_rate <= 0:
        return decide(targets, predictions, frozen, rng, **base)
    if rng.random() >= cfg.explore_rate:
        return decide(targets, predictions, frozen, rng, **base)
    # Explore only among cold-start eligible predictions (never override eligibility:
    # targets already filtered by the router; we only drop non-cold from the
    # prediction map so decide cannot prefer a fully-trained peer during seed).
    cold_preds = {
        key: value for key, value in predictions.items() if key in cold_keys
    }
    cold_est = {key: value for key, value in estimates.items() if key in cold_keys}
    if not cold_preds:
        return decide(targets, predictions, frozen, rng, **base)
    sampled = thompson_predictions(cold_preds, cold_est, rng)
    decision = decide(targets, sampled, frozen, rng, **base)
    return Decision(decision.primary, decision.fallbacks, "lrp:explore-cold-start")
