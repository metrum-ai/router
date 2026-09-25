# External Routing Policy Service Demo

This demo service implements prompt-size routing for Metrum AI Router's `strategy: external`.

Run it locally:

```bash
python3 examples/external-routing-policy/prompt_size_policy.py
```

Configure a model group with:

```yaml
strategy: external
external_policy:
  url: http://127.0.0.1:18090/route
  mode: shadow
  allow_hosts: [127.0.0.1]
  timeout_ms: 500
  max_response_bytes: 65536
  on_error: fail_closed
```

Start with `shadow`, which records a valid recommendation while serving normal
eligible order. Promote explicitly to `enforce`; use `baseline` to skip policy
calls during rollback. Omitting `mode` defaults to `enforce` for compatibility.

The service receives derived routing context, eligible-target inventory,
pseudonymous caller identity, pricing, capability data, key IDs and API-key
environment-variable names—but never provider API key values, raw router
tokens, or token hashes. Treat it as trusted infrastructure.

## Adaptive Signal Policy Reference

`adaptive_signal_policy.py` is a deployment-owned external policy that
demonstrates observed-signal scoring, short-lived conversation pins, cache-hit
exclusion, and serving-target fallback attribution. It is **not** built-in
router state and is **not** online learning from the usage database. Its pins
belong to this example policy service and are separate from the router's
`dynamic_score.affinity` store.

Run the synthetic wiring harness:

```bash
python3 scripts/run_adaptive_signal_policy_demo.py
# or:
make adaptive-signal-policy-demo
```

Configure a trusted local group with `include_request: true` only when the
policy service is allowed to inspect request content:

```yaml
models:
  adaptive-signal-demo:
    strategy: external
    external_policy:
      url: http://127.0.0.1:18092/route
      mode: enforce
      allow_hosts: [127.0.0.1]
      timeout_ms: 300
      max_response_bytes: 65536
      include_request: true
      on_error: fail_closed
    targets:
      - { provider: baseten, model_ref: gpt-oss-120b, tier: cheap, weight: 70 }
      - { provider: minimax, model_ref: m3, tier: heavy, weight: 30 }
```

Optional feedback: point `--tail-log` at the router's JSONL request log so
`/observe` can book outcomes. Prefer diagnostics-enabled logs so
`attempts_detail[].selected` identifies the serving target. Current router
releases also rewrite terminal `target_*` and request-time prices to the
serving target after a successful fallback.

## Outcome-Calibrated Reference

`outcome_calibrated_policy.py` is a separate reference implementation for
workloads whose required quality differs by task. It uses explicit classes and
representative prompts, human-reviewed outcomes, and an OpenAI-compatible
embedding endpoint to select the least-cost target that meets each class's
quality gate. It requires trusted request content:

```yaml
strategy: external
external_policy:
  url: http://127.0.0.1:18091/route
  allow_hosts: [127.0.0.1]
  include_request: true
  on_error: fail_closed
```

Only enable `include_request` when the policy service is trusted to receive
request content. A model group with `pii_filter` supplies redacted content.

The committed synthetic dataset has arithmetic, runnable HTTP benchmark-code,
and folder-listing workloads. Generate an operator-reviewable profile and
weight patch from reviewed JSONL:

```bash
python3 outcome_calibrated_policy.py calibrate \
  --dataset outcome_dataset.json \
  --reviews reviewed_outcomes.jsonl \
  --out-profile /tmp/outcome-policy-profile.json \
  --out-yaml /tmp/outcome-policy-weights.yaml

python3 outcome_calibrated_policy.py serve \
  --profile /tmp/outcome-policy-profile.json \
  --embedding-base-url https://api.openai.com/v1 \
  --embedding-model text-embedding-3-small
```

Set `OUTCOME_POLICY_EMBEDDING_API_KEY` only in the policy service's secret
store. The service makes real `POST /v1/embeddings` requests; it does not use
local or synthetic embeddings in this deployment mode.

To collect fresh candidate responses through the router, first create a
temporary calibration dispatch plan, run the service with it, and then collect
the review rows. The plan is only for exercising every candidate; do not deploy
it as a live profile.

```bash
python3 outcome_calibrated_policy.py plan \
  --dataset outcome_dataset.json \
  --out-overrides /tmp/calibration-overrides.json

python3 outcome_calibrated_policy.py serve \
  --profile /tmp/outcome-policy-profile.json \
  --calibration-overrides /tmp/calibration-overrides.json \
  --embedding-base-url https://embeddings.example/v1 \
  --embedding-model deployment-owned-embedding-model

ROUTER_TOKEN=deployment-owned-token \
python3 outcome_calibrated_policy.py collect \
  --dataset outcome_dataset.json \
  --router-base-url https://router.example.com \
  --model-group outcome-policy-demo \
  --out-reviews /tmp/review-draft.jsonl
```

The generated YAML is not applied automatically. Review the evidence, deploy
the profile through normal configuration change control, and validate the
router-level behavior before promotion. The real calibration workflow writes
one JSONL row per case/candidate and requires a reviewer to set `passed` to
`true` or `false`; the reference defaults to ten reviewed examples per
class/candidate, while the committed synthetic fixture lowers that threshold
only to demonstrate the workflow. Keep response-bearing review JSONL in the
same protected deployment boundary as the trusted policy service; do not use
captured production prompts or responses in the committed example.

## Synthetic Integration Test

Run the end-to-end synthetic coding demonstration with one command:

```bash
make outcome-calibrated-demo
```

It calibrates simple, medium, and difficult coding classes from the committed
synthetic review fixture; starts a fake OpenAI-compatible embedding endpoint
and the actual Python policy service; sends four new requests through the real
router test handler; and writes `profile.json`, `target-weights.yaml`,
`evidence.json`, `evidence.md`, and the router test log under the ignored
`tmp/outcome-calibrated-demo/` directory. The report shows the request and
selected candidate for simple, medium, difficult, and unclassified work.
This is a reproducible integration test, not provider-backed quality evidence.

## Provider-Backed Demonstration

Use the live runner only after the policy service has a promoted profile, an
authenticated `/audit` endpoint, and a dedicated non-production model group.
Require `X-Outcome-Policy-Request` on `/route` from a deployment secret. The
`/audit` endpoint requires `X-Outcome-Policy-Audit`; keep it private or access
it through an authenticated port-forward.

```bash
ROUTER_TOKEN=deployment-owned-token \
OUTCOME_POLICY_AUDIT_TOKEN=deployment-owned-audit-token \
python3 scripts/run_outcome_calibrated_live_demo.py \
  --router-base-url https://router.example.com \
  --model-group outcome-policy-demo \
  --dataset /protected/outcome-coding-dataset.json \
  --policy-audit-url http://127.0.0.1:18091 \
  --run-id 20260715-validation-01 \
  --out /protected/outcome-routing-evidence.json
```

`--run-id` adds a harmless system marker so an existing response-cache entry
cannot bypass a new policy evaluation. The evidence includes request IDs,
selected eligible targets, usage, responses, and verifier results; retain it in
the protected deployment boundary. Providers, model IDs, costs, thresholds,
and datasets remain deployment-defined.

This demo uses loopback HTTP, which the router treats as a trusted-local development exception. Non-local external policy services should use HTTPS, or set `external_policy.allow_http: true` only after deployment security review. Redirects are rechecked against the exact `allow_hosts` list before the router follows them.

## OpenJev Task-Class Routing

`openjev_policy.py` asks an OpenJev System One endpoint which task class a
request is (`simple` / `medium` / `advanced`), then maps that onto cheap /
medium / advanced OpenAI targets (default live-key ladder: `gpt-5.6-luna`,
`gpt-5.6-sol`, `gpt-6-astra`). OpenJev weights are **CC BY-NC 4.0**.

```bash
# Synthetic wiring (fake OpenJev, no GPU):
make openjev-routing-demo

# Policy against a real OpenJev shim (SSH tunnel to Shadeform):
OPENJEV_URL=http://127.0.0.1:3000 python3 examples/external-routing-policy/openjev_policy.py
```

Example router config: `config.openjev.example.yaml`. Full Shadeform runbook:
[`docs/OPENJEV_ROUTING_DEMO.md`](../../docs/OPENJEV_ROUTING_DEMO.md). Sanitized
evidence: [`docs/evidence/openjev-routing/`](../../docs/evidence/openjev-routing/).
