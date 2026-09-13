# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""One bounded feature path for normalized router IR and offline requests."""

from __future__ import annotations

import hashlib
import json
import math
import os
import re
import stat
import threading
from collections.abc import Iterator
from contextlib import contextmanager
from pathlib import Path
from typing import Any, BinaryIO, Protocol

import numpy as np
import numpy.typing as npt

from lrp.collect import (
    MAX_FILE_BYTES,
    MAX_ROW_BYTES,
    MAX_ROWS,
    DataError,
    _check_file,
    canonical,
    protected_path,
    validate_record,
)
from lrp.collect import read_rows as protected_rows

Vector = npt.NDArray[np.float32]
DIMENSIONS = 384
TEXT_LIMIT = 32768
SCALAR_NAMES = (
    "estimatedTokens",
    "textChars",
    "systemChars",
    "messageCount",
    "toolCount",
    "hasTools",
    "hasStructuredOutput",
    "maxTokens",
    "temperatureSet",
    "stream",
    "reasoning_requested",
    "reasoning_effort",
    "stopCount",
    "turn_index",
    "lang_id",
    "code_fence_count",
    "url_count",
    "json_brace_ratio",
    "question_mark_count",
)
FEATURE_NAMES = tuple(f"emb_{i:03d}" for i in range(DIMENSIONS)) + SCALAR_NAMES
EMBEDDING_NAMES = tuple(f"emb_{i:03d}" for i in range(DIMENSIONS))
FEATURE_VERSION = "lrp.features.v1"
# Reviewed default from LRP design (#15/#31): cosine above this drops later near-duplicates.
NEAR_DUP_COSINE_DEFAULT = 0.98
NEAR_DUP_SENSITIVITY_THRESHOLDS = (0.95, 0.98, 0.99)


def private_directory(path: str | Path, *, create: bool = False) -> Path:
    destination = protected_path(Path(path))
    if create and not destination.exists():
        if not destination.parent.exists():
            private_directory(destination.parent, create=True)
        destination.mkdir(mode=0o700, exist_ok=True)
    if destination.exists():
        info = destination.stat()
        if (
            not stat.S_ISDIR(info.st_mode)
            or info.st_uid != os.getuid()
            or info.st_mode & 0o077
        ):
            raise DataError("private_directory_required")
    return destination


def preflight_file(path: str | Path) -> Path:
    destination = protected_path(Path(path))
    if destination.exists():
        with private_file(destination):
            pass
    return destination


@contextmanager
def private_file(path: str | Path, *, write: bool = False) -> Iterator[BinaryIO]:
    destination = protected_path(Path(path))
    if write:
        private_directory(destination.parent, create=True)
    flags = os.O_RDWR | os.O_CREAT if write else os.O_RDONLY
    fd = os.open(destination, flags | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    with os.fdopen(fd, "r+b" if write else "rb") as handle:
        _check_file(handle.fileno())
        if write:
            import fcntl

            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
            handle.truncate(0)
        yield handle
        if write:
            if handle.tell() > MAX_FILE_BYTES:
                raise DataError("file_size_limit")
            handle.flush()
            os.fsync(handle.fileno())


def private_bytes(path: str | Path, limit: int = MAX_FILE_BYTES) -> bytes:
    with private_file(path) as handle:
        data = handle.read(limit + 1)
    if len(data) > limit:
        raise DataError("file_size_limit")
    return data


def write_private(path: str | Path, data: bytes) -> None:
    if len(data) > MAX_FILE_BYTES:
        raise DataError("file_size_limit")
    with private_file(path, write=True) as handle:
        handle.write(data)


def embedding_fingerprint(spec: dict[str, Any]) -> str:
    identity: dict[str, Any] = {
        "feature_version": FEATURE_VERSION,
        "kind": spec["kind"],
    }
    if spec["kind"] == "onnx":
        for key in ("model_path", "tokenizer_path"):
            digest = hashlib.sha256()
            with private_file(Path(spec[key])) as stream:
                for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                    digest.update(chunk)
            identity[key] = digest.hexdigest()
        identity.update(
            pooling=spec.get("pooling", "cls"), prefix=spec.get("prefix", "")
        )
    return hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest()


LANGUAGES = (
    "unknown",
    "af",
    "ar",
    "bg",
    "bn",
    "ca",
    "cs",
    "cy",
    "da",
    "de",
    "el",
    "en",
    "es",
    "et",
    "fa",
    "fi",
    "fr",
    "gu",
    "he",
    "hi",
    "hr",
    "hu",
    "id",
    "it",
    "ja",
    "kn",
    "ko",
    "lt",
    "lv",
    "mk",
    "ml",
    "mr",
    "ne",
    "nl",
    "no",
    "pa",
    "pl",
    "pt",
    "ro",
    "ru",
    "sk",
    "sl",
    "so",
    "sq",
    "sv",
    "sw",
    "ta",
    "te",
    "th",
    "tl",
    "tr",
    "uk",
    "ur",
    "vi",
    "zh-cn",
    "zh-tw",
)


def as_dict(value: Any) -> dict[str, Any]:
    if hasattr(value, "model_dump"):
        return dict(value.model_dump(by_alias=True))
    if isinstance(value, dict):
        return value
    raise ValueError("expected a mapping")


def capped_threads(threads: int) -> int:
    if isinstance(threads, bool) or not 1 <= threads <= 4:
        raise ValueError("threads must be between 1 and 4")
    return threads


class Embedder(Protocol):
    kind: str
    fingerprint: str

    def encode(self, text: str) -> Vector: ...


class SyntheticEmbedder:
    """Explicit test wiring only; never a semantic embedding fallback."""

    kind = "synthetic"
    fingerprint = embedding_fingerprint({"kind": "synthetic"})

    def encode(self, text: str) -> Vector:
        result = np.zeros(DIMENSIONS, dtype=np.float32)
        for token in re.findall(r"\w+|[^\w\s]", text[-TEXT_LIMIT:].lower())[-512:]:
            digest = hashlib.sha256(token.encode()).digest()
            result[int.from_bytes(digest[:4], "big") % DIMENSIONS] += (
                1.0 if digest[4] & 1 else -1.0
            )
        norm = float(np.linalg.norm(result))
        return result / norm if norm else result


class ONNXEmbedder:
    """Local int8 transformer inference, left truncation, explicit pooling.

    BGE uses CLS pooling and L2 normalization. Mean pooling supports operator
    exports such as E5; the model-specific text prefix is manifest-bound.
    No downloads, remote model code, or automatic synthetic fallback occur.
    """

    kind = "onnx"

    def __init__(
        self,
        model_path: str | Path,
        tokenizer_path: str | Path,
        threads: int = 1,
        pooling: str = "cls",
        prefix: str = "",
    ) -> None:
        import onnx
        import onnxruntime as ort
        from tokenizers import Tokenizer

        if pooling not in {"cls", "mean"}:
            raise ValueError("invalid embedding pooling")
        self.pooling, self.prefix = pooling, prefix
        self.fingerprint = embedding_fingerprint(
            {
                "kind": "onnx",
                "model_path": model_path,
                "tokenizer_path": tokenizer_path,
                "pooling": pooling,
                "prefix": prefix,
            }
        )
        model_bytes = private_bytes(model_path)
        tokenizer_bytes = private_bytes(tokenizer_path, 16 * 1024 * 1024)
        graph = onnx.load_model_from_string(model_bytes)
        if any(t.external_data for t in graph.graph.initializer):
            raise ValueError("external ONNX tensor files are unsupported")
        quantized = any(t.data_type in (2, 3) for t in graph.graph.initializer)
        if not quantized:
            raise ValueError("embedding must contain int8 or uint8 weights")
        os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")
        self.tokenizer = Tokenizer.from_str(tokenizer_bytes.decode("utf-8"))
        self.tokenizer.enable_truncation(max_length=512, direction="left")
        self.tokenizer.no_padding()
        options = ort.SessionOptions()
        options.intra_op_num_threads = capped_threads(threads)
        options.inter_op_num_threads = 1
        options.execution_mode = ort.ExecutionMode.ORT_SEQUENTIAL
        options.add_session_config_entry("session.intra_op.allow_spinning", "0")
        options.add_session_config_entry("session.inter_op.allow_spinning", "0")
        available = ort.get_available_providers()
        # Prefer CUDA when present. Evidence and promotion runs require a real
        # GPU host; CPUExecutionProvider remains for unit tests and CI only.
        providers = (
            ["CUDAExecutionProvider", "CPUExecutionProvider"]
            if "CUDAExecutionProvider" in available
            else ["CPUExecutionProvider"]
        )
        self.session = ort.InferenceSession(
            model_bytes, sess_options=options, providers=providers
        )
        self.ort_providers = list(self.session.get_providers())

    def encode(self, text: str) -> Vector:
        encoded = self.tokenizer.encode(self.prefix + text[-TEXT_LIMIT:])
        values = {
            "input_ids": encoded.ids,
            "attention_mask": encoded.attention_mask,
            "token_type_ids": encoded.type_ids,
        }
        feed = {}
        for item in self.session.get_inputs():
            if item.name not in values or item.type not in {
                "tensor(int64)",
                "tensor(int32)",
            }:
                raise ValueError("unsupported embedding input contract")
            dtype = np.int64 if item.type == "tensor(int64)" else np.int32
            feed[item.name] = np.asarray([values[item.name]], dtype=dtype)
        output = np.asarray(self.session.run(None, feed)[0], dtype=np.float32)
        if output.ndim == 3:
            if self.pooling == "cls":
                vector = output[0, 0]
            else:
                mask = np.asarray(encoded.attention_mask, dtype=np.float32)
                vector = (output[0] * mask[:, None]).sum(axis=0) / max(
                    float(mask.sum()), 1
                )
        elif output.ndim == 2 and output.shape[0] == 1:
            vector = output[0]
        else:
            raise ValueError("unsupported embedding output contract")
        if vector.shape != (DIMENSIONS,) or not np.isfinite(vector).all():
            raise ValueError("invalid embedding vector")
        norm = float(np.linalg.norm(vector))
        if norm <= 0:
            raise ValueError("zero embedding vector")
        return np.asarray(vector / norm, dtype=np.float32)


def _text(value: Any) -> str:
    if isinstance(value, str):
        return value[-TEXT_LIMIT:]
    if isinstance(value, list):
        return "\n".join(_text(p) for p in value[-128:])[-TEXT_LIMIT:]
    if isinstance(value, dict) and value.get("type") in {
        "text",
        "input_text",
        "output_text",
    }:
        return _text(value.get("text", ""))
    return ""


def normalize(payload: Any) -> tuple[dict[str, Any], str, str, list[Any]]:
    data = as_dict(payload)
    request = as_dict(data.get("request") or data)
    messages = request.get("messages") or []
    system = _text(request.get("system", ""))
    turns: list[str] = []
    for raw in messages[-128:]:
        message = as_dict(raw)
        content = _text(message.get("content", "")) or _text(message.get("parts", []))
        if message.get("role") in {"system", "developer"}:
            system = (system + "\n" + content)[-TEXT_LIMIT:]
        elif message.get("role") in {"user", "assistant"}:
            turns.append(content)
    input_text = _text(request.get("input", "")) or _text(
        request.get("input_parts", [])
    )
    if input_text:
        turns.append(input_text)
    if not turns:
        turns.append(_text(data.get("text", "")))
    text = "\n\n".join([system, *turns[-6:]])[-TEXT_LIMIT:]
    return request, text, system, messages


def _number(value: Any) -> float:
    try:
        number = float(value)
    except (TypeError, ValueError):
        return 0.0
    return min(max(number, 0.0), 1e12) if math.isfinite(number) else 0.0


class FeatureBuilder:
    def __init__(self, embedder: Embedder) -> None:
        self.embedder = embedder
        from langdetect.detector_factory import PROFILES_DIRECTORY, DetectorFactory

        self._language_factory = DetectorFactory()
        self._language_factory.load_profile(PROFILES_DIRECTORY)
        self._language_factory.set_seed(42)
        self._language_lock = threading.Lock()

    def language(self, text: str) -> str:
        from langdetect.lang_detect_exception import LangDetectException

        if not text.strip():
            return "unknown"
        with self._language_lock:
            detector = self._language_factory.create()
        detector.append(text[-2048:])
        try:
            code = str(detector.detect())
            return code if code in LANGUAGES else "unknown"
        except LangDetectException:
            return "unknown"

    def build(self, payload: Any, turn_index: int | None = None) -> Vector:
        data = as_dict(payload)
        request, text, system, messages = normalize(data)
        context = as_dict(data.get("context") or {})
        reasoning = as_dict(context.get("reasoning") or request.get("reasoning") or {})
        byte_count = len(text.encode("utf-8"))
        tools = request.get("tools") or []
        defaults: dict[str, Any] = {
            "estimatedTokens": max(1, (byte_count + 3) // 4),
            "textChars": byte_count,
            "systemChars": len(system.encode("utf-8")),
            "messageCount": len(messages),
            "toolCount": len(tools),
            "hasTools": bool(tools),
            "hasStructuredOutput": bool(request.get("response_format")),
            "maxTokens": request.get("max_tokens", request.get("max_output_tokens", 0)),
            "temperatureSet": request.get("temperature") is not None,
            "stream": request.get("stream", False),
            "stopCount": len(request.get("stop") or []),
        }
        scalar = {
            name: _number(context.get(name, value)) for name, value in defaults.items()
        }
        scalar["reasoning_requested"] = float(bool(reasoning.get("requested", False)))
        scalar["reasoning_effort"] = float(
            {"minimal": 1, "low": 2, "medium": 3, "high": 4, "xhigh": 5}.get(
                str(reasoning.get("effort", "")), 0
            )
        )
        # Derive from content in both paths, unless the serving session tracker
        # deliberately supplies the same explicit turn index used offline.
        scalar["turn_index"] = _number(
            turn_index
            if turn_index is not None
            else max(0, sum(as_dict(m).get("role") == "user" for m in messages) - 1)
        )
        scalar["lang_id"] = float(LANGUAGES.index(self.language(text)))
        scalar.update(
            code_fence_count=text.count("```"),
            url_count=len(re.findall(r"https?://", text)),
            json_brace_ratio=(text.count("{") + text.count("}")) / max(len(text), 1),
            question_mark_count=text.count("?"),
        )
        vector = np.concatenate(
            (
                self.embedder.encode(text),
                np.asarray([scalar[n] for n in SCALAR_NAMES], dtype=np.float32),
            )
        )
        if vector.shape != (len(FEATURE_NAMES),) or not np.isfinite(vector).all():
            raise ValueError("invalid feature vector")
        return np.asarray(vector, dtype=np.float32)


def build(payload: Any, *, embedder: Embedder, turn_index: int | None = None) -> Vector:
    return FeatureBuilder(embedder).build(payload, turn_index)


def session_split(session_key: str, seed: int = 42) -> str:
    if not session_key:
        raise ValueError("session_key is required for leakage-free splits")
    bucket = (
        int.from_bytes(
            hashlib.sha256(f"{seed}\0{session_key}".encode()).digest()[:8], "big"
        )
        % 100
    )
    return "train" if bucket < 70 else "valid" if bucket < 85 else "test"


def _validate_cosine_threshold(threshold: float) -> float:
    if isinstance(threshold, bool) or not isinstance(threshold, (int, float)):
        raise TypeError("near_dup_cosine must be a finite float in (0, 1]")
    value = float(threshold)
    if not math.isfinite(value) or not 0.0 < value <= 1.0:
        raise ValueError("near_dup_cosine must be a finite float in (0, 1]")
    return value


def embedding_matrix(rows: list[dict[str, Any]] | Any) -> Vector:
    """Return L2-normalized embedding rows only (no request content)."""
    if hasattr(rows, "loc"):
        matrix = np.asarray(rows.loc[:, list(EMBEDDING_NAMES)], dtype=np.float32)
    else:
        matrix = np.asarray(
            [[float(row[name]) for name in EMBEDDING_NAMES] for row in rows],
            dtype=np.float32,
        )
    if matrix.ndim != 2 or matrix.shape[1] != DIMENSIONS:
        raise ValueError("invalid embedding matrix")
    if matrix.size and not np.isfinite(matrix).all():
        raise ValueError("non-finite embedding values")
    return matrix


def near_duplicate_keep_mask(
    embeddings: Vector, threshold: float = NEAR_DUP_COSINE_DEFAULT
) -> npt.NDArray[np.bool_]:
    """Keep earliest rows; drop later rows with cosine strictly above threshold.

    Embeddings must already be L2-normalized so cosine equals the dot product.
    Comparison order is row order (earliest first). No request text is retained.
    """
    limit = _validate_cosine_threshold(threshold)
    matrix = np.asarray(embeddings, dtype=np.float32)
    if matrix.ndim != 2 or matrix.shape[1] != DIMENSIONS:
        raise ValueError("invalid embedding matrix")
    keep = np.ones(matrix.shape[0], dtype=bool)
    kept: list[int] = []
    for index in range(matrix.shape[0]):
        if kept:
            similarity = matrix[kept] @ matrix[index]
            if float(np.max(similarity)) > limit:
                keep[index] = False
                continue
        kept.append(index)
    return keep


def near_duplicate_sensitivity(
    embeddings: Vector,
    thresholds: tuple[float, ...] = NEAR_DUP_SENSITIVITY_THRESHOLDS,
) -> dict[str, int]:
    """Scalar removed counts at reviewed thresholds; never stores request content."""
    matrix = np.asarray(embeddings, dtype=np.float32)
    report: dict[str, int] = {}
    for threshold in thresholds:
        limit = _validate_cosine_threshold(threshold)
        removed = int((~near_duplicate_keep_mask(matrix, limit)).sum())
        report[f"{limit:.2f}"] = removed
    return report


def deduplicate_near_duplicates(
    rows: list[dict[str, Any]],
    *,
    threshold: float = NEAR_DUP_COSINE_DEFAULT,
    sensitivity_thresholds: tuple[float, ...] = NEAR_DUP_SENSITIVITY_THRESHOLDS,
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    """Drop near-duplicates before split assignment; keep earliest samples.

    Returns kept rows plus a content-free report of removed counts and
    threshold sensitivity. Request text, tool payloads and prompts are never
    copied into the report.
    """
    limit = _validate_cosine_threshold(threshold)
    matrix = embedding_matrix(rows)
    keep = near_duplicate_keep_mask(matrix, limit)
    kept = [row for row, retain in zip(rows, keep, strict=True) if retain]
    report = {
        "schema_version": "lrp.near_dup.v1",
        "threshold": limit,
        "input_rows": len(rows),
        "kept_rows": len(kept),
        "removed_rows": int((~keep).sum()),
        "sensitivity_removed": near_duplicate_sensitivity(
            matrix, sensitivity_thresholds
        ),
        "synthetic": any(bool(row.get("synthetic")) for row in rows),
    }
    return kept, report


def read_rows(source: Any, kind: str = "request") -> list[dict[str, Any]]:
    iterator = (
        protected_rows(Path(source))
        if isinstance(source, (str, Path))
        else iter(source)
    )
    result: list[dict[str, Any]] = []
    for row in iterator:
        value = as_dict(row)
        if len(result) >= MAX_ROWS or len(canonical(value).encode()) > MAX_ROW_BYTES:
            raise DataError("dataset_limit")
        result.append(validate_record(value, kind))
    return result


def featurize(
    requests: Any,
    out: str | Path | None = None,
    *,
    builder: FeatureBuilder,
    seed: int = 42,
    near_dup_cosine: float | None = None,
) -> Any:
    import pandas as pd

    if out is not None:
        destination = preflight_file(out)
        temporary = preflight_file(destination.with_suffix(destination.suffix + ".tmp"))
        near_dup_path = preflight_file(
            destination.with_name(destination.name + ".near_dup.json")
        )
    rows = []
    seen: set[str] = set()
    for request in read_rows(requests):
        request_id = str(request["request_id"])
        if request_id in seen:
            raise ValueError("duplicate request_id")
        seen.add(request_id)
        vector = builder.build(request)
        session = str(request.get("session_key") or "")
        row = dict(zip(FEATURE_NAMES, vector, strict=True))
        row.update(
            request_id=request_id,
            session_key=session,
            group=request.get("group", ""),
            source=request.get("source", "unknown"),
            synthetic=builder.embedder.kind == "synthetic"
            or request.get("source") == "synthetic",
            embedding_kind=builder.embedder.kind,
            embedding_fingerprint=builder.embedder.fingerprint,
        )
        row["language"] = LANGUAGES[int(vector[FEATURE_NAMES.index("lang_id")])]
        rows.append(row)
    near_duplicate: dict[str, Any] = {
        "schema_version": "lrp.near_dup.v1",
        "enabled": False,
        "input_rows": len(rows),
        "kept_rows": len(rows),
        "removed_rows": 0,
        "threshold": None,
        "sensitivity_removed": {},
        "synthetic": any(bool(row.get("synthetic")) for row in rows),
    }
    if near_dup_cosine is not None:
        rows, near_duplicate = deduplicate_near_duplicates(
            rows, threshold=near_dup_cosine
        )
        near_duplicate["enabled"] = True
    for row in rows:
        row["split"] = session_split(str(row["session_key"]), seed)
    frame = pd.DataFrame(rows)
    frame.attrs["near_duplicate"] = near_duplicate
    if out is not None:
        with private_file(temporary, write=True) as handle:
            frame.to_parquet(handle, index=False)
        write_private(near_dup_path, (canonical(near_duplicate) + "\n").encode())
        preflight_file(destination)
        temporary.replace(destination)
    return frame