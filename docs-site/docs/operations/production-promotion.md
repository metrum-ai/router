---
title: Production Promotion And Rollback
doc_type: howto
---

Production promotion is a deliberate, evidence-gated operation. A deployment
starts with an immutable release artifact, staging verification, workload and
client acceptance evidence, a bounded canary, and a documented rollback path.

The router does not provide a one-click production deploy. Operators use their
protected change-management workflow with objective automated promotion gates.
Runtime credentials, router tokens, provider keys, customer content, and deployment configuration remain in the protected deployment
boundary. Promotion manifests record automated gate results and validity windows;
they never substitute for objective evidence or protected execution controls.

Authenticate to AWS and Kubernetes **before** running any install, delivery, or
promotion command. Login, SSO, MFA bootstrap, and profile selection are
prerequisites, not steps in these docs.

Before a promotion, confirm that the intended model groups meet their quality,
cost, latency, tool, modality, and API-compatibility criteria. For Learned
Routing Policy bundles, also confirm held-out evaluation applicability for any
configured project floors, latency gates, pins, or uncertainty abstention, and
that signed-bundle trust settings used at serve match validation. Start with a
small approved caller or traffic scope, observe readiness, safe error and
fallback rates, latency, usage persistence, quota behavior, and caller
acceptance, then expand only when the recorded gates pass.

For an EKS staging delivery, retain the safe image-normalized rendered-manifest
configuration fingerprint. The production review gate matches that fingerprint
and immutable artifact exactly across passed staging promotion-plan and
migration-rehearsal evidence, without storing or publishing raw deployment
configuration. Metrum-specific EKS delivery runbooks stay in internal operator
documentation; this page describes generic promotion expectations only.

The protected review bundle contains one hash-pinned supply-chain validation
result rather than direct references to SBOM, provenance, scan, or signature
artifacts. The result binds the candidate image, architecture derived from the
approved target policy, release-binding/SBOM/provenance/scan hashes, a verified
signature, a passing scan, and a current timestamp. The promotion review checks
its exact safe shape, resolved-byte hash, image binding, target-policy
architecture, and freshness. Raw supply-chain artifacts remain inside the
protected build and release boundary.

Rollback keeps its pull reference but must use a different SHA-256 image content
digest from the release under review.

Passed staging, migration-rehearsal, and supply-chain validation-result evidence
must each carry a current RFC3339 timestamp. The protected validator rejects
timestamps from the future or more than 24 hours old. A new approval therefore
cannot make older evidence acceptable.

The promotion review accepts fixed, allowlisted evidence projections rather than
arbitrary operational JSON. The staging projection contains only schema version,
passed outcome, timestamp, staging environment, promotion-plan action,
immutable image digest, configuration fingerprint, and the required review
result. The migration rehearsal projection contains only schema version, passed
outcome, timestamp, image digest, migration identifier, and configuration
fingerprint. An additional field fails the review; raw plan events, command
text, cloud metadata, rendered manifests, and credentials never enter this
surface.

The protected deployment workflow keeps raw staging evidence separate and
publishes the reduced staging projection only after its plan checks succeed. It
assembles that projection with separately safe supply-chain and migration
rehearsal results into the protected review bundle; raw EKS evidence is not
provided to the review gate. The workflow is the trust root for the evidence;
the reduction is a sanitizer, not a replacement for workflow authorization or
artifact integrity controls. Raw supply-chain artifacts are separately verified
before their safe result is emitted and are not read by the promotion review.

Rollback returns traffic and configuration to a known-good release. Database
or durable-state changes need a separately rehearsed compatibility and recovery
plan; they are never an implicit consequence of a traffic rollback. For the
full quality evidence model, see [Deployment Readiness](../evaluation/deployment-readiness)
and [Operational Acceptance](../evaluation/operational-acceptance).

For an adaptive external policy, begin with `external_policy.mode: shadow`.
Compare the safe recommendation signal with the target actually served and
confirm that authorization, request-shape, and contract filters remain
unchanged. Promote the reviewed config to `mode: enforce` only after those
checks pass. Roll back policy influence immediately with `mode: baseline`,
which preserves eligible configured order and does not call the policy service.
