# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Bounded, governed offline collection; router JSONL is metadata, never content."""

from __future__ import annotations

import fcntl
import hashlib
import importlib
import json
import os
import stat
from collections.abc import Callable, Iterator
from contextlib import contextmanager
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, BinaryIO, cast

MAX_ROW_BYTES = 2 * 1024 * 1024
MAX_ROWS = 100_000
MAX_FILE_BYTES = 512 * 1024 * 1024


class DataError(ValueError):
    """Messages are fixed safe classifications, never validation input."""


def utc_now() -> str:
    return datetime.now(UTC).isoformat().replace("+00:00", "Z")


def canonical(value: Any) -> str:
    return json.dumps(
        value,
        sort_keys=True,
        separators=(",", ":"),
        ensure_ascii=False,
        allow_nan=False,
    )


def identity(value: Any) -> str:
    return hashlib.sha256(canonical(value).encode()).hexdigest()


def strict_json(raw: str | bytes) -> dict[str, Any]:
    def pairs(items: list[tuple[str, Any]]) -> dict[str, Any]:
        result: dict[str, Any] = {}
        for key, value in items:
            if key in result:
                raise DataError("duplicate_json_key")
            result[key] = value
        return result

    def bad_constant(_: str) -> Any:
        raise DataError("nonfinite_json")

    try:
        value = json.loads(raw, object_pairs_hook=pairs, parse_constant=bad_constant)
        if not isinstance(value, dict):
            raise DataError("object_required")
        return value
    except (ValueError, UnicodeError, RecursionError):
        raise DataError("invalid_json") from None


def protected_path(path: Path, *, fixture_read: bool = False) -> Path:
    """Reject symlinks and repository storage, including linked worktrees."""
    path = Path(os.path.abspath(path))
    for part in (path, *path.parents):
        if part.is_symlink():
            raise DataError("symlink_forbidden")
    in_repo = any((parent / ".git").exists() for parent in path.parents)
    fixtures = Path(__file__).resolve().parents[1] / "tests" / "fixtures"
    if in_repo and not (fixture_read and path.parent == fixtures):
        raise DataError("data_must_be_outside_repository")
    return path


def _check_file(fd: int, *, fixture: bool = False) -> None:
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
        raise DataError("regular_private_file_required")
    if not fixture and (info.st_uid != os.getuid() or info.st_mode & 0o077):
        raise DataError("private_file_permissions_required")
    if info.st_size > MAX_FILE_BYTES:
        raise DataError("file_size_limit")


def open_private(path: Path, flags: int, *, fixture: bool = False) -> int:
    """Walk pinned directory descriptors; never follow an intermediate symlink.

    Operator data requires a private containing directory. Sticky system temp
    ancestors are permitted; writable non-sticky ancestors are not.
    """
    path = protected_path(path, fixture_read=fixture)
    directory = os.open("/", os.O_RDONLY | os.O_DIRECTORY)
    try:
        for component in path.parent.parts[1:]:
            child = os.open(
                component,
                os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                dir_fd=directory,
            )
            os.close(directory)
            directory = child
            info = os.fstat(directory)
            if not fixture and info.st_mode & 0o022 and not info.st_mode & stat.S_ISVTX:
                raise DataError("unsafe_parent_directory")
        parent = os.fstat(directory)
        if not fixture and (parent.st_uid != os.getuid() or parent.st_mode & 0o077):
            raise DataError("private_directory_required")
        fd = os.open(
            path.name, flags | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600, dir_fd=directory
        )
        try:
            _check_file(fd, fixture=fixture)
        except BaseException:
            os.close(fd)
            raise
        return fd
    except OSError:
        raise DataError("protected_file_open_failed") from None
    finally:
        os.close(directory)


def read_rows(path: Path) -> Iterator[dict[str, Any]]:
    path = protected_path(path, fixture_read=True)
    fixtures = Path(__file__).resolve().parents[1] / "tests" / "fixtures"
    fd = open_private(path, os.O_RDONLY, fixture=path.parent == fixtures)
    with os.fdopen(fd, "rb") as handle:
        _check_file(handle.fileno(), fixture=path.parent == fixtures)
        for index in range(MAX_ROWS + 1):
            raw = handle.readline(MAX_ROW_BYTES + 1)
            if not raw:
                return
            if index == MAX_ROWS or len(raw) > MAX_ROW_BYTES:
                raise DataError("dataset_limit")
            if not raw.endswith(b"\n"):
                raise DataError("incomplete_input_row")
            yield strict_json(raw)


@contextmanager
def journal(path: Path) -> Iterator[BinaryIO]:
    """Single writer, durable row commits; discard only an interrupted last row."""
    path = protected_path(path)
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd = open_private(path, os.O_RDWR | os.O_CREAT)
    with os.fdopen(fd, "r+b") as handle:
        _check_file(handle.fileno())
        try:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise DataError("output_locked") from None
        offset = 0
        for index in range(MAX_ROWS + 1):
            raw = handle.readline(MAX_ROW_BYTES + 1)
            if not raw:
                break
            if len(raw) > MAX_ROW_BYTES or index == MAX_ROWS:
                raise DataError("dataset_limit")
            if not raw.endswith(b"\n"):
                handle.seek(offset)
                handle.truncate()
                handle.flush()
                os.fsync(handle.fileno())
                break
            strict_json(raw)
            offset = handle.tell()
        handle.seek(0, os.SEEK_END)
        yield handle


def append_row(handle: BinaryIO, row: dict[str, Any]) -> None:
    raw = (canonical(row) + "\n").encode()
    if len(raw) > MAX_ROW_BYTES or handle.tell() + len(raw) > MAX_FILE_BYTES:
        raise DataError("dataset_limit")
    handle.write(raw)
    handle.flush()
    os.fsync(handle.fileno())


def validate_record(row: dict[str, Any], kind: str) -> dict[str, Any]:
    schemas = importlib.import_module("lrp.schemas")

    model = getattr(
        schemas,
        {"request": "RequestRow", "response": "ResponseRow", "judgment": "JudgmentRow"}[
            kind
        ],
    )
    try:
        _fields(row, set(model.model_fields))
        _bounded_text(row.get("request_id"), 256, required=True)
        source = row.get("source")
        if source not in ("synthetic", "content_capture") and not (
            isinstance(source, str)
            and source.startswith("dataset:")
            and 8 < len(source) <= 256
        ):
            raise DataError("invalid_source")
        stamp = row.get(
            {
                "request": "captured_at",
                "response": "started_at",
                "judgment": "judged_at",
            }[kind]
        )
        if not isinstance(stamp, str) or not stamp.endswith("Z"):
            raise DataError("utc_timestamp_required")
        datetime.fromisoformat(stamp)
        if kind == "request":
            _validate_request(row)
        else:
            _fields(row["target"], {"provider", "model", "model_ref"})
            for field in ("provider", "model"):
                _bounded_text(row["target"].get(field), 512, required=True)
        if kind == "response":
            for name, fields in (
                ("usage", {"input_tokens", "output_tokens"}),
                (
                    "pricing",
                    {"input_per_m_usd", "output_per_m_usd", "source", "fetched_at"},
                ),
            ):
                if row.get(name) is not None:
                    _fields(row[name], fields)
            _tool_calls(row.get("tool_calls", []))
            attempts = row.get("attempts", [])
            if not isinstance(attempts, list) or len(attempts) > 3:
                raise DataError("invalid_attempts")
            for index, attempt in enumerate(attempts):
                _fields(
                    attempt,
                    {
                        "sequence",
                        "status",
                        "error_class",
                        "http_status",
                        "duration_ms",
                        "ttfb_ms",
                        "usage",
                        "cost_usd",
                        "billed_cost_usd",
                    },
                )
                if attempt.get("sequence") != index + 1:
                    raise DataError("invalid_attempt_sequence")
                if attempt.get("usage") is not None:
                    _fields(attempt["usage"], {"input_tokens", "output_tokens"})
        if kind == "judgment":
            _fields(
                row.get("detail", {}),
                {
                    "anchor_identity",
                    "votes",
                    "swapped_agree",
                    "parse_failed",
                    "unsupported",
                    "error_class",
                    "timeout",
                    "passed",
                },
            )
        # strict prevents booleans becoming token counts, numeric strings becoming costs.
        return cast(
            dict[str, Any],
            model.model_validate(row, strict=True).model_dump(
                mode="json", exclude_none=True
            ),
        )
    except (ValueError, TypeError):
        raise DataError("invalid_" + kind + "_record") from None


def _fields(value: Any, fields: set[str]) -> None:
    if not isinstance(value, dict) or set(value) - fields:
        raise DataError("unknown_or_invalid_fields")


def _bounded_text(value: Any, limit: int, *, required: bool = False) -> None:
    if (
        not isinstance(value, str)
        or len(value.encode()) > limit
        or (required and not value)
    ):
        raise DataError("invalid_text")


def _tool_calls(value: Any) -> None:
    if not isinstance(value, list) or len(value) > 128:
        raise DataError("invalid_tool_calls")
    for call in value:
        _fields(call, {"id", "type", "function"})
        if call.get("type") != "function":
            raise DataError("unsupported_tool_call")
        _fields(call.get("function"), {"name", "arguments"})
        _bounded_text(call["function"].get("name"), 256, required=True)
        _bounded_text(call["function"].get("arguments"), MAX_ROW_BYTES)


def _validate_request(row: dict[str, Any]) -> None:
    _fields(row.get("caller", {}), {"project", "environment"})
    _bounded_text(row.get("group"), 256, required=True)
    messages = row.get("messages", [])
    if not isinstance(messages, list) or len(messages) > 1024:
        raise DataError("invalid_messages")
    for message in messages:
        _fields(message, {"role", "content", "tool_calls", "tool_call_id", "name"})
        if message.get("role") not in (
            "system",
            "developer",
            "user",
            "assistant",
            "tool",
        ):
            raise DataError("invalid_message_role")
        _bounded_text(message.get("content", ""), 1_100_000)
        _tool_calls(message.get("tool_calls", []))
    for field in ("system", "input"):
        _bounded_text(row.get(field, ""), 1_100_000)
    if not messages and not row.get("input"):
        raise DataError("missing_content")
    _fields(
        row.get("context", {}),
        {
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
            "reasoning",
            "stopCount",
            "imageCount",
        },
    )
    context = row.get("context", {})
    for key, value in context.items():
        if key == "reasoning":
            _fields(value, {"requested", "effort"})
        elif not isinstance(value, (int, float, bool)):
            raise DataError("invalid_context_scalar")
    if context.get("imageCount", 0):
        raise DataError("multimodal_not_supported")
    tools = row.get("tools", [])
    if not isinstance(tools, list) or len(tools) > 128:
        raise DataError("invalid_tools")
    for tool in tools:
        _fields(tool, {"type", "function"})
        if tool.get("type") != "function":
            raise DataError("unsupported_tool")
        _fields(tool.get("function"), {"name", "description", "parameters", "strict"})
    if row.get("response_format") is not None:
        _fields(row["response_format"], {"type", "json_schema"})
        if "json_schema" in row["response_format"]:
            _fields(
                row["response_format"]["json_schema"],
                {"name", "description", "schema", "strict"},
            )
    verifier = row.get("verifier", {"kind": "none"})
    _fields(verifier, {"kind", "spec", "version"})
    extras = row.get("provider_request_fields", {})
    if extras:
        if not isinstance(extras, dict):
            raise DataError("invalid_provider_request_fields")
        for provider, fields in extras.items():
            if not isinstance(provider, str) or not provider or not isinstance(fields, dict):
                raise DataError("invalid_provider_request_fields")
            for key in fields:
                if not isinstance(key, str) or not key:
                    raise DataError("invalid_provider_request_fields")
    # Structural request-row checks; semantic allowlists live in lrp.judge.contracts.
    from .judge.contracts import ALLOWED_PLUGIN_IDS, structural_spec_keys

    kind = verifier.get("kind", "none")
    try:
        required, optional = structural_spec_keys(str(kind))
    except DataError:
        raise DataError("unsupported_verifier") from None
    spec = verifier.get("spec", {})
    _fields(spec, required | optional)
    if not required <= set(spec) <= required | optional:
        raise DataError("missing_verifier_spec")
    if verifier.get("version") is not None and not isinstance(verifier.get("version"), str):
        raise DataError("unsupported_verifier_version")
    for key, value in spec.items():
        if key == "schema":
            if not isinstance(value, dict):
                raise DataError("invalid_verifier_schema")
        elif key == "expected_rows":
            if not isinstance(value, list) or len(canonical(value)) > MAX_ROW_BYTES:
                raise DataError("invalid_sql_result_expected")
            for item in value:
                if not isinstance(item, dict):
                    raise DataError("invalid_sql_result_expected")
        elif key == "columns":
            if (
                not isinstance(value, list)
                or not value
                or not all(isinstance(name, str) and name for name in value)
            ):
                raise DataError("invalid_sql_result_columns")
        elif key == "ignore_row_order":
            if type(value) is not bool:
                raise DataError("invalid_sql_result_order_flag")
        elif key == "plugin_id":
            if not isinstance(value, str) or value not in ALLOWED_PLUGIN_IDS:
                raise DataError("unsupported_plugin_id")
        elif key == "params":
            if not isinstance(value, dict) or len(canonical(value)) > MAX_ROW_BYTES:
                raise DataError("invalid_plugin_params")
        else:
            _bounded_text(value, MAX_ROW_BYTES)


def existing_rows(
    path: Path, kind: str, key: Callable[[dict[str, Any]], str]
) -> dict[str, dict[str, Any]]:
    result: dict[str, dict[str, Any]] = {}
    for raw in read_rows(path):
        row = validate_record(raw, kind)
        row_key = key(row)
        if row_key in result:
            raise DataError("duplicate_record_identity")
        result[row_key] = row
    return result


def request_key(row: dict[str, Any]) -> str:
    return str(row["request_id"])


def response_key(row: dict[str, Any]) -> str:
    return identity(
        [row["request_id"], row["target"]["provider"], row["target"]["model"]]
    )


def authorize_content(rows: list[dict[str, Any]], approved: bool) -> None:
    if not approved and any(row.get("source") != "synthetic" for row in rows):
        raise DataError("governed_content_approval_required")


def collect(
    *,
    out: Path,
    dataset: Path | None = None,
    content_capture: Path | None = None,
    router_log: Path | None = None,
    usage_db: str | None = None,
    approved_content: bool = False,
) -> dict[str, int]:
    """Inputs are versioned request records exported by the operator, not encrypted captures.

    Log joins select IDs and fill missing safe metadata only. They cannot manufacture
    messages, tools, verifier ground truth or a PII restoration map. A read-only
    ``usage_db`` DSN supplies the same safe scalars for explore-tagged requests.
    """
    logs: dict[str, dict[str, Any]] = {}
    if router_log:
        for raw in read_rows(router_log):
            rid = raw.get("request_id")
            if not isinstance(rid, str) or not rid or len(rid) > 256:
                raise DataError("invalid_log_request_id")
            if rid in logs:
                raise DataError("duplicate_log_request_id")
            logs[rid] = {
                k: raw[k]
                for k in (
                    "ts",
                    "resolved_group",
                    "inbound_dialect",
                    "caller_project",
                    "caller_environment",
                )
                if k in raw
            }
    if usage_db:
        from lrp.usage_import import iter_usage_log_metadata

        for raw in iter_usage_log_metadata(usage_db):
            rid = raw["request_id"]
            if rid in logs:
                raise DataError("duplicate_log_request_id")
            logs[rid] = {
                k: raw[k]
                for k in (
                    "ts",
                    "resolved_group",
                    "inbound_dialect",
                    "caller_project",
                    "caller_environment",
                )
                if k in raw
            }
    inputs: dict[str, dict[str, Any]] = {}
    for source in (dataset, content_capture):
        if source is None:
            continue
        for raw in read_rows(source):
            # Explicitly discard caller identity fields before strict offline validation.
            caller = raw.get("caller", {})
            if not isinstance(caller, dict):
                raise DataError("invalid_caller")
            raw["caller"] = {
                k: caller[k] for k in ("project", "environment") if k in caller
            }
            for key in ("caller_id", "tokenId", "token_id", "callerId"):
                raw.pop(key, None)
            log = logs.get(raw.get("request_id", ""), {})
            for dest, src in (
                ("captured_at", "ts"),
                ("group", "resolved_group"),
                ("dialect", "inbound_dialect"),
            ):
                if dest not in raw and src in log:
                    raw[dest] = log[src]
            for key in ("project", "environment"):
                if key not in raw["caller"] and "caller_" + key in log:
                    raw["caller"][key] = log["caller_" + key]
            messages = raw.get("messages", [])
            first = next(
                (
                    m.get("content", "")
                    for m in messages
                    if isinstance(m, dict) and m.get("role") == "user"
                ),
                "",
            )
            if "session_key" not in raw:
                raw["session_key"] = "sha256:" + identity(
                    [
                        raw.get("group", ""),
                        raw["caller"].get("project", ""),
                        raw["caller"].get("environment", ""),
                        first,
                    ]
                )
            row = validate_record(raw, "request")
            rid = request_key(row)
            if rid in inputs and inputs[rid] != row:
                raise DataError("conflicting_request_content")
            inputs[rid] = row
            if len(inputs) > MAX_ROWS:
                raise DataError("dataset_limit")
    authorize_content(list(inputs.values()), approved_content)
    stats = {
        "written": 0,
        "skipped": 0,
        "missing_content": len(set(logs) - inputs.keys()),
    }
    with journal(out) as handle:
        done = existing_rows(out, "request", request_key)
        if len(done.keys() | inputs.keys()) > MAX_ROWS:
            raise DataError("dataset_limit")
        for rid, row in inputs.items():
            if rid in done:
                if row != done[rid]:
                    raise DataError("resume_input_changed")
                stats["skipped"] += 1
            else:
                append_row(handle, row)
                stats["written"] += 1
    return stats
