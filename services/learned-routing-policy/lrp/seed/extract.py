# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Teacher-forced turn extraction from OpenHands-style trajectories."""

from __future__ import annotations

import json
from typing import Any

from ..collect import DataError, identity
from . import SOURCE_DATASET, SOURCE_REVISION


def _has_tool_calls(message: dict[str, Any]) -> bool:
    calls = message.get("tool_calls")
    return isinstance(calls, list) and len(calls) > 0


def _serialize_tool_arguments(message: dict[str, Any]) -> dict[str, Any]:
    """Keep OpenAI-chat tool argument strings for replay requests."""
    out = dict(message)
    calls = out.get("tool_calls")
    if not isinstance(calls, list):
        return out
    normalized: list[dict[str, Any]] = []
    for call in calls:
        if not isinstance(call, dict):
            continue
        item = dict(call)
        function = item.get("function")
        if isinstance(function, dict):
            fn = dict(function)
            args = fn.get("arguments")
            if isinstance(args, (dict, list)):
                fn["arguments"] = json.dumps(args, sort_keys=True, separators=(",", ":"))
            item["function"] = fn
        normalized.append(item)
    out["tool_calls"] = normalized
    return out


def extract_teacher_forced_turns(
    row: dict[str, Any],
    *,
    dataset: str = SOURCE_DATASET,
    revision: str = SOURCE_REVISION,
) -> list[dict[str, Any]]:
    """Extract one turn per assistant message.

    Prompt messages are the trajectory prefix through the last non-assistant
    message before the reference assistant continuation. The reference
    continuation is stored separately and is not appended to the replay prompt.
    """
    trajectory = row.get("trajectory")
    if not isinstance(trajectory, list) or not trajectory:
        raise DataError("invalid_trajectory")
    session_key = row.get("trajectory_id") or row.get("instance_id")
    if not isinstance(session_key, str) or not session_key:
        raise DataError("missing_session_key")
    turns: list[dict[str, Any]] = []
    assistant_index = 0
    for index, message in enumerate(trajectory):
        if not isinstance(message, dict) or message.get("role") != "assistant":
            continue
        assistant_index += 1
        prefix = trajectory[:index]
        if not prefix:
            continue
        # Require the prompt to end on a non-assistant observation (user/tool).
        if prefix[-1].get("role") == "assistant":
            continue
        messages = [
            _serialize_tool_arguments(item) if isinstance(item, dict) else item
            for item in prefix
        ]
        reference = _serialize_tool_arguments(message)
        kind = "tool-call" if _has_tool_calls(reference) else "text"
        turn_id = identity(
            {
                "dataset": dataset,
                "revision": revision,
                "session_key": session_key,
                "assistant_index": assistant_index,
            }
        )
        turns.append(
            {
                "turn_id": turn_id,
                "session_key": session_key,
                "assistant_index": assistant_index,
                "turn_kind": kind,
                "source": {
                    "dataset": dataset,
                    "revision": revision,
                    "instance_id": row.get("instance_id"),
                    "repo": row.get("repo"),
                },
                "messages": messages,
                "reference_assistant": reference,
                "tools": row.get("tools"),
            }
        )
    return turns


def estimate_prompt_tokens(messages: list[dict[str, Any]]) -> int:
    """Deterministic sampling metadata approximation when cl100k counts are absent.

    This is not model-native eligibility evidence. Operators should replace it
    with recorded ``cl100k_base`` counts for production sampling reports.
    """
    encoded = json.dumps(messages, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    return max(1, len(encoded.encode("utf-8")) // 4)
