# TypeScript Request-Shape Routing Policy Demo

This deployment-owned TypeScript example chooses among eligible targets in one
requested model group using safe request-shape signals already present in the
script context:

- prompt size (`ctx.text` / `ctx.context.textChars`)
- tools (`ctx.context.hasTools` / `toolCount`)
- images (`ctx.context.imageCount`)
- structured outputs (`ctx.context.hasStructuredOutput`)
- explicit reasoning or thinking (`ctx.context.reasoning.requested`)

It is **not** live observed-latency routing, Chat-to-Responses stateful-session
storage, or a built-in router strategy. For in-process observed performance
scoring and built-in caller/prefix pinning, use `strategy: dynamic_score` with
`dynamic_score.affinity`. For arbitrary online policy with service-owned
observations and pins, use a trusted `strategy: external` service.

Safe class labels look like:

- `request-shape:chat`
- `request-shape:long-context`
- `request-shape:tools`
- `request-shape:image`
- `request-shape:reasoning`
- `request-shape:structured`

## Example Config

```yaml
models:
  script-request-shape:
    strategy: script
    script: examples/typescript-request-shape/router.ts
    targets:
      - { provider: hosted_openai_compatible, model_ref: compact, tier: cheap, weight: 70 }
      - { provider: hosted_openai_compatible, model_ref: long-context, tier: heavy, weight: 20 }
      - { provider: hosted_openai_compatible, model_ref: tool-model, tier: tool, weight: 10 }
```

Callers still request the deployment-defined group name from `/v1/models`.

Package `router.ts` with the rest of the deployment-owned config scripts. The
router bundles the TypeScript file at startup.
