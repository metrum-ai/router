# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Mixed hosted portfolio target descriptors for iteration-1 seed replay.

Prices are source-dated constants from issue #157. Secrets are never stored here.
Refresh OpenRouter catalog and Fireworks/Baseten list prices before a paid run.
No OpenAI endpoints in this portfolio.
"""

from __future__ import annotations

from typing import Any

OPENROUTER_PRICE_AS_OF = "2026-09-14"
FIREWORKS_PRICE_AS_OF = "2026-06-28"
BASETEN_PRICE_AS_OF = "2026-06-18"


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
            "target_id": "openrouter-minimax-m3",
            "provider": "openrouter",
            "model": "minimax/minimax-m3",
            "model_ref": "minimax/minimax-m3",
            "router_group": "lrp-seed-minimax-m3",
            "honors_max_tokens": True,
            "output_token_field": "max_tokens",
            "router_targets": [
                {
                    "provider": "openrouter",
                    "model": "minimax/minimax-m3",
                    "model_ref": "minimax/minimax-m3",
                }
            ],
            "context_tokens": 1_048_576,
            "pricing": {
                "input_per_m_usd": 0.30,
                "output_per_m_usd": 1.20,
                "cached_input_per_m_usd": None,
                "source": "openrouter_models_endpoint",
                "as_of": OPENROUTER_PRICE_AS_OF,
            },
            "pass1_controls": {"cache": "disabled"},
            "request_fields_scope": "all",
        },
        {
            "target_id": "fireworks-kimi-k2p7-code",
            "provider": "fireworks",
            "model": "accounts/fireworks/models/kimi-k2p7-code",
            "model_ref": "kimi-k2p7-code",
            "router_group": "lrp-seed-fireworks-kimi",
            "honors_max_tokens": True,
            "output_token_field": "max_tokens",
            "router_targets": [
                {
                    "provider": "fireworks",
                    "model": "accounts/fireworks/models/kimi-k2p7-code",
                    "model_ref": "kimi-k2p7-code",
                }
            ],
            "context_tokens": 262144,
            "pricing": {
                "input_per_m_usd": 0.95,
                "output_per_m_usd": 4.00,
                "cached_input_per_m_usd": 0.19,
                "source": "fireworks_serverless_pricing",
                "as_of": FIREWORKS_PRICE_AS_OF,
            },
            "pass1_controls": {"cache": "disabled"},
            "request_fields_scope": "all",
        },
        {
            "target_id": "baseten-glm-5-2",
            "provider": "baseten",
            "model": "zai-org/GLM-5.2",
            "model_ref": "glm-5-2",
            "router_group": "lrp-seed-baseten-glm",
            "honors_max_tokens": True,
            "output_token_field": "max_tokens",
            "router_targets": [
                {
                    "provider": "baseten",
                    "model": "zai-org/GLM-5.2",
                    "model_ref": "glm-5-2",
                }
            ],
            "context_tokens": 202752,
            "pricing": {
                "input_per_m_usd": 1.50,
                "output_per_m_usd": 4.50,
                "cached_input_per_m_usd": 0.30,
                "source": "baseten_model_api_pricing",
                "as_of": BASETEN_PRICE_AS_OF,
            },
            "pass1_controls": {"cache": "disabled"},
            "request_fields_scope": "all",
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
