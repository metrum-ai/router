---
title: Serve and promote Learned Routing Policy
doc_type: howto
---

# Serve and Promote Learned Routing Policy

`lrp serve` loads a versioned bundle and answers `strategy: external` on a
loopback URL in the router's network namespace. The router still enforces
caller access, PII, budgets, and request-shape eligibility before asking LRP.

## Router modes

`external_policy.mode` values:

- `baseline`: no policy call. First eligible configured target is served.
- `shadow`: LRP is called and the recommendation is recorded. First eligible
  configured target is still served.
- `enforce`: the LRP recommendation is served. Omitted `mode` defaults to
  `enforce`.

Rollback of learned influence is `mode: baseline` or restoring the previous
group config. That does not restore a former weighted mix by itself.

## Shadow first

```yaml
models:
  workload-staging:
    strategy: external
    external_policy:
      url: http://127.0.0.1:18093/route
      allow_hosts: [127.0.0.1]
      include_request: true
      mode: shadow
      timeout_ms: 750
      on_error: fail_closed
      feedback:
        enabled: true
      headers:
        X-LRP-Auth: ${LRP_POLICY_AUTH_HEADER}
```

`include_request: true` is trusted-infrastructure only. Configure group
`pii_filter` first when prompts must not leave the router unredacted.

Feedback posts status, usage, cost, latency, TTFB, and selected target. It does
not retrain quality. Quality labels arrive from offline verifiers or judges.

Synthetic demonstrations prove wiring. They do not authorize live target
promotion. A public seed reference bundle is also an example for specific
targets and data. First-run success is not automatic production readiness.
Operator promotion gates and rollback:
[LEARNED_ROUTING_POLICY.md](https://github.com/metrum-ai/router/blob/main/docs/LEARNED_ROUTING_POLICY.md#validation-promotion-and-rollback).
Signed load: [Operator-signed LRP bundles](lrp-signed-bundles.md).
External-policy contract: [External Routing Policy](../configuration/external-routing-policy.md).
