# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Official OpenAI and Anthropic SDK contracts against the router fixture (HTTP-01..03)."""

from __future__ import annotations

import pytest
from anthropic import Anthropic, AuthenticationError as AnthropicAuthenticationError
from anthropic import omit
from openai import omit as openai_omit
from openai import AuthenticationError as OpenAIAuthenticationError
from openai import OpenAI

from conftest import PROMPT_CANARY
from harness import CALLER


def test_openai_sdk_lists_models_and_chat_unary(router):
    client = OpenAI(base_url=router["base"] + "/v1", api_key=CALLER, max_retries=0)
    models = client.models.list()
    ids = {item.id for item in models.data}
    assert {"chat", "responses", "messages"} <= ids
    assert all(not id.startswith("synthetic-") for id in ids)

    completion = client.chat.completions.create(
        model="chat",
        messages=[{"role": "user", "content": PROMPT_CANARY}],
    )
    assert completion.choices[0].message.content == "synthetic chat"
    assert router["upstream"].calls
    assert router["upstream"].calls[0]["path"] == "/v1/chat/completions"
    assert router["upstream"].calls[0]["body"]["model"] == "synthetic-chat"


def test_anthropic_sdk_messages_unary(router):
    # auth_token pins Bearer so ambient ANTHROPIC_AUTH_TOKEN cannot win over x-api-key.
    client = Anthropic(
        base_url=router["base"] + "/anthropic",
        api_key=CALLER,
        auth_token=CALLER,
        max_retries=0,
    )
    message = client.messages.create(
        model="messages",
        max_tokens=32,
        messages=[{"role": "user", "content": PROMPT_CANARY}],
    )
    assert message.type == "message"
    assert message.content[0].type == "text"
    assert message.content[0].text == "synthetic messages"
    assert router["upstream"].calls
    assert router["upstream"].calls[0]["path"] == "/anthropic/v1/messages"
    assert router["upstream"].calls[0]["body"]["model"] == "synthetic-messages"


def test_openai_sdk_missing_auth_rejects_with_zero_upstream(router):
    # openai>=3 validates credentials at client construction, so supply a
    # placeholder key and omit the Authorization header per request. The
    # assertion under test is still the router's 401 with zero upstream calls.
    client = OpenAI(base_url=router["base"] + "/v1", api_key="placeholder", max_retries=0)
    with pytest.raises(OpenAIAuthenticationError) as exc_info:
        client.models.list(extra_headers={"Authorization": openai_omit})
    assert exc_info.value.status_code == 401
    assert router["upstream"].calls == []


def test_anthropic_sdk_missing_auth_rejects_with_zero_upstream(router):
    client = Anthropic(base_url=router["base"] + "/anthropic", api_key="unused", max_retries=0)
    with pytest.raises(AnthropicAuthenticationError) as exc_info:
        client.messages.create(
            model="messages",
            max_tokens=32,
            messages=[{"role": "user", "content": PROMPT_CANARY}],
            extra_headers={"Authorization": omit, "X-Api-Key": omit},
        )
    assert exc_info.value.status_code == 401
    assert router["upstream"].calls == []
