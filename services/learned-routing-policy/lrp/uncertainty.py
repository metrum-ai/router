# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Bootstrap ensemble uncertainty and abstention (#24, #158).

Five LightGBM quality models are fit on bootstrap resamples. At serve time the
calibrated standard deviation across members is compared to a reviewed
threshold; when the would-be primary is too uncertain, the policy abstains to
the configured anchor (or first eligible fallback) with label ``lrp:uncertain``.

When abstention is enabled but ensemble evidence is unavailable or insufficient,
selection fails closed to the same conservative anchor path with
``lrp:uncertain-unavailable``. Missing evidence is never treated as confidence.
"""

from __future__ import annotations

import math
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from enum import Enum
from pathlib import Path
from typing import Any

import numpy as np

from lrp.policy import Decision, Prediction, safe_label
from lrp.schemas import Target, TargetKey

# Reviewed defaults from issue #15 deferred Wave-2 design.
ENSEMBLE_SIZE = 5
DEFAULT_UNCERTAINTY_THRESHOLD = 0.15


class UncertaintyAvailability(Enum):
    """Operator-visible uncertainty evidence states (#158 item 6)."""

    DISABLED = "disabled"
    AVAILABLE = "available"
    UNAVAILABLE = "unavailable"
    INSUFFICIENT = "insufficient"


@dataclass(frozen=True)
class UncertaintyEstimate:
    """Bounded per-target uncertainty; never carries prompts or credentials."""

    mean: float
    std: float
    n_members: int

    def above_threshold(self, threshold: float = DEFAULT_UNCERTAINTY_THRESHOLD) -> bool:
        return (
            math.isfinite(self.std)
            and self.std > threshold
            and self.n_members >= 2
        )

    def usable_for_threshold(self) -> bool:
        """True when member count can support a threshold comparison."""
        return self.n_members >= 2 and math.isfinite(self.std) and math.isfinite(self.mean)


def ensemble_stats(scores: Sequence[float]) -> UncertaintyEstimate:
    """Return mean/std of finite calibrated ensemble member scores."""
    finite = [float(v) for v in scores if math.isfinite(float(v))]
    if not finite:
        return UncertaintyEstimate(mean=0.5, std=1.0, n_members=0)
    arr = np.asarray(finite, dtype=float)
    std = float(arr.std(ddof=0)) if len(arr) > 1 else 0.0
    return UncertaintyEstimate(
        mean=float(arr.mean()),
        std=std if math.isfinite(std) else 1.0,
        n_members=len(arr),
    )


def classify_estimate(
    estimate: UncertaintyEstimate | None,
    *,
    enabled: bool,
) -> UncertaintyAvailability:
    """Map a per-target estimate into an availability state."""
    if not enabled:
        return UncertaintyAvailability.DISABLED
    if estimate is None:
        return UncertaintyAvailability.UNAVAILABLE
    if estimate.n_members < 2 or not math.isfinite(estimate.std):
        return UncertaintyAvailability.INSUFFICIENT
    return UncertaintyAvailability.AVAILABLE


def calibrate_raw(raw: float, xs: Sequence[float], ys: Sequence[float]) -> float:
    """Piecewise-linear isotonic lookup matching bundle serving."""
    if not xs or len(xs) != len(ys):
        return float(np.clip(raw, 0, 1))
    return float(np.clip(np.interp(raw, xs, ys), 0, 1))


def predict_ensemble_quality(
    members: Sequence[Any],
    vector: np.ndarray,
    calibrations: Sequence[tuple[Sequence[float], Sequence[float]]],
    *,
    threads: int = 1,
) -> UncertaintyEstimate:
    """Predict calibrated quality across bootstrap members for one feature vector."""
    scores: list[float] = []
    row = np.asarray(vector, dtype=np.float32)[None, :]
    for index, model in enumerate(members):
        raw = float(model.predict(row, num_threads=threads)[0])
        if index < len(calibrations):
            xs, ys = calibrations[index]
            scores.append(calibrate_raw(raw, xs, ys))
        else:
            scores.append(float(np.clip(raw, 0, 1)))
    return ensemble_stats(scores)


def should_abstain(
    estimate: UncertaintyEstimate | None,
    *,
    threshold: float = DEFAULT_UNCERTAINTY_THRESHOLD,
    enabled: bool = True,
    availability: UncertaintyAvailability | None = None,
) -> bool:
    """Return whether to abstain.

    When abstention is enabled and evidence is missing or insufficient, abstain
    (fail closed). A ``None`` estimate with enabled=True is not confidence.
    """
    if not enabled:
        return False
    state = availability
    if state is None:
        state = classify_estimate(estimate, enabled=True)
    if state in (
        UncertaintyAvailability.UNAVAILABLE,
        UncertaintyAvailability.INSUFFICIENT,
    ):
        return True
    if estimate is None:
        return True
    return estimate.above_threshold(threshold)


def find_anchor_index(
    targets: Sequence[Target],
    anchor: TargetKey | None,
) -> int | None:
    if anchor is None:
        return None
    for index, target in enumerate(targets):
        if target.key == anchor:
            return index
    return None


def _route_to_anchor(
    decision: Decision,
    targets: Sequence[Target],
    anchor: TargetKey | None,
    label: str,
) -> Decision:
    anchor_index = find_anchor_index(targets, anchor)
    if anchor_index is None:
        for candidate in decision.fallbacks:
            if candidate != decision.primary:
                anchor_index = candidate
                break
    if anchor_index is None or anchor_index == decision.primary:
        return Decision(
            decision.primary,
            decision.fallbacks,
            safe_label(label),
        )
    fallbacks = tuple(
        i for i in (decision.primary, *decision.fallbacks) if i != anchor_index
    )
    return Decision(anchor_index, fallbacks, safe_label(label))


def apply_abstention(
    decision: Decision,
    targets: Sequence[Target],
    estimates: Mapping[TargetKey, UncertaintyEstimate],
    *,
    anchor: TargetKey | None = None,
    threshold: float = DEFAULT_UNCERTAINTY_THRESHOLD,
    enabled: bool = True,
    availability: UncertaintyAvailability | None = None,
) -> Decision:
    """If primary uncertainty exceeds threshold, route to anchor (or first fallback).

    Missing or insufficient ensemble evidence with abstention enabled fails closed
    to the conservative anchor path. Never invents targets outside ``targets``.
    """
    if not enabled or not targets:
        return decision
    state = availability or UncertaintyAvailability.AVAILABLE
    if state in (
        UncertaintyAvailability.UNAVAILABLE,
        UncertaintyAvailability.INSUFFICIENT,
    ):
        return _route_to_anchor(
            decision, targets, anchor, "lrp:uncertain-unavailable"
        )
    primary = targets[decision.primary]
    estimate = estimates.get(primary.key)
    local = classify_estimate(estimate, enabled=True)
    if local in (
        UncertaintyAvailability.UNAVAILABLE,
        UncertaintyAvailability.INSUFFICIENT,
    ):
        return _route_to_anchor(
            decision, targets, anchor, "lrp:uncertain-unavailable"
        )
    if not should_abstain(
        estimate, threshold=threshold, enabled=True, availability=local
    ):
        return decision
    return _route_to_anchor(decision, targets, anchor, "lrp:uncertain")


def merge_mean_predictions(
    base: Mapping[TargetKey, Prediction],
    estimates: Mapping[TargetKey, UncertaintyEstimate],
) -> dict[TargetKey, Prediction]:
    """Replace point qualities with ensemble means when available."""
    merged = dict(base)
    for key, estimate in estimates.items():
        if key not in merged:
            continue
        out_tokens = merged[key].out_tokens
        merged[key] = Prediction(estimate.mean, out_tokens)
    return merged


def compare_abstention_cost(
    *,
    enforce_cost: float,
    abstain_cost: float,
    enforce_quality: float,
    abstain_quality: float,
    enforce_latency_ms: float,
    abstain_latency_ms: float,
) -> dict[str, float | str]:
    """Safe scalar comparison of abstention vs v1 enforce path (synthetic wiring)."""
    return {
        "cost_delta_usd": float(abstain_cost - enforce_cost),
        "quality_delta": float(abstain_quality - enforce_quality),
        "latency_delta_ms": float(abstain_latency_ms - enforce_latency_ms),
        "label_enforce": "lrp:caf",
        "label_abstain": "lrp:uncertain",
    }


def load_ensemble_member_paths(entry: Mapping[str, Any]) -> list[str]:
    """Return relative ensemble quality paths from a manifest target entry."""
    files = entry.get("ensemble_quality_files")
    if not isinstance(files, list):
        return []
    return [str(path) for path in files if isinstance(path, str)]


@dataclass(frozen=True)
class TargetEnsemble:
    members: tuple[Any, ...]
    calibrations: tuple[tuple[tuple[float, ...], tuple[float, ...]], ...]


@dataclass
class BundleEnsemble:
    """Optional on-disk bootstrap members; absent when a bundle predates Wave 2."""

    by_target: dict[TargetKey, TargetEnsemble]

    @property
    def empty(self) -> bool:
        return not self.by_target

    def estimate(
        self,
        vector: np.ndarray,
        key: TargetKey,
        *,
        threads: int = 1,
    ) -> UncertaintyEstimate | None:
        entry = self.by_target.get(key)
        if entry is None or not entry.members:
            return None
        return predict_ensemble_quality(
            entry.members, vector, entry.calibrations, threads=threads
        )

    def estimates_for(
        self,
        vector: np.ndarray,
        keys: Sequence[TargetKey],
        *,
        threads: int = 1,
    ) -> dict[TargetKey, UncertaintyEstimate]:
        result: dict[TargetKey, UncertaintyEstimate] = {}
        for key in keys:
            estimate = self.estimate(vector, key, threads=threads)
            if estimate is not None:
                result[key] = estimate
        return result


def load_bundle_ensemble(bundle_dir: str | Path, manifest: Mapping[str, Any]) -> BundleEnsemble:
    """Load ensemble boosters referenced by the manifest (best-effort, empty if none)."""
    import json
    from pathlib import Path as PathType

    import lightgbm as lgb

    root = PathType(bundle_dir)
    by_target: dict[TargetKey, TargetEnsemble] = {}
    for entry in manifest.get("targets", []):
        if not isinstance(entry, dict) or entry.get("skipped"):
            continue
        quality_files = load_ensemble_member_paths(entry)
        cal_files = entry.get("ensemble_calibration_files")
        if not quality_files or not isinstance(cal_files, list):
            continue
        members = []
        calibrations: list[tuple[tuple[float, ...], tuple[float, ...]]] = []
        for q_rel, c_rel in zip(quality_files, cal_files, strict=False):
            q_path = root / str(q_rel)
            c_path = root / str(c_rel)
            if not q_path.is_file() or not c_path.is_file():
                continue
            members.append(lgb.Booster(model_str=q_path.read_text()))
            payload = json.loads(c_path.read_text())
            xs = tuple(float(v) for v in payload["x"])
            ys = tuple(float(v) for v in payload["y"])
            calibrations.append((xs, ys))
        if len(members) >= 2:
            key = (str(entry["provider"]), str(entry["model"]))
            by_target[key] = TargetEnsemble(tuple(members), tuple(calibrations))
    return BundleEnsemble(by_target)
