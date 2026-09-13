# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Verifier-class labeling for seed replay candidates.

Four nullable columns stay separate from training ``quality`` unless an operator
applies an explicit documented mapping rule. Plugins are allowlisted for the
judge sandbox; host-side evaluation is only for unit tests of plugin bodies.
"""

from __future__ import annotations

import json
from collections.abc import Callable
from typing import Any

from ..judge.plugins_runtime import run_plugin

LABEL_COLUMNS = (
    "verifier_tool_presence",
    "verifier_tool_name",
    "verifier_args_schema",
    "verifier_normalized_args",
)

PluginRunner = Callable[[str, dict[str, Any], str], bool]


def candidate_payload(response: dict[str, Any]) -> str:
    return json.dumps(
        {
            "content": response.get("content") or "",
            "tool_calls": response.get("tool_calls") or [],
        },
        sort_keys=True,
        separators=(",", ":"),
        ensure_ascii=False,
    )


def reference_message(turn: dict[str, Any]) -> dict[str, Any]:
    reference = turn.get("reference_assistant")
    if not isinstance(reference, dict):
        return {"content": "", "tool_calls": []}
    return {
        "content": reference.get("content") or "",
        "tool_calls": reference.get("tool_calls") or [],
    }


def default_tool_schemas(reference: dict[str, Any]) -> dict[str, Any]:
    """Minimal object schemas derived from reference argument keys."""
    schemas: dict[str, Any] = {}
    for call in reference.get("tool_calls") or []:
        if not isinstance(call, dict):
            continue
        function = call.get("function")
        if not isinstance(function, dict):
            continue
        name = function.get("name")
        if not isinstance(name, str) or not name:
            continue
        raw_args = function.get("arguments", "{}")
        try:
            args = json.loads(raw_args) if isinstance(raw_args, str) else raw_args
        except (TypeError, ValueError, json.JSONDecodeError):
            continue
        if isinstance(args, dict):
            schemas[name] = {
                "type": "object",
                "required": sorted(str(key) for key in args),
            }
        elif isinstance(args, list):
            schemas[name] = {"type": "array"}
    return schemas


def label_verifier_columns(
    *,
    turn: dict[str, Any],
    response: dict[str, Any],
    tool_schemas: dict[str, Any] | None = None,
    runner: PluginRunner = run_plugin,
) -> dict[str, float | None]:
    """Return the four verifier-class columns. Missing evidence stays null."""
    labels: dict[str, float | None] = {name: None for name in LABEL_COLUMNS}
    if response.get("status") != "ok":
        return labels
    reference = reference_message(turn)
    content = candidate_payload(response)
    schemas = tool_schemas if tool_schemas is not None else default_tool_schemas(reference)

    checks: list[tuple[str, str, dict[str, Any]]] = [
        (
            "verifier_tool_presence",
            "tool_call_presence_v1",
            {"reference": reference},
        ),
        (
            "verifier_tool_name",
            "tool_name_match_v1",
            {"reference": reference},
        ),
        (
            "verifier_args_schema",
            "tool_args_schema_v1",
            {"schemas": schemas} if schemas else {},
        ),
        (
            "verifier_normalized_args",
            "tool_normalized_arg_match_v1",
            {"reference": reference},
        ),
    ]
    for column, plugin_id, params in checks:
        if plugin_id == "tool_args_schema_v1" and not schemas:
            labels[column] = None
            continue
        if not (reference.get("tool_calls") or []) and plugin_id in {
            "tool_name_match_v1",
            "tool_normalized_arg_match_v1",
        }:
            # Text turns: name/arg match is not applicable evidence.
            if plugin_id == "tool_name_match_v1":
                labels[column] = (
                    1.0 if not json.loads(content).get("tool_calls") else 0.0
                )
            else:
                labels[column] = None
            continue
        try:
            labels[column] = 1.0 if runner(plugin_id, params, content) else 0.0
        except (TypeError, ValueError, json.JSONDecodeError):
            labels[column] = None
    return labels


def quality_blend_rule() -> dict[str, Any]:
    """Documented non-default mapping. Trainers must opt in explicitly."""
    return {
        "rule_id": "seed_verifier_mean_v1",
        "default_applied": False,
        "description": (
            "Do not silently write verifier columns into training quality. "
            "Operators may set quality to the mean of non-null verifier_* "
            "columns only under an explicit configuration that cites this rule."
        ),
        "columns": list(LABEL_COLUMNS),
    }


def sandbox_plugin_specs(turn: dict[str, Any]) -> list[dict[str, Any]]:
    """Allowlisted plugin verifier specs for judge-sandbox execution."""
    reference = reference_message(turn)
    schemas = default_tool_schemas(reference)
    specs = [
        {
            "kind": "plugin",
            "version": "plugin.v1",
            "spec": {
                "plugin_id": "tool_call_presence_v1",
                "params": {"reference": reference},
            },
        },
        {
            "kind": "plugin",
            "version": "plugin.v1",
            "spec": {
                "plugin_id": "tool_name_match_v1",
                "params": {"reference": reference},
            },
        },
        {
            "kind": "plugin",
            "version": "plugin.v1",
            "spec": {
                "plugin_id": "tool_normalized_arg_match_v1",
                "params": {"reference": reference},
            },
        },
    ]
    if schemas:
        specs.insert(
            2,
            {
                "kind": "plugin",
                "version": "plugin.v1",
                "spec": {
                    "plugin_id": "tool_args_schema_v1",
                    "params": {"schemas": schemas},
                },
            },
        )
    return specs
