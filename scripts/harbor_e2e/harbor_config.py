# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Router configuration for the Harbor matrix.

Two model groups, one per API style, both translating across dialects so the
same task set can be exercised direct and routed. Prices are list-rate labels
from the operator config; the relay and the router do the metering.
"""
from __future__ import annotations

import hashlib
from pathlib import Path

GPT_MODEL = "gpt-4o-mini"
CLAUDE_MODEL = "claude-haiku-4-5"


def caller_tokens() -> dict[tuple[str, str], str]:
    """Per-lane caller tokens; the adapter writes one into the tmpfs file per job."""
    return {(style, group): f"harbor-{style}-{group}" for style in ("openai-responses", "anthropic-messages") for group in ("gpt", "claude")}


def make_config(work: Path, router_port: int, relay_port: int, relay_host: str = "172.17.0.1") -> str:
    callers = []
    for (style, group), token in caller_tokens().items():
        digest = hashlib.sha256(token.encode()).hexdigest()
        callers.append(
            f"  - id: {style}-{group}\n"
            f'    token_sha256: "{digest}"\n'
            "    allow: [gpt, claude]\n"
            "    rate: {rpm: 2400, tpm: 100000000, concurrent: 10}\n"
            "    quota: {day: {requests: 500, tokens: 2000000}, month: {tokens: 4000000}}\n"
            "    key: {lifetime_tokens: 4000000}"
        )
    target_tools = (
        "tool_support: {openai_responses: [function, tool_choice], openai_chat: [tools, tool_choice], "
        "anthropic_messages: [client_tools, tool_choice]}"
    )
    shape = "request_shape_support: {supported_inbound_dialects: [openai-chat, openai-responses, anthropic], validation_status: passed}"
    bridge = "bridges: {chat_to_responses: {enabled: true, text: true, tools: true, tool_choice: true, parallel_tool_calls: true, streaming: true}, responses_to_chat: {enabled: true, text: true, tools: true, tool_choice: true, streaming: true}}"
    return f"""server:
  listen: "0.0.0.0:{router_port}"
  identifiers: {{mode: passthrough}}
  default_model_group: gpt
  cache: {{enabled: false}}
  usage_db: {{enabled: true, driver: sqlite, path: "{work / 'usage.sqlite'}", migration_policy: auto-safe}}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  openai_direct: {{base_url: "http://{relay_host}:{relay_port}/openai/v1", dialect: openai-responses, api_key: "synthetic-upstream"}}
  anthropic_direct: {{base_url: "http://{relay_host}:{relay_port}/anthropic", dialect: anthropic, api_key: "synthetic-upstream"}}
models:
  gpt:
    strategy: static
    targets:
      - provider: openai_direct
        model: {GPT_MODEL}
        force_store_false: true
        input_price_per_million_usd: 0.15
        output_price_per_million_usd: 0.6
        {target_tools}
        {shape}
        {bridge}
  claude:
    strategy: static
    targets:
      - provider: anthropic_direct
        model: {CLAUDE_MODEL}
        input_price_per_million_usd: 1.0
        output_price_per_million_usd: 5.0
        {target_tools}
        {shape}
        {bridge}
callers:
""" + "\n".join(callers) + "\n"
