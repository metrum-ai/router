# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Allowlisted plugin bodies executed only inside the isolated verifier worker.

This module is concatenated into the bubblewrap Python -c payload. It must stay
stdlib-only and must not import the host lrp package.
"""

from __future__ import annotations

import json
import math
from collections.abc import Callable
from typing import Any


def _contains_v1(params: dict[str, Any], content: str) -> bool:
    needle = params.get("needle")
    if not isinstance(needle, str) or not needle:
        raise TypeError("invalid_params")
    return needle in content


def _json_equals_v1(params: dict[str, Any], content: str) -> bool:
    if "expected" not in params:
        raise ValueError("invalid_params")
    return bool(json.loads(content) == params["expected"])


def _numeric_equals_v1(params: dict[str, Any], content: str) -> bool:
    raw_expected = params.get("expected")
    if isinstance(raw_expected, bool) or not isinstance(raw_expected, (int, float)):
        raise TypeError("invalid_params")
    expected = float(raw_expected)
    raw_tol = params.get("abs_tol", 0.0)
    if isinstance(raw_tol, bool) or not isinstance(raw_tol, (int, float)) or raw_tol < 0:
        raise TypeError("invalid_params")
    abs_tol = float(raw_tol)
    actual = float(content.strip())
    return math.isclose(actual, expected, rel_tol=0.0, abs_tol=abs_tol)


def _candidate_message(content: str) -> dict[str, Any]:
    parsed = json.loads(content)
    if not isinstance(parsed, dict):
        raise TypeError("invalid_candidate")
    return parsed


def _tool_calls(message: dict[str, Any]) -> list[dict[str, Any]]:
    raw = message.get("tool_calls") or []
    if not isinstance(raw, list):
        raise TypeError("invalid_tool_calls")
    return [item for item in raw if isinstance(item, dict)]


def _tool_names(calls: list[dict[str, Any]]) -> list[str]:
    names: list[str] = []
    for call in calls:
        function = call.get("function")
        if not isinstance(function, dict):
            continue
        name = function.get("name")
        if isinstance(name, str) and name:
            names.append(name)
    return names


def _parse_arguments(raw: Any) -> Any:
    if isinstance(raw, (dict, list)):
        return raw
    if not isinstance(raw, str):
        raise TypeError("invalid_arguments")
    return json.loads(raw)


def _normalize_path(value: str) -> str:
    text = value.replace("\\", "/")
    while "//" in text:
        text = text.replace("//", "/")
    if len(text) > 1 and text.endswith("/"):
        text = text[:-1]
    return text


def _normalize_shell(value: str) -> str:
    return " ".join(value.split())


def _normalize_value(value: Any, *, path_keys: frozenset[str], shell_keys: frozenset[str]) -> Any:
    if isinstance(value, dict):
        out: dict[str, Any] = {}
        for key, child in value.items():
            if not isinstance(key, str):
                raise TypeError("invalid_arg_key")
            if key in path_keys and isinstance(child, str):
                out[key] = _normalize_path(child)
            elif key in shell_keys and isinstance(child, str):
                out[key] = _normalize_shell(child)
            else:
                out[key] = _normalize_value(
                    child, path_keys=path_keys, shell_keys=shell_keys
                )
        return out
    if isinstance(value, list):
        return [
            _normalize_value(child, path_keys=path_keys, shell_keys=shell_keys)
            for child in value
        ]
    return value


def _tool_call_presence_v1(params: dict[str, Any], content: str) -> bool:
    reference = params.get("reference")
    if not isinstance(reference, dict):
        raise TypeError("invalid_params")
    candidate = _candidate_message(content)
    return bool(_tool_calls(candidate)) == bool(_tool_calls(reference))


def _tool_name_match_v1(params: dict[str, Any], content: str) -> bool:
    reference = params.get("reference")
    if not isinstance(reference, dict):
        raise TypeError("invalid_params")
    candidate = _candidate_message(content)
    return _tool_names(_tool_calls(candidate)) == _tool_names(_tool_calls(reference))


def _tool_args_schema_v1(params: dict[str, Any], content: str) -> bool:
    schemas = params.get("schemas")
    if not isinstance(schemas, dict) or not schemas:
        raise TypeError("invalid_params")
    candidate = _candidate_message(content)
    calls = _tool_calls(candidate)
    if not calls:
        return False
    for call in calls:
        function = call.get("function")
        if not isinstance(function, dict):
            return False
        name = function.get("name")
        if not isinstance(name, str) or name not in schemas:
            return False
        schema = schemas[name]
        if not isinstance(schema, dict):
            raise TypeError("invalid_params")
        try:
            args = _parse_arguments(function.get("arguments"))
        except (TypeError, ValueError, json.JSONDecodeError):
            return False
        # stdlib-only structural check: required object keys when declared.
        if schema.get("type") == "object":
            if not isinstance(args, dict):
                return False
            required = schema.get("required", [])
            if isinstance(required, list):
                for key in required:
                    if not isinstance(key, str) or key not in args:
                        return False
        elif schema.get("type") == "array" and not isinstance(args, list):
            return False
    return True


def _tool_normalized_arg_match_v1(params: dict[str, Any], content: str) -> bool:
    reference = params.get("reference")
    if not isinstance(reference, dict):
        raise TypeError("invalid_params")
    path_keys = params.get("path_keys", ["path", "file", "filename", "directory"])
    shell_keys = params.get("shell_keys", ["command", "cmd", "shell"])
    if not isinstance(path_keys, list) or not isinstance(shell_keys, list):
        raise TypeError("invalid_params")
    path_set = frozenset(key for key in path_keys if isinstance(key, str))
    shell_set = frozenset(key for key in shell_keys if isinstance(key, str))
    candidate = _candidate_message(content)
    ref_calls = _tool_calls(reference)
    cand_calls = _tool_calls(candidate)
    if len(ref_calls) != len(cand_calls):
        return False
    for ref_call, cand_call in zip(ref_calls, cand_calls, strict=True):
        ref_fn = ref_call.get("function")
        cand_fn = cand_call.get("function")
        if not isinstance(ref_fn, dict) or not isinstance(cand_fn, dict):
            return False
        if ref_fn.get("name") != cand_fn.get("name"):
            return False
        try:
            ref_args = _parse_arguments(ref_fn.get("arguments", "{}"))
            cand_args = _parse_arguments(cand_fn.get("arguments", "{}"))
        except (TypeError, ValueError, json.JSONDecodeError):
            return False
        if _normalize_value(
            ref_args, path_keys=path_set, shell_keys=shell_set
        ) != _normalize_value(cand_args, path_keys=path_set, shell_keys=shell_set):
            return False
    return True


ALLOWED_PLUGINS: dict[str, Callable[[dict[str, Any], str], bool]] = {
    "contains_v1": _contains_v1,
    "json_equals_v1": _json_equals_v1,
    "numeric_equals_v1": _numeric_equals_v1,
    "tool_call_presence_v1": _tool_call_presence_v1,
    "tool_name_match_v1": _tool_name_match_v1,
    "tool_args_schema_v1": _tool_args_schema_v1,
    "tool_normalized_arg_match_v1": _tool_normalized_arg_match_v1,
}


def run_plugin(plugin_id: str, params: dict[str, Any], content: str) -> bool:
    runner = ALLOWED_PLUGINS.get(plugin_id)
    if runner is None:
        raise ValueError("unknown_plugin")
    if not isinstance(params, dict):
        raise TypeError("invalid_params")
    return bool(runner(params, content))
