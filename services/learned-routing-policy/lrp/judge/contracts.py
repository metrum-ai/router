# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Versioned allowlisted verifier contracts. Dataset code never executes on the host."""

from __future__ import annotations

from typing import Any

from ..collect import DataError

# Built-in plugin IDs only. Operators extend by shipping a new allowlisted ID in
# plugins_runtime.py and rebuilding the verifier rootfs evidence, not by mounting
# host code or accepting dataset-supplied callables.
ALLOWED_PLUGIN_IDS: frozenset[str] = frozenset(
    {
        "contains_v1",
        "json_equals_v1",
        "numeric_equals_v1",
        "tool_call_presence_v1",
        "tool_name_match_v1",
        "tool_args_schema_v1",
        "tool_normalized_arg_match_v1",
    }
)

# (kind, version) -> required spec keys, optional spec keys
CONTRACTS: dict[tuple[str, str], tuple[frozenset[str], frozenset[str]]] = {
    ("none", "none.v1"): (frozenset(), frozenset()),
    ("exact", "exact.v1"): (frozenset({"expected"}), frozenset()),
    ("regex", "regex.v1"): (frozenset({"pattern"}), frozenset()),
    ("json_schema", "json_schema.v1"): (frozenset({"schema"}), frozenset()),
    ("pytest", "pytest.v1"): (frozenset({"tests"}), frozenset()),
    ("sql_result", "sql_result.v1"): (
        frozenset({"expected_rows"}),
        frozenset({"columns", "ignore_row_order"}),
    ),
    ("plugin", "plugin.v1"): (frozenset({"plugin_id"}), frozenset({"params"})),
}

def default_version(kind: str) -> str:
    for contract_kind, version in CONTRACTS:
        if contract_kind == kind:
            return version
    raise DataError("unsupported_verifier")


def normalize_verifier(verifier: dict[str, Any]) -> dict[str, Any]:
    """Return a validated verifier with an explicit contract version."""
    if not isinstance(verifier, dict):
        raise DataError("unsupported_verifier")
    kind = verifier.get("kind", "none")
    if not isinstance(kind, str):
        raise DataError("unsupported_verifier")
    version = verifier.get("version") or default_version(kind)
    if not isinstance(version, str):
        raise DataError("unsupported_verifier_version")
    key = (kind, version)
    if key not in CONTRACTS:
        raise DataError("unsupported_verifier_contract")
    required, optional = CONTRACTS[key]
    spec = verifier.get("spec", {})
    if not isinstance(spec, dict):
        raise DataError("missing_verifier_spec")
    allowed = required | optional
    if set(spec) - allowed:
        raise DataError("unsupported_verifier_spec_field")
    if not required <= set(spec):
        raise DataError("missing_verifier_spec")
    if kind == "plugin":
        plugin_id = spec.get("plugin_id")
        if not isinstance(plugin_id, str) or plugin_id not in ALLOWED_PLUGIN_IDS:
            raise DataError("unsupported_plugin_id")
        params = spec.get("params", {})
        if not isinstance(params, dict):
            raise DataError("invalid_plugin_params")
    if kind == "sql_result":
        rows = spec.get("expected_rows")
        if not isinstance(rows, list) or len(rows) > 10_000:
            raise DataError("invalid_sql_result_expected")
        for row in rows:
            if not isinstance(row, dict):
                raise DataError("invalid_sql_result_expected")
        columns = spec.get("columns")
        if columns is not None and (
            not isinstance(columns, list)
            or not columns
            or not all(isinstance(name, str) and name for name in columns)
        ):
            raise DataError("invalid_sql_result_columns")
        if "ignore_row_order" in spec and type(spec["ignore_row_order"]) is not bool:
            raise DataError("invalid_sql_result_order_flag")
    return {"kind": kind, "version": version, "spec": spec}


def structural_spec_keys(kind: str) -> tuple[set[str], set[str]]:
    """Required and optional spec keys for request-row structural validation."""
    version = default_version(kind)
    required, optional = CONTRACTS[(kind, version)]
    return set(required), set(optional)
