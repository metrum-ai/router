# Smoke Test Matrix

Run smokes at the narrowest layer that proves the change, then run production-level smokes for deployed behavior.

For new provider, model, endpoint, or API-skin onboarding, follow the canonical 9-step procedure in [Model And Endpoint Onboarding Guide](onboard-model.md). Use [Smoke Commands Reference](smoke-commands-reference.md) for placeholder-based curl templates covering OpenAI Chat, OpenAI Responses, and Anthropic Messages capability probes.

For quality complaints or router-versus-fixed-model decisions, do not treat a smoke test as a full evaluation. A smoke proves that one request shape works. Use [Evaluation Evidence Playbook](EVALUATION_EVIDENCE_PLAYBOOK.md) when the decision depends on workload outcomes, repeated runs, cost, latency, fallbacks, and a fixed-model or previous-policy control.

Harbor or another outcome harness is required when a change promotes, demotes, or broadly reweights a model group based on task quality. Lighter compatibility smokes are enough for narrow checks such as "does this API skin authenticate," "is `max_tokens: 1` forwarded," or "does the client reach a tool-capable target," as long as no broader quality claim is being made.

## Model-Group Promotion Gate

Use this gate before moving a provider, model, endpoint, API skin, or bridge target from catalog or smoke-only status into a stable caller-facing group. A stable group is any group ordinary applications, production users, or coding agents depend on for day-to-day work. A staging group is a deployment-defined group with deliberate caller access for opt-in validation.

Promotion requires evidence for the exact request shape, not just the model name:

- direct upstream smokes for the exact provider credential, model ID, endpoint family, account or region, and API skin;
- router-level smokes through a restricted smoke or staging group before the target enters a broad group;
- OpenAI Chat, OpenAI Responses, Anthropic Messages, and bridge validation kept separate unless each surface passed independently;
- large coding-agent payload coverage when the group serves IDE or agent traffic: representative message count, tool count, serialized tool-schema bytes, request byte bucket, output cap, tool-choice mode, streaming behavior, and prompt-token scale;
- negative no-eligible-target coverage for unsupported tools, modalities, bridge fields, reasoning controls, hosted tools, large payloads, or output caps;
- safe usage/report evidence for request ID, selected provider/model/dialect, attempts, fallback, request-shape buckets, latency, token totals, and sanitized upstream error class;
- a rollback plan that can lower weight, tighten metadata, move the target back to staging, or restore the previous config snapshot.

Repeated upstream HTTP 400s are compatibility evidence. Do not rely on weighted retry luck to mask them. Add request-shape gates such as `request_shape_support.max_request_bytes`, tool-schema byte limits, supported inbound dialects, explicit bridge flags, or capability metadata so unsupported shapes skip before upstream while validated ordinary traffic can continue using the target.

## Core API Smokes

| Change type | Required smoke |
|---|---|
| Config validation | YAML parse and `docker compose config` |
| API compatibility dependency bootstrap | `python3 scripts/api_compat_bootstrap_test.py`; it proves a failed locked Python provision stops before Go provisioning, a failed normal bootstrap stops before offline conformance, clean-cache offline use fails closed, and offline conformance succeeds only after bootstrap |
| Health/deploy | `/readyz`, `/version`, router logs |
| Auth/allow list | `/v1/models` with caller token |
| Codex model catalog | missing token gets `401`; a restricted caller gets only its allow-listed, Responses-eligible groups from `/v1/codex/models.json`; prove native Responses and explicitly bridged Chat targets agree between catalog and routing, while unbridged Chat-only and Anthropic-only targets are absent. For every advertised image modality, repeat the exact mixed text/image Responses request and prove the selected native or enabled `responses_to_chat` target is eligible; a bridge without `images: true` or a target with an image request-shape exclusion must omit image metadata and return safe `no-eligible-target`. Parse returned `models[]` as Codex metadata, verify no upstream/provider/token/weight fields, and fetch it to a mode-0600 local file before an installed-Codex Responses-wire smoke |
| Anthropic namespace | `/anthropic/v1/messages` and `/anthropic/v1/messages/count_tokens` with a Claude-compatible caller token; legacy `/v1/messages` paths only as compatibility checks |
| Admin Basic Auth | `/admin/auth/check` with missing, bad, and valid Basic credentials when enabled |
| Omitted model behavior | request without `model`, expect configured default or `400 missing-model` |
| Text routing | relevant dialect with realistic token budget |
| Max-token cap | request with `max_tokens: 1`, OpenAI Chat `max_completion_tokens: 1`, or Responses `max_output_tokens: 1` |
| OpenAI-compatible encoding | prove `force_store_false`, `output_token_field`, and any target-scoped `default_openai_chat_thinking` produce the upstream payload the exact provider accepts without overriding caller intent |
| Reasoning production proof | restricted `reasoning-smoke` or deployment-selected group; `/v1/models` reasoning metadata, OpenAI Chat `reasoning_effort`, OpenAI Responses `reasoning.effort`, Anthropic Messages `thinking`, and usage DB `request_translation_shapes.translated_reasoning_control` for every request ID |
| Chat-to-Responses bridge | restricted smoke group with `bridges.chat_to_responses.enabled: true`; Chat unary and streaming text requests, Chat function-tool request when `tools: true`, Chat `reasoning_effort` only when `bridges.chat_to_responses.reasoning: true`, optional two-request stateful-session proof when `stateful_sessions.enabled`, incremental Responses SSE → Chat SSE with `server.streaming.translator: incremental`, negative unsupported shape such as unvalidated image input, usage rows with inbound Chat and target Responses, and safe translation-shape diagnostics |
| Usage/cost fields | query usage DB/report after a request |
| Request evidence bundle | produce one success or error request, query `/admin/reports/api/request-evidence?request_id=<request_id>` with a drilldown-authorized admin, verify section completeness, attempts when applicable, stored request-time costs, `Cache-Control: no-store`, ordinary caller `403 reports-forbidden`, and no raw prompts/images/tool schemas/tool outputs/tokens/token hashes/provider keys/upstream bodies |
| Caller traffic-shaping reports | enable a low caller `traffic_shape`, produce one queued or rejected request, run `router-usage-report --traffic-shaped-only`, and open Traffic shaping overview / Shaping users in admin reports |
| Traffic tuning advisor | run `router-usage-report --traffic-tuning-advisor --since 24h` and `/admin/reports/api/traffic-tuning-advisor?since=24h`; verify admitted upstream 400 cases recommend route-around/investigation, actual shaping rejections recommend burst/queue review, provider 429s across users recommend provider shaping/backoff, and outputs contain only safe scalar evidence |
| Provider/model/target shaping | enable a low local `traffic_shape`, send parallel requests from two caller tokens, verify skip/fallback or `503 upstream-capacity-throttled`, `Retry-After` when calculable, and safe `request_upstream_shape_events` rows |
| Provider-shaping reports | after provider/model/target shaping smoke, open Provider shaping and Backoff admin tabs and confirm charts/tables show skipped targets or cooldown starts without prompts, tokens, token hashes, or provider keys |
| Adaptive upstream backoff | simulate upstream `429` with bounded `Retry-After` and provider quota/billing exhaustion; verify the next request skips the affected target until cooldown expires and records `adaptive-backoff-provider-429` or `adaptive-backoff-provider-quota` |
| Request-shape context fit | send small text, large coding-agent, explicit output-cap, and all-target-too-small requests; verify target candidates show estimates, context headroom, safe skip reasons, and no raw prompt/tool/image data |
| Responses-to-Chat bridge | enable `responses_to_chat` on one Chat-only smoke target, send `/v1/responses` text and function-tool requests, verify the upstream path is `/chat/completions`, downstream shape is Responses, usage records inbound `openai-responses` and target `openai-chat`, and `request_translation_shapes.bridge_direction = responses_to_chat` |
| Responses-to-Chat negative gates | with the same smoke group, send `previous_response_id`, hosted tools, image, reasoning, structured-output, and streaming shapes that are not validated; verify `no-eligible-target` or a bounded router error before upstream and safe filter reasons such as `responses-to-chat-previous-response-id` or `responses-to-chat-reasoning` |
| Effective provider-skin eligibility | for each exposed group and intended client surface, run Chat, Responses, and Anthropic text/tool smokes as applicable; inspect provider catalog status for active target `activeEligibilitySkin`, `effectiveToolSupport`, `inactiveToolSupport`, image, structured-output, and reasoning fields |
| Claude Code restricted-group validation | use a caller allowed only to the intended group; set `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_MODEL`, and `CLAUDE_CODE_SUBAGENT_MODEL`; run plain text, client-tool/file edit, subagent, long-context/tool-schema, and tiny-cap smokes; verify `/v1/models`, no blank-model `403`, selected Anthropic Messages or explicitly validated bridge target, attempts/fallback/latency/tokens, and safe response-shape diagnostics |
| Decision telemetry | with `server.decision_telemetry.enabled: true`, run success, no-eligible-target, policy fail-closed, policy fallback, upstream-fallback-success, and cache-bypass requests; query `request_policy_executions`, `request_fallback_transitions`, score/ranking rows, safe fingerprints, and `router-usage-report` summary buckets |
| Multimodal/VLM routing | direct upstream image smoke for the exact provider/model/dialect, router-level image smoke through the intended model group, URL safety negative smoke, tiny-cap smoke, usage row image/cost fields, and no raw image persistence |
| Coding-agent client compatibility | deterministic fixture matrix with `python3 scripts/coding_agent_matrix.py --mode mock`, opencode API capability matrix with `python3 scripts/opencode_api_matrix.py` for provider/model skin support, then live Codex/Claude Code/opencode/aider smokes when the route change affects those clients |
| Kubernetes deployment artifacts | `kubectl kustomize deploy/kubernetes/overlays/example`, YAML parse, `kubectl apply --dry-run=client` or server dry-run when available, then staging port-forward smoke for `/readyz`, `/docs/`, `/version`, `/v1/models`, one chat request, admin reports when enabled, and metrics/admin denial for ordinary caller tokens |

## Anthropic Endpoint Split And Metadata Migration Proof

Use this proof after changing Anthropic endpoint routing, stricter Anthropic inbound eligibility, or production metadata for groups used by Claude Code or other Anthropic-compatible clients. The implementation slice for the endpoint split was completed by PRs #385, #386, and #387; this proof is the production-safe closeout evidence for issue #476-style migration work.

Local validation:

1. Sync from `origin/main` and verify the relevant endpoint split commits are present.
2. Use an ignored temporary config derived from the intended deployment config. Do not print or commit production config, raw caller tokens, token hashes, provider keys, prompts, images, or tool payloads.
3. For every broad group that should accept Anthropic Messages text through a non-native OpenAI Chat or Responses target, verify the active target has `request_shape_support.supported_inbound_dialects` including `anthropic` and a source-dated validation note. Native Anthropic Messages targets do not need this translation opt-in, but tool-bearing Claude Code traffic still needs `tool_support.anthropic_messages` or an explicitly validated Messages bridge.
4. Start the router and smoke `/readyz`, `/v1/models`, `/anthropic/v1/messages`, and `/anthropic/v1/messages/count_tokens` with a scoped caller token and safe synthetic payloads. Smoke legacy `/v1/messages` and `/v1/messages/count_tokens` only as compatibility aliases.
5. Run at least one negative Messages request against a group without an eligible target and expect `502 no-eligible-target` before upstream, with safe request ID and candidate/filter evidence.
6. Run a representative coding-agent compatibility check for the affected group. Harbor is appropriate for coding-agent outcome proof; a narrower Claude Code text/tool/file smoke is enough when the change is only endpoint or metadata eligibility.

Production validation, when deployment is in scope:

1. Verify `/version` or package metadata reports the expected post-merge build timestamp/version.
2. Verify production config metadata with a sanitized summary: group name, target provider/model label, target dialect, whether Anthropic inbound text is enabled, whether the target is native Anthropic Messages, and whether `tool_support.anthropic_messages` is present for tool traffic. Do not print full config or secret values.
3. Smoke `/readyz`, `/version`, authenticated `/v1/models`, `/anthropic/v1/messages`, and `/anthropic/v1/messages/count_tokens` with safe payloads. Record only status, request ID, model group, selected provider/model/dialect, fallback flag, latency, and sanitized error class.
4. Run the approved Harbor production smoke with the reusable Harbor caller when the model group is being validated for coding-agent quality. Separate results by case ID, client, model group, timestamps, and usage-report filters; never print the raw Harbor token.
5. Check safe usage/report evidence for `inbound_dialect`, selected target dialect, bridge direction when present, status, latency, attempts, fallback, and terminal errors. A plain Anthropic Messages text request that fails with `no-eligible-target` usually means the group is authorized but missing native Messages targets or `supported_inbound_dialects: [anthropic]` on the translated text target.

Rollback options are config-local: restore the previous target list, remove an invalid Anthropic inbound opt-in, remove or lower a bad target's weight, isolate the target in a restricted smoke group, or temporarily direct affected Claude Code users to a previously validated group that their token is allowed to use. After rollback, rerun `/v1/models`, a positive Messages smoke, and the negative `no-eligible-target` smoke.

## Automated Capability Probe

Use the capability probe before adding or changing provider catalog metadata. It runs direct upstream smokes, records pass/fail evidence, and emits a `recommended_config` block that maps directly to catalog fields:

```bash
scripts/probe-model-capabilities.sh \
  --base-url https://api.provider.example/v1 \
  --model provider-model-id \
  --api-key-env PROVIDER_API_KEY \
  --dialect openai-chat \
  --output yaml | tee tmp/probe-results.yaml
```

Supported dialects are `openai-chat`, `openai-responses`, and `anthropic`. The probe covers basic text, streaming, tiny max-token cap, auto tools, forced tools where applicable, structured outputs, tools plus structured outputs, optional image input/OCR when `--receipt-image-url` is provided, and reasoning controls. It uses realistic default budgets for acceptance probes and a tiny budget only for cap-honoring checks.

The output is direct-provider evidence only. Before adding active route weight, repeat every declared capability through a router smoke group, verify usage/cost/latency rows, and confirm requests that require omitted capabilities return safe `no-eligible-target` behavior without upstream attempts. Do not copy failed, skipped, or informational probe rows into `tool_support`, `input_modalities`, `reasoning`, or max-token metadata.

The full onboarding procedure is tracked in `docs/onboard-model.md` when present; the probe automates the direct-smoke step but does not replace pricing source validation, catalog review, smoke group setup, router-level smokes, production rollout, or rollback documentation.

## Reasoning Production Proof

Use this proof after any deployment or config change that affects reasoning metadata, model groups used by Codex/agent clients, provider skins, or usage diagnostics. The reference config includes `reasoning-smoke` as an example restricted group with one validated target for each enabled surface. Hosted deployments may instead pass a deployment-defined group such as a staging group or `big-coder` when that group is intended to advertise reasoning.

```bash
python3 scripts/reasoning_smoke.py \
  --base-url "$ROUTER_BASE_URL" \
  --token-file "$ROUTER_TOKEN_FILE" \
  --model reasoning-smoke \
  --postgres-dsn "$ROUTER_USAGE_DB_DSN" \
  --expect 'chat:fireworks:accounts/fireworks/models/gpt-oss-20b:openai-chat' \
  --expect 'responses:minimax_responses:MiniMax-M3:openai-responses' \
  --expect 'anthropic:kimi_anthropic:kimi-k2.7-code:anthropic'
```

The script prints only safe JSON evidence: model group, reasoning level count, request IDs, selected provider/model/dialect, translated reasoning control, and fallback flag. It does not print router tokens, provider keys, prompts, tool schemas, raw responses, or full config. It exits nonzero when `/v1/models` does not advertise reasoning levels, any surface fails, a request ID is missing from usage DB telemetry, the translated control is not `reasoning_effort`, `reasoning`, or `thinking` for the matching surface, the selected target differs from the expected set, or fallback was used.

For SQLite-backed local/staging checks, replace `--postgres-dsn` with `--sqlite-db <usage-db-path>`. For a deployment that intentionally has no Anthropic reasoning target, run `--surfaces chat,responses` and document the Anthropic blocker instead of claiming full three-surface coverage.

Local regression coverage must stay in the normal test suite so reasoning translation can be proven without a live provider. Run:

```bash
go test ./internal/router -run TestReasoning
python3 scripts/prod_reasoning_smoke_test.py
python3 scripts/reasoning_smoke.py --help
```

The Go matrix verifies OpenAI Chat, OpenAI Responses, and Anthropic Messages requests filter out non-reasoning targets, translate the correct upstream control field, persist `request_shapes.reasoning_present`, select the reasoning-capable candidate, and record `request_translation_shapes.translated_reasoning_control`. The Python tests verify the standalone smoke's DB proof logic against the current relational schema. `scripts/prod_reasoning_smoke.py` is kept only as a compatibility wrapper.

## Reasoning Bridge Proof

Reasoning bridge support must be proven separately from same-dialect reasoning. Same-dialect smokes should show these translated controls:

| Caller surface | Same-dialect upstream proof |
|---|---|
| OpenAI Chat to OpenAI Chat | Direct upstream and router smoke include `reasoning_effort`; telemetry records `translated_reasoning_control = reasoning_effort`. |
| OpenAI Responses to OpenAI Responses | Direct upstream and router smoke include `reasoning.effort`, and `reasoning.summary` only when the target supports summaries; telemetry records `translated_reasoning_control = reasoning`. |
| Anthropic Messages to Anthropic Messages | Direct upstream and router smoke include `thinking`; budget caveats such as `budget_tokens < max_tokens` or interleaved-thinking exceptions are recorded in target metadata; telemetry records `translated_reasoning_control = thinking`. |

For Chat-to-Responses reasoning, use a restricted bridge smoke group whose selected target is `dialect: openai-responses`, has compatible `reasoning` metadata, and explicitly sets `bridges.chat_to_responses.reasoning: true`. Run a Chat Completions request with `reasoning_effort` and verify the upstream body contains Responses `reasoning.effort`, the attempt path is `/v1/responses`, the response is returned in Chat shape, `bridge_direction = chat_to_responses`, and no fallback was used. Repeat a negative test against a similar target without the reasoning bridge flag and require `502 no-eligible-target` with a safe filter reason before any upstream attempt.

For Responses-to-Chat reasoning, treat reasoning as unsupported unless the target explicitly opts into `responses_to_chat.reasoning` and exact bridge tests prove the mapping. The default negative proof is a `/v1/responses` request with a `reasoning` object against a Chat-only bridge target that lacks that flag; expected evidence is `responses-to-chat-reasoning`, zero upstream attempts, and no translated reasoning control. Do not describe broad Responses-to-Chat reasoning support from same-dialect Chat reasoning smokes.

Bridge state is separate from reasoning. Chat-to-Responses is stateless unless `bridges.chat_to_responses.stateful_sessions.enabled` is implemented and enabled for that target. The `memory` backend is for local, single-process, or sticky-routed deployments. Use `backend: redis` only after the deployment-owned Redis address, namespace, env credential refs, TLS settings, and timeout caps validate at startup. OpenAI's Responses documentation checked on 2026-06-30 recommends `previous_response_id` or replaying prior output items for reasoning workflows; the stateless bridge does not provide that continuity by itself. MiniMax M3 documentation checked on 2026-06-30 says tool loops should preserve full response messages, including reasoning fields, and Anthropic extended-thinking documentation checked on 2026-06-30 documents API-specific thinking and interleaved-thinking constraints. Record those source dates in model onboarding notes when they affect target metadata.

## Production-Derived Regression Smokes

Run this proof after a production incident exposes a request shape or upstream error class that ordinary provider smokes missed. Fixtures live under `testdata/smokes/production-derived/` and contain only safe scalar shape metadata, target eligibility expectations, and issue references. Do not add captured prompts, raw tool schemas, images, bearer tokens, token hashes, provider keys, or full config.

Local regression gate:

```bash
go test ./internal/router -run 'ProductionDerived'
python3 scripts/prod_smoke_regressions_test.py
```

Deployment smoke for all sanitized production-derived fixtures:

```bash
python3 scripts/prod_smoke_regressions.py \
  --mode prod \
  --base-url "$ROUTER_BASE_URL" \
  --token-file "$ROUTER_TOKEN_FILE" \
  --fixture all \
  --model-group '<deployment-smoke-group>' \
  --postgres-dsn "$ROUTER_USAGE_DB_DSN"
```

The checked-in reference config includes `reasoning-bridge-smoke` for agent/reasoning bridge fixtures and `responses-to-chat-bridge-smoke` for the inverse bridge fixture that must not be satisfied by a native Responses target. Deployments must grant a reusable, scoped evaluation caller access to deployment-defined smoke groups before staging or production runs. Do not edit or repurpose a broad production group just to run bridge fixtures; use a dedicated smoke group or document the missing caller/group access as a blocker.

For local SQLite-backed router runs, use `--mode local` and replace `--postgres-dsn` with `--sqlite-db <usage-db-path>`. The script prints request ID, status, surface, message/tool counts, selected provider/model/dialect, and request-shape buckets only. It exits nonzero if an unexpected request fails, a fixture's must-not-select target is selected, an expected error/status is not observed, or persisted shape buckets do not match the fixture. `--fixture all` skips fixtures marked `replayable: false`, such as diagnostics-only upstream error classification contracts that require a mocked or staging error upstream; select those fixtures explicitly when that environment is configured.

The shared fixture set covers:

| Source | Fixture coverage |
|---|---|
| #319/#320 | Cursor image+tools no-eligible diagnostics with request ID and safe candidate/filter evidence |
| #321 | Upstream auth/entitlement classification and fallback proof |
| #330 | Provider-skin mismatch across Chat, Responses, and Messages |
| #333/#335 | Chat-to-Responses and Responses-to-Chat bridge fixtures |
| #344/#350 | Reasoning metadata, OpenAI Chat `reasoning_effort`, Responses `reasoning`, and Anthropic `thinking` |
| #352/#353 | Large Cursor/OpenAI Chat tool payloads and upstream/router error classification |
| #357/#358/#359 | Agent-specific bridge negatives, previous-response handling, streaming bridge rejection, thinking budget/cap edge cases, and Codex/Cursor/Claude Code/opencode/aider surfaces |
| #487 | `high` gt-1mb opencode/OpenAI Chat tool payloads route around ordinary non-MiniMax targets and select validated MiniMax Chat |

## opencode API Capability Matrix

Use the opencode matrix when a model or endpoint is expected to serve opencode-style coding-agent traffic. It checks OpenAI Chat and Anthropic Messages request shapes with synthetic text, client-tool, and image payloads, then writes sanitized JSON and Markdown evidence without printing API keys, raw prompts, raw images, tool outputs, or provider bodies.

```bash
python3 scripts/opencode_api_matrix.py \
  --base-url https://api.provider.example/v1 \
  --model provider-model-id \
  --api-key-env PROVIDER_API_KEY \
  --dialects openai-chat,anthropic \
  --tasks text,tools,image \
  --output-dir tmp/opencode-api-matrix
```

For Fireworks direct validation:

```bash
python3 scripts/opencode_api_matrix.py \
  --base-url https://api.fireworks.ai/inference/v1 \
  --model accounts/fireworks/models/deepseek-v4-flash \
  --api-key-env FIREWORKS_API_KEY \
  --env-json env.json \
  --dialects openai-chat,anthropic \
  --tasks text,tools,image \
  --output-dir tmp/opencode-fireworks-deepseek
```

The matrix exits zero after writing evidence by default, even when a capability row fails. Use `--strict-exit` for CI gates that should fail on any non-passing row. Text rows are marked pass only when the response contains the expected text, default `OK`; image rows are marked pass only when the response contains the expected receipt text, default `Rite Aid`.

On 2026-06-30, direct Fireworks `accounts/fireworks/models/deepseek-v4-flash` passed OpenAI Chat text/tools and Anthropic Messages text/tools in this matrix. The same direct matrix returned sanitized HTTP 400 rows for OpenAI Chat image and Anthropic Messages image, so keep that model text-only until image support passes for the exact endpoint and router skin.

## Release Validation Matrix

Use the release validation matrix before handing a binary, Docker package, Compose bundle, or Kubernetes manifest set to another operator:

```bash
make release-validation-matrix
```

The target runs the package-content self-tests, build-metadata validator, clean-tree validator self-test, Docker build-context guard, Compose security check, and Kubernetes overlay rendering when `kubectl` or `kustomize` is available. It does not require provider keys, router tokens, a Docker daemon, or a live Kubernetes cluster. After packages are built, run the same matrix with artifact validation:

```bash
python3 scripts/validate_release_matrix.py --include-artifacts
```

Full package creation and live deployment validation remain separate, potentially expensive checks:

```bash
make package-all
make package-docker-all
make e2e-compose-live
```

## Release Package Smokes

Release packaging smokes prove that artifacts are deterministic, external-safe, and architecture-correct before handoff.

| Artifact | Required smoke |
|---|---|
| Package validation self-test | `python3 scripts/validate_package_contents_test.py` and `python3 scripts/validate_release_clean_test.py` |
| Binary amd64 package | Clean tree, `make package-all`, validate `dist/metrum-ai-router-*-linux-amd64.tar.gz`, confirm x86-64 ELF binaries |
| Binary arm64 package | Clean tree, `make package-all`, validate `dist/metrum-ai-router-*-linux-arm64.tar.gz`, confirm aarch64 ELF binaries |
| Docker amd64 package | Docker daemon available, `make package-docker-all`, validate `dist/metrum-ai-router-*-docker-linux-amd64.tar.gz`, load image tar, run `/app/bin/metrum-ai-router --version` and helper `--version` commands |
| Docker arm64 package | Docker daemon and buildx platform support available, `make package-docker-all`, validate `dist/metrum-ai-router-*-docker-linux-arm64.tar.gz`, load image tar, run `/app/bin/metrum-ai-router --version` and helper `--version` commands where runner architecture or emulation allows |
| macOS release host | Confirm package tarballs contain no AppleDouble `._*` entries; package recipes set `COPYFILE_DISABLE=1` and validator rejects any accidental metadata entries |
| Compose assets | Extract Docker package, verify `compose/.env` pins `SMART_LLMROUTER_VERSION=<version>-linux-<arch>`, set deployment-owned passwords/DSNs, and run `docker compose config >/dev/null` |

## Evidence Evaluation Smokes

Before promoting or rolling back a model group based on a quality claim:

- run the narrow smoke for every affected API shape, tool dialect, modality, reasoning control, token cap, and timeout;
- record the config version or safe routing/config summary, selected provider/model, usage tokens, latency, attempts, fallback, and error fields;
- join the smoke window to any Harbor, acceptance-test, or external evaluation result by timestamp, caller/project, client, model group, request ID, or run label;
- if the smoke passes but the workload evaluation fails, treat it as a model-group quality issue rather than a transport compatibility issue;
- if the smoke fails, fix or roll back the target before interpreting broader evaluation results.


## API Dialect Conformance Gate

Run this gate before changing request parsing, upstream encoding, tool routing, structured outputs, reasoning controls, streaming behavior, max-token handling, or provider-hosted tool policy.

| Surface | Required deterministic checks | Router test coverage |
|---|---|---|
| OpenAI Chat Completions | Plain text, native same-dialect SSE timing/chunks/cancellation, `stream_options.include_usage`, `max_tokens`, `max_completion_tokens`, same-dialect tool deltas, `tool_choice`, JSON-schema `response_format`, and `reasoning_effort` | `go test ./internal/router -run 'TestAPIDialectConformance|TestNative'` |
| OpenAI Responses | Plain input, `max_output_tokens`, same-dialect function/namespace tool passthrough, generic hosted search/image descriptor stripping, remote hosted tool rejection, JSON-schema `text.format` | `go test ./internal/router -run TestResponsesConformance` |
| Anthropic Messages | Messages payloads, native same-dialect SSE content/tool-use/usage events and cancellation, caller `max_tokens`, `thinking` passthrough, default max-token injection when omitted | `go test ./internal/router -run 'TestAPIDialectConformance|TestNative'` |
| Gemini `generateContent` target | Unary text encode/decode goldens, exact model endpoint, unsupported-shape rejection, activation-evidence gate | `go test ./internal/router -run TestGemini` |
| Cross-surface routing | Tool/structured/reasoning/image/cap eligibility and `no-eligible-target` behavior | existing `service_test.go` request-shape and target-filter tests plus live smokes for provider activation |

The conformance gate is intentionally mock-upstream and deterministic. It proves router semantics, not provider quality. Provider/model activation still requires the direct and router-level live smokes in the provider sections below.

The deterministic bootstrap regression also verifies that approved internal Go
mirror settings (`API_COMPAT_BOOTSTRAP_GO_PROXY` and
`API_COMPAT_BOOTSTRAP_GO_SUMDB`) survive normal recursive bootstrap
orchestration when supplied by the environment, while command-line overrides
are delivered only to `go mod download` as data and are not evaluated by Make
or the shell. At the offline recursive-Make boundary, standalone mirror values
and GNU Make override transport (`MAKEFLAGS` and `MAKEOVERRIDES`) are scrubbed,
so that phase cannot reimport command-line mirrors. Python provisioning does
not receive either form. A malformed setting must not run embedded syntax;
provisioning may fail, but the separately invokable offline conformance target
remains strictly offline.

For OpenAI-compatible providers, distinguish generic translation from
same-dialect passthrough. Same-dialect OpenAI Chat and Anthropic Messages
streaming sets upstream streaming and proxies complete native SSE events.
Verify first-delta timing, more than two caller chunks, tool-call/tool-use
events, optional final usage, caller cancellation, and no replay or fallback
after the first committed event. Generic translation, OpenAI Responses, and
cross-dialect bridges can normalize fields and use unary upstream calls with
router-encoded caller streaming.

Responses-to-Chat bridging is opt-in target metadata, not automatic cross-skin compatibility. Before enabling `responses_to_chat`, direct-smoke the exact Chat upstream for text, max-token caps, and function tools, then router-smoke `/v1/responses` text and function-tool requests through a restricted group. Enable `responses_to_chat.reasoning` only after a smoke proves Responses `reasoning.effort` reaches the upstream as Chat `reasoning_effort` and usage diagnostics record `bridge_direction = responses_to_chat` with `translated_reasoning_control = reasoning_effort`. Do not enable stateful `previous_response_id`, hosted tools, images, structured output, or streaming unless those exact bridge flags and smokes exist.

Responses-body-on-Chat-endpoint compatibility is server-level and disabled by default with `server.openai_compatibility.tolerate_responses_body_on_chat_endpoint: false`. Before enabling it, run deterministic mock-upstream tests plus a restricted router smoke that posts a Responses-shaped body to `/v1/chat/completions`. Required proof: disabled mode returns `400 responses-body-on-chat-endpoint-disabled` while ordinary Chat still passes; enabled mode routes `input`, `instructions`, `reasoning`, function `tools`, `tool_choice`, `text.format` or `response_format`, one positive output cap normalized to `max_output_tokens`, and `stream` through eligible native Responses or Responses-to-Chat targets; unsupported fields and ambiguous multiple cap fields return a 400 before upstream; `previous_response_id` is accepted only for groups with a native Responses target; safe diagnostics show `openai_compatibility_shape`, inbound Responses shape, selected target dialect, bridge direction when applicable, and bounded rejection/filter reasons. Roll back by disabling the flag and redeploying.

For Chat-to-Responses bridge validation, use an `openai-responses` target with explicit `bridges.chat_to_responses` metadata. Run `POST /v1/chat/completions` through the router and verify the mock or live upstream receives `/v1/responses`, `input`, optional `instructions`, and `max_output_tokens`. For reasoning smokes, enable `bridges.chat_to_responses.reasoning` only on a dedicated smoke group, send Chat `reasoning_effort`, and verify the upstream receives Responses `reasoning.effort`; usage diagnostics should record `bridge_direction = chat_to_responses` and `translated_reasoning_control = reasoning`. For tool smokes, verify Chat function tools translate to Responses function tools and Responses function-call output maps back to Chat `tool_calls`. For stateful session smokes, enable `bridges.chat_to_responses.stateful_sessions`, send two requests with the same configured session header, verify the first upstream body omits `previous_response_id`, verify the second upstream body includes the first upstream Responses `id`, and verify a different caller or session header value does not reuse that id. For `backend: redis`, repeat the two-turn proof across two router processes sharing the Redis namespace, then test Redis outage or timeout behavior and expect a stateless request with `bridge_session_backend_error` rather than cross-session continuation. Also inject a stale `previous_response_id` 4xx in a mock upstream and verify the router emits `bridge_session_previous_response_stale_purged` plus `bridge_session_stateless_retry`, then succeeds once without the stale ID. Unless bridge streaming is implemented and enabled, `stream:true` must produce `502 no-eligible-target` with a safe filter reason and zero upstream attempts.

For production or staging validation, create restricted smoke/test groups such as `chat-to-responses-reasoning-smoke`, `responses-to-chat-reasoning-smoke`, and `chat-to-responses-stateful-smoke`; do not repurpose broad production groups such as coding-agent groups while validating bridge contracts. Grant one reusable, deployment-owned evaluation caller access to the smoke groups for the validation window and remove or narrow that access after evidence is captured.

For provider-skin eligibility, treat OpenAI Chat, OpenAI Responses, and Anthropic Messages as separate active pools inside the same model group. Catalog metadata for a model can list capabilities for several skins, but a target is effectively eligible only for the resolved provider or target dialect that is active in `models.<group>.targets[]`. A Responses function-tool smoke must select an `openai-responses` target or an explicitly documented bridge target; a Chat-only target with `tool_support.openai_responses` metadata is expected to be skipped until a native Responses skin or bridge is configured and validated. The provider catalog status report should show wrong-skin metadata under `inactiveToolSupport` with `metadata-for-inactive-provider-skin`.

## Robustness And Fallback Smokes

Use deterministic mock upstreams for robustness gates. Do not use live providers for timeout, malformed body, controlled `429`, forced `5xx`, or queue-depth failure injection unless the provider explicitly offers a staging endpoint for that behavior.

| Failure mode | Required proof |
|---|---|
| Fallback ordering | Static/failover groups attempt targets in configured order; weighted groups use a deterministic test seed or mock distribution check; fallback stays inside the requested group. |
| Retryable upstream failure | `429`, `5xx`, timeout, connection reset, malformed JSON, and truncated streaming fixtures either recover through a compatible fallback or return the documented terminal error. |
| Non-retryable upstream failure | Ordinary upstream `400` stops fallback unless a deployment explicitly treats the class as safe to replay. |
| Request-shape fallback safety | Tool, image, structured-output, reasoning, and explicit max-token-cap requests never fallback to a target that was filtered out for that request shape. |
| Caller limits | RPM, TPM, concurrency, quota, lifetime budget, and `traffic_shape` failures return safe `429` or `403` errors with request IDs and no upstream attempt when blocked before routing. |
| Adaptive backoff | Provider `429` with and without `Retry-After` and provider quota/billing responses start bounded cooldown rows and route around the affected target when another compatible target exists. |
| Provider access fallback | Simulate provider `401`, entitlement-shaped `403`, generic access `403`, and model-access `404`. Invalid credentials should stop with sanitized `503 upstream-access-denied`; target-specific entitlement/model-access failures should route around to another eligible target without marking the failed attempt caller-retryable. |
| Diagnostics | `request_usage`, `request_attempts`, `request_trace_events`, `request_fallback_transitions`, `request_traffic_shape_events`, and `request_upstream_shape_events` contain safe scalar fields for the request ID without prompts, images, raw provider bodies, provider keys, router tokens, or token hashes. |

Focused local command:

```bash
go test ./internal/router -run 'TestFallbackOrdering429RetryAfterTelemetryAndSecretRedaction|TestFallbackDoesNotCrossToolEligibility|TestCallerRPMErrorIsSafeAndSkipsUpstream|TestAdaptiveBackoffHonorsBoundedRetryAfter|TestTrafficShapeRequestStartRejectsBeforeUpstream'
```

Production-safe robustness smoke:

1. Use a dedicated test caller and a mock/staging upstream target for forced timeout, `429`, and `5xx` behavior.
2. Run a small burst within limits, then a controlled burst exceeding the test caller's shaping or RPM limit.
3. Run one route-around smoke where one target is cooled down and a compatible fallback succeeds.
4. Query a report window by request IDs, caller project/environment, client, and model group.
5. Confirm the report shows attempts, fallback, shaping/backoff, latency, cost, errors, and selected upstreams without secret material.

## Outcome Workload Gates

Smoke tests prove transport compatibility; outcome gates prove the model group still completes the workload. Before promotion, document the run matrix, reward/verifier, client/API matrix, fixed-model or previous-policy control when practical, pass/fail thresholds, cost and latency ceilings, and rollback criteria.

Use Harbor for coding-agent outcome gates when it matches the workload and agent surface. Use unit tests, OCR checks, extraction goldens, browser-control tasks, tool-call correctness checks, or product acceptance tests when those are the better verifier. A single Harbor task can catch regressions, but promotion evidence should use repeated runs and a control whenever practical.

Local/mock CI command:

```bash
python3 scripts/evaluate_workload_gate_test.py
python3 scripts/outcome_calibrated_policy_test.py
```

The outcome-calibrated policy self-test uses synthetic arithmetic,
benchmark-code, folder-listing, and unclassified requests with fake embeddings;
it must select the calibrated target or strong default without live credentials.

Run `make outcome-calibrated-synthetic-demo` to record the local regression
fixture under `tmp/outcome-calibrated-demo/`. It uses fake embeddings and mock
upstreams, so it verifies wiring only. For a provider-backed smoke, use a
dedicated staging group, real OpenAI embeddings, real candidate responses, an
objective workload verifier, and `scripts/run_outcome_calibrated_live_demo.py`.
The protected evidence must join each verifier result to the router request ID
and the authenticated policy audit selection. Supply a unique run ID to bypass
response-cache reuse.

Harbor/workload gate command:

```bash
python3 scripts/evaluate_workload_gate.py \
  --matrix examples/harbor-algotune-pca/workload_gate_matrix.json \
  --results examples/harbor-algotune-pca/runs/<CASE_ID>/results.tsv \
  --out-json examples/harbor-algotune-pca/reports/<CASE_ID>/workload-gate.json \
  --out-md examples/harbor-algotune-pca/reports/<CASE_ID>/workload-gate.md
```

When a safe usage JSON export is available, pass `--usage-json` so the gate summary includes selected provider/model distribution, request IDs, stored request-time cost, latency, status, and fallback correlation. Generated gate outputs belong under ignored artifact directories. Do not print raw Harbor caller tokens, provider keys, token hashes, raw prompts, raw images, raw tool outputs, or full production config.

## Hosted OpenAI-Compatible Provider Smokes

Hosted OpenAI-compatible providers such as Crusoe Managed Inference and Fireworks AI use the same router dialect as other `/v1/chat/completions` upstreams, but every provider/model/account combination still needs direct evidence before activation.

For Crusoe, public docs checked on 2026-06-24 list `https://api.inference.crusoecloud.com/v1` as the OpenAI-compatible endpoint and `meta-llama/Llama-3.3-70B-Instruct` as the quickstart model. Direct validation on 2026-06-24 required an explicit `User-Agent`; configure one under provider `headers`. Use `CRUSOE_API_KEY` only from a protected environment or ignored `env.json`; never print it. On 2026-06-25, `nvidia/Nemotron-3-Nano-Omni-Reasoning-30B-A3B` was available in the account and passed direct text/cap smokes, but direct receipt-image smokes returned incorrect or non-merchant answers, so it must remain limited to a dedicated smoke group until OCR/workload validation passes.

Direct checks before any active route:

```bash
curl -fsS https://api.inference.crusoecloud.com/v1/models \
  -H "User-Agent: metrum-ai-router-validation" \
  -H "Authorization: Bearer ${CRUSOE_API_KEY}"

curl -fsS https://api.inference.crusoecloud.com/v1/chat/completions \
  -H "User-Agent: metrum-ai-router-validation" \
  -H "Authorization: Bearer ${CRUSOE_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"<crusoe-model-id>","messages":[{"role":"user","content":"Reply OK only."}],"max_tokens":16,"stream":false}'

curl -N https://api.inference.crusoecloud.com/v1/chat/completions \
  -H "User-Agent: metrum-ai-router-validation" \
  -H "Authorization: Bearer ${CRUSOE_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"<crusoe-model-id>","messages":[{"role":"user","content":"Reply OK only."}],"max_tokens":16,"stream":true}'

curl -fsS https://api.inference.crusoecloud.com/v1/chat/completions \
  -H "User-Agent: metrum-ai-router-validation" \
  -H "Authorization: Bearer ${CRUSOE_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"<crusoe-model-id>","messages":[{"role":"user","content":"Write five sentences about routing."}],"max_tokens":1,"stream":false}'
```

Run OpenAI Chat tool, forced `tool_choice`, and `response_format` structured-output checks only for models intended to serve those request shapes. Add `tool_support.openai_chat` entries only after both direct Crusoe and router-level smokes pass for the exact model. Keep Crusoe out of Codex Responses and Claude Code Anthropic groups unless Crusoe exposes and passes those exact skins.

For direct MiniMax and Kimi coding-agent routes, the 2026-06-30 opencode API capability matrix passed OpenAI Chat text/tools for MiniMax `MiniMax-M3` at `https://api.minimax.io/v1` and Kimi `kimi-k2.7-code` at `https://api.moonshot.ai/v1`. MiniMax Anthropic-compatible text and forced client-tool smokes passed at `https://api.minimax.io/anthropic/v1/messages`; auto tool selection returned ordinary text in the direct probe, so validate the exact Claude-compatible client workflow before relying on auto tool calls. These routes may be added to `big-coder` as additional direct provider options, but large Cursor/OpenCode payloads still require the large-payload smoke before increasing weight further.

MiniMax `MiniMax-M3` also has a separate OpenAI Responses skin at `https://api.minimax.io/v1/responses`. MiniMax documentation checked on 2026-06-30 lists non-streaming and streaming Responses calls, `max_output_tokens`, `tools`, `tool_choice` values `auto` and `none`, `store`, and `reasoning.effort`. Direct MiniMax Responses smokes on 2026-06-30 passed text with `store:false`, SSE streaming, `max_output_tokens:1` cap behavior, function calls with `tool_choice:"auto"`, and reasoning efforts `none`, `minimal`, `low`, `medium`, and `high`. MiniMax documents non-`none` efforts as compatibility values that enable Adaptive Thinking for `MiniMax-M3`, not as depth controls. The reference config therefore uses a dedicated `minimax_responses` provider with `dialect: openai-responses`, text-only modalities, `force_store_false: true`, `tool_support.openai_responses: [function]`, and opt-in reasoning metadata. Do not route Codex/OpenAI Responses traffic through the `minimax` OpenAI Chat provider skin or infer image support for Responses until exact direct and router-level image smokes pass.

Router-level MiniMax Responses smokes on 2026-06-30 passed against `minimax-responses-smoke` and `minimax-responses-tool-smoke` for text, SSE streaming, function calls, tiny output cap, and an explicit `reasoning.effort` request. The local smoke used the `dev_no_license` build tag with a generated temporary config and did not touch production.

xAI Grok 4.5 is configured as an external OpenAI-compatible Chat target at `https://api.x.ai/v1` with `api_key_env: XAI_API_KEY`. xAI docs checked on 2026-07-09 list `grok-4.5`, Chat Completions and Responses APIs, 500K context, $2.00/M input, $0.50/M cached input, $6.00/M output, low/medium/high reasoning controls, function calling, and structured outputs. Direct xAI smokes on 2026-07-09 passed Chat text, Chat streaming, `max_tokens: 1`, OpenAI Chat auto tools, forced `tool_choice`, JSON schema structured outputs, `reasoning_effort` low/medium, Responses text, and Responses JSON schema structured outputs. Local router-level OpenAI Chat smokes passed the same day for `/v1/models` reasoning metadata, text, `reasoning_effort: low`, forced function tools, structured outputs, usage, request-time cost, latency, and no fallback. Keep the reference catalog entry text-only until direct and router-level image smokes pass for this exact model and endpoint. For broad coding groups, use the OpenAI Chat skin for validated Cursor/OpenCode-style tools and reasoning, but keep the production-derived opencode/AI SDK `stream_options` shape gated until exact direct and router smokes pass; do not infer Anthropic Messages client-tool compatibility from Chat smokes.

For Fireworks, public docs checked on 2026-06-28 list `https://api.fireworks.ai/inference/v1` as the OpenAI-compatible endpoint and Serverless pricing for active reference candidates. Use `FIREWORKS_API_KEY` only from a protected environment or ignored `env.json`; never print it. Direct validation showed completions may require an explicit `User-Agent` from this environment. Configure one under provider `headers`. Fireworks `accounts/fireworks/models/gpt-oss-20b` passed direct text, streaming, `max_tokens: 1`, OpenAI Chat `reasoning_effort` low/medium/high, auto tools with `max_tokens >= 256`, forced `tool_choice`, and JSON schema structured-output smokes on 2026-06-27. Fireworks `accounts/fireworks/models/glm-5p2`, `accounts/fireworks/models/kimi-k2p7-code`, `accounts/fireworks/models/deepseek-v4-flash`, and `accounts/fireworks/models/qwen3p6-plus` passed direct OpenAI Chat text and auto-tool smokes on 2026-06-28, then production router-level text smokes through `big-coder`. Fireworks DeepSeek-V4-Flash passed direct and local router-level synthetic 524 KB OpenAI Chat coding-agent payload smokes on 2026-06-29, and the same direct/local evidence was rerun on 2026-06-30 for `accounts/fireworks/models/deepseek-v4-flash` with 524,375 request bytes, 24 tools, about 50 KB of serialized tool schemas, `max_tokens:32`, and about 91K prompt tokens. The production dedicated Fireworks GPT OSS 20B smoke group passed the same request shape on 2026-06-29 with about 90K prompt tokens; keep broad coding-group promotion and any request-shape caps separate from this validation evidence unless an operator explicitly approves a production routing change. The opencode API capability matrix showed direct Fireworks DeepSeek-V4-Flash OpenAI Chat text/tools and Anthropic Messages text/tools passing on 2026-06-30, while both image request shapes returned sanitized HTTP 400 rows. On 2026-07-09, production-derived opencode/AI SDK Chat requests with `stream_options` showed repeated upstream 400s on Fireworks DeepSeek-V4-Flash; keep that shape gated until direct and router-level smokes pass for the exact provider/model/skin. Keep this evidence source-dated and revalidate larger requests, image-bearing requests, untested Anthropic Messages models, or materially different tool-schema shapes before increasing broad routing weight. Keep Fireworks image-capable targets text-only until direct image and router-level image smokes pass for the exact endpoint.

Direct Fireworks checks before any active route:

```bash
curl -fsS https://api.fireworks.ai/inference/v1/models \
  -H "User-Agent: metrum-ai-router-validation" \
  -H "Authorization: Bearer ${FIREWORKS_API_KEY}"

curl -fsS https://api.fireworks.ai/inference/v1/chat/completions \
  -H "User-Agent: metrum-ai-router-validation" \
  -H "Authorization: Bearer ${FIREWORKS_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"accounts/fireworks/models/gpt-oss-20b","messages":[{"role":"user","content":"Reply OK only."}],"max_tokens":64,"stream":false}'

curl -fsS https://api.fireworks.ai/inference/v1/chat/completions \
  -H "User-Agent: metrum-ai-router-validation" \
  -H "Authorization: Bearer ${FIREWORKS_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"accounts/fireworks/models/gpt-oss-20b","messages":[{"role":"user","content":"Reply OK only."}],"reasoning_effort":"low","max_tokens":128,"stream":false}'

python3 scripts/large_payload_chat_smoke.py \
  --base-url https://api.fireworks.ai/inference/v1 \
  --model accounts/fireworks/models/deepseek-v4-flash \
  --api-key-env FIREWORKS_API_KEY \
  --env-json env.json \
  --target-bytes 524288 \
  --tool-count 24 \
  --max-tokens 32
```

Production closeout uses the same helper against a deployment-owned router URL and a safe caller token that is already allowed to the target smoke group or model group. Record only scalar output from the helper plus the smoke window, selected provider/model/dialect, request-byte bucket, tool-count bucket, status, latency, usage tokens, attempts, and fallback flag. If the current workstation lacks a production router base URL or reusable safe caller token, document that blocker instead of opening local token files or printing token material.

Fireworks GPT OSS 20B returns `reasoning_content` alongside visible content. Declare `reasoning` metadata only after a router-level `reasoning_effort` smoke confirms the selected target preserves the caller request shape and usage/cost telemetry remains populated. Keep Fireworks image/audio/video routes and untested Fireworks Anthropic Messages models disabled until those exact direct and router-level skins pass.

Fireworks Responses validation on 2026-06-28 used the official Responses API docs and Serverless pricing docs. The docs list `/inference/v1/responses`, client-executed function tools, provider-executed MCP/SSE tools, streaming, `max_tool_calls`, and `store=false`; they also note the Responses API has different retention behavior from Chat Completions. The router config uses a separate `fireworks_responses` provider, sets `force_store_false: true` on the validated target, rejects caller-supplied remote provider-hosted `mcp`/`sse` tools before upstream, and strips generic hosted search/image tool descriptors when the router is not exposing those services.

Direct Fireworks Responses results on 2026-06-28:

| Model | Text `store:false` | Function tool | Tool-result continuation | Streaming tool | `max_tool_calls:1` | `max_output_tokens:1` | Activation |
|---|---|---|---|---|---|---|---|
| `accounts/fireworks/models/kimi-k2p7-code` | Passed | Passed | Passed | Passed | Returned one call with incomplete status | Honored cap with incomplete status | Added to `fireworks_responses`, smoke groups, and low-weight `big-coder` tool-only target |
| `accounts/fireworks/models/glm-5p2` | Accepted but tiny text budget returned incomplete reasoning text | Passed | Passed | Passed | Returned one call with incomplete status | Honored cap with incomplete status | Not activated; keep for future workload validation |
| `accounts/fireworks/models/deepseek-v4-flash` | Accepted but tiny text budget returned incomplete text | Passed | Passed | Passed | Returned one call with incomplete status | Honored cap with incomplete status | Active in production/reference `big-coder` ordinary-text routing at 15% as of 2026-06-30; keep image and untested skins disabled until separately validated |
| `accounts/fireworks/models/qwen3p6-plus` | Accepted but tiny text budget returned incomplete text | Passed | Passed | Passed | Returned one call with incomplete status | Honored cap with incomplete status | Not activated; keep for future workload validation |
| `accounts/fireworks/models/gpt-oss-20b` | Passed | Passed | Failed acceptance: continuation returned unrelated incomplete content | Passed | Did not call the tool in the probe | Honored cap with incomplete status | Active in production/reference `big-coder` OpenAI Chat routing at 25% for explicit reasoning-capable Chat traffic; not activated for Responses tools |

Direct hosted-tool probes using `mcp` and `sse` with a public test URL returned Fireworks HTTP 500. Do not expose provider-hosted Fireworks tools through the router without a separate security design with explicit allowlists, timeouts, and privacy review.

Direct Fireworks Responses checks:

```bash
curl -fsS https://api.fireworks.ai/inference/v1/responses \
  -H "User-Agent: metrum-ai-router-validation" \
  -H "Authorization: Bearer ${FIREWORKS_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"accounts/fireworks/models/kimi-k2p7-code","input":"Reply OK only.","max_output_tokens":64,"store":false}'

curl -fsS https://api.fireworks.ai/inference/v1/responses \
  -H "User-Agent: metrum-ai-router-validation" \
  -H "Authorization: Bearer ${FIREWORKS_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"accounts/fireworks/models/kimi-k2p7-code","input":"Use the weather tool for San Francisco, CA.","max_output_tokens":512,"store":false,"tool_choice":"auto","tools":[{"type":"function","name":"get_weather","description":"Get current weather for a city","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}}]}'
```

Router-level Fireworks Responses smokes on 2026-06-28 passed for `fireworks-responses-smoke` text and `fireworks-responses-tool-smoke` function-tool requests, tool-result continuation, downstream SSE synthesis, usage fields, and `store:false` passthrough on tool requests. Negative router smokes for `mcp` and `sse` tools returned `400 provider-hosted-tools-forbidden` before upstream.

For Crusoe VLM candidates, add an image smoke before broad routing:

```bash
curl -fsS https://api.inference.crusoecloud.com/v1/chat/completions \
  -H "User-Agent: metrum-ai-router-validation" \
  -H "Authorization: Bearer ${CRUSOE_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"<crusoe-model-id>","messages":[{"role":"user","content":[{"type":"text","text":"Read the receipt image carefully. Reply with only the merchant/store chain name printed on the receipt."},{"type":"image_url","image_url":{"url":"https://cdn.learnopencv.com/wp-content/uploads/2018/06/04100007/receipt.png"}}]}],"max_tokens":512,"stream":false}'
```

Accepting an image is not enough for production `vision` traffic. The response must satisfy the deployment's quality gate, for example returning the expected receipt merchant for OCR validation, and the router-level smoke must log image request metadata and request-time pricing.

Router-level checks for a dedicated Crusoe smoke group:

- `/readyz` starts cleanly with `CRUSOE_API_KEY` configured and logs do not contain secrets.
- `/v1/models` only exposes the Crusoe smoke group to an explicitly allowed test caller.
- Non-streaming `/v1/chat/completions` returns `OK` and usage rows include `target_provider=crusoe`, upstream model, tokens, cost, latency, TTFB, duration, attempts, and no fallback.
- Streaming `/v1/chat/completions` returns valid SSE if the route will serve streaming callers.
- Tool, forced-tool, and structured-output requests return `502 no-eligible-target` before an upstream attempt until validated capability metadata is present.
- Bad-key, 401/403, 429, timeout, and 5xx responses are sanitized and produce stable caller-visible router errors.
- Harbor e2e passes before a hosted OpenAI-compatible provider joins broad ordinary-text coding-agent traffic. Limited tool-only targets may be added after exact direct and router-level tool smokes when the caller dialect matches the upstream dialect and the existing fallback target set remains intact.

## Tool Smokes

| Client/API | Smoke |
|---|---|
| OpenAI Chat tools | `/v1/chat/completions` with `tools`, `tool_choice`, and streaming when supported |
| OpenAI Responses tools | Codex CLI or direct `/v1/responses` function tool request |
| Anthropic Messages tools | Claude Code or direct `/v1/messages` client tool request |
| OpenRouter-specific tool route | validate the OpenRouter skin used by the target |

For agent CLI smokes, the agent must create a file and the test must assert the file contents.

## Request-Shape Context-Fit Smokes

Run these smokes when adding or changing `request_shape_support`, `context_tokens`, tool metadata, coding-agent groups, or provider targets that previously returned invalid-request or context-limit errors for large Cursor, Codex, Claude Code, or opencode payloads.

The reference config includes `large-openai-chat-tools-smoke` as a restricted validation group for the production-derived OpenAI Chat shape with `stream:true`, 105 messages, 19 tools, no images, no caller output cap, `64kb-256kb` request/text buckets, a `16kb-64kb` tool-schema bucket, and a huge estimated input-token bucket. It also includes a reference gt-1mb OpenAI Chat tool fixture that proves targets with `max_request_bytes: 1048576` are skipped while an explicitly validated large-payload target remains eligible. Grant a reusable, deployment-owned evaluation caller access to dedicated smoke groups before staging validation; for production validation of caller-visible groups, use an existing scoped caller and confirm selected provider/model/dialect from safe usage/report rows. Do not change a broad production coding group just to run these regression fixtures.

| Case | Smoke |
|---|---|
| Small text request | Send a short text-only request with no tools and a modest output cap. Expect the normal target pool to remain eligible. |
| Large coding-agent payload | Send a synthetic large request with safe filler text, many messages or tool-output-shaped items, and representative tool schemas. Targets whose context/request/tool limits cannot fit the estimate must be skipped before upstream. |
| Mixed OpenAI Chat tools plus image | Send a Cursor-style `/v1/chat/completions` request with `stream:true`, multiple messages, many function tools, one image part, and no output cap. At least one target in the requested group must support OpenAI Chat tool passthrough and image input on the same target; text-only tool targets, Responses-only targets, and Anthropic-only targets must not be selected for that OpenAI Chat request. |
| Explicit output reserve | Repeat a request with low and high `max_tokens`, `max_completion_tokens`, or `max_output_tokens`. The high cap should skip targets where estimated input plus reserve exceeds context. |
| Unknown limits | Include one target without `context_tokens` or request-size metadata. By default it remains eligible and records `limit_unknown`; use this as an inventory gap, not proof of support. |
| All targets too small | Configure a test group where every target is too small. Expect `502 no-eligible-target`, a request ID, safe requirement/reason details, and no upstream attempt. |
| Safe persistence | Query `request_token_estimates`, `request_target_candidates`, and `request_target_filter_reasons`; verify scalar estimates, caps, limits, headroom, decisions, and reasons are present, and raw prompts, raw tool schemas, raw tool outputs, image data/URLs, router tokens, token hashes, and provider keys are absent. |

Rollback is metadata-only unless code changed: remove or relax the affected `request_shape_support` override, remove the target from the affected model groups, or move it into a restricted smoke group. Then rerun the small request, large request, and all-target-too-small smokes to confirm the selected target and diagnostics match the intended behavior.

## Multimodal And VLM Smokes

Before activating an image-capable target in a broad coding or VLM group, run both direct-provider and router-level checks for the exact provider, model ID, dialect, and account entitlement.

| Gate | Required evidence |
|---|---|
| Direct upstream image smoke | Exact upstream API path accepts the intended image shape and returns a useful answer with `max_tokens` or equivalent at least `512` |
| Router-level image smoke | Same workload through the intended model group selects the expected image-capable target and records request ID, provider/model/dialect, latency, attempts, usage, and fallback state |
| Payload shape | OpenAI Chat `image_url`, OpenAI Responses `input_image`, and Anthropic Messages `image.source` shapes are validated when the group serves those clients |
| URL safety | Loopback, link-local, RFC1918/private, multicast, unspecified, malformed schemes, DNS failures, and bounded DNS timeouts fail before upstream and no provider key is used; deployment egress controls separately cover redirects and DNS rebinding because the router does not dereference accepted image URLs |
| Private URL override | `server.upstream.allow_private_image_urls: true` is tested only for deployments that intentionally allow private VLM dereference |
| Cost and telemetry | Usage rows include `input_has_image`, `input_image_count`, upstream image tokens when reported, calculated image cost, and upstream-reported billed costs when present |
| Cap behavior | Tiny explicit caps are forwarded exactly; targets marked `honors_max_tokens: false` are skipped for capped requests |
| Quality | OCR-specific routes must return the expected merchant/name/value; merely accepting or describing an image is not sufficient |

Production examples should include receipt OCR, screenshot/UI understanding, a mixed coding task with an attached image, and negative SSRF URL rejection. Keep raw images, provider keys, router tokens, token hashes, and full production config out of logs and reports.

If an image target fails quality, cap, or URL-safety smokes after activation, roll back by removing or lowering that target in the affected model group, restoring the previous config backup, restarting the router, and rerunning `/readyz`, `/v1/models`, a text request, and the failing image smoke.

## Coding-Agent Client Matrix

Run [Coding-Agent E2E Matrix](CODING_AGENT_E2E_MATRIX.md) for route changes that affect coding groups, tool support, multimodal agent traffic, client authentication behavior, or model-group access.

Minimum deterministic check:

```bash
python3 scripts/coding_agent_matrix.py --mode mock --output-dir tmp/coding-agent-matrix
```

Production or staging promotion should add live smokes for Codex CLI over OpenAI Responses, Claude Code CLI over Anthropic Messages, opencode, and aider where the client is installed and supported. Record client version, model group, request dialect, request IDs, selected upstream provider/model/dialect when available from reports, verifier result, elapsed time, and token totals.

## Reasoning And Thinking Smokes

Reasoning and thinking controls are dialect-specific. Declare `reasoning` metadata only for the exact provider/model/dialect/skin that passes the relevant direct upstream and router-level smoke. A model name or marketing claim is not enough.

| Client/API | Smoke |
|---|---|
| OpenAI Chat reasoning | Direct upstream `/chat/completions` with `reasoning_effort`, then the same request through the router group |
| OpenAI Responses reasoning | Direct upstream `/responses` with a `reasoning` object, then the same request through the router group |
| Anthropic Messages thinking | Direct upstream `/v1/messages` with `thinking.type: enabled` and `budget_tokens`, then the same request through the router group |
| Negative eligibility | Router request against a group with no compatible target; expect `502 no-eligible-target` and no upstream attempt |
| Low output cap | Verify any required `max_tokens` to `max_completion_tokens` translation and budget/max-token constraints for the exact upstream |

OpenAI Responses `gpt-5.4-nano` direct smokes passed on 2026-06-30 for no reasoning and `reasoning.effort` values `low`, `medium`, and `high`; each returned completed visible `OK` output. A direct medium-reasoning function-tool smoke returned a `function_call`. A direct `max_output_tokens: 1` probe was rejected with a documented minimum of 16, so this target should use realistic output budgets for acceptance and coding-agent traffic and should not be treated as a tiny-cap target.

Add metadata only after validation:

```yaml
providers:
  example:
    models:
      reasoning-model:
        model: provider-model-id
        reasoning:
          supported: true
          mode: opt_in
          control: effort_enum
```

Use `control: effort_enum` for OpenAI-style levels and `control: token_budget` for Anthropic-style thinking budgets. If an upstream rejects `max_tokens`, temperature, top-p, or requires thinking budget to be lower than the output cap, record that as scalar `reasoning` metadata and run a router-level smoke that proves the translation or filter.

If validation fails, remove `reasoning` metadata from the provider model or target override. If the target is active and unsafe for explicit reasoning traffic, remove it from active `models.<group>.targets[]` or keep it catalog-only until validation passes.

### Existing Weighted Group Rollout Checklist

When enabling reasoning routing in an existing weighted group, keep ordinary traffic and explicit reasoning traffic distinct:

- inventory the requested group and identify which active targets should continue to serve ordinary traffic;
- direct-smoke each intended reasoning target with the exact provider, model ID, dialect, API skin, and control field before adding metadata;
- run router-level smokes for OpenAI Chat `reasoning_effort`, OpenAI Responses `reasoning`, and Anthropic Messages `thinking` for every skin the group exposes;
- use realistic acceptance budgets, because reasoning-heavy models can consume tiny output caps and return empty final content;
- run low-cap smokes with `max_tokens: 1`, `max_completion_tokens: 1`, or `max_output_tokens: 1` to prove cap forwarding, skip behavior, or configured translation;
- add `reasoning` metadata only to validated provider models or target overrides, leaving non-reasoning targets in the weighted mix for ordinary requests;
- verify `/v1/models` with an allowed caller and confirm safe reasoning metadata such as `supported_reasoning_levels` and `supports_reasoning_summaries` appears only where intended;
- run a negative request against a test group with no compatible reasoning target and expect `502 no-eligible-target` with no upstream attempt;
- query usage, attempts, traces, and reports after success and failure cases for selected provider/model, status, latency, TTFB, duration, throughput, token counts, cost fields, fallback state, and safe reasoning metadata;
- document rollback: remove unsafe reasoning metadata, relax a reasoning-only contract or dynamic-score hard filter, restore the previous group config backup, and rerun the failing smoke.

## Structured-Output Smokes

Structured-output requests are dialect-specific. Declare `structured_outputs` only for the exact provider/model/dialect/skin that passes the relevant smoke. A target that only accepts the request field syntactically is not validated until it returns schema-shaped content, reports normal usage when the upstream normally does, and fails or rejects unsupported strict schemas in an understandable way.

| Client/API | Smoke |
|---|---|
| OpenAI Chat structured outputs | Direct upstream `/chat/completions` with `response_format.type: json_schema`, then the same request through the router group |
| OpenAI Responses structured outputs | Direct upstream `/responses` with `text.format.type: json_schema`, then the same request through the router group |
| Negative eligibility | Router request against a group with no compatible target; expect `502 no-eligible-target` and no upstream attempt |
| Tools plus structured outputs | Combined request when the target claims both capabilities for the same dialect |
| Streaming structured outputs | Verify caller-visible behavior for clients that request streaming; note whether downstream SSE is provider-native or router-synthesized |

The router forwards schema payloads to the selected upstream. It does not validate arbitrary JSON Schema subsets or repair model output. Unsupported schemas may produce upstream/provider errors even when eligibility metadata is correct.

Add metadata only after validation:

```yaml
providers:
  example:
    models:
      example-model:
        model: provider-model-id
        tool_support:
          openai_chat: [structured_outputs]
          openai_responses: [structured_outputs]
```

If a target supports both tools and structured outputs on one surface, keep both capabilities on that same surface, for example `openai_chat: [tools, tool_choice, structured_outputs]`. A tool-bearing structured-output request must not route to a tools-only target or a structured-only target.

### Direct Upstream Checks

Run these checks directly against the upstream endpoint before routing traffic through the router. Use placeholder-safe prompts and do not print provider keys.

OpenAI Chat `response_format.type: json_schema`:

```bash
curl -fsS "$UPSTREAM_BASE_URL/chat/completions" \
  -H "Authorization: Bearer $UPSTREAM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<provider-model-id>",
    "messages": [{"role": "user", "content": "Extract INC-1234 high"}],
    "response_format": {
      "type": "json_schema",
      "json_schema": {
        "name": "ticket_extract",
        "strict": true,
        "schema": {
          "type": "object",
          "properties": {
            "ticket_id": {"type": "string"},
            "priority": {"type": "string", "enum": ["low", "medium", "high"]}
          },
          "required": ["ticket_id", "priority"],
          "additionalProperties": false
        }
      }
    },
    "max_tokens": 128,
    "stream": false
  }'
```

OpenAI Responses `text.format.type: json_schema`:

```bash
curl -fsS "$UPSTREAM_BASE_URL/responses" \
  -H "Authorization: Bearer $UPSTREAM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<provider-model-id>",
    "input": "Extract INC-1234 high",
    "text": {
      "format": {
        "type": "json_schema",
        "name": "ticket_extract",
        "strict": true,
        "schema": {
          "type": "object",
          "properties": {
            "ticket_id": {"type": "string"},
            "priority": {"type": "string", "enum": ["low", "medium", "high"]}
          },
          "required": ["ticket_id", "priority"],
          "additionalProperties": false
        }
      }
    },
    "max_output_tokens": 128,
    "stream": false
  }'
```

When declaring strict tool/function argument support, also run a tool-call request with a strict parameter schema on the same surface. Record only safe evidence: provider, model ID, API surface, HTTP status, finish reason or response status, whether parsed content matched the schema, usage token counts, and whether intentionally unsupported schemas failed clearly.

### Router-Level Checks

After direct upstream checks pass, add the capability metadata to the target and run router-level smokes through a deployment-defined test group before activating or increasing the target in broad groups.

For OpenAI-compatible providers, validate encoding metadata explicitly. Set `force_store_false: true` only after the upstream accepts `store:false`; leave it unset for providers that reject `store`. Set `output_token_field: max_completion_tokens` only after a Chat Completions smoke proves the target requires `max_completion_tokens` instead of `max_tokens`. If a tool route also needs a target-level dialect override, keep that routing/config change separate from the metadata unless the rollout scope includes it.

OpenAI Chat through the router:

```bash
curl -fsS "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<structured-chat-test-group>",
    "messages": [{"role": "user", "content": "Extract INC-1234 high"}],
    "response_format": {
      "type": "json_schema",
      "json_schema": {
        "name": "ticket_extract",
        "strict": true,
        "schema": {
          "type": "object",
          "properties": {
            "ticket_id": {"type": "string"},
            "priority": {"type": "string", "enum": ["low", "medium", "high"]}
          },
          "required": ["ticket_id", "priority"],
          "additionalProperties": false
        }
      }
    },
    "max_tokens": 128,
    "stream": false
  }'
```

OpenAI Responses through the router:

```bash
curl -fsS "$ROUTER_BASE_URL/v1/responses" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<structured-responses-test-group>",
    "input": "Extract INC-1234 high",
    "text": {
      "format": {
        "type": "json_schema",
        "name": "ticket_extract",
        "strict": true,
        "schema": {
          "type": "object",
          "properties": {
            "ticket_id": {"type": "string"},
            "priority": {"type": "string", "enum": ["low", "medium", "high"]}
          },
          "required": ["ticket_id", "priority"],
          "additionalProperties": false
        }
      }
    },
    "max_output_tokens": 128,
    "stream": false
  }'
```

Negative and mixed-capability router checks:

- Send the same structured-output request to a test group with no `structured_outputs` target and expect `502 no-eligible-target` with `structured_outputs` in `details.requirements`.
- For tool plus structured-output requests, verify this matrix:

| Target capabilities | Expected eligibility |
|---|---|
| tools only | skipped |
| structured outputs only | skipped for tool-bearing request |
| tools + structured outputs | eligible |
| neither | skipped |

Structured-output requests bypass the response cache because schema fields are part of the contract and should not be coalesced with ordinary text requests or other schemas. Confirm repeated structured-output smokes reach the upstream each time and that request logs record `cache=bypass`.

### Rollback

If validation fails, remove `structured_outputs` or `json_schema` from the affected target metadata. If the target is already active, remove it from the affected model groups, restart/reload using the normal deployment process, and rerun the negative router smoke to confirm structured-output traffic no longer reaches that upstream. Do not leave a target in active routing with stale structured-output metadata.

## Image Smokes

Use the receipt image when validating generic VLM/OCR transport:

```text
https://cdn.learnopencv.com/wp-content/uploads/2018/06/04100007/receipt.png
```

Check separately:

- transport accepted the image;
- output is nonempty;
- upstream reports image/token usage when available;
- OCR/analysis answer is correct when the route is intended for OCR accuracy.

## CLI Smokes

Claude Code should use router bearer token settings. Run tool-bearing CLI smokes inside a disposable container or equivalent sandbox with only the scratch workdir mounted and only the scoped router token in the environment:

```bash
mkdir -p "$PWD/claude-tool-smoke"
docker run --rm --network host --cap-drop ALL --security-opt no-new-privileges \
  --cpus 1 --memory 1g --pids-limit 256 --read-only \
  --tmpfs /tmp:rw,nosuid,nodev,size=256m \
  --mount type=bind,source="$PWD/claude-tool-smoke",target=/workspace \
  -e "ANTHROPIC_BASE_URL=$ROUTER_BASE_URL/anthropic" \
  -e "ANTHROPIC_AUTH_TOKEN=$ROUTER_TOKEN" \
  -w /workspace "$TOOL_SMOKE_IMAGE" \
  claude -p "Create claude_tool_smoke.txt containing exactly claude-tool-ok, run cat claude_tool_smoke.txt, then finish with claude-tool-ok." \
    --model "<tool-smoke-model-group>" \
    --permission-mode bypassPermissions \
    --allowedTools "Write,Bash"
```

Codex CLI should use an OpenAI-compatible provider with `wire_api="responses"` and a router-issued token.

## Acceptance Rule

Do not activate a provider/model in broad routing until the relevant direct provider smokes, router smokes, docs, config, and production checks have all passed.
