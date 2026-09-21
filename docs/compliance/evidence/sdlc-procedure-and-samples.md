# SDLC procedure and sanitized sample record — #971 / #961

## Scope

This stand-alone evidence record supports parent subissue **#971** and parent issue
**#961**. It records only repository-visible, sanitized SDLC procedure evidence for
planning, implementation, review, testing, release, and rollback or incident
handling. It does not assert that a particular change completed any of those steps.

## Evidence collected

Collected 2026-09-03 from the following repository sources:

- `docs/MIGRATION_DEPLOYMENT_JOURNAL_TEMPLATE.md` — a placeholder-only pre-deploy
  change-record template. It requires a change reference, planned UTC window,
  package version, migration scope, execution and rollback classes, preconditions,
  safe deployment-job results, a release decision, and a rollback decision.
- `CONTRIBUTING.md` — contribution procedure requiring every commit to carry a
  Developer Certificate of Origin `Signed-off-by` trailer and preserving that
  trailer through rebase, squash, and amendment.
- `.github/workflows/close-review-followups.yml` — repository automation that runs
  when a labeled review-followup rollup issue closes, runs its cascade-behavior
  check, and closes linked follow-up issues.
- `docs/SMOKE_TEST_MATRIX.md` — testing procedure that requires the narrowest smoke
  that proves a change, distinguishes smoke compatibility evidence from broader
  outcome evaluation, specifies deterministic and live-smoke examples, and
  requires sanitized evidence fields for relevant validation.
- `docs/CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md` — historical Fleet customer
  instance operations and recorded security/operations review checklist
  (document later removed with the Fleet lifecycle subsystem; cite only as
  collection-date evidence).
- `docs/DOCKER_DEPLOYMENT.md` — customer Compose install and upgrade procedure.
  The 2026-09-03 collection described the former customer Compose procedure body.
- Supplied Google tracker — anonymous access returned HTTP 401. No authenticated
  tracker content was collected or relied upon.

## Demonstrated facts

- **Planning:** The migration journal template defines a reconstructible change
  record with planned window, scope, execution class, rollback class, reviewed
  plan result, preconditions, and safe deployment evidence fields. The template is
  explicitly placeholder-only and forbids secrets, customer data, full
  configuration, private endpoints, backup contents, and command transcripts.
- **Implementation:** Repository contribution procedure requires an attributable
  `Signed-off-by` commit trailer. The deployment journal associates implementation
  with a package version and migration scope; it does not provide an executed
  implementation record.
- **Review:** The promotion contract requires objective gate evidence before a
  protected workflow may request a human decision and explicitly does not infer
  authorization from the manifest. The review-followup workflow demonstrates that
  labeled, closed review follow-up rollups are checked before linked follow-ups are
  closed. Neither source demonstrates an individual pull-request approval.
- **Testing:** The smoke matrix directs maintainers to choose the narrowest proving
  smoke and to use broader outcome evidence where a quality decision requires it.
  It describes recording only safe scalar validation results such as status,
  request identifier, selected route, latency, attempt count, fallback state, and
  sanitized error class when applicable. The production runbook also prescribes
  targeted tests before package deployment and health/version verification after a
  customer deployment.
- **Release:** The promotion contract binds release review to immutable and
  hash-pinned, fresh evidence; requires a bounded canary and a distinct
  previously-known-good rollback artifact; and prevents its validator from applying
  production changes. The customer runbook documents package build, plan/apply/
  rollback tooling, and post-change verification as a separate procedure.
- **Rollback and incident handling:** The journal template requires a rollback
  decision and, when recovery is required, an approved restore reference and next
  safe step. The runbook includes a rollback operation in the package-upgrade
  procedure. The smoke matrix requires production-derived regression fixtures to
  contain safe scalar metadata and issue references only after an incident exposes
  a missed request shape or upstream error class.

## Evidence gaps and compliance-owner handoff

- No authenticated issue-tracker record, approval history, change ticket, or named
  reviewer evidence was available because the supplied tracker returned HTTP 401.
  **Control owner:** Engineering compliance or the issue-tracker administrator
  should export a sanitized, access-controlled record containing issue linkage,
  assignee, approvals, timestamps, and closure state.
- The repository sources establish procedures and templates, not a completed
  planning, implementation, review, test, or release event for a specific change.
  **Control owner:** Release engineering should provide the retained, sanitized
  change record, immutable artifact identifiers, gate outputs, test summary, and
  rollback decision for the relevant release.
- No incident ticket, incident commander decision, recovery execution record, or
  post-incident review was available. **Control owner:** Incident management should
  provide a sanitized incident record and any linked production-derived regression
  evidence.
- No proof was available that branch protection, required reviewer approvals, or
  CI status checks were enforced for a particular merge. **Control owner:** Source
  control administration should provide the applicable protected-branch policy and
  a sanitized merge/check record.

## Sanitization

This record contains repository paths, issue identifiers, procedure descriptions,
and safe status terms only. It excludes credentials, tokens, token hashes, customer
or production endpoints, private infrastructure identifiers, full configuration,
raw provider output, command transcripts, request or response content, backup
contents, and authenticated Google tracker content. The HTTP 401 boundary is
reported without identifying or reproducing the tracker URL.
