# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Deterministic per-target supervised training; test rows are never fitted."""

from __future__ import annotations

import math
import shutil
import tempfile
from pathlib import Path
from typing import Any

import numpy as np
import pandas as pd

from lrp.bundle import (
    SCHEMA_VERSION,
    bundle_version,
    canonical_json,
    sha256_file,
    target_id,
    target_key,
)
from lrp.collect import MAX_FILE_BYTES, MAX_ROWS, DataError
from lrp.features import (
    FEATURE_NAMES,
    SentenceTransformersEmbedder,
    capped_threads,
    create_embedder,
    embedding_fingerprint,
    private_bytes,
    private_directory,
    private_file,
    read_rows,
    session_split,
    write_private,
)
from lrp.schemas import TargetKey


def feature_frame(source: Any, seed: int = 42) -> pd.DataFrame:
    frame: pd.DataFrame
    if isinstance(source, (str, Path)):
        import pyarrow.parquet as pq

        with private_file(source) as handle:
            parquet = pq.ParquetFile(handle)
            if (
                parquet.metadata.num_rows > MAX_ROWS
                or parquet.metadata.num_columns > len(FEATURE_NAMES) + 32
            ):
                raise DataError("feature_dataset_limit")
            decoded_bytes = sum(
                parquet.metadata.row_group(i).total_byte_size
                for i in range(parquet.metadata.num_row_groups)
            )
            if decoded_bytes > MAX_FILE_BYTES:
                raise DataError("feature_dataset_limit")
            frame = parquet.read().to_pandas()
    else:
        frame = source.copy()
    if len(frame) > MAX_ROWS:
        raise DataError("feature_dataset_limit")
    required = {
        *FEATURE_NAMES,
        "request_id",
        "session_key",
        "split",
        "group",
        "source",
        "synthetic",
        "embedding_kind",
        "embedding_fingerprint",
    }
    if not required.issubset(frame.columns) or frame.empty:
        raise ValueError("incomplete feature dataset")
    if frame.request_id.duplicated().any() or frame.session_key.isna().any():
        raise ValueError("duplicate request or missing session")
    expected = frame.session_key.map(lambda value: session_split(str(value), seed))
    if not (expected == frame.split).all():
        raise ValueError("split does not match deterministic session split")
    if not np.isfinite(
        frame.loc[:, list(FEATURE_NAMES)].to_numpy(dtype=np.float32)
    ).all():
        raise ValueError("nonfinite features")
    return frame.sort_values("request_id").reset_index(drop=True)


def trusted_judgment(row: dict[str, Any]) -> bool:
    quality = row.get("quality")
    detail = row.get("detail") or {}
    return (
        quality is not None
        and isinstance(quality, (float, int))
        and math.isfinite(quality)
        and 0 <= quality <= 1
        and not detail.get("parse_failed")
        and not detail.get("uncertain")
        and not detail.get("unsupported")
        and not detail.get("infrastructure_error")
    )


def index_rows(source: Any, kind: str) -> dict[tuple[str, TargetKey], dict[str, Any]]:
    result = {}
    for row in read_rows(source, kind):
        key = (str(row["request_id"]), target_key(row["target"]))
        if key in result:
            raise ValueError("duplicate request-target row")
        result[key] = row
    return result


def bradley_terry(
    judgments: list[dict[str, Any]],
    anchor: TargetKey | None = None,
    iterations: int = 200,
) -> dict[TargetKey, float]:
    """Fit regularized anchor-relative BT using fractional pairwise wins.

    Only explicit pairwise labels contribute. Verifier/rubric cohorts have no
    pairwise meaning and must not masquerade as BT observations.
    """
    keys = sorted({target_key(row["target"]) for row in judgments})
    if not keys:
        return {}
    positions = {key: i for i, key in enumerate(keys)}
    wins = np.zeros((len(keys), len(keys)), dtype=float)
    for row in judgments:
        if not trusted_judgment(row) or not str(row.get("method", "")).startswith(
            "pairwise"
        ):
            continue
        candidate = target_key(row["target"])
        other = (row.get("detail") or {}).get("anchor")
        reference = target_key(other) if isinstance(other, dict) else anchor
        if reference not in positions or reference == candidate:
            continue
        i, j = positions[candidate], positions[reference]
        wins[i, j] += float(row["quality"])
        wins[j, i] += 1 - float(row["quality"])
    strength = np.ones(len(keys))
    for _ in range(min(max(iterations, 1), 1000)):
        # Symmetric pseudo-observations prevent infinite strengths/separation.
        regularized = wins + 0.5 * (1 - np.eye(len(keys)))
        totals = regularized + regularized.T
        denominator = (totals / (strength[:, None] + strength[None, :])).sum(axis=1)
        updated = regularized.sum(axis=1) / np.maximum(denominator, 1e-12)
        updated = np.maximum(updated, 1e-12)
        updated /= np.exp(np.log(updated).mean())
        if np.max(np.abs(np.log(updated / strength))) < 1e-10:
            strength = updated
            break
        strength = updated
    reference_strength = strength[positions[anchor]] if anchor in positions else 1.0
    return {
        key: (
            1.0
            if key == anchor
            else float(strength[i] / (strength[i] + reference_strength))
        )
        for key, i in positions.items()
    }


def _binary_data(x: Any, y: Any) -> tuple[Any, Any, Any]:
    # Fractional labels are weighted copies of both classes, including ties.
    labels = np.asarray(y, dtype=float)
    matrix = np.asarray(x, dtype=np.float32)
    return (
        np.concatenate((matrix, matrix)),
        np.concatenate((np.ones(len(labels)), np.zeros(len(labels)))),
        np.concatenate((labels, 1 - labels)),
    )


def _fit_quality_member(
    lgb: Any,
    params: dict[str, Any],
    training: pd.DataFrame,
    validation: pd.DataFrame,
    *,
    num_boost_round: int,
    threads: int,
    member_seed: int,
) -> tuple[Any, Any, np.ndarray, np.ndarray]:
    """Fit one quality booster + isotonic calibrator; returns model, calib, raw, calibrated."""
    from sklearn.isotonic import IsotonicRegression

    member_params = {**params, "seed": member_seed}
    x, y, w = _binary_data(training[list(FEATURE_NAMES)], training.quality)
    vx, vy, vw = _binary_data(validation[list(FEATURE_NAMES)], validation.quality)
    dataset = lgb.Dataset(x, label=y, weight=w, feature_name=list(FEATURE_NAMES))
    valid = lgb.Dataset(vx, label=vy, weight=vw, reference=dataset)
    quality_model = lgb.train(
        member_params,
        dataset,
        num_boost_round=num_boost_round,
        valid_sets=[valid],
        callbacks=[lgb.early_stopping(30, verbose=False)],
    )
    raw = quality_model.predict(
        validation[list(FEATURE_NAMES)].to_numpy(), num_threads=threads
    )
    calibration = IsotonicRegression(y_min=0, y_max=1, out_of_bounds="clip").fit(
        raw, validation.quality
    )
    calibrated = calibration.predict(raw)
    return quality_model, calibration, raw, calibrated


def train(
    features: Any,
    judgments: Any,
    responses: Any,
    out: str | Path,
    *,
    embedding: dict[str, Any],
    seed: int = 42,
    threads: int = 1,
    min_train_rows: int = 200,
    num_boost_round: int = 400,
    anchor: TargetKey | None = None,
    ensemble_size: int = 5,
    cold_start_prompts: int = 200,
    device: str = "cpu",
    strict_device: bool = False,
    intended_serve_device: str | None = None,
) -> Path:
    import lightgbm as lgb

    from lrp.device import parse_device_request, resolve_compute_device
    from lrp.thompson import COLD_START_ANCHOR_PROMPTS, cold_start_entry
    from lrp.uncertainty import ENSEMBLE_SIZE

    capped_threads(threads)
    root = private_directory(out)
    if min_train_rows < 200 or not 1 <= num_boost_round <= 400:
        raise ValueError("invalid training resource or minimum-row limits")
    if not 1 <= ensemble_size <= ENSEMBLE_SIZE:
        # Cap at the reviewed five-model bag; smaller sizes remain valid for tests.
        raise ValueError("invalid ensemble size")
    if cold_start_prompts < COLD_START_ANCHOR_PROMPTS:
        raise ValueError("cold start requires at least 200 anchor prompts")
    frame = feature_frame(features, seed)
    judgment_index, response_index = (
        index_rows(judgments, "judgment"),
        index_rows(responses, "response"),
    )
    feature_ids = set(frame.request_id)
    if any(key[0] not in feature_ids for key in judgment_index | response_index):
        raise ValueError("outcome has no feature row")
    kind = embedding.get("kind")
    backend = embedding.get("backend")
    requested, _ = parse_device_request(device)
    config_device_class = embedding.get("device_class") or (
        "auto" if requested == "auto" else requested
    )
    if kind == "synthetic":
        backend_name = "onnxruntime"
        resolved = resolve_compute_device(
            device, strict_device=strict_device, backend="onnxruntime"
        )
    elif kind == "onnx" or backend in {None, "onnxruntime"}:
        kind = "onnx"
        backend_name = "onnxruntime"
        if set(frame.embedding_kind) != {"onnx"}:
            raise ValueError("embedding provenance mismatch")
        resolved = resolve_compute_device(
            device, strict_device=strict_device, backend="onnxruntime"
        )
    elif kind == "sentence-transformers" or backend == "sentence-transformers":
        kind = "sentence-transformers"
        backend_name = "sentence-transformers"
        if set(frame.embedding_kind) != {"sentence-transformers"}:
            raise ValueError("embedding provenance mismatch")
        resolved = resolve_compute_device(
            device, strict_device=strict_device, backend="sentence-transformers"
        )
    else:
        raise ValueError("embedding provenance mismatch")
    if kind == "synthetic" and set(frame.embedding_kind) != {"synthetic"}:
        raise ValueError("embedding provenance mismatch")
    train_spec = dict(embedding)
    train_spec["kind"] = kind
    if kind != "synthetic":
        train_spec["backend"] = backend_name
        train_spec.setdefault("max_seq_len", 512)
        train_spec.setdefault(
            "precision", "int8" if kind == "onnx" else "fp32"
        )
        train_spec["device_class"] = config_device_class
    fingerprint = embedding_fingerprint(train_spec)
    if set(frame.embedding_fingerprint) != {fingerprint}:
        raise ValueError("embedding artifact differs from featurization")
    serve_request = (
        intended_serve_device if intended_serve_device is not None else device
    )
    serve_class, _ = parse_device_request(serve_request)
    if serve_class == "auto":
        intended_serve_class = resolved.device_class
    else:
        intended_serve_class = serve_class
    synthetic = (
        kind == "synthetic"
        or bool(frame.synthetic.any())
        or any(
            row.get("source") == "synthetic"
            for row in [*judgment_index.values(), *response_index.values()]
        )
    )
    train_ids = set(frame.loc[frame.split == "train", "request_id"])
    train_judgments = [
        row for (rid, _), row in judgment_index.items() if rid in train_ids
    ]
    bt = bradley_terry(train_judgments, anchor)
    manifest: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "feature_names": list(FEATURE_NAMES),
        "seed": seed,
        "threads": threads,
        "min_train_rows": min_train_rows,
        "synthetic": synthetic,
        "embedding": {"kind": kind},
        "embedding_fingerprint": fingerprint,
        "train_device_class": resolved.device_class,
        "intended_serve_device_class": intended_serve_class,
        "targets": [],
        "skipped": [],
        "files": {},
        "split_counts": {k: int(v) for k, v in frame.split.value_counts().items()},
        "uncertain_judgments": sum(
            not trusted_judgment(r) for r in judgment_index.values()
        ),
        "anchor": {"provider": anchor[0], "model": anchor[1]} if anchor else None,
        "ensemble_size": ensemble_size,
        "cold_start_prompts": cold_start_prompts,
        "training_versions": {"lightgbm": lgb.__version__, "numpy": np.__version__},
    }
    private_directory(root, create=True)
    temporary = Path(tempfile.mkdtemp(prefix=".training-", dir=root))
    try:
        if kind == "onnx":
            # Validate operator assets before copying into a published snapshot.
            create_embedder(
                train_spec,
                threads=threads,
                device=device,
                strict_device=strict_device,
            ).encode("Synthetic validation.")
            private_directory(temporary / "embed", create=True)
            for asset_key, name in [
                ("model_path", "model.onnx"),
                ("tokenizer_path", "tokenizer.json"),
            ]:
                relative = "embed/" + name
                write_private(temporary / relative, private_bytes(embedding[asset_key]))
                manifest["embedding"][asset_key] = relative
            manifest["embedding"].update(
                backend="onnxruntime",
                pooling=embedding.get("pooling", "cls"),
                prefix=embedding.get("prefix", ""),
                max_seq_len=int(embedding.get("max_seq_len", 512)),
                precision="int8",
                device_class=config_device_class,
                batch_size=int(embedding.get("batch_size", 1)),
            )
        elif kind == "sentence-transformers":
            embedder = create_embedder(
                train_spec,
                threads=threads,
                device=device,
                strict_device=strict_device,
            )
            if not isinstance(embedder, SentenceTransformersEmbedder):
                raise DataError("sentence_transformers_embedder_required")
            embedder.encode("Synthetic validation.")
            private_directory(temporary / "embed" / "st", create=True)
            source = Path(embedder.model_path)
            for item in sorted(source.rglob("*")):
                if not item.is_file() or item.is_symlink():
                    continue
                asset_rel = Path("embed/st") / item.relative_to(source)
                private_directory(temporary / asset_rel.parent, create=True)
                write_private(temporary / asset_rel, item.read_bytes())
            manifest["embedding"].update(
                backend="sentence-transformers",
                model_path="embed/st",
                pooling=embedding.get("pooling", "cls"),
                prefix=embedding.get("prefix", ""),
                max_seq_len=int(embedding.get("max_seq_len", 512)),
                precision="fp32",
                device_class=config_device_class,
                normalize_embeddings=bool(
                    embedding.get("normalize_embeddings", True)
                ),
                batch_size=int(embedding.get("batch_size", 1)),
            )
        params = {
            "objective": "binary",
            "metric": "binary_logloss",
            "num_leaves": 31,
            "learning_rate": 0.05,
            "seed": seed,
            "num_threads": threads,
            "deterministic": True,
            "force_col_wise": True,
            "verbosity": -1,
            "histogram_pool_size": 64,
        }
        for target in sorted({key[1] for key in judgment_index}):
            records = []
            for (request_id, key), judgment in judgment_index.items():
                response = response_index.get((request_id, key))
                if (
                    key != target
                    or not trusted_judgment(judgment)
                    or response is None
                    or response.get("status") == "ineligible"
                ):
                    continue
                usage = response.get("usage") or {}
                output = usage.get("output_tokens")
                records.append(
                    {
                        "request_id": request_id,
                        "quality": float(judgment["quality"]),
                        "output_tokens": output,
                        "response_ok": response.get("status") == "ok",
                    }
                )
            joined = (
                frame.merge(
                    pd.DataFrame(records), on="request_id", validate="one_to_one"
                )
                if records
                else frame.iloc[:0].copy()
            )
            training = joined.loc[joined.split == "train"]
            validation = joined.loc[joined.split == "valid"]
            output_training = (
                training.loc[training.response_ok & training.output_tokens.notna()]
                if records
                else training
            )
            output_validation = (
                validation.loc[
                    validation.response_ok & validation.output_tokens.notna()
                ]
                if records
                else validation
            )
            entry: dict[str, Any] = {
                "provider": target[0],
                "model": target[1],
                "id": target_id(target),
                "n_train": len(training),
                "n_valid": len(validation),
                "bt_strength": bt.get(target, 0.5),
                "mean_out_tokens": float(output_training.output_tokens.mean())
                if len(output_training)
                else 0.0,
            }
            if (
                len(training) < min_train_rows
                or len(output_training) < min_train_rows
                or len(validation) < 2
                or len(output_validation) < 2
            ):
                # Evidence-backed cold start (#30): 200-prompt anchor seed before
                # the normal training minimum. Never writes a learned quality model.
                seeded = cold_start_entry(
                    provider=target[0],
                    model=target[1],
                    bt_strength=float(entry["bt_strength"]),
                    n_anchor_prompts=len(training),
                    mean_out_tokens=float(entry["mean_out_tokens"]),
                    min_prompts=cold_start_prompts,
                )
                if seeded is not None and target != anchor:
                    entry.update(seeded)
                    reason = "cold_start_anchor_seed"
                else:
                    entry["skipped"] = "insufficient_train_or_validation_rows"
                    reason = entry["skipped"]
                manifest["skipped"].append(
                    {
                        "provider": target[0],
                        "model": target[1],
                        "reason": reason,
                    }
                )
                manifest["targets"].append(entry)
                continue
            quality_model, calibration, raw, calibrated = _fit_quality_member(
                lgb,
                params,
                training,
                validation,
                num_boost_round=num_boost_round,
                threads=threads,
                member_seed=seed,
            )
            entry["calibration_brier_raw"] = float(
                np.mean((raw - validation.quality.to_numpy()) ** 2)
            )
            entry["calibration_brier"] = float(
                np.mean((calibrated - validation.quality.to_numpy()) ** 2)
            )
            token_dataset = lgb.Dataset(
                output_training[list(FEATURE_NAMES)],
                label=np.log1p(output_training.output_tokens.astype(float)),
            )
            token_valid = lgb.Dataset(
                output_validation[list(FEATURE_NAMES)],
                label=np.log1p(output_validation.output_tokens.astype(float)),
                reference=token_dataset,
            )
            token_model = lgb.train(
                {**params, "objective": "regression", "metric": "l2"},
                token_dataset,
                num_boost_round=num_boost_round,
                valid_sets=[token_valid],
                callbacks=[lgb.early_stopping(30, verbose=False)],
            )
            for field, directory, suffix in [
                ("quality_file", "quality", ".lgbm.txt"),
                ("out_tokens_file", "out_tokens", ".lgbm.txt"),
                ("calibration_file", "calibration", ".isotonic.json"),
            ]:
                private_directory(temporary / directory, create=True)
                entry[field] = f"{directory}/{entry['id']}{suffix}"
            write_private(
                temporary / entry["quality_file"],
                quality_model.model_to_string().encode(),
            )
            write_private(
                temporary / entry["out_tokens_file"],
                token_model.model_to_string().encode(),
            )
            write_private(
                temporary / entry["calibration_file"],
                canonical_json(
                    {
                        "x": calibration.X_thresholds_.tolist(),
                        "y": calibration.y_thresholds_.tolist(),
                    }
                ),
            )
            # Five-model bootstrap ensemble for uncertainty (#24). Member 0 is
            # the primary quality model already written above.
            ensemble_quality: list[str] = [entry["quality_file"]]
            ensemble_calibration: list[str] = [entry["calibration_file"]]
            member_stds: list[float] = []
            private_directory(temporary / "ensemble", create=True)
            rng = np.random.default_rng(seed)
            n_train = len(training)
            for member in range(1, ensemble_size):
                if n_train < 2:
                    break
                # Bootstrap row indices with replacement; keep validation fixed.
                sample_idx = rng.choice(n_train, size=n_train, replace=True)
                boot = training.iloc[sample_idx].reset_index(drop=True)
                member_model, member_cal, member_raw, _ = _fit_quality_member(
                    lgb,
                    params,
                    boot,
                    validation,
                    num_boost_round=num_boost_round,
                    threads=threads,
                    member_seed=seed + member,
                )
                q_rel = f"ensemble/{entry['id']}.m{member}.lgbm.txt"
                c_rel = f"ensemble/{entry['id']}.m{member}.isotonic.json"
                write_private(
                    temporary / q_rel, member_model.model_to_string().encode()
                )
                write_private(
                    temporary / c_rel,
                    canonical_json(
                        {
                            "x": member_cal.X_thresholds_.tolist(),
                            "y": member_cal.y_thresholds_.tolist(),
                        }
                    ),
                )
                ensemble_quality.append(q_rel)
                ensemble_calibration.append(c_rel)
                member_stds.append(float(np.std(member_raw, ddof=0)))
            entry["ensemble_quality_files"] = ensemble_quality
            entry["ensemble_calibration_files"] = ensemble_calibration
            entry["ensemble_size"] = len(ensemble_quality)
            if member_stds:
                entry["ensemble_raw_std_mean"] = float(np.mean(member_stds))
            manifest["targets"].append(entry)
        for file in sorted(temporary.rglob("*")):
            if file.is_file():
                manifest["files"][file.relative_to(temporary).as_posix()] = sha256_file(
                    file
                )
        manifest["version"] = bundle_version(manifest)
        write_private(temporary / "manifest.json", canonical_json(manifest))
        destination = root / str(manifest["version"])
        private_directory(destination)
        if destination.exists():
            if private_bytes(destination / "manifest.json") != canonical_json(
                manifest
            ) or any(
                sha256_file(destination / name) != digest
                for name, digest in manifest["files"].items()
            ):
                raise ValueError("existing bundle version was modified")
        else:
            temporary.rename(destination)
        return destination
    finally:
        if temporary.exists():
            shutil.rmtree(temporary)
