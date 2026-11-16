# Marketing copy handoff for www.metrum.ai

Source-only handoff. Do not package this file. The marketing site is a separate
Next.js property and is not edited from this repository.

## Exact replacement strings for `/router`

Replace the current meta strings that lead with "Apache-2.0 open-source AI
gateway" with:

| Field | Replacement |
|---|---|
| `meta-description` | `Apache-2.0 open-source LLM smart router. Selects a different upstream model per request from a deployment-owned policy, then records why. Gateway functions underneath routing keep keys, quotas, and cost evidence enforceable.` |
| `og:description` | `Apache-2.0 open-source LLM smart router. Selects a different upstream model per request from a deployment-owned policy, then records why.` |
| `twitter:description` | `Apache-2.0 open-source LLM smart router. Selects a different upstream model per request from a deployment-owned policy, then records why.` |

Canonical product noun: **Metrum AI Router**.

## Capability comparison matrix accessibility

The `/router` capability comparison matrix currently renders checkmarks as icons
only. Text-based fetches therefore extract six rows of empty cells. Add
visually-hidden text per cell (`Full` / `Partial` / `None`) and a `<caption>`
that names the comparison so crawlers and assistive tech can recover the matrix.

## Apex `/llms.txt`

Add `https://www.metrum.ai/llms.txt` with the same content as the docs-site file
served at `/docs/llms.txt` (source: `docs-site/static/llms.txt`). Keep category
language as "LLM router" and keep gateway functions subordinate to routing.

## Feature-card copy

Current feature-card language that stops at "external-policy routing" should be
extended to name learned routing. Proposed string:

> Policy-owned model groups with dynamic score, TypeScript, external-policy, and
> Learned Routing Policy selection. The router picks an eligible upstream per
> request, records the decision as evidence, and keeps gateway controls
> (quotas, keys, budgets, telemetry) enforceable underneath routing.
