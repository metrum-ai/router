# ADR: Catalog bundle ownership

- **Status:** Proposed
- **Owner:** Chetan Gadgil
- **Decision due:** 2026-09-19

## Context and current state

Catalog bundles (a Metrum-published, signed manifest of supported model providers,
API surfaces, and routing-time features that the appliance verifies offline) do not
exist today in `internal/`. No `CatalogBundle` type, catalog signing key, catalog
public-key reference, or offline-verifier is present anywhere in the request path,
Fleet package, or customer-local CLI. A grep across `internal/` for catalog-bundle
shape, signature, or verifier surfaces returns no production source. Any
Metrum-hosted "model catalog" the appliance might consume is therefore currently a
hypothetical, not a code path.

This decision is purely about ownership and shape of such an artifact; it is not
about the Fleet runtime bundles (`aws-ssm:///` or `aws-secretsmanager:///`
`runtime_bundle_ref` references carrying `config.yaml` and `env.json` to a
specific deployment, governed by [ADR: Separate fleet lifecycle authority from
customer operations](ADR_FLEET_AND_CUSTOMER_CLI_BOUNDARIES.md)) and not about
LRP signed bundles (the usage-reporting path governed by docs such as
[LRP verifiers](LRP_VERIFIERS.md) and [Usage reporting playbook](USAGE_REPORTING_PLAYBOOK.md)).
Fleet runtime bundles are per-deployment, transport-only references resolved in
memory against a typed `config.yaml`/`env.json` schema; LRP signed bundles are
tenant-attested usage evidence delivered to the customer's own reporting
infrastructure. A catalog bundle, if adopted, is a third, distinct trust object:
a vendor-issued, cryptographically signed list of providers and capabilities the
appliance trusts before invoking a remote API. Mixing any two of these in code,
documentation, or naming is a defect.

This ADR also explicitly does not commit to either implementation. No
`CatalogBundle` type, signing verifier, host list, or revocation store is
introduced by this decision; both options below are described solely so the
owner can pick one before any code is written.

## Options

### Option 1: Private targets only — no hosted provider catalog

The appliance never consumes a Metrum-published provider catalog. It ships with
deployment-owned targets: each customer commits their own provider endpoints,
model identifiers, and routing features in a customer-local config that the
Fleet-protected `runtime_bundle_ref` resolves at deploy time (see [ADR: Separate
fleet lifecycle authority from customer operations](ADR_FLEET_AND_CUSTOMER_CLI_BOUNDARIES.md#runtime-bundle-boundary)).
There is no Metrum catalog update path, no catalog signature to verify, no
catalog revocation story, and no vendor-controlled "supported providers" list
that the appliance consults at request time.

Obligations:

- **Immediate availability.** No new artifact, key, or verifier exists; the
  current Fleet path is the entire answer. Available today, with no follow-up
  engineering beyond documenting that "supported providers" means the
  customer-committed targets in their `config.yaml`.
- **Closes the support-drift gap by removing it.** "Provider X is in our
  catalog" never becomes a procurement-side claim, because there is no catalog
  for procurement to read. Metrum-side support is governed entirely by what the
  customer committed in their own bundle, exactly as today.
- **Customer owns provider freshness.** New providers, model IDs, and API
  surfaces reach a customer only through their own config change, signed and
  validated as any other `runtime_bundle_ref` write (operator IAM write to
  Secrets Manager or signed customer intent). There is no Metrum "patch
  Tuesday" that can change routing behavior without a customer-visible diff.
- **Reviewer admits the cost.** The catalog team (and any roadmap that depends
  on Metrum being able to declare "we support provider X across the fleet")
  must explicitly accept that this ADR removes that lever as a possibility;
  the team's contribution is documentation, examples, and conformance tests
  in customer-owned config, not artifact publishing.
- **No key, signature, verifier, or revocation code is added.** A reviewer
  scanning `internal/` finds zero catalog-related types and confirms the
  Fleet/`runtime_bundle_ref` boundary in [ADR: Separate fleet lifecycle
  authority from customer operations](ADR_FLEET_AND_CUSTOMER_CLI_BOUNDARIES.md)
  remains the only deployment-shape authority.

### Option 2: Signed catalog bundle — Metrum publishes, appliance verifies offline

Metrum operates a publishing pipeline that emits a catalog bundle (JSON or CBOR,
fixed schema, versioned) containing the supported provider list, model
identifiers, capability flags, and any routing-time metadata the appliance may
consult. The bundle is signed by a Metrum-held catalog signing key, the
appliance embeds the corresponding public key at build time, and verification
happens offline (no online revocation check, no telemetry call) before any
catalog field is consumed.

Obligations:

- **~one quarter to first production bundle.** A signing key custody story, a
  canonical serialization format, a public-key embedding step in the build, an
  offline verifier (`internal/.../catalogverify/...` style, scoped to the
  Fleet-owned loading path so the request path stays free of vendor trust
  state — see [ADR: Separate fleet lifecycle authority from customer
  operations](ADR_FLEET_AND_CUSTOMER_CLI_BOUNDARIES.md#go-package-boundary)),
  version-negotiation handling, and a Metrum publishing workflow with reviews
  are all greenfield work.
- **Permanent publishing cadence.** Once the first signed bundle ships, every
  provider or model change at Metrum becomes a publishable event. Reviewers
  must accept a permanent release-engineering cost (signer access control,
  publication-record retention, build-of-record for the embedded public key,
  rotated-key migration story) for as long as customers rely on Metrum-signed
  catalogs. Silence is no longer a valid response to "is provider X
  supported?".
- **Revocation story required.** "Offline" means revocation cannot be a
  network call. The bundle must carry a signed validity window and the
  appliance must refuse catalog-consulted paths when no fresh-enough bundle is
  present, with a graceful degraded-mode shape that does not depend on a live
  Metrum endpoint. The reviewer signing the ADR accepts that this clock
  starts at "first signed bundle" and never stops.
- **Catalog bundles stay separate from Fleet runtime bundles and LRP signed
  bundles.** Names, schema, signing keys, key custody, verifier paths, and
  audit trails are independent; any code review that conflates them is a
  defect and is rejected.
- **Builder of the trust path accepts responsibility for the W1
  license-signing retrigger.** See the explicit warning below.

## Explicit warning: W1 Phase B deletes license signing

W1 Phase B deliberately deletes license signing from the appliance; the
license path is a one-key `license.json` Secret and mount distinct from any
runtime-bundle or catalog content (see [ADR: Separate fleet lifecycle authority
from customer operations](ADR_FLEET_AND_CUSTOMER_CLI_BOUNDARIES.md#runtime-bundle-boundary)
and [Customer instance operations](CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md)).
If Option 2 is selected after W1 Phase B lands, the catalog trust path is built
from scratch — it shares no key material, no verifier package, no build-of-record
process, and no revocation posture with the deleted license signer. The real
cost of W1's deletion is that a future "we want a signed catalog" decision
rebuilds an independent signing key custody, signing pipeline, embedded-public-key
build step, and offline verifier from zero, instead of reusing a signer that
would have already existed. The reviewer signing this ADR accepts that those
costs (key custody, rotation story, build-of-record, permanent publishing
cadence) are not recoverable as "we already had this for licenses".

## Decision

Not yet taken. The owner (Chetan Gadgil) chooses between Option 1 and Option 2
no later than 2026-09-19. Until then, no catalog-bundle code, no
`CatalogBundle` type, no signing key, and no catalog verifier is added under
`internal/`. Any contribution that introduces such code before the decision
date is rejected and re-scoped to a follow-up PR that cites this ADR and the
selected option.

## Consequences

- Whichever option is chosen, reviewers can grep `internal/` for catalog
  artifacts and expect either zero hits (Option 1) or a narrowly scoped
  verifier package (Option 2). Hits anywhere outside the scoped path are a
  defect.
- The Fleet runtime bundle (`config.yaml` + `env.json` via `runtime_bundle_ref`)
  and the LRP signed bundle (tenant-attested usage evidence) remain independent
  trust objects regardless of the option chosen; this ADR grants no authority
  to fold them together.
- No code change required by either option lands in the same PR as this ADR.
  Option 1's only follow-up is documentation; Option 2's follow-up is a
  separate ADR or amendment that names the new verifier package, the embedded
  public-key location, and the publishing-cadence owner.
