# Fleet deployment lifecycle CLI

`metrum-ai-router-fleetctl` is the only #555 deployment authority. It uses typed AWS and
Kubernetes clients; it never invokes `aws`, `kubectl`, Helm, Terraform, Make,
or a shell command. It owns one normalized GORM+SQLite lifecycle registry for
deployment jobs plus operator inventory of tenants and license safe-summaries.
Lifecycle verbs remain deterministic `plan`, idempotent `deploy`, exact-job
`status`, and separately approved `delete`. Inventory verbs are
`tenants list|get|sync` and `licenses list|get|register`. Operator convenience
verbs under
`customer publish-runtime-bundle|prepare-runtime-bundle|bootstrap|write-manifest|create|status|smoke|grant-caller|get-config|list-callers|revoke-caller|update-quota|quota-status|update-config|repair|delete`
orchestrate disposable SQLite Fleet instances from the same binary package
without a source tree or Python helper. They consume externally signed intents
and delete approvals, call `plan`/`deploy`/`delete`, and never become a second
provisioner or a local signing authority. The customer path is SQLite-only:
it never emits or accepts `database_profile`. Dedicated RDS stays a core Fleet
`deploy`/`delete` option that requires an external mode-`0600` admission and
is not a customer-verb input. Offline verification:

```bash
make test-tenant-deploy-all
```

Older CLI names (`metrum-fleetctl`, `metrum-smartrouterctl`, `metrum-fleet-sign`,
`router-license`, `smartrouterctl`, and prior `metrum-genai-*` / `metrum-router*`
binaries) remain as source-only exit-2 notices under `cmd/` and are not packaged.
Customer operators use the separate `metrum-ai-routerctl` local operations CLI
described in the customer runbook.

Fleet commands are shipped in binary tarballs only. They are intentionally
absent from standard Router Docker/Compose images. Run them on a separate
trusted administration host.

## Authority and secret boundary

The signed deployment intent is reference-only. It never accepts or emits
provider keys, raw Router tokens or hashes, license payloads, DSNs, kubeconfigs,
full configuration, secret values, or raw adapter errors. Its mode-`0600` JSON
contains an opaque `intent_id`, issuer/timestamps/signature, protected
`profile_ref`, and the immutable deployment manifest. Protected profiles carry
non-secret policy: account/region/cluster, immutable release digest, approved
resource/state profiles, `approved_compute_profiles` (Kubernetes scheduling
contracts only), storage or RDS sizing, ingress policy, and the
`lifecycle_approval_public_key`. Live Fleet commands resolve profiles only from
`aws-ssm:///`; `file://` is limited to the local-fake `plan` contract.

Every Fleet status is bounded scalar evidence for **one** `--profile-ref`,
`--job`, and private `--registry` record. It reports customer/instance/job IDs,
environment, region/cluster aliases, namespace, resource/state/compute profile
names, node-class alias and CPU/memory buckets, desired/ready/available
replicas, pod phase counts and restart aggregate, PVC/service/ingress ownership
classes, and dedicated-RDS opaque identity only when applicable. Hostname
appears only once activation succeeds. Status is **not** an account-wide EC2,
EKS, or RDS inventory. It never contains DSNs, endpoints other than the
activated caller hostname, credentials, secret references, raw Kubernetes
objects, logs, or configuration.

Compute selection is an approved profile name, not free-form EC2 mutation.
Omit `compute_profile` on the sealed manifesto to select the default
`t3a.medium` from the protected allowlist. Alternate names must exist in
`approved_compute_profiles`. Each entry maps to architecture, CPU/memory
requests and limits, a `node_class_alias`, and required `node_selector` when
`allow_shared_worker_fallback` is false (the default). Fleet fails closed before
deploy when the selected name is missing or cannot be represented. Fleet does
not create EC2 instances, node groups, ASGs, Karpenter resources, or launch
templates. Shared `c6a`/`c6g` workers are not a silent fallback unless the
protected profile explicitly sets `allow_shared_worker_fallback: true` for that
compute profile.

The intent manifest's `runtime_bundle_ref` is the sole runtime configuration
input. It accepts only an `aws-ssm:///` or `aws-secretsmanager:///` reference
without query data; it never accepts raw configuration, credentials, or
environment values. The resolver handles its protected JSON payload only in
memory. The payload has exactly `config.yaml` (a YAML mapping) and `env.json`
(a JSON string map); malformed, additional, or empty fields fail without
echoing protected data. The adapter writes an owned `router-runtime` Secret
with those two exact keys and mounts it read-only at `/app/config`.
Runtime licensing was removed in 3.0.0; do not mount a separate license Secret.

Profiles for deployments that require continuous browser-report availability
set `require_admin_reports: true`. Fleet then rejects the runtime bundle before
writing the Secret unless `/admin/reports` is enabled, Basic or OIDC admin
authentication is enabled, and admin authorization is enabled. Keep the flag
unset for deployments that deliberately do not expose browser reports.

Such profiles should also set `admin_reports_proxy_cidrs` to the reverse-proxy
networks that front the router, for example the cluster pod network that the
ingress controller draws from:

```json
"require_admin_reports": true,
"admin_reports_proxy_cidrs": ["192.168.0.0/16"]
```

Basic Auth evaluates the forwarded-HTTPS check before it compares the password,
so a bundle whose `server.admin_auth.basic.trusted_proxy_cidrs` does not cover
the reverse proxy answers every admin request with a `401` challenge even when
the credentials are correct. When `admin_reports_proxy_cidrs` is set and the
bundle does not set `allow_insecure_http: true`, Fleet fails the deployment with
`runtime_bundle_policy_failed` unless each approved proxy network is fully
contained by a configured trusted range. Leave the field empty to skip the
check.

The control plane writes this file outside the repository and signs the
canonical fields with the profile approval key:

```json
{
  "api_version": "metrum.ai/smartrouter-deployment-intent/v1",
  "intent_id": "acme2-initial",
  "issuer_role": "fleet-lifecycle-admin",
  "issued_at": "2026-08-11T12:00:00Z",
  "expires_at": "2026-08-11T13:00:00Z",
  "profile_ref": "aws-ssm:///approved/nonproduction/acme2-profile",
  "manifest": {
    "api_version": "metrum.ai/smartrouter-deployment/v1",
    "customer_id": "acme2",
    "stage": "nonproduction",
    "release": "latest-approved",
    "resource_profile": "small",
    "state_profile": "sqlite-rwo-small",
    "compute_profile": "t3a.medium",
    "runtime_bundle_ref": "aws-secretsmanager:///tenants/acme2/runtime",
    "config_revision": "acme2-r1",
    "license": {
      "request_ref": "aws-ssm:///tenants/acme2/license-request",
      "validity": "168h"
    }
  },
  "signature": "<base64-ed25519-signature>"
}
```


## Non-production lifecycle

```bash
metrum-ai-router-fleetctl plan \
  --intent /protected/acme2-deploy-intent.json \
  --output json

metrum-ai-router-fleetctl deploy \
  --intent /protected/acme2-deploy-intent.json \
  --registry /protected/tenant-deployments.sqlite \
  --output json

metrum-ai-router-fleetctl status \
  --profile-ref aws-ssm:///approved/nonproduction/profile \
  --job job-<opaque-id> \
  --registry /protected/tenant-deployments.sqlite \
  --output json
```

Use one shared `--registry` SQLite file for the Fleet admin host so
`tenants`/`licenses` inventory spans every customer. Deploy and delete upsert
`fleet_tenants`, `fleet_tenant_instances`, and intended/bound/retired
`fleet_license_bindings` with safe scalars only (including
`license_ref_digest` and `license_validity_hours` on the plan). Register full
Fleet license inventory was removed in 3.0.0; do not register licenses from
envelopes or protected refs:

```bash
metrum-ai-router-fleetctl tenants list --registry /protected/tenant-deployments.sqlite
metrum-ai-router-fleetctl tenants get --customer-id acme2 --registry /protected/tenant-deployments.sqlite
metrum-ai-router-fleetctl tenants sync --registry /protected/tenant-deployments.sqlite

# license CLI removed in 3.0.0
chmod 0600 /protected/acme2-license-summary.json
metrum-ai-router-fleetctl licenses register \
  --summary-file /protected/acme2-license-summary.json \
  --registry /protected/tenant-deployments.sqlite
metrum-ai-router-fleetctl licenses list --registry /protected/tenant-deployments.sqlite
```

Tenant/license list is operator inventory of this registry, not an AWS/EKS/RDS
account scan. Exact-job `status` remains the live ownership-scoped observe path.

The default path is SQLite state with exactly one Router container and one
replica; it does not provision or bind RDS. The ordered SQLite lifecycle is
namespace, network policy, runtime-secret binding, license binding, state PVC,
one-replica Router, activation, then hostname. An explicit approved
`database_profile` branch inserts dedicated private RDS after network policy
and before runtime-secret/DSN-reference binding. Hostname publication is
impossible before activation. Reuse the same intent to resume; use a new
immutable config revision/intent to reconcile the same instance. Unknown RDS
or PVC outcomes require operator reconciliation.

Deletion requires the same signed intent and a mode-`0600`, expiring,
job-bound approval. `retain_pvc` controls PVC retention; `retain_database`
controls dedicated-RDS retention. Deletion never guesses ownership.

```bash
metrum-ai-router-fleetctl delete \
  --intent /protected/acme2-deploy-intent.json \
  --registry /protected/tenant-deployments.sqlite \
  --confirm-file /protected/delete-approval.json \
  --output json
```

When a deletion approval selects `retain_database: false` for a
dedicated-RDS job, pass the same still-valid `--rds-admission-file` used for
that disposable E2E. Retained RDS data is never deleted and does not consume
the RDS admission.

## Dedicated RDS contract

An optional protected non-production `database_mode: dedicated-rds` profile
permits exactly its approved `database_profile`; a manifest must explicitly
select that profile. SQLite remains the default when the manifest omits
`database_profile`. The RDS branch requires private subnet/security-group
policy, RDS-Proxy disabled, instance class, storage, backup retention, and
managed master-credential policy. The plan exposes only a deterministic
`database_id` and `database_profile`; it never stores or emits a DSN or credential.
`tenant_deployment_rds.go` uses typed AWS RDS calls to observe ownership tags
and policy, provision only private encrypted PostgreSQL, and delete only
owned instances with a final snapshot. It returns scalar evidence only.

The typed adapter is unreachable from the default EKS constructor. A first
disposable E2E can attach it only with an externally issued, mode-`0600`,
strict-JSON admission document signed by the profile's
`lifecycle_approval_public_key`. `metrum-ai-router-fleetctl` does not create, update,
emit, or persist the document or signing material; it validates non-secret
content, signature, expiry, and exact binding to the non-production profile,
deterministic job ID/intent, namespace, database profile, and manifest digest
before opening the lifecycle registry or AWS/EKS clients.

After running the existing side-effect-free `plan` with the signed intent, an
authorized maintainer obtains the scoped document outside Fleet and passes it
only to the existing `deploy` verb. The delivery and admission-issuer sessions
are separately scoped even when one qualified maintainer performs both roles;
see [Admission issuance under a single maintainer](CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md#admission-issuance-under-a-single-maintainer).

```bash
metrum-ai-router-fleetctl deploy \
  --intent /protected/acme2-deploy-intent.json \
  --rds-admission-file /protected/disposable-e2e-rds-admission.json \
  --registry /protected/tenant-deployments.sqlite \
  --output json
```

The admission is valid only for `action: disposable-e2e` and expires within 24
hours. It permits the first E2E, including its bounded cleanup, but not a
production-like rehearsal or production. After its passing E2E evidence
exists, one qualified reviewer records the security/operations review; the
implementing maintainer may self-review. See [First disposable E2E
admission](CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md#first-disposable-e2e-admission)
for the full safe-field and ordering contract. Production-stage Fleet tenants
use an operator-owned production profile and separately authorized
Release-approver intent; see
[Customer instance operations](CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md). Dedicated-RDS
production-like paths remain separately gated.

```bash
metrum-ai-router-fleetctl databases status --profile-ref aws-ssm:///approved/nonproduction/profile \
  --job job-<opaque-id> --registry /protected/tenant-deployments.sqlite
metrum-ai-router-fleetctl smoke run activation --profile-ref aws-ssm:///approved/nonproduction/profile \
  --job job-<opaque-id> --registry /protected/tenant-deployments.sqlite
```

Both commands are bounded status readbacks; they do not create resources or
activate configuration.

The release-package core-lifecycle E2E requires `EKS_E2E_INTENT`,
`EKS_E2E_DELETE_APPROVAL_FILE`, `EKS_E2E_PACKAGE_DIR` (the extracted release
package root), and `EKS_E2E_REGISTRY` (the protected shared lifecycle
registry). It never builds from the checkout or uses a temporary registry.
SQLite intents require no RDS admission and must leave
`EKS_E2E_RDS_ADMISSION_FILE` unset. Only intents whose manifest selects
`database_profile` additionally require `EKS_E2E_RDS_ADMISSION_FILE`.

This core-lifecycle test does not replace the customer acceptance sequence:
the externally signed `customer` create/grant/re-create path and a real Codex
Responses smoke remain separate required operator evidence.

## Validation

```bash
go test ./internal/fleet ./cmd/metrum-ai-router-fleetctl -run TenantDeployment -count=1
go test ./internal/architecture -count=1
```

The architecture test keeps the serving request path independent from Fleet
packages. Customer image/package validation separately confirms
that Fleet lifecycle binaries and compatibility aliases are absent from
customer Docker images.
