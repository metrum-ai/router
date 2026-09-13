# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Feature drift monitoring and reversible shadow recommendation (#28).

Computes Population Stability Index (PSI) on scalar features and mean cosine
distance of new embeddings to the training centroid. Above reviewed thresholds,
emits bounded safe metrics and may recommend native-router shadow. Automatic
shadow never mutates router config itself; it returns a recommendation that
composes with the router's ``external_policy.mode`` activation authority.
"""

from __future__ import annotations

import math
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from typing import Any

import numpy as np

from lrp.features import EMBEDDING_NAMES, FEATURE_NAMES, SCALAR_NAMES
from lrp.policy import Decision, safe_label

# Reviewed defaults (Wave-2 deferred design + common PSI practice).
DEFAULT_PSI_THRESHOLD = 0.25
DEFAULT_EMBEDDING_DISTANCE_THRESHOLD = 0.15
PSI_BINS = 10
EPS = 1e-6


@dataclass(frozen=True)
class DriftThresholds:
    psi: float = DEFAULT_PSI_THRESHOLD
    embedding_distance: float = DEFAULT_EMBEDDING_DISTANCE_THRESHOLD


@dataclass(frozen=True)
class DriftReport:
    """Bounded safe drift scalars — no prompts, embeddings payloads, or credentials."""

    psi_max: float
    psi_mean: float
    psi_by_feature: dict[str, float]
    embedding_centroid_distance: float
    n_reference: int
    n_observed: int
    above_threshold: bool
    recommend_shadow: bool
    reasons: tuple[str, ...]

    def metrics(self) -> dict[str, float | int | bool]:
        return {
            "psi_max": self.psi_max,
            "psi_mean": self.psi_mean,
            "embedding_centroid_distance": self.embedding_centroid_distance,
            "n_reference": self.n_reference,
            "n_observed": self.n_observed,
            "above_threshold": self.above_threshold,
            "recommend_shadow": self.recommend_shadow,
        }


def _psi_from_counts(exp_counts: np.ndarray, act_counts: np.ndarray) -> float:
    """PSI from paired bin counts; never drops mass by renormalizing away zeros."""
    exp_total = float(exp_counts.sum())
    act_total = float(act_counts.sum())
    if exp_total <= 0 or act_total <= 0:
        return 0.0
    exp_pct = exp_counts.astype(float) / exp_total
    act_pct = act_counts.astype(float) / act_total
    exp_pct = np.clip(exp_pct, EPS, None)
    act_pct = np.clip(act_pct, EPS, None)
    # Renormalize after clipping only; underflow/overflow bins already hold
    # out-of-range mass so dropped observations are not silently lost.
    exp_pct = exp_pct / exp_pct.sum()
    act_pct = act_pct / act_pct.sum()
    psi = float(np.sum((act_pct - exp_pct) * np.log(act_pct / exp_pct)))
    return psi if math.isfinite(psi) else float("nan")


def _constant_feature_psi(expected: np.ndarray, actual: np.ndarray) -> float:
    """Two-bin PSI for a constant reference: match vs changed."""
    ref = float(expected[0])
    if not math.isfinite(ref):
        return float("nan")
    exp_match = int(np.isclose(expected, ref).sum())
    exp_other = int(expected.size - exp_match)
    act_match = int(np.isclose(actual, ref).sum())
    act_other = int(actual.size - act_match)
    return _psi_from_counts(
        np.asarray([exp_match, exp_other], dtype=float),
        np.asarray([act_match, act_other], dtype=float),
    )


def population_stability_index(
    expected: np.ndarray,
    actual: np.ndarray,
    *,
    bins: int = PSI_BINS,
) -> float:
    """PSI between two 1-d samples using shared quantile edges from expected.

    Finite observations outside the reference support land in explicit
    underflow/overflow bins so mass is not discarded. A changed constant
    reference produces drift evidence rather than unconditional zero.
    """
    exp = np.asarray(expected, dtype=float).ravel()
    act = np.asarray(actual, dtype=float).ravel()
    if exp.size < 2 or act.size < 2 or bins < 2:
        return 0.0
    exp = exp[np.isfinite(exp)]
    act = act[np.isfinite(act)]
    if exp.size < 2 or act.size < 2:
        return float("nan")
    edges = np.unique(np.quantile(exp, np.linspace(0, 1, bins + 1)))
    if edges.size < 3:
        return _constant_feature_psi(exp, act)
    # Interior edges only; ±inf capture underflow/overflow support.
    interior = edges[1:-1]
    full_edges = np.concatenate(([-np.inf], interior, [np.inf]))
    exp_counts, _ = np.histogram(exp, bins=full_edges)
    act_counts, _ = np.histogram(act, bins=full_edges)
    return _psi_from_counts(exp_counts, act_counts)


def scalar_psi_by_feature(
    reference: np.ndarray,
    observed: np.ndarray,
    feature_names: Sequence[str] | None = None,
) -> dict[str, float]:
    """PSI for each scalar column. ``reference``/``observed`` are (n, n_features)."""
    names = list(feature_names or SCALAR_NAMES)
    ref = np.asarray(reference, dtype=float)
    obs = np.asarray(observed, dtype=float)
    if ref.ndim != 2 or obs.ndim != 2 or ref.shape[1] != obs.shape[1]:
        raise ValueError("scalar matrices must be 2-d with matching columns")
    if ref.shape[1] != len(names):
        raise ValueError("feature name count mismatch")
    return {
        names[i]: population_stability_index(ref[:, i], obs[:, i])
        for i in range(len(names))
    }


def embedding_centroid(matrix: np.ndarray) -> np.ndarray:
    arr = np.asarray(matrix, dtype=np.float32)
    if arr.ndim != 2 or arr.shape[0] < 1:
        raise ValueError("embedding matrix required")
    centroid = arr.mean(axis=0)
    norm = float(np.linalg.norm(centroid))
    if not math.isfinite(norm) or norm <= 0:
        raise ValueError("invalid embedding centroid")
    return centroid / norm


def mean_cosine_distance_to_centroid(
    centroid: np.ndarray,
    observed: np.ndarray,
) -> float:
    """Mean cosine distance (1 - cosine similarity) of rows to a unit centroid."""
    center = np.asarray(centroid, dtype=np.float32).ravel()
    obs = np.asarray(observed, dtype=np.float32)
    if obs.ndim != 2 or obs.shape[1] != center.shape[0]:
        raise ValueError("embedding shape mismatch")
    norms = np.linalg.norm(obs, axis=1, keepdims=True)
    norms = np.maximum(norms, EPS)
    unit = obs / norms
    sims = unit @ center
    distances = 1.0 - sims
    value = float(np.mean(distances))
    return value if math.isfinite(value) else float("nan")


def evaluate_drift(
    *,
    reference_scalars: np.ndarray,
    observed_scalars: np.ndarray,
    reference_embeddings: np.ndarray,
    observed_embeddings: np.ndarray,
    thresholds: DriftThresholds | None = None,
    auto_shadow: bool = False,
    scalar_names: Sequence[str] | None = None,
) -> DriftReport:
    """Compute PSI + embedding-distance drift and optional shadow recommendation."""
    thresholds = thresholds or DriftThresholds()
    psi_map = scalar_psi_by_feature(
        reference_scalars, observed_scalars, feature_names=scalar_names
    )
    finite_psi = [v for v in psi_map.values() if math.isfinite(v)]
    psi_max = max(finite_psi) if finite_psi else 0.0
    psi_mean = float(sum(finite_psi) / len(finite_psi)) if finite_psi else 0.0
    centroid = embedding_centroid(reference_embeddings)
    emb_dist = mean_cosine_distance_to_centroid(centroid, observed_embeddings)
    reasons: list[str] = []
    if psi_max > thresholds.psi:
        reasons.append("psi_threshold")
    if math.isfinite(emb_dist) and emb_dist > thresholds.embedding_distance:
        reasons.append("embedding_distance_threshold")
    above = bool(reasons)
    recommend = bool(auto_shadow and above)
    return DriftReport(
        psi_max=float(psi_max),
        psi_mean=float(psi_mean),
        psi_by_feature={k: float(v) for k, v in sorted(psi_map.items())},
        embedding_centroid_distance=float(emb_dist),
        n_reference=int(np.asarray(reference_scalars).shape[0]),
        n_observed=int(np.asarray(observed_scalars).shape[0]),
        above_threshold=above,
        recommend_shadow=recommend,
        reasons=tuple(reasons),
    )


def feature_matrix_split(
    frame_values: np.ndarray,
) -> tuple[np.ndarray, np.ndarray]:
    """Split a FEATURE_NAMES-ordered matrix into embeddings and scalars."""
    matrix = np.asarray(frame_values, dtype=np.float32)
    if matrix.ndim != 2 or matrix.shape[1] != len(FEATURE_NAMES):
        raise ValueError("expected full feature matrix")
    n_emb = len(EMBEDDING_NAMES)
    return matrix[:, :n_emb], matrix[:, n_emb:]


def shadow_decision_from_recommendation(
    decision: Decision,
    *,
    recommend_shadow: bool,
    force_index_zero: bool = False,
) -> Decision:
    """Compose a reversible drift-shadow signal with native router authority.

    Wave-1 contract: LRP returns the real recommendation; the router owns
    ``external_policy.mode: shadow``. When drift auto-shadow recommends action,
    emit a bounded ``lrp:shadow-drift`` label. Optionally force ``targetIndex: 0``
    only when an operator explicitly enables that fail-closed local behavior;
    default is label-only so rollback remains a router config change.
    """
    if not recommend_shadow:
        return decision
    label = safe_label("lrp:shadow-drift")
    if force_index_zero:
        n_fallback = len(decision.fallbacks) + (0 if decision.primary == 0 else 1)
        return Decision(0, tuple(i for i in range(1, n_fallback + 1)), label)
    return Decision(decision.primary, decision.fallbacks, label)


def training_centroid_record(embeddings: np.ndarray) -> dict[str, Any]:
    """Serialize a unit centroid for offline drift jobs (safe floats only)."""
    centroid = embedding_centroid(embeddings)
    return {
        "schema_version": "lrp.drift.centroid.v1",
        "dims": int(centroid.shape[0]),
        "centroid": [float(x) for x in centroid.tolist()],
        "n_rows": int(np.asarray(embeddings).shape[0]),
    }


def load_centroid(record: Mapping[str, Any]) -> np.ndarray:
    if record.get("schema_version") != "lrp.drift.centroid.v1":
        raise ValueError("incompatible drift centroid schema")
    values = record.get("centroid")
    if not isinstance(values, list) or not values:
        raise ValueError("invalid drift centroid")
    arr = np.asarray(values, dtype=np.float32)
    if arr.ndim != 1 or not np.isfinite(arr).all():
        raise ValueError("invalid drift centroid values")
    return arr
