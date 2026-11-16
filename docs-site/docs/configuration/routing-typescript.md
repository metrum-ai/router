---
title: TypeScript Routing Policy
doc_type: howto
---

# TypeScript Routing Policy

Model groups can delegate target selection to a TypeScript routing script. Callers still request a deployment-defined router model group; the router runs the script server-side and forwards the request to one configured backing target. Treat TypeScript routing scripts as operator-trusted deployment code: they run inside the router process and receive request text (`ctx.text`) plus normalized request fields (and `ctx.request.raw` when present). If the group enables `pii_filter`, that context is built from the redacted request. Raw caller tokens, token hashes, and raw provider API keys are not passed to scripts. Names such as `fast` or `big-coder` are examples from a reference deployment, not product-required names.

Use TypeScript for compact local policy that should run inside the router process. Use an [External Routing Policy Service](./external-routing-policy) when the policy should be developed, tested, deployed, logged, and debugged as its own web service.

For the canonical strategy comparison, start with [Routing Strategy Decision Tree](../routing/strategy-decision-tree). For the broader routing-policy ownership model, see [Customer-Controlled Routing](../routing/customer-controlled-routing).

## Admin Setup

Configure a model group with `strategy: script`, point `script` at a TypeScript file relative to the config file, and list the targets the script may choose from:

```yaml
models:
  adaptive:
    strategy: script
    script: scripts/router.ts
    # Each admitted decision gets a fresh isolated VM. Zero/unset defaults to 16.
    script_max_concurrent: 16
    targets:
      - { provider: baseten, model_ref: gpt-oss-120b, tier: cheap, weight: 70 }
      - { provider: minimax, model_ref: m3, tier: heavy, weight: 30 }
```

If the script must call an external policy service, the deployment must explicitly enable it and allow the service host:

```yaml
models:
  adaptive:
    strategy: script
    script: scripts/router.ts
    script_http:
      enabled: true
      allow_hosts: [routing-policy.internal.example]
      timeout_ms: 200
      max_response_bytes: 65536
      headers:
        Authorization: ${ROUTING_POLICY_AUTH_HEADER}
    targets:
      - { provider: baseten, model_ref: gpt-oss-120b, tier: cheap, weight: 70 }
      - { provider: minimax, model_ref: m3, tier: heavy, weight: 30 }
```

Callers continue to use the group name:

```bash
curl "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "adaptive",
    "messages": [{"role": "user", "content": "Summarize this note in one sentence."}]
  }'
```

## Script Context

The script must export `route(ctx)` and return either a configured `targetIndex` or a target selector such as `{ provider, model }`.

```typescript
type RouteContext = {
  group: string;
  text: string;
  request: {
    model: string;
    max_tokens?: number;
    stream?: boolean;
  };
  caller?: {
    id: string;
    user: string;
    project: string;
    environment: string;
    tokenId: string;
  };
  targets: Array<{
    provider: string;
    model: string;
    dialect: string;
    weight: number;
    keyId?: string;
    apiKeyEnv?: string;
    keyConfigured: boolean;
    honorsMaxTokens?: boolean;
  }>;
};
```

`ctx.text` is the normalized request text from chat messages, Responses input, or Anthropic messages. Use it for content and size rules. If the model group enables `pii_filter`, `ctx.text`, normalized request fields, and `ctx.request.raw` are already redacted before the script runs. Placeholder mappings remain request-local and are not exposed to scripts. When the group has a model-group contract, `ctx.contract` contains safe contract metadata and `ctx.targets[]` has already been filtered by the contract. Target validation metadata is available under `ctx.targets[].validation`. Returned targets are validated against the eligible target list before use.

## Imports And Dependencies

Relative TypeScript imports are bundled when the router loads the script, so helpers can live next to the route file:

```typescript
import { scorePrompt } from "./policy";

export function route(ctx: RouteContext) {
  const score = scorePrompt(ctx.text);
  return { targetIndex: score > 10 ? 1 : 0, classLabel: "local-policy" };
}
```

For deployment-owned helpers, keep files under the packaged script directory, for example:

```text
config/scripts/router.ts
config/scripts/policy.ts
config/scripts/scoring.ts
```

For third-party packages, install and lock dependencies before packaging, then deploy the resolved package files with the script directory. The router bundles from the deployment filesystem at startup; it does not run `npm install`, download packages, or resolve network dependencies at runtime.

Recommended admin workflow:

```bash
cd config/scripts
npm init -y
npm install some-policy-lib
npm install --save-dev typescript
```

Package the files required by the deployment, including `package.json`, lockfile, local helper files, and the resolved dependency tree or a pre-bundled script bundle according to your release process. Keep this directory free of provider keys, router tokens, and private host credentials. If a dependency is large or has native modules, prefer pre-bundling the routing script during release and deploying the generated JavaScript/TypeScript entrypoint plus any review assets required by your change-control process.

## Request-Shape Routing Example

The checked-in example at `examples/typescript-request-shape/router.ts` keeps
short chat prompts on a cheaper tier and routes long-context, tool, image,
structured-output, and reasoning requests to matching target tiers. It falls
back to the first configured eligible target if a preferred tier is not
available. Packaged `scripts/router.ts` is a separate caller-regex weighted
sample and does not implement this request-shape policy.

```typescript
type Target = {
  provider: string;
  model: string;
  tier?: string;
  weight: number;
  keyConfigured: boolean;
};

type RouteContext = {
  text: string;
  targets: Target[];
  context?: {
    textChars?: number;
    toolCount?: number;
    imageCount?: number;
    hasStructuredOutput?: boolean;
    reasoning?: { requested?: boolean };
  };
};

export function route(ctx: RouteContext) {
  const eligible = ctx.targets
    .map((target, index) => ({ target, index }))
    .filter((entry) => entry.target.keyConfigured && entry.target.weight > 0);

  if (eligible.length === 0) {
    return { targetIndex: 0, classLabel: "request-shape:no-eligible-targets" };
  }

  const preferredTiers = (ctx.text || "").length > 8000 ? ["heavy"] : ["cheap"];
  const preferred =
    eligible.find((entry) => preferredTiers.includes(entry.target.tier || "")) ||
    eligible[0];

  return {
    targetIndex: preferred.index,
    fallbackIndexes: eligible
      .filter((entry) => entry.index !== preferred.index)
      .map((entry) => entry.index),
    classLabel: `request-shape:${preferred.target.tier || "default"}`,
  };
}
```

When a script omits `fallbackIndexes` and `fallbacks`, the router uses the remaining eligible targets as the retry order. When either fallback field is present, the supplied entries are the complete retry set. Return an empty fallback list for fail-closed routes that must not retry to another target.

## PII-Aware Routing Example

A deployment can use TypeScript policy to send likely sensitive prompts to private targets while leaving ordinary prompts on normal targets. The demo in `examples/typescript-pii-policy/router.ts` detects common PII-like shapes in `ctx.text`, chooses a target whose `tier`, `displayName`, or model name is marked `sensitive` or `private`, restricts fallback retries to sensitive/private targets, and returns only safe labels such as `pii-detected:sensitive-route` or `pii-detected:none`.

```yaml
models:
  pii-aware:
    strategy: script
    script: scripts/pii-policy/router.ts
    targets:
      - { provider: public-provider, model_ref: normal, tier: normal, weight: 90 }
      - { provider: private-provider, model_ref: private, tier: private, display_name: "Private sensitive target", weight: 10 }
```

This pattern is routing only. TypeScript scripts do not redact outbound request content; if the sensitive target is selected, the original request content is still forwarded to that upstream. The demo fails closed with `pii-detected:no-sensitive-target` when a PII-detected request has no eligible sensitive/private target, rather than retrying to a normal/public target. Use model-group `pii_filter` when the deployment requires router-managed redaction, restoration, or fail-on-match behavior.

## Weighted Selection With Content Rules

Scripts can combine prompt content, caller metadata, target metadata, and group weights:

```typescript
export function route(ctx: RouteContext) {
  let candidates = ctx.targets
    .map((target, index) => ({ target, index }))
    .filter((entry) => entry.target.keyConfigured && entry.target.weight > 0);

  if (/refactor|debug|test failure|segfault/i.test(ctx.text)) {
    candidates = candidates.filter((entry) =>
      /MiniMax-M3|kimi-k2\.7-code|gpt-oss-120b/i.test(entry.target.model)
    );
  }

  if (ctx.caller?.environment === "prod") {
    candidates = candidates.filter((entry) => entry.target.provider !== "experimental-provider");
  }

  const total = candidates.reduce((sum, entry) => sum + entry.target.weight, 0);
  let cursor = Math.random() * total;
  for (const entry of candidates) {
    cursor -= entry.target.weight;
    if (cursor <= 0) {
      return { targetIndex: entry.index, classLabel: "weighted-content-policy" };
    }
  }

  return { targetIndex: candidates[0]?.index || 0, classLabel: "weighted-content-policy" };
}
```

## External Policy Calls

Scripts run synchronously inside the router process after TypeScript
transpilation. Each decision uses a fresh Goja VM so mutable JavaScript globals
cannot cross request boundaries; VMs are not pooled. `script_max_concurrent`
caps simultaneously executing VMs per model group, defaults to `16` when unset
or zero, and accepts values through `256`. Requests waiting for a slot honor
caller cancellation.

The cap is admission control, not an execution deadline. After a VM starts,
pure JavaScript has no configured timeout or context preemption; an infinite
loop can hold its slot and request goroutine indefinitely. Keep policy bounded,
fast, deterministic, and free of unbounded loops. `router.fetchJSON` has its
own configured HTTP timeout, but that timeout does not bound unrelated
JavaScript execution. Roll back a stuck or slow policy by restoring the
previous reviewed config/script and restarting affected instances.

If a script returns `classLabel`, treat it as a telemetry token only: use at
most 64 characters from letters, numbers, `_`, `-`, `.`, and `:`. Do not echo
prompt text, secrets, HTML, or user input into `classLabel`; unsafe values are
stored as `unsafe_class_label`. External calls use the router-provided
`router.fetchJSON(url, options)` helper, not browser `fetch`, and only work
when `script_http.enabled` is true for that model group.

```typescript
export function route(ctx: RouteContext) {
  const response = router.fetchJSON("https://routing-policy.internal.example/route", {
    method: "POST",
    body: {
      group: ctx.group,
      text: ctx.text,
      targets: ctx.targets.map((target, index) => ({
        index,
        provider: target.provider,
        model: target.model,
        tier: target.tier,
        keyConfigured: target.keyConfigured,
      })),
    },
  });

  if (response.ok && typeof response.body?.targetIndex === "number") {
    return { targetIndex: response.body.targetIndex, classLabel: "external-policy" };
  }
  return { targetIndex: 0, classLabel: "external-policy:fallback" };
}
```

`router.fetchJSON` supports `GET` and `POST`, JSON request bodies, JSON responses, and script-supplied headers limited to `Accept`, `Content-Type`, and `X-*`. Hosts, timeout, response size, and deployment-owned headers are controlled by config. HTTPS is required by default; plaintext HTTP is accepted only for loopback hosts such as `localhost`, `127.0.0.1`, and `::1`, or when `script_http.allow_http: true` is set for a trusted non-local policy service. Non-local allowlisted hosts may use only the scheme default ports (`443` for HTTPS, `80` for HTTP); other ports are rejected. Redirects are followed only when every hop keeps an allowed `http`/`https` scheme, an exact allowlisted hostname, and an allowed port. Connect-time checks also reject destinations that resolve to private, link-local, or metadata addresses for non-local hosts. Use `script_http.headers` for policy-service authentication such as `Authorization: ${ROUTING_POLICY_AUTH_HEADER}`; config values are env-expanded when the router loads config and are not passed into the script context. `timeout_ms` may be at most `5000`, and smaller values are recommended because routing happens before the upstream model request. Raw provider keys, raw caller tokens, token hashes, unrestricted file access, and runtime package installation are not available to scripts.
