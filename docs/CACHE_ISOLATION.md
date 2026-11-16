<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Tenant cache isolation

Router response-cache keys include the caller and project. This is the default. Set `share_cache_within_project: true` on a project only when its callers are allowed to reuse each other's identical cached responses.

For self-hosted vLLM or SGLang providers that accept the OpenAI-compatible `cache_salt` request field, set `upstream_kind` to `vllm` or `sglang`. Set `server.cache.upstream_salt_key_env` to an environment variable containing at least 32 random bytes. Every router replica that reaches the same model server must receive the same key. The router derives a stable, opaque 256-bit salt per project; callers without a project receive a caller-scoped salt. The salt is added after request telemetry and is not stored in router logs, events, metrics or usage rows. Caller-supplied `cache_salt` values are replaced.

The upstream kind is explicit so the router sends the field only to a server configured to support it. If the upstream rejects the field or times out, the request fails through the existing upstream error path; it is never retried without a salt. Missing or short key material stops startup when a vLLM or SGLang provider is configured. Database-backed provider configuration currently rejects `upstream_kind`; use YAML configuration for this opt-in until its relational schema supports the setting.

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

The vLLM v0.33.0 OpenAI-compatible server accepts `cache_salt` in request bodies; it mixes the value into the first prefix-cache block hash so different salts cannot reuse prefix blocks. See the [pinned vLLM serving contract](https://docs.vllm.ai/en/v0.33.0/serving/online_serving/openai_compatible_server/) and [prefix cache design](https://docs.vllm.ai/en/v0.33.0/design/prefix_caching/). Check the selected SGLang release's OpenAI-compatible request schema before marking a provider `upstream_kind: sglang`.
