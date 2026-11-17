---
title: CPU tier for compact requests
doc_type: howto
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# CPU tier for compact requests

The hierarchy reference includes an optional `agent-small` group that can route
compact requests to a CPU-hosted vLLM model. It uses dynamic scoring, a bounded
request shape, and a frontier fallback. It is reference configuration; validate
quality and saturation on your own CPU hardware before granting callers access.

```yaml
models:
  agent-small:
    strategy: dynamic_score
    targets:
      - provider: cpu_small
        model_ref: ${CPU_MODEL}
        weight: 10
        timeout_ms: 15000
        tier: cpu
        tags: [cpu, small-model]
        request_shape_support:
          max_estimated_input_tokens: 800
          max_requested_output_tokens: 128
          validation_status: limited
      - provider: frontier
        model_ref: ${FRONTIER_MODEL}
        weight: 1
        tier: frontier
        tags: [frontier]
```

The CPU target is eligible only for requests with at most 800 estimated input
tokens and at most 128 requested output tokens. Other request shapes use the
frontier target. CPU errors and timeouts retry the frontier before response
streaming begins. The attempt timeout is 15 seconds; dynamic routing also uses
its configured latency, reliability, complexity, and cost signals.

Configure the CPU provider endpoint, model, image, and resources in the
[hierarchy reference](https://github.com/metrum-ai/router/tree/main/deploy/reference/hierarchy).
Reference callers may request `agent-small` explicitly. Existing groups and
caller behavior do not change unless this group is added and allowed.
