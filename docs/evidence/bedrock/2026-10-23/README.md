<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# Bedrock validation — 2026-10-23

Metrum AI Router working-tree localhost validation, `us-east-1`. All calls used the default AWS credential chain. No keys or signed request headers are stored here. Text-only capabilities; no vision claim.

| Exact model ID | Chat | ConverseStream → Chat SSE | Tools + file/result continuation |
| --- | --- | --- | --- |
| `us.meta.llama3-3-70b-instruct-v1:0` | pass | pass | pass |
| `mistral.mistral-large-3-675b-instruct` | pass | pass | pass |
| `qwen.qwen3-32b-v1:0` | pass | pass | pass |
| `deepseek.v3.2` | pass | pass | pass |
| `openai.gpt-oss-20b-1:0` | pass | pass | pass |

`matrix.json` summarizes cells; `<family>-<mode>.json` includes requests, model responses, usage and (tools) a second response after a real bounded local file write and submission of the tool result. Output caps are 256 tokens, temperature 0. DeepSeek tools uses `tool_choice: required`, cleared before submitting the result continuation. Other families use automatic tool selection. `bedrock-proof.txt` is the file written by the client. The client never executes the model's proposed arbitrary file path.

Additional Mistral validation: Responses and Anthropic text/unary and SSE (`responses*.txt`, `messages*.txt`), function tool unary + tool-result continuation, and streaming tool arguments (`responses-tools*.json`, `anthropic-tools*.json`). Both dialects use system instructions in the tool smoke. `dialect-proof.txt` records the bounded write. Reasoning, vision, hosted tools and server-side Responses sessions are unsupported by this configured surface.

## Commands actually run

```sh
python3 scripts/local_dev_bootstrap.py --out-dir tmp/bedrock-dev
go build -o tmp/bedrock-router ./cmd/metrum-ai-router
./tmp/bedrock-router -config tmp/bedrock-dev/config.yaml
python3 scripts/bedrock_validate.py --token-file tmp/bedrock-dev/router.token --out docs/evidence/bedrock/2026-10-23
python3 scripts/bedrock_validate_dialects.py --token-file tmp/bedrock-dev/router.token --out docs/evidence/bedrock/2026-10-23
ROUTER_BEDROCK_LIVE=1 go test ./internal/router -run '^TestBedrock' -v -count=1
go test ./internal/router -run 'TestBedrock|TestResponsesToChat' -count=1
```

All matrix cells and tool continuations passed. Setup failures were corrected before collecting the final evidence: local caller initially allowed only `local` (403 `model-not-allowed`, not an AWS denial); bridge declarations initially lacked required persisted validation and client-tool flags (502 `no-eligible-target`, no AWS call). No model-access denials occurred. A later final rerun exposed a DeepSeek **automatic** tool-selection refusal (HTTP 200 text, not an adapter error), preserved in `deepseek-auto-refusal.json`. A forced-choice probe passed; the final script now forces DeepSeek tool selection and clears it for continuation. The rerun passed all 15 cells. This is not a claim that automatic tool use is deterministic. One direct five-family probe, one SDK live stream smoke and repeated localhost validation were run; no provisioned capacity or IAM changes.

## Throttle failover

A localhost fixture on 18431 returned AWS `ThrottlingException` with HTTP 429 for `/model/.../converse` and a successful OpenAI Chat completion for its fallback endpoint. A localhost router configured with `AWS_ENDPOINT_URL_BEDROCK_RUNTIME=http://127.0.0.1:18431` selected Bedrock first, exhausted normal SDK retries, then selected a separate `fallback` Chat upstream. No AWS invocation spend for this test. `failover-attempts.json` was read from the router SQLite database after graceful shutdown: first attempt 429, `upstream_rate_limited`, retryable; second attempt 200. `failover.json` is the returned fallback completion. `TestBedrockThrottleFailover` reproduces this offline through the router handler and a real SDK client. All local servers were stopped after validation.

## Cost

`pricing.json` is the selected public AWS standard on-demand price-list evidence (effective 2026-10-01), downloaded from the public regional AmazonBedrock pricing index. `cost-check.json` captures router-persisted tokens/rates/cost: `(75 × 0.50 + 12 × 1.50) / 1e6 = $0.0000555`, exactly matching the recorded cost. `matrix-cost.json` estimates the latest 20 matrix calls at $0.0013478. Earlier reruns, direct probes and dialect smokes bring the conservative total estimate to **under $0.10**, far below the $25 budget.

**Billing reconciliation remains pending:** the scoped runtime identity has no billing permissions and the AWS bill lags. The account owner must compare these token-based estimates with the eventual bill; this evidence does not claim a bill match.

AWS binary stream fixture `testdata/bedrock/converse-stream.json` is a sanitized protocol-shaped tool/usage fixture, not a saved authenticated HTTP exchange. Unit tests encode it as an AWS eventstream and feed the real SDK decoder without network access.
