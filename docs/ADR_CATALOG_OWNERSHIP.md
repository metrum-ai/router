# ADR: Catalog ownership — private targets only vs signed catalog bundle

- **Status:** Proposed
- **Owner:** Chetan Gadgil
- **Decision due:** 2026-09-19
- **Scope:** Whether Metrum AI Router appliances carry a Metrum-hosted provider
  catalog, and what ongoing obligations that choice creates.

## Context

The router already treats the provider catalog as deployment-owned YAML inventory
and model-group `targets[]` as the only activation and weight authority. There is
no `CatalogBundle` type, no catalog signing, and no catalog verification path in
`internal/` today. Licence signing exists in the customer-lifecycle / router-license
path and is being removed wholesale by the W1 Phase B packaging change.

Signed catalog bundles and private-targets-only are different products with
different permanent obligations. This ADR forces the product decision before
either option is implemented. **Neither option is implemented in the same change
as this ADR.**

This decision is distinct from:

- **Fleet runtime bundles** (`config.yaml` + `env.json` in SM/SSM) — instance
  config packaging, not a provider catalog.
- **LRP signed bundles** — optional Ed25519 manifests for learned-routing model
  artifacts under `$LRP_DATA_DIR`, not the OpenAI-compatible provider catalog.
- **License Ed25519 signing** — runtime entitlement envelopes (being deleted in
  W1 Phase B).

## Decision options

### Option A — Private targets only

**Meaning.** The appliance ships with deployment-owned targets (and optional
local catalog rows the operator authors). Metrum does not publish or maintain a
hosted provider catalog for the appliance.

**Commitments.**

- Scoping decision available immediately; no new signing or publishing pipeline.
- Support obligation for catalog drift is closed by removing the hosted catalog,
  not by policing it.
- Operators own catalog accuracy, pricing metadata, and target eligibility for
  their deployment.
- Public docs and reports continue to avoid leaking private production target
  names.

**Trade-offs.** Buyers who expected a Metrum-curated offline catalog must author
or import their own inventory. Marketing and onboarding copy must not imply a
Metrum-published catalog.

### Option B — Signed catalog bundle

**Meaning.** Metrum publishes a signed catalog artifact that the appliance
verifies and loads offline (air-gapped friendly).

**Commitments.**

- Roughly a quarter of engineering to design the bundle format, trust roots,
  loader, failure modes, and operator docs.
- A permanent publishing cadence (who signs, how often, where artifacts live).
- A revocation and supersession story for bad or stale catalogs.
- Ongoing support for operators when verification fails or catalogs drift from
  live provider SKUs.

**Trade-offs.** Higher product surface and permanent ops cost. Must not reuse the
deleted licence-signing toolchain without an explicit rebuild (see below).

## W1 interaction (required flag)

W1 Phase B deletes licence issuance and verification (`metrum-ai-router-license`,
embedded licence keys, customer-lifecycle licence publish). **If Option B is
chosen after that deletion, catalog signing and verification must be rebuilt as
an independent trust path** — new key hierarchy, new artifact format, new
loader. That rebuild cost is a real consequence of W1 and must be visible to the
decision-maker before W1 Phase B merges. Do not assume leftover licence crypto
will be available to bootstrap Option B.

## Decision

_Pending owner decision on or before 2026-09-19._

When accepted, replace this section with the chosen option, the decision date,
and the first follow-up issue IDs (implementation stays out of this ADR PR).

## Consequences (once decided)

- Update `docs/DOCS_MAINTENANCE.md` source-of-truth map and public
  providers/models docs to state the chosen obligation clearly.
- Cross-link from customer lifecycle / Fleet docs so operators do not confuse
  catalog bundles with runtime or LRP bundles.
- If Option A: scrub any remaining copy that implies a Metrum-hosted appliance
  catalog.
- If Option B: open implementation issues for format, trust store, publish
  cadence, and revocation; explicitly schedule the post-W1 signing rebuild.

## Out of scope

- Implementing Option A or Option B in this change.
- Payment / commerce entitlement (#921).
- Config control-plane DB projection (#7).
- LRP bundle signing (`docs/LRP_SIGNED_BUNDLES.md`).
