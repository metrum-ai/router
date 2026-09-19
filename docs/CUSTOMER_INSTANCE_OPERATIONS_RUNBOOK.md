# Customer Router Instance Operations Runbook

> **Internal runbook.** `metrum-ai-router-fleetctl` is a binary-package-only Fleet lifecycle tool; `metrum-ai-routerctl` is the customer-local operations CLI and is included in Docker. This document grants no AWS, EKS, RDS, DNS, production, runtime-secret, credential, or Compose-to-EKS mutation authority.

## Purpose and ownership

Use this runbook to operate one customer router instance through onboarding, configuration/release updates, status inspection, activation, recovery, and eventual retirement.

At launch, one customer router instance maps to one isolated runtime identity and namespace, one approved state backend (single-writer PVC SQLite or an explicitly approved dedicated RDS profile), one Router replica, and one deployment-defined environment and region. Account and region are explicit profile data; no command, registry record, or fixture may infer a default region or derive a resource address from a customer name.

| Owner | Responsibility |
| --- | --- |
| Customer administrator | Supplies approved upstream/BYOK information through the protected onboarding path and accepts the activated instance. |
| Commercial/control-plane owner | Verifies entitlement and creates the authorized provisioning intent. #921 owns verified purchase entitlement and fulfillment enqueue; Fleet provisioning remains #555. |
| Platform operator | Uses the binary-package-only `metrum-ai-router-fleetctl` lifecycle when its non-production gates permit it. It resolves approved AWS/EKS policy, applies only instance-owned Kubernetes resources, and publishes ingress only after activation. |
| Infra/Security approver | Approves account/region, network, KMS, IAM, durability, quota, DNS, and change-control policy before live execution. |
| Release approver | Owns protected production-like rehearsal, promotion, and recovery authorization for customer and operator-owned Fleet tenants. |

These are responsibilities, not headcount. A single-maintainer deployment may
hold every non-production role, and the same person may implement and review the
change. The obligation that survives is the recorded evidence in [Recorded
security and operations review](#recorded-security-and-operations-review), not a
second signature. Split the roles across separate people and additional scoped
EKS roles when more than one qualified person is available or a customer
contract requires separation of duties. Production-stage Fleet tenants use the
same lifecycle commands with an operator-owned production profile reference and
`--stage production`; they do not require a separate authority document.

Human authorization is role-based rather than username-based. Any user whose
organization-controlled federated identity is assigned the approved operator
role may configure a local AWS profile that assumes the protected deployment
role and use the same lifecycle commands. The CLI accepts the profile name, not
credentials, and independently verifies the exact account, assumed-role name,
protected target policy, immutable runtime/image attestation, fail-closed
admission policy and binding, EKS access entry, and least-privilege RBAC before
cluster selection or mutation. The human role has no Secret, admission-policy
mutation, or resource-deletion authority; destructive cleanup remains a
separate approved bootstrap/recovery action. Removing the user's
identity-provider assignment or the source role's exact `sts:AssumeRole` grant
revokes access without changing the
CLI or customer instance. See the credential-free profile and verification
procedure in [EKS identity bootstrap](EKS_IDENTITY_BOOTSTRAP.md).

Issue #555 has one strict signed reference-only deployment intent, one normalized GORM+SQLite deployment-job registry with tenant/license inventory tables, and typed AWS/EKS contracts in `metrum-ai-router-fleetctl`. It provides deterministic plan, idempotent ownership-safe create/resume, classified state, activation-before-hostname, bounded exact-job status, registry-local `tenants`/`licenses` inventory, explicit PVC/RDS retention, and exact-job deletion. Live `plan`, `deploy`, and `delete` load the mode-`0600` signed intent, whose protected `aws-ssm:///` profile reference is authenticated before cloud access; `file://` is limited to the local fake `plan` contract. Deployment `status` accepts an exact job plus protected profile reference. Customer-local `metrum-ai-routerctl` has no Fleet, cloud, cross-customer, config-activation, key-rotation, or license-signing authority.

The Fleet-only manifest carries `runtime_bundle_ref`, not raw runtime files. It
is an `aws-ssm:///` or `aws-secretsmanager:///` reference without query data.
The resolver reads the protected JSON bundle only in memory; it must contain
exactly `config.yaml` as a YAML mapping and `env.json` as a JSON string map.
The owned `router-runtime` Secret contains only those keys and is mounted
read-only at `/app/config`. Runtime licensing was removed in 3.0.0. No bundle
value or reference belongs in a plan, registry, status, error, ticket, or
evidence record.

## Before any live action

The following are mandatory fail-closed preflight conditions. An absent condition means record a safe blocked status in the #555 durable provisioning job and escalate to the commercial/control-plane owner. That owner coordinates the listed Infra/Security or Release approver when the missing gate requires their decision; operators must not bypass the gate manually, select shared placement, or reuse another customer's resources.

| Required gate | Evidence to record safely |
| --- | --- |
| Approved runtime profile | Protected profile revision; explicit cloud-account reference, region, environment, instance alias, allowed domain policy, EKS target, and secret references. |
| Authorized identity | Read-only proof of the dedicated least-privilege IAM/runtime identity and namespace authorization; never use a root credential. |
| Workload admission | Exact reviewed policy/binding spec hashes, deny-on-missing parameter, immutable approved router/sidecar image attestation, representative rejected bypasses, and proof the delivery role cannot mutate policy state or delete durable resources. |
| Supply-chain and release inputs | Immutable image digest, reviewed config revision, compatible license revision, and previous known-good manifest/evidence reference. |
| Data compatibility | #507 forward-only migration compatibility and an approved recovery plan; no automatic database rollback. |
| Capacity | Reservation and fresh recheck for the explicit account+region. Cross-region automated backups are disabled at launch; no DR-copy reservation, destination region, or copy KMS key is assumed. |
| Network and crypto | Private-only RDS endpoint, approved subnets/security groups, regional KMS key reference, TLS-only DB access, least-privilege database/runtime roles, and secret-manager references only. |
| Durability | Explicit same-region automated-backup/PITR or explicitly approved no-automated-backup policy; recovery-point class, retention, deletion/final-snapshot behavior, and restore expectations. |
| Activation and promotion | Applicable #517 non-production evidence, #554 activation profile/evidence, and a separately authorized production-stage profile/intent when promoting beyond non-production. |
| Public/customer traffic | #13 security remediation and the approved public/customer release gate. |

RDS Proxy is disabled at launch. Cross-region backup is never implicit: it is disabled at launch and requires a later approved destination account/region profile, destination-region KMS policy, copy grant, account-wide capacity reservation, retention, and recovery evidence. A future proxy or shared database placement also requires an approved ADR, policy, adapter tests, and a separate rollout decision.

## First disposable E2E admission

The first disposable non-production EKS/RDS E2E is the narrow exception to the
post-E2E review ordering. After #818's least-privilege preflight passes, an
authorized qualified maintainer may obtain an out-of-band, mode-`0600` RDS
admission document for that one E2E. The maintainer may also be the
implementer and later self-reviewer. This is not a second review, an approval
chain, a new CLI verb, or a production authorization.

Run the existing side-effect-free `metrum-ai-router-fleetctl plan` first. The external
document must use `api_version:
metrum.ai/smartrouter-rds-admission/v1`, `action: disposable-e2e`, and bind
only the plan's `profile_id`, non-production `environment`,
`database_profile`, `job_id` (which is derived from the exact intent),
`namespace`, and `manifest_sha256`. It also carries a safe issuer-role alias,
opaque approval ID, UTC approval and expiry timestamps within 24 hours, and an
Ed25519 signature. It contains no credentials, DSN, endpoint, secret
reference, runtime configuration, license payload, or signing key.

The approved profile supplies only the non-secret
`lifecycle_approval_public_key` that verifies the document. `metrum-ai-router-fleetctl`
never creates, updates, prints, or persists either an admission or signing
material. It consumes `--rds-admission-file` only after validating private
file mode, strict JSON schema, non-secret content, signature, exact plan
binding, and expiry. A dedicated-RDS `deploy` requires it before the lifecycle
registry or AWS/EKS clients are opened. A `delete` that deletes the dedicated
RDS requires the same still-valid admission in addition to its existing
job-bound deletion approval. Invalid, stale, unsigned, or differently scoped
documents leave the typed RDS adapter unattached.

### Admission issuance under a single maintainer

One qualified maintainer may hold multiple non-production responsibilities, but
each is exercised through a separately scoped federated session:

| Session | Permitted activity | Must not hold |
| --- | --- | --- |
| Delivery | Read the approved SSM profile, complete the #818 preflight, and run the existing Fleet lifecycle command. | Admission-signing key or service authority; policy/RoleBinding mutation; general resource deletion. |
| Admission issuer | Verify the safe deterministic plan fields and produce one signed, mode-`0600` admission outside Fleet. | EKS/RDS delivery, runtime-secret, registry, or production-cutover authority. |
| Reviewer | Record the checklist evidence after the first E2E. | Any additional signature requirement merely because the reviewer is also the implementer. |

The same person may use these roles in sequence. The admission issuer can be a
protected signing service or an approved isolated signing workflow; the private
key never enters the Fleet process, manifest, profile, registry, logs, or
evidence. Record only the issuer-role alias and the admission's opaque ID and
digest. This preserves an external, independently constrained admission
without introducing a second approver or a new Fleet command.

The authorized sequence is: #818 repair and passing preflight; local suite
evidence and deterministic plan; external scoped admission; disposable E2E
including failure/retry and confirmed cleanup; then the recorded
single-reviewer review; then the separately authorized production-like
non-production rehearsal. Production-stage customer mutation remains a separate
Release-approver decision after that evidence exists.

## Recorded security and operations review

Live non-production EKS/RDS mutation under #555 needs one qualified reviewer,
not an approval chain. One maintainer may review their own change. Except for
the narrowly admitted first disposable E2E above, the written review is created
after the evidence exists and before a production-like non-production rehearsal.
A review that cannot cite the items below fails, and an unreviewed rehearsal
stays forbidden.

Record these safe scalar values in the #555 durable provisioning job:

| Review item | Recorded evidence |
| --- | --- |
| Reviewer and time | Reviewer identity or role alias, UTC review timestamp, and whether the reviewer also implemented the change. |
| Target scope | Approved profile ID, environment, account/region reference, cluster reference, namespace prefix, hostname, customer/instance/intent IDs, and explicit non-production classification. |
| Immutable inputs | Resolved `repository@sha256:...` digest, config revision, license revision, and the source commit that produced the package. |
| Local gates | Passing contract, fake-adapter, security, and activation suite results with counts and run timestamps. |
| Disposable proof | Disposable non-production EKS E2E result, including the injected failure/retry case and confirmed cleanup. |
| Isolation checks | Namespace/RBAC/NetworkPolicy denial, cross-namespace denial, wrong-Host and default-backend denial, and ordinary-caller `/metrics` `403 metrics-forbidden`. |
| Secret handling | Confirmation that runtime bundle values, DSNs, credentials, tokens, token hashes, and license payloads are absent from argv, plans, registry rows, statuses, logs, and evidence. |
| Data decision | PVC and dedicated-RDS retention decision, backup/PITR class, rollback trigger, and who may approve cleanup. |
| Outcome | Approved, approved with conditions, or rejected, plus the exact conditions and the next authorized action. |

Never record credentials, DSNs, kubeconfigs, raw router tokens, token hashes,
license payloads, full Router configuration, prompts, or upstream response
bodies in the review. Add a second reviewer only when another qualified person
is available or a customer contract requires separation of duties. Production-stage
Fleet changes follow this same review checklist with an explicit production
profile, intent, and Release-approver authorization recorded in the durable job.

## Existing EKS staging repair lifecycle

Historical staging Make/`eks_delivery` repair steps are retired. Disposable
non-production Fleet customers use the staging profile block below. Do not
revive retired Make delivery targets for live mutation.

## Onboard a new customer router instance

The customer-local CLI is intentionally separate from Fleet authority:

```bash
smartrouterctl config validate --config /etc/smart-llmrouter/config.yaml
smartrouterctl config diff --from current.yaml --to candidate.yaml
smartrouterctl callers generate --owner-user example-admin --project example \
  --allow <group-from-v1-models> --token-out /protected/caller.token
smartrouterctl status --config /etc/smart-llmrouter/config.yaml
smartrouterctl license status --config /etc/smart-llmrouter/config.yaml
smartrouterctl models list --config /etc/smart-llmrouter/config.yaml
smartrouterctl usage summary --config /etc/smart-llmrouter/config.yaml
```

`callers generate` creates a token once in a new mode-`0600` file and returns
only safe caller metadata. It refuses an existing output path and never prints
the raw token or token hash. The output requires the approved configuration
controller to activate; the CLI cannot activate config, rotate keys, sign
licenses, access AWS/EKS/RDS, or operate another customer.

The default customer deployment is SQLite state with exactly one Router
container and one replica; it does not provision or bind RDS. Dedicated RDS is
optional and requires an explicit approved `database_profile` manifest branch.
Omit `database_profile` for SQLite even when the protected profile's
`database_mode` is `dedicated-rds`.

Protected profiles also carry `approved_compute_profiles`. Manifests may set
optional `compute_profile` (default `t3a.medium`). That name selects Kubernetes
scheduling/resource policy only; it is not free-form EC2/`instance_type`
mutation and does not create node groups. Exact-job
`metrum-ai-router-fleetctl status` reports customer/instance ownership and compute
scalars for Fleet-labelled objects only
(`app.kubernetes.io/managed-by=metrum-fleetctl` and
`metrum.ai/smartrouter-instance=<instance_id>`). Status is not cluster inventory.
Cleanup of disposable customers uses signed `metrum-ai-router-fleetctl delete`
with a fresh job-bound approval—never direct kubectl.

For deployments where browser reports are an operational requirement, set
`require_admin_reports: true` in the protected profile. Before changing the
runtime Secret, Fleet then requires `server.admin_reports.enabled: true` at
`/admin/reports`, an enabled Basic or OIDC admin identity path, and enabled
admin authorization. Also run
`scripts/prepare_fleet_production_bundle.py <protected-config-path>` before
signing a production config revision.

Set `admin_reports_proxy_cidrs` in the same profile to the reverse-proxy
networks in front of the router, normally the cluster pod network used by the
ingress controller. Basic Auth checks forwarded HTTPS before comparing the
password, so a bundle whose `server.admin_auth.basic.trusted_proxy_cidrs` omits
that network returns `401` for correct credentials. Fleet fails such a bundle
with `runtime_bundle_policy_failed` instead of shipping it. Confirm the ingress
address with the currently authenticated cluster session:

```bash
kubectl get pods -n <ingress-namespace> -o wide
```

For repeatable non-production SQLite customer instances (`acme3`, `acme4`, …)
use `metrum-ai-router-fleetctl customer` from a release binary package
`bin/` directory on `PATH` (or set `METRUM_FLEET_BIN_DIR`). Sibling binary
`metrum-ai-router-token-gen` must be available beside it for `grant-caller`. Do not
`go build` / `go run` on operator hosts; packaged CLIs are binaries only.

Customer convenience verbs never hold, copy, generate, or accept lifecycle
private keys by default, and they ship with **no** ACME/staging/production-identical
reference defaults. Prepare an unsigned customer-bound manifest, then either
use optional `--sign-with-key` on `create`, `bootstrap`, `repair`, or
`delete` (mode-`0600` approval key path), or obtain an externally issued signed
intent/delete approval through the approved signing service. Do not search
donor customer workspaces for keys. Omit `database_profile` so Fleet stays on
SQLite + `auto-safe` rewrite at secret bind time.

### SQLite-only customer path and deferred dedicated RDS

The packaged `customer` convenience path is **SQLite only**:

- `write-manifest` never emits `database_profile`.
- `create` / intent peek refuse any signed intent that selects dedicated RDS.
- Dedicated RDS remains an optional **core** Fleet `deploy`/`delete` branch that
  requires an explicit approved `database_profile` plus an externally issued
  mode-`0600` admission. Customer verbs cannot attach the RDS adapter.
- Live dedicated-RDS disposable E2E stays deferred; do not treat it as part of
  the default operator smoke.

### Offline verification before live mutation

Repeat these credential-free suites on a clean checkout before any live
customer create (no MFA/SSO required):

```bash
make test-tenant-deploy-all
# or just the customer convenience gates:
make test-fleet-customer-cli
```

Those targets cover fake-adapter tenant deploy suites plus customer CLI
fail-closed cases (missing signed intent, missing delete approval, missing
refs, ACME-rehearsal mismatch, donor-key non-copy, dedicated-RDS refuse).

Gated live acceptance (packaged `dist/` binaries only; skipped without env):

```bash
make test-fleet-sqlite-customer-live
```

Set `FLEET_SQLITE_E2E_PROFILE_REF`, `FLEET_SQLITE_E2E_LICENSE_REF`,
`FLEET_SQLITE_E2E_CONFIG_FILE`, `FLEET_SQLITE_E2E_ENV_FILE`, and
`FLEET_SQLITE_E2E_SIGN_KEY` before running the live target. See
`scripts/fleet_sqlite_customer_e2e.sh`.

### Metrum operator quick reference (SQLite Fleet customers)

For **BYOK greenfield** (license SSM → bootstrap; payment out of band), use
[`CUSTOMER_LIFECYCLE_CLI.md`](CUSTOMER_LIFECYCLE_CLI.md)
(`metrum-ai-router-customer-lifecycle onboard`) with **packaged** Fleet/lifecycle
binaries only (`METRUM_FLEET_BIN_DIR`). Instance config must already use
top-level `state_path` under `/var/lib/smart-llmrouter` (not under `server:`)
and `/etc/smart-llmrouter-license` license paths before publish. The block below remains the direct
Fleet-only path when license refs are already satisfied.

#### Disposable non-production customers (staging profile)

Copy-paste block for disposable or non-production SQLite Fleet customers on the
shared registry (`~/.local/share/metrum-fleet/registry/tenant-deployments.sqlite`).
Replace `<customer-id>` with your tenant (example **`acme`** →
`acme.apps.example.test`). Use operator-owned protected profile and secret
references; do not paste live production paths into tickets or public notes.
Do **not** reuse a non-production staging profile for a production-stage tenant.

```bash
export METRUM_FLEET_BIN_DIR=/path/to/release/bin
export FLEET_PROFILE_REF='aws-ssm:///example/smartrouter/profiles/staging'
export FLEET_LICENSE_REF='aws-ssm:///example/smartrouter/fleet/<customer-id>/license-request'
export FLEET_RUNTIME_BUNDLE_REF="aws-secretsmanager:///example/smartrouter/fleet/customers/<customer-id>/runtime-bundle"

# List all customer instances (registry + local workspaces)
metrum-ai-router-fleetctl customer list

# Greenfield nonproduction
metrum-ai-router-fleetctl customer bootstrap \
  --customer-id acme \
  --profile-ref "$FLEET_PROFILE_REF" \
  --license-ref "$FLEET_LICENSE_REF" \
  --config-file /protected/config.yaml \
  --env-file /protected/env.json \
  --rewrite-paths fleet-eks \
  --sign-with-key /protected/lifecycle_approval_private_key.b64 \
  --owner-user acme-admin --project acme \
  --allow-from-config \
  --token-out ~/.local/share/metrum-fleet/acme/CALLER_TOKEN_ADMIN.txt \
  --model high

# Status / smoke
metrum-ai-router-fleetctl customer status \
  --customer-id acme --profile-ref "$FLEET_PROFILE_REF"
metrum-ai-router-fleetctl customer smoke \
  --customer-id acme \
  --token-file ~/.local/share/metrum-fleet/acme/CALLER_TOKEN_ADMIN.txt \
  --model high

# Download live config (mode 0600 files; stdout is hashes and counts only)
metrum-ai-router-fleetctl customer get-config \
  --customer-id acme \
  --profile-ref "$FLEET_PROFILE_REF" \
  --runtime-bundle-ref "$FLEET_RUNTIME_BUNDLE_REF" \
  --license-ref "$FLEET_LICENSE_REF" \
  --config-out /protected/config.yaml \
  --env-out /protected/env.json

# List callers (safe JSON; includes project/user fields)
metrum-ai-router-fleetctl customer list-callers \
  --customer-id acme \
  --profile-ref "$FLEET_PROFILE_REF" \
  --runtime-bundle-ref "$FLEET_RUNTIME_BUNDLE_REF" \
  --license-ref "$FLEET_LICENSE_REF"

# Live quota remaining (reports admin basic auth; filter by user or caller)
metrum-ai-router-fleetctl customer quota-status \
  --customer-id acme \
  --admin-basic-file /protected/basic-admin \
  --owner-user acme-admin

# Delete (single command with --sign-with-key; reads job_id from workspace plan.json)
metrum-ai-router-fleetctl customer delete \
  --customer-id acme \
  --sign-with-key /protected/lifecycle_approval_private_key.b64
```

#### Production-stage Fleet customers

Production-stage tenants use the same `customer` verbs with an operator-owned
**production** profile and `--stage production` only:

```bash
export FLEET_PROFILE_REF='aws-ssm:///example/smartrouter/profiles/production'
export FLEET_RUNTIME_BUNDLE_REF='aws-secretsmanager:///example/smartrouter/fleet/customers/<customer-id>/runtime-bundle'
export FLEET_LICENSE_REF='aws-ssm:///example/smartrouter/fleet/<customer-id>/license-request'
```

Record topology, ownership, routine deploy, verification, rollback, and
decommission criteria in the durable #555 job and the protected operator
boundary—not in public copy-paste examples.

`customer list` uses the shared registry automatically (no `--registry` in CWD).
`customer delete --sign-with-key` writes a mode-`0600` delete approval under the
customer workspace, then runs Fleet delete. External `--confirm-file` remains
supported for out-of-band signing. Optional `--retain-database` and
`--retain-pvc` pass through to `metrum-ai-router-fleet-sign delete`.

### Greenfield bootstrap (preferred)

Use the [quick reference](#metrum-operator-quick-reference-sqlite-fleet-customers)
`customer bootstrap` command for disposable non-production customers. Secrets Manager rejects runtime bundles above
**65536** bytes; run `customer prepare-runtime-bundle --trim-catalog-only` first
when the source config is larger. `--rewrite-paths fleet-eks` rewrites
`/app/state` and `/app/logs` to `/var/lib/smart-llmrouter` before publish.
`customer create` accepts optional `--sign-with-key`, `--resume` (default true),
and `--auto-smoke`. Use `customer repair` when a prior deploy left retryable
`failed` or `operator_required` registry state.

### Step-by-step customer path

For grant-caller, update-config, recreate-with-delete-first, or external signing
workflows, follow the same env exports and flags as the
[disposable non-production quick reference](#disposable-non-production-customers-staging-profile), then
chain `publish-runtime-bundle` → `write-manifest` → sign → `customer create`.
For production-stage tenants, use the [production-stage block](#production-stage-fleet-customers)
(`write-manifest --stage production`) with Release-approver authorization.
`ROUTER_MODEL` / `--model` must match a group from
the caller's `/v1/models`; smoke requires exact assistant content `OK`.

`metrum-ai-routerctl callers generate` remains the customer-local draft tool and
returns `activation: configuration-controller-required`. On Metrum-managed EKS
SQLite customers, Fleet `customer grant-caller` / `customer update-config` / `customer revoke-caller` / `customer update-quota` is the
configuration controller prepare path: it writes
`aws-secretsmanager:///example/smartrouter/fleet/customers/<id>/runtime-bundle` with the
operator IAM identity (CreateSecret/PutSecretValue) and a new unsigned manifest;
activation still requires an externally signed intent plus `customer create
--intent` (or `customer create --sign-with-key`). Do not use kubectl, manual
`aws secretsmanager put-secret-value`, or one-off migrate Jobs. Router
deployments on Fleet EKS use **Recreate** strategy so SQLite PVC updates do
not require manual pod deletion.

Artifacts stay under `~/.local/share/metrum-fleet/<customer_id>/` (mode `0700`).

The typed AWS RDS adapter enforces private/encrypted/no-proxy policy, ownership
tags, and final-snapshot deletion. Its default EKS constructor remains
fail-closed; the first disposable E2E may attach it only through the external
time-bounded admission described in [First disposable E2E
admission](#first-disposable-e2e-admission). The Fleet CLI consumes but never
creates that document. Credential-binding, ownership, activation, disposable
E2E, security, and operations evidence is then recorded through [Recorded
security and operations review](#recorded-security-and-operations-review).
Production-stage profile authority stays with the operator-owned protected
profile and Release approver. See [the Fleet lifecycle
contract](MULTI_ENVIRONMENT_DEPLOYMENT_CLI.md) and [CLI boundary
ADR](ADR_FLEET_AND_CUSTOMER_CLI_BOUNDARIES.md).

On any live failure, stop customer handoff. Retry only the classified safe
stage. Compensation or cleanup is separately authorized and must not delete an
RDS instance, snapshot, PVC, or customer data by default.

## Update configuration or release

Each proposed change is one instance-scoped, explicit operation. The release manifest binds the instance/profile revision, immutable image digest, configuration revision, license compatibility, PVC-backed state policy, prior known-good manifest, and approval/evidence references. It contains no credentials, endpoints, DSNs, or full configuration.

1. Create a reviewed draft configuration using protected secret references. Validate YAML/contract shape and caller/model access; keep new provider/model/API-skin combinations in a smoke or staging group until exact request-shape validation passes.
2. Render and plan against the named profile/environment/instance. Confirm the target identity, manifest diff, configuration fingerprint, migration class, rollback classification, and expected caller-visible impact.
3. Run exact-surface staging checks: readiness, allowed `/v1/models`, applicable Chat/Responses/Messages, streaming, tools, image behavior, quota/license behavior, report/admin authorization, and ordinary-caller `/metrics` `403`. When reports are required, unauthenticated `/admin/reports/` and `/admin/auth/check` must challenge with `401` rather than return `404`, and an authorized browser-admin probe must load the report shell plus a SQL-backed summary.
4. Promote only through the approved source-to-target manifest/evidence handoff. A config or application rollback returns to a known-good manifest; it never implies a database/data rollback.
5. Record sanitized outcome, release/config/migration versions, safe error class, and next action. Keep raw request bodies, provider keys, router tokens, token hashes, customer content, and full config out of evidence.

## Backup and recovery requests

Do not use the phrase "backup to a cluster." An EKS cluster is a runtime placement, not a database-backup destination. The system keeps three separate artifacts with separate retention and authorization:

| Artifact | Canonical owner | Contents and recovery behavior |
| --- | --- | --- |
| Configuration backup | #7 and #545 | Versioned configuration, safe secret references, entitlement-compatible metadata, and audit linkage. It never contains raw BYOK, caller tokens/hashes, DSNs, or Kubernetes Secrets. Restore creates a draft and requires revalidation. |
| Tenant PVC recovery record | #555 | A protected record for one tenant's retained SQLite PVC. It is not a raw database download or tenant cloud credential. |
| Release manifest | #581 and #517 | Immutable image/config/license/migration and safe evidence references. It is a release record, not a data backup. |

### Operator recovery primitive (planned #581)

An operator may create or inspect a tenant RDS recovery point only with an explicit tenant, source router-instance, account/region/environment profile, backup class, idempotency/intent ID, and approved policy reference. The future primitive must fail closed for an unapproved profile, capacity/retention violation, tenant mismatch, unknown source instance, or missing authorization.

A restore never overwrites a serving tenant state volume. It names an explicit **recovery router instance** for the same tenant and an approved retained PVC recovery record. Arbitrary EKS clusters, arbitrary PVCs, cross-tenant targets, and unapproved account/region targets are rejected. The result starts as a non-serving `recovery_candidate` with ingress and caller handoff disabled.

The candidate then follows #507-compatible migration/compatibility checks, #7 configuration reconciliation as a draft where required, and #554 activation verification. Only a separately authorized promotion/cutover can make it serving. Database recovery is never an automatic rollback or a silent replacement of live customer data.

### Tenant-admin request boundary (planned #545 child work)

An eligible customer owner/admin may request or inspect a per-tenant recovery point through the control plane, subject to entitlement, configured on-demand limit, retention, cost, and audit policy. The portal never exposes AWS, Kubernetes, database, KMS, snapshot, endpoint, DSN, or credential access.

An admin may request a restore, but the launch default is a non-serving recovery candidate. A customer request cannot self-cut over a serving instance. Final cutover follows the configured protected approval rule and Release-approver authorization for the target profile. #555 owns the durable restore/recovery job, secret/config/license/namespace/ingress wiring, and compensation; #554 supplies the required revalidation evidence.

Store policy, requests, attempts, immutable artifact references, restore approvals, recovery targets, retention/hold/cleanup events, and audit evidence relationally with tenant, source instance, profile scope, actor, and policy-revision foreign keys. Status surfaces only safe scalar references, timestamps, state, and error class.

## Inspect status and verify service

The shipped command has two read-only status modes. Inventory `status` remains
bounded to 1–100 local rows and reports tenant/stage identity, schema drift,
release digest, and dedicated placement. Deployment `status --profile-ref
aws-ssm:///... --job <exact-job>` accepts only a protected profile reference,
opens the exact local job registry read-only, and performs scoped read-only EKS
observation for that profile. Neither mode reads Router health, RDS state,
credentials, configuration, or customer databases.

The status adapter is authorized and scoped to an explicit
profile/environment/instance. It answers:


| Question | Safe status/evidence |
| --- | --- |
| What is deployed? | Instance alias, environment/region aliases, manifest/release/config revision, and migration version. |
| Is it serving? | Readiness, bounded uptime/observed timestamp, activation state, safe DNS/ingress health, and sanitized error class. |
| Is the database aligned? | Dedicated-DB connectivity summary, desired versus last-observed migration state, observation time, and drift count. |
| Is policy satisfied? | Profile revision, `ReadWriteOnce` PVC storage policy, one-replica enforcement, and retained-state recovery policy. |
| What should be checked next? | Latest approved evidence reference, required verification, and a safe next action or escalation. |

For a newly activated instance, verify:

- readiness and version endpoint;
- authenticated `/v1/models` for each caller class;
- the API surfaces and modalities promised to that customer;
- tenant/network isolation and ordinary-caller `/metrics` `403`;
- report/admin authorization and a sanitized usage/latency evidence window;
- license state and configuration fingerprint;
- the approved durability/recovery rehearsal and activation evidence.

## Failure, rollback, and recovery

Treat application/config rollback and database/data recovery as separate operations.

- For release/config failure, stop expansion, retain sanitized evidence, and return only application/config traffic to the previous known-good manifest after approval.
- For migration, RDS, secret, DNS, Linkerd, activation, backup, or restore failure, halt handoff. Retry only an idempotent safe stage; otherwise use the approved restore/reconcile plan.
- Never automatically drop a database, delete a snapshot, revoke a customer instance, or infer that a failed deploy can undo durable data.
- After a rollback or recovery, rerun readiness, caller API, isolation, license, activation, and reporting checks before returning the instance to `ready`.
- Escalate an unknown state, failed recovery, policy gap, or authorization denial with the issue number, safe evidence, impact, and the exact decision needed.

## Promotion and retirement boundary

#581 preflights and records source-to-target manifest/evidence handoff.
Production-stage Fleet tenants require an operator-owned production profile,
signed production-stage intent, and Release-approver authorization. Routine
customer onboarding does not grant production profile authority.

Customer retirement or incident cleanup requires a separately confirmed plan covering customer notification, traffic disablement, retention, license/caller access, and RDS/snapshot disposition. Do not treat a normal failed provisioning attempt as authorization to delete durable resources.

## Related records

- Issue #555 — customer EKS lifecycle and provisioning orchestration
- Issue #507 — forward-only migration framework
- Issue #592 — tenant-admin protected RDS recovery requests
- [Deployment Runbook](DEPLOYMENT.md)
- [Customer Docker Compose](DOCKER_DEPLOYMENT.md)
- [Fleet lifecycle contract](MULTI_ENVIRONMENT_DEPLOYMENT_CLI.md)
- [CLI boundary ADR](ADR_FLEET_AND_CUSTOMER_CLI_BOUNDARIES.md)
