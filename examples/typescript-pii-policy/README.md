<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# External PII-Aware Routing Policy Demo

This standalone Python service detects common PII-like patterns and routes
matching requests only to eligible targets marked sensitive or private. It
returns safe class labels and does not log or return matched text. If PII is
detected but no sensitive target is eligible, it returns an error and the
router fails closed under the example configuration.

This policy needs request text, so the model group must explicitly set
`external_policy.include_request: true`. Run the policy only as a trusted
service authorized to receive that content. Routing does not redact outbound
requests; use the router's `pii_filter` for redaction or fail-on-match behavior.

The policy scans at most 8,192 text characters: for longer prompts it scans the
first and last 4,096 characters. The HTTP endpoint rejects bodies over 1 MiB.
PII located only in the unscanned middle of a longer prompt will not affect
routing.

```yaml
models:
  pii-aware:
    strategy: external
    external_policy:
      url: http://127.0.0.1:18094/route
      allow_hosts: [127.0.0.1]
      timeout_ms: 200
      on_error: fail_closed
      include_request: true
    targets:
      - { provider: public-provider, model_ref: normal, tier: normal, weight: 90 }
      - { provider: private-provider, model_ref: private, tier: private, weight: 10 }
```

Run with `python3 pii_policy.py --host 127.0.0.1 --port 18094`. The service
exposes `/healthz` and accepts policy requests at `POST /route`.
