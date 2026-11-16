<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# External Request-Shape Routing Policy Demo

This standalone Python service chooses among eligible model-group targets using
safe request-shape context: prompt size, tools, images, structured output, and
reasoning. It never runs inside the router and does not log request content.

The service listens on `127.0.0.1:18093` by default. Configure the router with
`strategy: external` and point `external_policy.url` to its `/route` endpoint.
Allowlist `127.0.0.1` for a sidecar sharing the router network namespace.
External policy receives safe derived context by default; this example does not
need `external_policy.include_request: true`.

```yaml
models:
  request-shape:
    strategy: external
    external_policy:
      url: http://127.0.0.1:18093/route
      allow_hosts: [127.0.0.1]
      timeout_ms: 200
      on_error: fail_closed
    targets:
      - { provider: hosted_openai_compatible, model_ref: compact, tier: cheap, weight: 70 }
      - { provider: hosted_openai_compatible, model_ref: long-context, tier: heavy, weight: 20 }
      - { provider: hosted_openai_compatible, model_ref: tool-model, tier: tool, weight: 10 }
```

Run with `python3 request_shape_policy.py --host 127.0.0.1 --port 18093`.
The service exposes `/healthz` for readiness and accepts policy requests at
`POST /route`.
