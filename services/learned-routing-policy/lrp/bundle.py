# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Hash-checked immutable model snapshots; optional Ed25519 signatures."""

from __future__ import annotations

import base64
import hashlib
import json
import math
import threading
from collections.abc import Iterable, Mapping
from dataclasses import dataclass
from itertools import pairwise
from pathlib import Path, PurePosixPath
from types import MappingProxyType
from typing import Any, Literal

import numpy as np
from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives.asymmetric.ed25519 import (
    Ed25519PrivateKey,
    Ed25519PublicKey,
)

from lrp.collect import strict_json
from lrp.features import (
    FEATURE_NAMES,
    FeatureBuilder,
    Vector,
    capped_threads,
    create_embedder,
    private_bytes,
    private_directory,
    private_file,
)
from lrp.policy import Prediction
from lrp.schemas import TargetKey

SCHEMA_VERSION = "lrp.bundle.v1"
SIGNATURE_SCHEMA = "lrp.bundle.sig.v1"
TRUST_SCHEMA = "lrp.bundle.trust.v1"
SIGNATURE_FILE = "manifest.sig"
ALGORITHM = "ed25519"
_ED25519_SEED = 32
_ED25519_PRIVATE = 64
_ED25519_PUBLIC = 32
_ED25519_SIGNATURE = 64
_MAX_TRUST_KEYS = 64
_MAX_KEY_ID = 128


def target_key(value: Mapping[str, Any]) -> TargetKey:
    provider, model = value.get("provider"), value.get("model")
    if (
        not isinstance(provider, str)
        or not provider
        or not isinstance(model, str)
        or not model
    ):
        raise ValueError("invalid target identity")
    return provider, model


def target_id(key: TargetKey) -> str:
    return hashlib.sha256(json.dumps(key, separators=(",", ":")).encode()).hexdigest()[
        :32
    ]


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with private_file(path) as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def canonical_json(value: Any) -> bytes:
    return (
        json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False) + "\n"
    ).encode()


def bundle_version(manifest: Mapping[str, Any]) -> str:
    return hashlib.sha256(
        canonical_json({k: v for k, v in manifest.items() if k != "version"})
    ).hexdigest()[:24]


def _file(root: Path, relative: str) -> Path:
    path = PurePosixPath(relative)
    if path.is_absolute() or ".." in path.parts or "\\" in relative or not path.parts:
        raise ValueError("unsafe bundle path")
    candidate = root.joinpath(*path.parts)
    for directory in candidate.parents:
        if directory == root:
            break
        private_directory(directory)
    if candidate.is_symlink() or any(
        p.is_symlink() for p in candidate.parents if p != root.parent
    ):
        raise ValueError("bundle symlinks are forbidden")
    if (
        not candidate.resolve().is_relative_to(root.resolve())
        or not candidate.is_file()
    ):
        raise ValueError("missing or unsafe bundle file")
    return candidate


def _decode_b64(value: str, expected: int, label: str) -> bytes:
    if not isinstance(value, str) or not value:
        raise ValueError(f"invalid {label}")
    try:
        raw = base64.b64decode(value, validate=True)
    except Exception as exc:
        raise ValueError(f"invalid {label}") from exc
    if len(raw) != expected:
        raise ValueError(f"invalid {label}")
    return raw


def _normalize_key_id(key_id: str) -> str:
    if (
        not isinstance(key_id, str)
        or not key_id
        or len(key_id) > _MAX_KEY_ID
        or any(c.isspace() for c in key_id)
    ):
        raise ValueError("invalid signature key id")
    return key_id


def generate_keypair() -> tuple[bytes, bytes]:
    """Return (public_key, private_seed) for operator or test fixtures only."""
    private = Ed25519PrivateKey.generate()
    return private.public_key().public_bytes_raw(), private.private_bytes_raw()


def load_private_key(path: str | Path) -> bytes:
    text = private_bytes(path, 512).decode().strip()
    try:
        decoded = base64.b64decode(text, validate=True)
    except Exception as exc:
        raise ValueError("invalid private key") from exc
    if len(decoded) == _ED25519_SEED:
        return decoded
    if len(decoded) == _ED25519_PRIVATE:
        return decoded[:_ED25519_SEED]
    raise ValueError("invalid private key")


def load_public_key(path: str | Path) -> bytes:
    return _decode_b64(
        private_bytes(path, 512).decode().strip(), _ED25519_PUBLIC, "public key"
    )


def load_trust_keys(source: str | Path | Mapping[str, bytes]) -> dict[str, bytes]:
    if isinstance(source, Mapping):
        mapped = {
            _normalize_key_id(key_id): bytes(public)
            for key_id, public in source.items()
        }
        for public in mapped.values():
            if len(public) != _ED25519_PUBLIC:
                raise ValueError("invalid trust public key")
        if not mapped or len(mapped) > _MAX_TRUST_KEYS:
            raise ValueError("invalid trust key set")
        return mapped
    path = Path(source)
    if path.stat().st_size > 256 * 1024:
        raise ValueError("trust file too large")
    document = strict_json(private_bytes(path, 256 * 1024))
    if document.get("schema_version") != TRUST_SCHEMA:
        raise ValueError("incompatible trust schema")
    entries = document.get("keys")
    if not isinstance(entries, list) or not entries or len(entries) > _MAX_TRUST_KEYS:
        raise ValueError("invalid trust key set")
    trusted: dict[str, bytes] = {}
    for entry in entries:
        if not isinstance(entry, Mapping):
            raise ValueError("invalid trust key entry")  # noqa: TRY004
        key_id = _normalize_key_id(str(entry.get("key_id", "")))
        if entry.get("algorithm") != ALGORITHM or key_id in trusted:
            raise ValueError("invalid trust key entry")
        trusted[key_id] = _decode_b64(
            str(entry.get("public_key_base64", "")), _ED25519_PUBLIC, "public key"
        )
    return trusted


def signature_message(
    *, key_id: str, manifest_version: str, manifest_sha256: str
) -> bytes:
    return (
        f"{SIGNATURE_SCHEMA}\n{ALGORITHM}\n{key_id}\n{manifest_version}\n{manifest_sha256}\n"
    ).encode()


def write_trust_keys(path: str | Path, keys: Mapping[str, bytes]) -> None:
    trusted = load_trust_keys(keys)
    payload = {
        "schema_version": TRUST_SCHEMA,
        "keys": [
            {
                "key_id": key_id,
                "algorithm": ALGORITHM,
                "public_key_base64": base64.b64encode(public).decode(),
            }
            for key_id, public in sorted(trusted.items())
        ],
    }
    destination = Path(path)
    destination.write_bytes(canonical_json(payload))
    destination.chmod(0o600)


def sign_bundle(
    path: str | Path, private_key: bytes | str | Path, key_id: str
) -> dict[str, Any]:
    root = private_directory(path)
    key_id = _normalize_key_id(key_id)
    seed = (
        private_key
        if isinstance(private_key, (bytes, bytearray))
        else load_private_key(private_key)
    )
    if len(seed) == _ED25519_PRIVATE:
        seed = seed[:_ED25519_SEED]
    if len(seed) != _ED25519_SEED:
        raise ValueError("invalid private key")
    manifest_file = _file(root, "manifest.json")
    manifest_bytes = private_bytes(manifest_file, 4 * 1024 * 1024)
    manifest = strict_json(manifest_bytes)
    version = manifest.get("version")
    if not isinstance(version, str) or version != bundle_version(manifest):
        raise ValueError("manifest digest mismatch")
    digest = hashlib.sha256(manifest_bytes).hexdigest()
    message = signature_message(
        key_id=key_id, manifest_version=version, manifest_sha256=digest
    )
    signature = Ed25519PrivateKey.from_private_bytes(bytes(seed)).sign(message)
    envelope = {
        "schema_version": SIGNATURE_SCHEMA,
        "algorithm": ALGORITHM,
        "key_id": key_id,
        "manifest_version": version,
        "manifest_sha256": digest,
        "signature_base64": base64.b64encode(signature).decode(),
    }
    destination = root / SIGNATURE_FILE
    if destination.exists() and (
        destination.is_symlink() or not destination.is_file()
    ):
        raise ValueError("unsafe signature path")
    destination.write_bytes(canonical_json(envelope))
    destination.chmod(0o600)
    return envelope


def verify_bundle_signature(
    path: str | Path, trusted_keys: str | Path | Mapping[str, bytes]
) -> str:
    root = private_directory(path)
    keys = load_trust_keys(trusted_keys)
    signature_path = _file(root, SIGNATURE_FILE)
    if signature_path.stat().st_size > 64 * 1024:
        raise ValueError("signature too large")
    envelope = strict_json(private_bytes(signature_path, 64 * 1024))
    if (
        envelope.get("schema_version") != SIGNATURE_SCHEMA
        or envelope.get("algorithm") != ALGORITHM
    ):
        raise ValueError("incompatible bundle signature")
    key_id = _normalize_key_id(str(envelope.get("key_id", "")))
    public = keys.get(key_id)
    if public is None:
        raise ValueError("unknown signature key id")
    manifest_file = _file(root, "manifest.json")
    manifest_bytes = private_bytes(manifest_file, 4 * 1024 * 1024)
    manifest = strict_json(manifest_bytes)
    version = manifest.get("version")
    digest = hashlib.sha256(manifest_bytes).hexdigest()
    if (
        not isinstance(version, str)
        or envelope.get("manifest_version") != version
        or envelope.get("manifest_sha256") != digest
        or version != bundle_version(manifest)
    ):
        raise ValueError("signature hash binding mismatch")
    signature = _decode_b64(
        str(envelope.get("signature_base64", "")), _ED25519_SIGNATURE, "signature"
    )
    message = signature_message(
        key_id=key_id, manifest_version=version, manifest_sha256=digest
    )
    try:
        Ed25519PublicKey.from_public_bytes(public).verify(signature, message)
    except InvalidSignature as exc:
        raise ValueError("bundle signature mismatch") from exc
    return key_id


@dataclass(frozen=True)
class TargetModel:
    quality: Any
    out_tokens: Any
    calibration_x: tuple[float, ...]
    calibration_y: tuple[float, ...]
    n_train: int


@dataclass(frozen=True)
class ModelBundle:
    version: str
    manifest: Mapping[str, Any]
    builder: FeatureBuilder
    models: Mapping[TargetKey, TargetModel]
    baseline: Mapping[TargetKey, Prediction]
    threads: int = 1
    signature_key_id: str | None = None

    def build(self, payload: Any, turn_index: int | None = None) -> Vector:
        return self.builder.build(payload, turn_index)

    def predict(
        self, vector: Vector, targets: Iterable[TargetKey] | None = None
    ) -> dict[TargetKey, Prediction]:
        vector = np.asarray(vector, dtype=np.float32)
        if vector.shape != (len(FEATURE_NAMES),) or not np.isfinite(vector).all():
            raise ValueError("invalid prediction vector")
        predictions = {}
        for key in self.models if targets is None else targets:
            if key not in self.models:
                continue
            model = self.models[key]
            raw = float(
                model.quality.predict(vector[None, :], num_threads=self.threads)[0]
            )
            quality = float(np.interp(raw, model.calibration_x, model.calibration_y))
            log_tokens = float(
                model.out_tokens.predict(vector[None, :], num_threads=self.threads)[0]
            )
            output = math.expm1(min(max(log_tokens, 0), math.log1p(10_000_000)))
            if not math.isfinite(quality) or not math.isfinite(output):
                raise ValueError("nonfinite model prediction")
            predictions[key] = Prediction(quality, output)
        return predictions

    def bt_predictions(
        self, targets: Iterable[TargetKey] | None = None
    ) -> dict[TargetKey, Prediction]:
        return {
            key: self.baseline[key]
            for key in (self.baseline if targets is None else targets)
            if key in self.baseline
        }

    def _explain_target(
        self, vector: Vector, key: TargetKey, limit: int = 10
    ) -> list[dict[str, Any]]:
        if key not in self.models:
            return []
        values = np.asarray(
            self.models[key].quality.predict(
                vector[None, :], pred_contrib=True, num_threads=self.threads
            )
        )[0, :-1]
        order = np.argsort(-np.abs(values), kind="stable")[: min(max(limit, 0), 10)]
        return [
            {"feature": FEATURE_NAMES[i], "contribution": float(values[i])}
            for i in order
        ]

    def explain(
        self, vector: Vector, keys: Iterable[TargetKey] | None = None
    ) -> dict[TargetKey, list[dict[str, Any]]]:
        return {
            key: self._explain_target(vector, key)
            for key in (self.models if keys is None else keys)
        }


def load_bundle(
    path: str | Path,
    threads: int = 1,
    *,
    require_signed: bool = False,
    trusted_keys: str | Path | Mapping[str, bytes] | None = None,
    device: str = "cpu",
    strict_device: bool = False,
) -> ModelBundle:
    import lightgbm as lgb

    from lrp.device import check_device_class_compatibility, resolve_compute_device

    capped_threads(threads)
    root = private_directory(path)
    manifest_file = _file(root, "manifest.json")
    if manifest_file.stat().st_size > 4 * 1024 * 1024:
        raise ValueError("manifest too large")
    manifest = strict_json(private_bytes(manifest_file, 4 * 1024 * 1024))
    if manifest.get("schema_version") != SCHEMA_VERSION or manifest.get(
        "feature_names"
    ) != list(FEATURE_NAMES):
        raise ValueError("incompatible bundle schema or features")
    if manifest.get("version") != bundle_version(manifest):
        raise ValueError("manifest digest mismatch")
    hashes = manifest.get("files", {})
    if not isinstance(hashes, dict) or len(hashes) > 2048:
        raise ValueError("invalid bundle file inventory")
    for name, digest in hashes.items():
        if sha256_file(_file(root, name)) != digest:
            raise ValueError("bundle file digest mismatch")

    signature_key_id: str | None = None
    signature_present = (root / SIGNATURE_FILE).is_file()
    if require_signed and not signature_present:
        raise ValueError("bundle signature required")
    if require_signed or signature_present:
        if trusted_keys is None:
            raise ValueError(
                "trusted keys required"
                if require_signed
                else "bundle signature present without trusted keys"
            )
        signature_key_id = verify_bundle_signature(root, trusted_keys)

    def verified(name: str) -> Path:
        if name not in hashes:
            raise ValueError("unhashed bundle dependency")
        return _file(root, name)

    embedding = dict(manifest["embedding"])
    kind = embedding.get("kind")
    if kind == "synthetic":
        if not manifest.get("synthetic"):
            raise ValueError("synthetic embedding requires synthetic provenance")
        builder = FeatureBuilder(create_embedder({"kind": "synthetic"}))
    elif kind == "onnx":
        embedding["backend"] = "onnxruntime"
        embedding["model_path"] = str(verified(embedding["model_path"]))
        embedding["tokenizer_path"] = str(verified(embedding["tokenizer_path"]))
        embedding.setdefault("max_seq_len", 512)
        embedding.setdefault("precision", "int8")
        embedding.setdefault("device_class", "cpu")
        builder = FeatureBuilder(
            create_embedder(
                embedding,
                threads=threads,
                device=device,
                strict_device=strict_device,
            )
        )
    elif kind == "sentence-transformers":
        embedding["backend"] = "sentence-transformers"
        model_rel = str(embedding["model_path"])
        rel = PurePosixPath(model_rel)
        if rel.is_absolute() or ".." in rel.parts or "\\" in model_rel or not rel.parts:
            raise ValueError("unsafe bundle path")
        model_dir = root.joinpath(*rel.parts)
        prefix = model_rel.rstrip("/") + "/"
        if not any(name.startswith(prefix) for name in hashes):
            raise ValueError("unhashed bundle dependency")
        if (
            model_dir.is_symlink()
            or not model_dir.is_dir()
            or not model_dir.resolve().is_relative_to(root.resolve())
        ):
            raise ValueError("missing or unsafe bundle file")
        embedding["model_path"] = str(model_dir)
        embedding.setdefault("max_seq_len", 512)
        embedding.setdefault("precision", "fp32")
        embedding.setdefault("device_class", "cpu")
        builder = FeatureBuilder(
            create_embedder(
                embedding,
                threads=threads,
                device=device,
                strict_device=strict_device,
            )
        )
    else:
        raise ValueError("unknown embedding kind")
    if builder.embedder.fingerprint != manifest.get("embedding_fingerprint"):
        raise ValueError("embedding feature fingerprint mismatch")
    serve_backend: Literal["onnxruntime", "sentence-transformers"] = (
        "onnxruntime" if kind in {"synthetic", "onnx"} else "sentence-transformers"
    )
    serve_resolved = resolve_compute_device(
        device, strict_device=strict_device, backend=serve_backend
    )
    check_device_class_compatibility(
        train_device_class=manifest.get("train_device_class"),
        serve_device_class=serve_resolved.device_class,
        strict_device=strict_device,
    )
    models: dict[TargetKey, TargetModel] = {}
    baseline = {}
    seen = set()
    for entry in manifest["targets"]:
        key = target_key(entry)
        if key in seen or entry["id"] != target_id(key):
            raise ValueError("duplicate or inconsistent bundle target")
        seen.add(key)
        strength, output = float(entry["bt_strength"]), float(entry["mean_out_tokens"])
        if (
            not math.isfinite(strength)
            or not 0 <= strength <= 1
            or not math.isfinite(output)
            or output < 0
        ):
            raise ValueError("invalid baseline prediction")
        if entry.get("skipped"):
            continue
        if entry["n_train"] < max(200, manifest.get("min_train_rows", 200)):
            raise ValueError("undertrained learned model")
        baseline[key] = Prediction(strength, output)
        calibration = strict_json(
            private_bytes(verified(entry["calibration_file"]), 4 * 1024 * 1024)
        )
        xs, ys = calibration["x"], calibration["y"]
        if (
            not xs
            or len(xs) != len(ys)
            or any(not math.isfinite(v) for v in [*xs, *ys])
        ):
            raise ValueError("invalid calibration")
        if (
            any(a >= b for a, b in pairwise(xs))
            or any(a > b for a, b in pairwise(ys))
            or any(not 0 <= v <= 1 for v in ys)
        ):
            raise ValueError("nonmonotone calibration")
        quality = lgb.Booster(
            model_str=private_bytes(verified(entry["quality_file"])).decode()
        )
        tokens = lgb.Booster(
            model_str=private_bytes(verified(entry["out_tokens_file"])).decode()
        )
        if (
            quality.num_feature() != len(FEATURE_NAMES)
            or tokens.num_feature() != len(FEATURE_NAMES)
            or quality.feature_name() != list(FEATURE_NAMES)
            or tokens.feature_name() != list(FEATURE_NAMES)
        ):
            raise ValueError("model feature count mismatch")
        models[key] = TargetModel(
            quality, tokens, tuple(xs), tuple(ys), entry["n_train"]
        )
    if not models:
        raise ValueError("bundle has no trained targets")
    snapshot = ModelBundle(
        manifest["version"],
        MappingProxyType(manifest),
        builder,
        MappingProxyType(models),
        MappingProxyType(baseline),
        threads,
        signature_key_id,
    )
    # Validation and readiness include a real inference through both stages.
    snapshot.predict(snapshot.build({"text": "Synthetic warmup request."}))
    return snapshot


class AtomicBundle:
    def __init__(self, bundle: ModelBundle | None = None) -> None:
        self._bundle = bundle
        self._lock = threading.Lock()
        self._reload_lock = threading.Lock()

    def snapshot(self) -> ModelBundle | None:
        with self._lock:
            return self._bundle

    def reload(
        self,
        path: str | Path,
        threads: int = 1,
        *,
        require_signed: bool = False,
        trusted_keys: str | Path | Mapping[str, bytes] | None = None,
        device: str = "cpu",
        strict_device: bool = False,
    ) -> ModelBundle:
        with self._reload_lock:
            candidate = load_bundle(
                path,
                threads,
                require_signed=require_signed,
                trusted_keys=trusted_keys,
                device=device,
                strict_device=strict_device,
            )
            with self._lock:
                self._bundle = candidate
            return candidate


def validate(
    path: str | Path,
    threads: int = 1,
    *,
    require_signed: bool = False,
    trusted_keys: str | Path | Mapping[str, bytes] | None = None,
    device: str = "cpu",
    strict_device: bool = False,
) -> ModelBundle:
    return load_bundle(
        path,
        threads,
        require_signed=require_signed,
        trusted_keys=trusted_keys,
        device=device,
        strict_device=strict_device,
    )
