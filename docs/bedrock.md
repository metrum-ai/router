<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# AWS Bedrock upstream

Metrum AI Router uses the official AWS SDK for Go v2, SigV4 and the **default AWS credential chain** (environment, shared profile, workload/IAM role). No AWS keys belong in router YAML or command arguments. Grant only Bedrock runtime access for the desired model resources and regions.

Merge `config/bedrock.example.yaml` into an existing authenticated router configuration. Add the groups to the caller's `allow` list. The provider uses `auth_scheme: bedrock` and `dialect: openai-chat`: Chat is the **internal bridge format**, not an unsigned OpenAI endpoint. The adapter calls Converse/ConverseStream for every family; it never uses InvokeModel. `base_url` is required by the router provider schema; use the corresponding regional runtime origin. SDK endpoint resolution controls the actual AWS destination.

Set `region` on each **group target** (persisted in the configuration control plane). If omitted, SDK region resolution uses the environment/profile. Model IDs are passed intact, including optional `us.*` cross-region inference profile IDs. Cross-region inference requires permission for the profile and destination resources; residency and profile availability are AWS concerns. Standard SDK endpoint overrides are useful for isolated local tests, but must not be set in production to untrusted hosts.

## Least-privilege IAM

Converse requires IAM action `bedrock:InvokeModel`; ConverseStream requires
`bedrock:InvokeModelWithResponseStream`. The SDK calls Converse APIs, but IAM
uses these InvokeModel action names. Limit `Resource` to the foundation-model
ARNs actually enabled in the router, not `*`. Cross-region inference profiles
also require the inference-profile ARN and the destination-region model ARNs;
restrict the allowed destinations in IAM/SCPs and check AWS's current profile
routing before deployment. A policy that permits only the source region may
fail cross-region inference. Grant neither model administration nor billing
permissions to the serving role. Model entitlement/account prerequisites still
apply; IAM permission alone is not activation evidence.

Prefer short-lived workload-role credentials. Shared development profiles and
environment credentials are supported by the SDK chain, not stored in the
router control plane. Do not enable untrusted SDK endpoint overrides: requests
are signed and sent to that destination.

## Capabilities and limits

The example enables only validated text capabilities for these exact IDs in `us-east-1`:

| Model ID | System prompt | Text | Stream | Function tools |
| --- | --- | --- | --- | --- |
| `us.meta.llama3-3-70b-instruct-v1:0` | Supported by Converse | yes | yes | yes |
| `mistral.mistral-large-3-675b-instruct` | validated | yes | yes | yes |
| `qwen.qwen3-32b-v1:0` | Supported by Converse | yes | yes | yes |
| `deepseek.v3.2` | Supported by Converse | yes | yes | yes |
| `openai.gpt-oss-20b-1:0` | Supported by Converse | yes | yes | yes |

Capabilities are **per model**, not inferred from a family name. Existing `input_modalities`, `tool_support`, `honors_max_tokens`, `request_shape_support` and bridge flags govern router eligibility. Unknown IDs emit a warning, not a panic or hard catalog rejection: explicitly configure their capability and pricing evidence before routing production traffic.

Chat, Responses and Anthropic Messages inputs map system/messages, function tool declarations, automatic/required/specific tool choice, tool results, temperature, output cap and stops. Chat/Responses/Anthropic streaming uses the existing incremental SSE bridges. Tool IDs and streamed argument fragments survive conversion. Responses function history additionally requires the persisted `bridges.responses_to_chat.tools: true` capability, alongside the legacy `responses_to_chat` bridge flags. This is stateless full-history continuation; `previous_response_id` is not supported by this bridge.

Vision, documents, provider-hosted tools, structured-output guarantees, reasoning controls/content, cache-control blocks and multimodal outputs are **not advertised** by this adapter. Non-text Chat content is rejected rather than silently discarded. Do not advertise vision merely because an AWS model family supports it. Forced tool choice is still subject to the selected model's Converse support; AWS validation errors remain 400s.

## Usage, prices and errors

Converse `usage.inputTokens`/`outputTokens`/`totalTokens` map to router accounting. ConverseStream's final `metadata.usage` event supplies usage; completion is not considered clean until the SDK event stream finishes. Prices in the example are standard on-demand **us-east-1** prices, verified against the AWS public Price List on 2026-10-23. Refresh prices when changing model, region or service tier; never assume cross-region or batch/flex/priority rates are interchangeable.

| Model | Input USD / million | Output USD / million |
| --- | ---: | ---: |
| Llama 3.3 70B | 0.72 | 0.72 |
| Mistral Large 3 | 0.50 | 1.50 |
| Qwen3 32B | 0.15 | 0.60 |
| DeepSeek v3.2 | 0.62 | 1.85 |
| gpt-oss 20B | 0.07 | 0.30 |

Cost is `(inputTokens * inputRate + outputTokens * outputRate) / 1e6`, using the existing router billing pipeline. Unknown IDs require explicit prices; there is no invented fallback rate. The recorded sample matches this math exactly. Actual AWS bill reconciliation requires the account owner after billing data arrives; this scoped runtime identity has no billing permission.

SDK errors become ordinary upstream HTTP outcomes: ThrottlingException → 429 (failover eligible), ValidationException → 400, AccessDeniedException → 403, ModelNotReadyException/ServiceUnavailableException → 503 (retry/failover eligible). The SDK also performs its standard bounded retries. Midstream errors propagate as stream failures, never a false successful `[DONE]`.

## Validation

Credential-free tests: `go test ./internal/router -run 'TestBedrock|TestResponsesToChat' -count=1`. SDK transport fixtures exercise SigV4, unary mapping, binary AWS eventstream decoding, tool fragments, metadata usage and throttle failover without AWS calls.

Opt-in direct live smoke: `ROUTER_BEDROCK_LIVE=1 go test ./internal/router -run '^TestBedrockLive$' -count=1` (never set by `make test`).

For the localhost matrix, build `go build -o tmp/bedrock-router ./cmd/metrum-ai-router`, bootstrap authenticated local config with `python3 scripts/local_dev_bootstrap.py --out-dir tmp/bedrock-dev`, merge the example provider/groups, allow those groups for the generated caller, set listen to `127.0.0.1:18430` and streaming translator to `incremental`, then start `./tmp/bedrock-router -config tmp/bedrock-dev/config.yaml`.

```sh
python3 scripts/bedrock_validate.py --token-file tmp/bedrock-dev/router.token --out docs/evidence/bedrock/2026-10-23
python3 scripts/bedrock_validate_dialects.py --token-file tmp/bedrock-dev/router.token --out docs/evidence/bedrock/2026-10-23
```

The scripts keep tokens out of argv and evidence. They create a bounded proof file, submit tool results and verify continuation; model-provided paths are never executed. The second script verifies Responses/Anthropic tools and streaming against Mistral. See [dated evidence](evidence/bedrock/2026-10-23/README.md) for exact IDs, failures encountered, costs and the simulated-throttle failover.
