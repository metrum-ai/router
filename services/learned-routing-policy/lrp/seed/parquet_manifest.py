# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Parquet writer and SHA-256 manifest for seed corpus artifacts."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any

import pyarrow as pa
import pyarrow.parquet as pq

from ..collect import DataError, canonical, protected_path, utc_now
from .labels import LABEL_COLUMNS

SEED_COLUMNS: list[str] = [
    "turn_id",
    "session_id",
    "split",
    "source_dataset",
    "source_revision",
    "target_id",
    "provider",
    "model",
    "cache_pass",
    "router_request_id",
    "eligible",
    "eligibility_reason",
    "prompt_tokens_cl100k",
    "input_tokens",
    "cached_input_tokens",
    "cache_write_tokens",
    "reasoning_tokens",
    "output_tokens",
    "cost_usd",
    "latency_ms",
    *LABEL_COLUMNS,
    "judge_quality",
    "human_quality",
]


def _nullable(value: Any) -> Any:
    return value


def rows_to_table(rows: list[dict[str, Any]]) -> pa.Table:
    if not rows:
        raise DataError("empty_parquet_rows")
    columns: dict[str, list[Any]] = {name: [] for name in SEED_COLUMNS}
    for row in rows:
        for name in SEED_COLUMNS:
            columns[name].append(_nullable(row.get(name)))
    arrays: dict[str, pa.Array] = {}
    for name, values in columns.items():
        if name in LABEL_COLUMNS or name in {"judge_quality", "human_quality", "cost_usd"}:
            arrays[name] = pa.array(values, type=pa.float64())
        elif name in {
            "prompt_tokens_cl100k",
            "input_tokens",
            "cached_input_tokens",
            "cache_write_tokens",
            "reasoning_tokens",
            "output_tokens",
        }:
            arrays[name] = pa.array(values, type=pa.int64())
        elif name == "eligible":
            arrays[name] = pa.array(values, type=pa.bool_())
        elif name == "latency_ms":
            arrays[name] = pa.array(values, type=pa.float64())
        else:
            arrays[name] = pa.array(values, type=pa.string())
    return pa.table(arrays)


def write_seed_parquet(path: Path, rows: list[dict[str, Any]]) -> dict[str, Any]:
    path = protected_path(path)
    path.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
    table = rows_to_table(rows)
    pq.write_table(table, path)
    path.chmod(0o600)
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    return {
        "path": str(path),
        "sha256": digest,
        "rows": len(rows),
        "columns": list(SEED_COLUMNS),
    }


def sha256_file(path: Path) -> str:
    path = protected_path(path)
    return hashlib.sha256(path.read_bytes()).hexdigest()


def build_manifest(
    artifacts: list[dict[str, Any]],
    *,
    inputs: list[dict[str, Any]] | None = None,
    extra: dict[str, Any] | None = None,
) -> dict[str, Any]:
    manifest: dict[str, Any] = {
        "schema_version": "lrp.seed.manifest.v1",
        "created_at": utc_now(),
        "inputs": inputs or [],
        "artifacts": artifacts,
    }
    if extra:
        manifest["extra"] = extra
    manifest["manifest_sha256"] = hashlib.sha256(
        canonical({key: value for key, value in manifest.items() if key != "manifest_sha256"}).encode()
    ).hexdigest()
    return manifest


def write_manifest(path: Path, manifest: dict[str, Any]) -> Path:
    path = protected_path(path)
    path.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
    path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    path.chmod(0o600)
    return path
