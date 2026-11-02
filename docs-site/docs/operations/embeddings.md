---
title: Embeddings
doc_type: howto
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Embeddings

`POST /v1/embeddings` serves OpenAI Embeddings-style requests from model groups
that are explicitly configured as embeddings groups. The router never mixes a
chat group and an embeddings group, and it never falls back across vector
spaces or dimensions.

## Configure an embeddings group

```yaml
models:
  docs-embed:
    strategy: failover
    targets:
      - provider: openai
        model: text-embedding-3-small
        input_modalities: [text]
        output_modalities: [embeddings]
        embedding_space: openai-text-embedding-3-small-v1
        embedding_dimensions: [256, 1536]
        default_embedding_dimensions: 1536
        input_price_per_million_usd: 0.02
      - provider: openai-backup
        model: text-embedding-3-small
        input_modalities: [text]
        output_modalities: [embeddings]
        embedding_space: openai-text-embedding-3-small-v1
        embedding_dimensions: [256, 1536]
        default_embedding_dimensions: 1536
        input_price_per_million_usd: 0.02
```

Configuration is validated at load time and fails closed when:

- any target in the group is not an embeddings target (no mixed groups);
- a target omits `embedding_space`, or targets disagree on it;
- `embedding_space` is not lowercase `a-z 0-9 . _ -`;
- `embedding_dimensions` is empty, repeated, non-positive or above 8192;
- targets serve different dimension sets;
- `default_embedding_dimensions` is not one of `embedding_dimensions`, or
  targets disagree on the default;
- input modalities include anything other than `text`;
- the group uses `semantic`, `dynamic_score`, `external`, `script` or
  `intelligent`, whose selectors rank on chat prompt features.

`static`, `failover`, `weighted`, `latency` and `cost` are allowed.

Equality of `embedding_space` is an operator assertion. The router cannot prove
that two providers return interchangeable vectors, so only declare the same
space for targets you have verified produce comparable output.

## Call the endpoint

```sh
curl "$ROUTER_URL/v1/embeddings" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"model":"docs-embed","input":["first document","second document"],"dimensions":256}'
```

Supported input shapes are a string, an array of strings, a token array, or an
array of token arrays. `encoding_format` may be `float` or `base64`. Requests
without `dimensions` use the group's `default_embedding_dimensions`; when no
default is configured the request fails with 400 `unsupported_capability`
instead of guessing a size. Dimensions and spaces are never substituted, so a
request for an unserved dimension fails the same way.

The response preserves caller input order and the provider's `index` values, and
reports the caller-facing model name according to `server.responses.model_identity`.
Vector count and, for `float` responses, vector length are validated against the
request before any bytes reach the client. A shape violation returns 502
`upstream-failed`; provider error bodies are never forwarded.

## Admission, privacy and metering

The endpoint uses the same caller authentication, model allow-list, request
rate, concurrency and token-budget admission as chat requests. Input tokens are
reserved before the call and settled from the provider's `usage.prompt_tokens`;
if the provider omits usage, the row is labelled `estimated` and the response
carries `X-Metrum-Usage-Source: estimated`.

When the group configures `pii_filter`, textual input is redacted according to
that policy. Token-array input cannot be inspected, so a PII-enabled group
rejects it with 400 `pii-filter-blocked` rather than sending uninspected tokens
upstream.

Usage rows carry `endpoint=embeddings`, caller, project, model group and target
attribution, input tokens from the provider, zero output tokens, and the
configured input price. They appear in the existing usage and admin reports.

## No caching

Embeddings bypass the response and semantic caches, including on retries, and
never share a cache key with chat traffic. A future cache would need tenant
isolation, model revision, space, dimensions, input normalization and retention
guarantees.

## Limits

- One attempt per request and no streaming; the upstream call is unary.
- Only OpenAI-compatible upstream dialects are eligible. Anthropic, Bedrock and
  other dialects are not called.
- The provider must return one vector per input with distinct indices.
- Operator smoke tests against one provider do not prove that every
  OpenAI-compatible provider behaves identically.
