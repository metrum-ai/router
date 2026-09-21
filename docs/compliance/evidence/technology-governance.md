# Technology Governance Evidence Record — #967 / Parent #961

## Scope

This record supports subissue **#967, Technology governance structure**, under parent issue **#961** for Metrum AI Router. It records only repository-visible evidence of documented authority, role ownership, approval gates, and escalation paths. It does not establish that the documented controls operated in a production environment or that SOC 2 or ISO/IEC 27001 certification requirements have been met.

## Evidence collected

Collected 2026-09-03 from the following sanitized repository sources (paths as of that collection date):

- `docs/CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md`, lines 1–68 — internal customer-instance operations runbook, including role responsibilities, role-based authorization, and live-action preflight/escalation requirements.
- `docs/ADR_FLEET_AND_CUSTOMER_CLI_BOUNDARIES.md`, lines 1–18 and 98–124 — accepted 2026-08-08 architecture decision record defining Fleet lifecycle authority and review/cutover boundaries.
- Google tracker supplied for this evidence exercise — anonymous access returned HTTP 401. No private Google content was accessed or used.

**Update after Fleet removal:** those two source documents were later deleted with the Fleet lifecycle subsystem. The demonstrated facts below remain a historical record of what the repository contained on 2026-09-03. Current operator guidance for self-managed installs is in `docs/DEPLOYMENT.md` and `docs/DOCKER_DEPLOYMENT.md`.

## Demonstrated facts

- The internal operations runbook assigns documented responsibilities to a customer administrator, commercial/control-plane owner, platform operator, Infra/Security approver, and Release approver. The described responsibilities cover provisioning intent, approved-policy execution, pre-live approval, and protected production-like rehearsal, change-window, cutover, and recovery authorization.
- The runbook states that human authorization is role-based rather than username-based and describes verification of an approved operator role, protected deployment role, approved target policy, immutable runtime/image attestation, fail-closed admission policy, and least-privilege RBAC before cluster selection or mutation.
- The runbook defines a fail-closed escalation path: when a required preflight condition is absent, the provisioning job records a safe blocked status and escalates to the commercial/control-plane owner; that owner coordinates the relevant Infra/Security or Release approver. Operators must not bypass the gate manually.
- The accepted ADR identifies `metrum-ai-router-fleetctl` as the sole Fleet lifecycle authority for deterministic plan, deploy, status, and separately approved delete operations. It explicitly distinguishes this authority from customer operations. **Update after 2026-09-01 collection:** production-stage Fleet authority was documented in `docs/CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md` (operator-owned production profile and Release-approver intent); the ADR text no longer claimed production remained rejected pending #518. **Later update:** Fleet lifecycle tooling was removed from the repository; this fact is historical only.
- The ADR requires one qualified security/operations review recorded before an authorized production-like non-production rehearsal, with safe references to the approved profile/intent, immutable digest, configuration/license revisions, validation results, isolation and secret-handling checks, and retention/rollback decision. It allows additional reviewers or scoped roles when required by staffing or contract.
- These sources are design and runbook evidence. They demonstrate documented authority, ownership, and escalation expectations; they do not demonstrate completed approvals, live enforcement, assigned individuals, or control effectiveness.

## Evidence gaps and compliance-owner handoff

- **Governance charter and accountable executive:** The repository does not identify a technology-governance charter, governance committee, accountable executive, or named SOC 2/ISO/IEC 27001 compliance owner. A designated GRC/compliance owner should provide the approved charter, role-assignment record, review cadence, and current control-owner register.
- **Operational approval evidence:** The sources prescribe reviews and approvals but do not contain signed or ticketed approval records for a change, protected rehearsal, production-like promotion, or recovery decision. The Release approver and Infra/Security approver should provide sanitized approval records linked to the relevant change/control identifiers.
- **Control operation and effectiveness:** No evidence was collected of periodic access reviews, governance meetings, exception decisions, risk-register review, internal audit, management review, or control-testing results. The GRC/compliance owner should provide time-bounded, sanitized evidence and identify the applicable SOC 2 criteria and ISO/IEC 27001 controls.
- **Personnel assignment and segregation of duties:** The runbook permits a single maintainer to hold non-production roles and does not evidence current personnel assignments or any contract-specific separation-of-duties requirement. The compliance owner and HR/identity-control owner should provide approved role mappings, delegation records, and access-review evidence where separation is required.
- **Restricted tracker:** The supplied Google tracker was unavailable anonymously (HTTP 401), so this record cannot claim its issue status, approvals, attachments, or evidence. The tracker owner should grant appropriately scoped access or export sanitized, reviewable artifacts to the evidence repository.

## Sanitization

This record contains no credentials, tokens, token hashes, customer identifiers, production URLs, private Google content, runtime configuration, private infrastructure addresses, or secret references. Repository paths and issue identifiers are retained only as audit-traceable source references. The restricted Google tracker is identified only by its access outcome; its URL and contents are intentionally omitted.
