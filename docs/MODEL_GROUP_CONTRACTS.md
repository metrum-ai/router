# Model Group Contracts

Model group contracts are optional deployment config. They describe what a caller-facing group is intended to guarantee while preserving the existing rule that callers request one allowed model group and routing stays inside that group.

Public customer-facing strategy ownership guidance lives in `docs-site/docs/routing/customer-controlled-routing.md`. Keep this runbook aligned with that page when changing contract behavior, proof requirements, or examples.

## Policy Design Checklist

- Define the workload and owner.
- Choose whether this belongs in one stable model group, a staging/smoke group, multiple groups, or a separate router instance.
- Choose the strategy: `static`, `failover`, `weighted`, `dynamic_score`, `script`, `external`, or a contract-backed combination.
- Define eligible providers and models under `models.<group>.targets[]`; do not treat provider catalog entries as active routes.
- Document required API shapes, tool modes, modalities, reasoning controls, structured-output support, and max-token cap behavior.
- Document request-shape boundaries such as expected request bytes, tool count, serialized tool-schema bytes, output-cap fields, streaming mode, bridge direction, and large-agent payload coverage.
- Define quality, cost, latency, throughput, error-rate, timeout, and fallback targets.
- Run direct upstream smokes for every provider/model/dialect/skin being claimed.
- Run router-level smokes through each caller API shape and negative no-eligible-target path.
- Run representative evaluation or proof for the workload.
- Keep new provider/model/API-skin candidates in a restricted smoke or staging group until all required direct, router, client, and workload gates pass for the exact request shapes the stable group will receive.
- Define rollback: remove the target from affected groups, remove or tighten the capability metadata that made it eligible, isolate it behind a restricted smoke group, relax a contract only when the contract is too strict, switch strategy, or restore the previous config.

## Config Fields

Contracts live at `models.<group>.contract`:

- `display_name` and `caller_visible_notes`: safe descriptive text.
- `intended_workloads`: safe workload labels such as `support_chat`, `agent_coding`, `receipt_ocr`, or `private_gpu`.
- `supported_api_shapes`: `openai_chat`, `openai_responses`, or `anthropic_messages`.
- `required_capabilities`: hard requirements for tools, forced tool choice, structured outputs, reasoning or thinking support, input/output modalities, context tokens, and max-token cap safety.
- `quality_floor`: required target tags, validation status, quality score, pass rate, and optional validation age.
- `operational_targets`: error, timeout, p95 latency, and output-throughput thresholds when observations exist.
- `reporting`: enables safe workload and quality-floor buckets in usage/reporting surfaces.

Targets may include:

```yaml
region: deployment-region-a
validation:
  status: passed
  workload: support_chat
  validated_at: "2026-06-25"
  quality_score: 0.94
  pass_rate: 0.98
  harness: golden-support-set
  notes: safe non-sensitive note
```

Do not put prompts, images, tool outputs, bearer tokens, token hashes, provider keys, private headers, or full config snippets in validation notes.

`region` is optional deployment-declared metadata. When present, it is 1–64
characters, starts with an ASCII letter or digit, and then permits letters,
digits, `.`, `_`, `:`, `/`, and `-`. It is recorded for the target that actually served the request,
including a successful fallback, and appears in safe selected-target decision
telemetry. It is not inferred from the provider URL and does not enforce
residency, retention, jurisdiction, transfer, or training policy. If a workload
requires a processing location, operators must validate the provider and
infrastructure controls and encode that requirement in reviewed group
eligibility or policy; ordinary callers see allowed model groups through
`/v1/models`, not the per-target inventory. `region` is not passed to
TypeScript or external-policy inputs and does not itself filter candidates.
Use separate caller-authorized groups or another explicitly policy-visible,
validated classification when routing must enforce location.

Use `required_capabilities.reasoning` when the group itself promises reasoning or thinking support for every compatible request. Use per-target `reasoning` metadata without a contract requirement when a mixed weighted group should keep ordinary traffic on all ordinary eligible targets but restrict explicit OpenAI Chat `reasoning_effort`, OpenAI Responses `reasoning`, or Anthropic Messages `thinking` requests to validated reasoning targets. In both cases, target metadata must come from direct upstream and router-level smokes for the exact dialect and skin.

## Enforcement Order

1. Authenticate the caller.
2. Check the requested group against the caller allow list.
3. Load only the requested group.
4. Apply normal request eligibility for dialect, tools, modalities, structured outputs, reasoning or thinking requests, and explicit output caps.
5. Apply the group contract and target validation floor.
6. Run the configured strategy on the remaining targets.

`static`, `weighted`, `failover`, `dynamic_score`, `script`, and `external` all receive the same contract-filtered target list. TypeScript and external policies receive safe contract and target validation metadata, but their decisions are still validated against eligible targets.

## Validation

Startup fails for unsupported API shapes, invalid modalities, negative thresholds, out-of-range scores/pass rates, invalid validation dates, impossible validation statuses, required tags no target has, declared API shapes no target serves, or contracts no target can satisfy.

Use `go test ./cmd/... ./internal/...` after contract edits. For production-bound changes, validate the sample config, the local production snapshot, and the live production config with structured YAML parsing before restart.

## Rollout And Rollback

Roll out on a deployment-defined smoke or staging group first. Add at least two interchangeable validated targets when the stable group contract expects redundancy, then run representative text, tool, image, structured-output, reasoning or thinking, and low-token-cap smokes according to the declared contract. For coding-agent groups, include large OpenAI Chat tool payloads, OpenAI Responses function-tool flows, Anthropic Messages client-tool flows, tool-choice modes, streaming where supported, and any configured bridge direction. Confirm in usage reports that selected targets stay inside the requested group and that no-eligible failures use safe buckets such as `contract-quality-floor`, `contract-validation-expired`, `request-shape-max-request-bytes`, or `request-shape-tool-schema-bytes`.

Promotion from staging into a stable group is a separate decision. Promote only the provider/model/dialect/API skin that passed the exact request shapes expected for that group. If a target passes ordinary text or small tools but fails large coding-agent shapes, keep it in the stable group only behind accurate `request_shape_support` limits or leave it in staging until the large-shape gate passes.

Rollback is config-only: remove the `contract` block, relax a specific `quality_floor` or `operational_targets` field, remove stale validation age checks, lower or remove the affected target weight, move the candidate back to staging, tighten request-shape metadata, or restore the previous strategy and weights. Restart/reload with the normal deployment process and rerun the smoke that failed.

## Reporting

Usage rows store scalar contract fields only:

- `contract_present`
- `contract_bucket`
- `contract_failure_reason`
- `contract_workload`
- `target_validation_status`
- `target_validation_workload`
- `target_validation_age_bucket`

Browser admin reports expose contract buckets, contract workloads, and target validation buckets through scalar report APIs. Public docs and customer-facing examples must stay deployment-defined and must not expose private target names from a managed production deployment.
