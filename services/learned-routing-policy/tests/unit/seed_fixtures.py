# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Tiny OpenHands-shaped trajectory helpers for offline seed tests."""

from __future__ import annotations

from typing import Any


def sample_trajectory_row(
    *,
    trajectory_id: str = "traj-1",
    instance_id: str = "owner/repo-1",
) -> dict[str, Any]:
    return {
        "trajectory_id": trajectory_id,
        "instance_id": instance_id,
        "repo": "owner/repo",
        "trajectory": [
            {"role": "system", "content": "You are a coding agent."},
            {"role": "user", "content": "Fix the bug in main.py"},
            {
                "role": "assistant",
                "content": "",
                "tool_calls": [
                    {
                        "id": "call-1",
                        "type": "function",
                        "function": {
                            "name": "read_file",
                            "arguments": {"path": "src//main.py"},
                        },
                    }
                ],
            },
            {
                "role": "tool",
                "name": "read_file",
                "tool_call_id": "call-1",
                "content": "print('hi')",
            },
            {
                "role": "assistant",
                "content": "I will update the file.",
                "tool_calls": [
                    {
                        "id": "call-2",
                        "type": "function",
                        "function": {
                            "name": "run_shell",
                            "arguments": {"command": "pytest   tests/"},
                        },
                    }
                ],
            },
            {
                "role": "tool",
                "name": "run_shell",
                "tool_call_id": "call-2",
                "content": "1 passed",
            },
            {"role": "assistant", "content": "Done.", "tool_calls": None},
        ],
        "resolved": 1,
    }


def multi_session_pool() -> list[dict[str, Any]]:
    return [
        sample_trajectory_row(
            trajectory_id=f"traj-{index}",
            instance_id=f"owner/repo-{index}",
        )
        for index in range(6)
    ]
