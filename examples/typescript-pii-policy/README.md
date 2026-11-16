# TypeScript PII-Aware Routing Policy Demo

This demo shows a deployment-owned TypeScript routing policy that sends prompts with likely PII to targets marked for sensitive or private traffic, while ordinary prompts use the normal target.

The script exports `route(ctx)` and uses only `ctx.text` plus configured target metadata. It returns safe class labels:

- `pii-detected:sensitive-route`
- `pii-detected:none`

The script never logs, prints, or returns the matched text. It detects common PII-like shapes with regexes, including email addresses, phone numbers, SSNs, payment-card-like numbers, street addresses, likely full names, dates of birth, medical record identifiers, bank account or routing numbers, and passport or license identifiers.

This is routing policy only. TypeScript routing scripts do not redact outbound request content. Because the original request content still reaches the selected upstream, the demo restricts PII-detected primary and fallback targets to sensitive/private targets only and fails closed with `pii-detected:no-sensitive-target` when no approved sensitive/private target is eligible. Use model-group `pii_filter` for router-managed redaction or fail-on-match behavior.

## Example Config

```yaml
models:
  pii-aware:
    strategy: script
    script: scripts/pii-policy/router.ts
    targets:
      - provider: public-provider
        model_ref: normal
        tier: normal
        weight: 90
      - provider: private-provider
        model_ref: private
        tier: private
        display_name: Private sensitive target
        weight: 10
```

Callers still request the deployment-defined group name:

```bash
curl "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "pii-aware",
    "messages": [{"role": "user", "content": "Summarize this support ticket."}]
  }'
```

Package `router.ts` with the rest of the deployment-owned config scripts. The router bundles the TypeScript file at startup.
