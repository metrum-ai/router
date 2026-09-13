# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Mixed hosted portfolio target descriptors for iteration-1 seed replay.

Prices are source-dated constants from issue #157. Secrets are never stored here.
Refresh OpenRouter catalog and OpenAI primary-doc prices before a paid run.
"""

from __future__ import annotations

from typing import Any

OPENROUTER_PRICE_AS_OF = "2026-09-13"
OPENAI_PRICE_AS_OF = "2026-09-13"


def iteration1_targets() -> list[dict[str, Any]]:
    """Return fanout-compatible targets plus seed pricing metadata."""
    return [
        {
            "target_id": "openrouter-qwen3.5-9b",
            "provider": "openrouter",
            "model": "qwen/qwen3.5-9b",
            "model_ref": "qwen/qwen3.5-9b-20260310",
            "router_group": "lrp-seed-qwen35-9b",
            "honors_max_tokens": True,
            "output_token_field": "max_tokens",
            "router_targets": [
                {
                    "provider": "openrouter",
                    "model": "qwen/qwen3.5-9b",
                    "model_ref": "qwen/qwen3.5-9b-20260310",
                }
            ],
            "context_tokens": 262144,
            "pricing": {
                "input_per_m_usd": 0.10,
                "output_per_m_usd": 0.15,
                "cached_input_per_m_usd": None,
                "source": "openrouter_models_endpoint",
                "as_of": OPENROUTER_PRICE_AS_OF,
                "cached_input_status": "unpublished",
            },
            "pass1_controls": {"cache": "disabled"},
            "request_fields_scope": "all",
        },
        {
            "target_id": "openrouter-qwen3.8-27b",
            "provider": "openrouter",
            "model": "qwen/qwen3.8-27b",
            "model_ref": "qwen/qwen3.8-27b-20260814",
            "router_group": "lrp-seed-qwen38-27b",
            "honors_max_tokens": True,
            "output_token_field": "max_tokens",
            "router_targets": [
                {
                    "provider": "openrouter",
                    "model": "qwen/qwen3.8-27b",
                    "model_ref": "qwen/qwen3.8-27b-20260814",
                }
            ],
            "context_tokens": 262144,
            "catalog_context_tokens": 1_000_000,
            "pricing": {
                "input_per_m_usd": 0.214,
                "output_per_m_usd": 2.55,
                "cached_input_per_m_usd": 0.15,
                "source": "openrouter_models_endpoint",
                "as_of": OPENROUTER_PRICE_AS_OF,
            },
            "pass1_controls": {"cache": "disabled"},
            "request_fields_scope": "all",
        },
        {
            "target_id": "openai-gpt-5.6",
            "provider": "openai",
            "model": "gpt-5.6",
            "model_ref": "gpt-5.6",
            "router_group": "lrp-seed-gpt-5-6",
            "honors_max_tokens": True,
            "output_token_field": "max_completion_tokens",
            "router_targets": [
                {"provider": "openai", "model": "gpt-5.6", "model_ref": "gpt-5.6"}
            ],
            "context_tokens": 1_050_000,
            "common_eligible_context_tokens": 272_000,
            "pricing": {
                "input_per_m_usd": 4.0,
                "output_per_m_usd": 20.0,
                "cached_input_per_m_usd": 0.40,
                "cache_write_per_m_usd": 5.0,
                "source": "openai_primary_docs",
                "as_of": OPENAI_PRICE_AS_OF,
                "promotional_through": "2026-11-21",
                "fallback_if_unavailable": "gpt-5.5",
            },
            "pass1_controls": {
                "cache": "disabled",
                "prompt_cache_options": {"mode": "explicit"},
                "processing": "standard",
                "notes": (
                    "explicit mode with no breakpoints disables cache reads/writes "
                    "and write premium for pass 1"
                ),
            },
            "request_fields_scope": "openai",
            "provider_request_fields": {
                "openai": {"prompt_cache_options": {"mode": "explicit"}}
            },
        },
        {
            "target_id": "openai-gpt-5.4-mini",
            "provider": "openai",
            "model": "gpt-5.4-mini",
            "model_ref": "gpt-5.4-mini-2026-03-17",
            "router_group": "lrp-seed-gpt-5-4-mini",
            "honors_max_tokens": True,
            "output_token_field": "max_completion_tokens",
            "router_targets": [
                {
                    "provider": "openai",
                    "model": "gpt-5.4-mini",
                    "model_ref": "gpt-5.4-mini-2026-03-17",
                }
            ],
            "context_tokens": 400_000,
            "pricing": {
                "input_per_m_usd": 0.75,
                "output_per_m_usd": 4.50,
                "cached_input_per_m_usd": 0.075,
                "source": "openai_primary_docs",
                "as_of": OPENAI_PRICE_AS_OF,
            },
            "pass1_controls": {
                "cache": "disabled",
                "prompt_cache_options": {"mode": "explicit"},
                "processing": "standard",
            },
            "request_fields_scope": "openai",
            "provider_request_fields": {
                "openai": {"prompt_cache_options": {"mode": "explicit"}}
            },
        },
    ]


def fanout_target_dicts(targets: list[dict[str, Any]] | None = None) -> list[dict[str, Any]]:
    """Project seed descriptors to the fields ``run_fanout`` validates."""
    rows = targets if targets is not None else iteration1_targets()
    projected: list[dict[str, Any]] = []
    for row in rows:
        projected.append(
            {
                "provider": row["provider"],
                "model": row["model"],
                "model_ref": row["model_ref"],
                "honors_max_tokens": row["honors_max_tokens"],
                "router_group": row["router_group"],
                "router_targets": row["router_targets"],
                "output_token_field": row["output_token_field"],
            }
        )
    return projected
