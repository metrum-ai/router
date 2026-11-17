<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Tenant cache isolation

Router response-cache keys include the caller and project. This is the default. Set `share_cache_within_project: true` on a project only when its callers are allowed to reuse each other's identical cached responses.

For self-hosted vLLM providers, set `upstream_kind: vllm` and `server.cache.upstream_salt_key_env` to an environment variable containing at least 32 random bytes. Every router replica that reaches the same model server must receive the same key. By default, the router derives a stable, opaque 256-bit prefix-cache salt per caller, including callers in the same project. If the project explicitly sets `share_cache_within_project: true`, callers in that project share one salt and may reuse each other's prefix-cache entries. Caller-supplied `cache_salt` values are replaced. The salt is added after request telemetry and is not stored in router logs, events, metrics or usage rows.

The upstream kind is explicit so the router sends the field only to a vLLM server configured to support it. If the upstream rejects the field or times out, the request fails through the existing upstream error path; it is never retried without a salt. Missing or short key material stops startup when a vLLM provider is configured. SGLang prefix-cache isolation is not supported in v5.0.0; configuration that combines an SGLang provider with the salt key is rejected. Database-backed provider configuration currently rejects `upstream_kind`; use YAML configuration for this opt-in until its relational schema supports the setting.

To rotate the salt key, place a new random value in the configured environment variable on every router replica and restart or drain those replicas together. New salts make existing upstream prefix-cache entries unreachable until the cache warms again. If the rollout fails, restore the previous key on every replica and restart them.

```yaml
server:
  cache:
    upstream_salt_key_env: ROUTER_PREFIX_CACHE_KEY

providers:
  local-vllm:
    base_url: http://vllm.internal:8000/v1
    dialect: openai-chat
    upstream_kind: vllm
    api_key_env: LOCAL_VLLM_KEY

projects:
  - id: research
    share_cache_within_project: false
```

The pinned vLLM v0.33.0 OpenAI-compatible server accepts `cache_salt` in request bodies and mixes it into the prefix-cache block hash. See the [pinned vLLM serving contract](https://docs.vllm.ai/en/v0.33.0/serving/online_serving/openai_compatible_server/) and [prefix cache design](https://docs.vllm.ai/en/v0.33.0/design/prefix_caching/). This release makes no SGLang prefix-cache isolation claim.
