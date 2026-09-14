# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Pluggable embedder backend tests (#156 A)."""

from __future__ import annotations

import json
from pathlib import Path

# ruff: noqa: F811 -- pytest fixtures are imported from the owned training test module.
import numpy as np
import pytest
from lrp.collect import DataError
from lrp.features import (
    ONNXEmbedder,
    SyntheticEmbedder,
    _sentence_transformers_available,
    create_embedder,
    embedding_fingerprint,
)
from test_features import tiny_onnx
from test_train import dataset, trained  # noqa: F401


def test_create_embedder_synthetic_and_onnx(tmp_path):
    assert create_embedder({"kind": "synthetic"}).kind == "synthetic"
    model, tokenizer = tiny_onnx(tmp_path)
    embedder = create_embedder(
        {
            "kind": "onnx",
            "backend": "onnxruntime",
            "model_path": model,
            "tokenizer_path": tokenizer,
            "pooling": "cls",
            "prefix": "",
            "max_seq_len": 512,
            "precision": "int8",
            "device_class": "cpu",
        },
        device="cpu",
    )
    assert embedder.kind == "onnx"
    vector = embedder.encode("latest")
    assert vector.shape == (384,)
    assert np.isfinite(vector).all()


def test_sentence_transformers_import_guard():
    if _sentence_transformers_available():
        pytest.skip("sentence-transformers installed; import-guard path not exercised")
    with pytest.raises(DataError, match="sentence-transformers package is not installed"):
        create_embedder(
            {
                "kind": "sentence-transformers",
                "backend": "sentence-transformers",
                "model_path": "/tmp/missing-st-model",
                "device_class": "cpu",
            }
        )


def test_sentence_transformers_factory_when_present(tmp_path, monkeypatch):
    if not _sentence_transformers_available():
        pytest.skip("sentence-transformers not installed")

    class FakeModel:
        device = "cpu"
        max_seq_length = 512

        def encode(self, texts, **kwargs):
            return np.ones((len(texts), 384), dtype=np.float32)

    model_dir = tmp_path / "st-model"
    model_dir.mkdir()
    (model_dir / "config.json").write_text("{}")
    model_dir.chmod(0o700)
    (model_dir / "config.json").chmod(0o600)

    import sentence_transformers

    monkeypatch.setattr(
        sentence_transformers,
        "SentenceTransformer",
        lambda *args, **kwargs: FakeModel(),
    )
    embedder = create_embedder(
        {
            "kind": "sentence-transformers",
            "backend": "sentence-transformers",
            "model_path": model_dir,
            "max_seq_len": 128,
            "precision": "fp32",
            "device_class": "cpu",
            "normalize_embeddings": True,
        },
        device="cpu",
    )
    vector = embedder.encode("hello")
    assert vector.shape == (384,)
    assert np.isclose(np.linalg.norm(vector), 1.0)


def test_onnx_fingerprint_mismatch_fields(tmp_path):
    model, tokenizer = tiny_onnx(tmp_path)
    base = {
        "kind": "onnx",
        "backend": "onnxruntime",
        "model_path": model,
        "tokenizer_path": tokenizer,
        "pooling": "cls",
        "prefix": "",
        "max_seq_len": 512,
        "precision": "int8",
        "device_class": "cpu",
    }
    first = embedding_fingerprint(base)
    changed = dict(base, max_seq_len=256)
    assert embedding_fingerprint(changed) != first
    changed = dict(base, precision="fp32")
    assert embedding_fingerprint(changed) != first
    changed = dict(base, device_class="cuda")
    assert embedding_fingerprint(changed) != first
    embedder = ONNXEmbedder(model, tokenizer, max_seq_len=512, device="cpu")
    assert embedder.fingerprint == first


def test_synthetic_fingerprint_unchanged():
    assert SyntheticEmbedder.fingerprint == embedding_fingerprint({"kind": "synthetic"})


def test_cli_embedding_spec_backends():
    from argparse import Namespace

    from lrp.cli import embedding_spec

    onnx = embedding_spec(
        Namespace(
            synthetic=False,
            embedding_backend="onnxruntime",
            embedding_model=Path("/data/model.onnx"),
            tokenizer=Path("/data/tokenizer.json"),
            max_seq_len=512,
            device="cpu",
        )
    )
    assert onnx["backend"] == "onnxruntime"
    assert onnx["kind"] == "onnx"
    st = embedding_spec(
        Namespace(
            synthetic=False,
            embedding_backend="sentence-transformers",
            embedding_model=Path("/data/st"),
            tokenizer=None,
            max_seq_len=256,
            device="cuda:0",
        )
    )
    assert st["backend"] == "sentence-transformers"
    assert st["device_class"] == "cuda"
    assert st["max_seq_len"] == 256


def test_bundle_device_mismatch_and_fingerprint(trained, tmp_path):
    from lrp.bundle import bundle_version, canonical_json, load_bundle

    root = tmp_path / "bundle"
    import shutil

    shutil.copytree(trained, root)
    root.chmod(0o700)
    for path in root.rglob("*"):
        path.chmod(0o700 if path.is_dir() else 0o600)
    manifest = json.loads((root / "manifest.json").read_text())
    assert manifest.get("train_device_class") == "cpu"
    load_bundle(root, device="cpu")
    # Device-only mismatch warns by default for non-synthetic when train class set.
    manifest["train_device_class"] = "cuda"
    manifest["version"] = bundle_version(manifest)
    (root / "manifest.json").write_bytes(canonical_json(manifest))
    load_bundle(root, device="cpu", strict_device=False)
    with pytest.raises(ValueError, match="strict_device refuses"):
        load_bundle(root, device="cpu", strict_device=True)
    # Semantic fingerprint mismatch still refuses.
    manifest["embedding_fingerprint"] = "deadbeef"
    manifest["version"] = bundle_version(manifest)
    (root / "manifest.json").write_bytes(canonical_json(manifest))
    with pytest.raises(ValueError, match="fingerprint"):
        load_bundle(root, device="cpu", strict_device=False)
