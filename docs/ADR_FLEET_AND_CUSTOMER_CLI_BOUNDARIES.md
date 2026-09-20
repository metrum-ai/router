# ADR: Separate fleet lifecycle authority from customer operations

- **Status:** Accepted, 2026-08-08
- **Scope:** #555 lifecycle packages and customer-local Router operations.

## Decision

`metrum-ai-router-fleetctl` is the only Fleet authority. It owns the one #555 lifecycle
registry (GORM+SQLite jobs plus tenant/license inventory) and the deterministic
`plan`, idempotent `deploy`, exact-job `status`, separately approved `delete`,
and registry-local `tenants`/`licenses` inventory vocabulary. Its reference-only
manifests, protected profiles (including `approved_compute_profiles` for
Kubernetes scheduling defaults such as `t3a.medium`), ownership labels
(`app.kubernetes.io/managed-by=metrum-fleetctl` and
`metrum.ai/smartrouter-instance`), activation evidence, and retention
approvals are the only deployment authority. Exact-job status is not an AWS/EKS
account inventory; the registry inventory lists tenants and license
safe-summaries recorded in that SQLite file only. Free-form EC2/`instance_type`
intent fields and node-group/ASG/Karpenter mutation are out of scope. The legacy
`metrum-smartrouterctl` binary exists for one release only; it exits after
reporting the rename and performs no operation.

`metrum-ai-routerctl` is customer-local and ships in both binary and Router Docker
packages. It can validate or safely compare local config, generate a caller
token into a new mode-`0600` file exactly once, inspect safe config/license/
model status, and print aggregate usage. It cannot invoke AWS, EKS, RDS, DNS,
Fleet registries, cross-customer operations, config activation, key rotation,
or license signing.

Operator convenience verbs under `metrum-ai-router-fleetctl customer …`
orchestrate disposable SQLite prepare/activate flows only. They require
explicit reference-only inputs or an externally signed mode-`0600` intent /
delete approval, never ACME/staging/production-identical string defaults, and
never hold, copy, generate, or accept lifecycle private keys (including donor
workspace key discovery) except optional `--sign-with-key` on `create`,
`bootstrap`, `repair`, and `delete` when a mode-`0600` approval key file is
supplied. Local signing otherwise stays outside the customer path in the
approved signing service or isolated signing workflow.

| Verb | Authority | Notes |
| --- | --- | --- |
| `customer publish-runtime-bundle` | Operator IAM (Secrets Manager write) | Local config/env → per-customer SM bundle; `--rewrite-paths fleet-eks`; 64 KiB gate |
| `customer prepare-runtime-bundle` | Offline file transform | Trim catalog-only models / notes before publish |
| `customer bootstrap` | Orchestrator only | Chains publish → manifest → sign → create → grant → sign → create → smoke |
| `customer create --sign-with-key` | Optional local sign + Fleet deploy | Default `--resume true` repairs retryable stuck jobs |
| `customer repair` | Registry reconcile + redeploy | Marks stale attempts failed; no PVC delete |
| `customer get-config` | Fleet lifecycle role (SM read) | Writes mode-0600 `config.yaml`/`env.json`; stdout hashes/counts only |
| `customer list-callers` | Fleet lifecycle role (SM read) | Safe caller JSON with user/project/membership; never hashes |
| `customer revoke-caller` | Operator IAM (SM write) | Sets caller status; signed create still required |
| `customer update-quota` | Operator IAM (SM write) | Patches caller rate/quota limits; does not reset counters |
| `customer quota-status` | Reports-admin HTTP | Live remaining via `/admin/reports/api/quota-status` |
| `customer list` | Registry + workspace inventory | Uses shared registry path; safe JSON only |
| `customer delete --sign-with-key` | Optional local delete approval + Fleet delete | Reads `job_id` from workspace `plan.json` |
| Core `plan`/`deploy`/`delete` | Fleet lifecycle role | Reference-only signed intent required |

Fleet binaries are included only in binary tarballs. Customer Docker images
contain `metrum-ai-routerctl`, never `metrum-ai-router-fleetctl` or the compatibility binary.

## Go package boundary

Fleet lifecycle implementations and tests live under `internal/fleet`.
Dependency-neutral license summaries and limits shared across boundaries live
under `internal/licensecontract`. The serving command and request-path
`internal/router` package must not import `internal/fleet`;
`internal/architecture/dependencies_test.go` enforces
that direction and confirms the Fleet CLI imports the Fleet package.

Keep lifecycle adapters, Kubernetes/AWS clients, registry mutation, and Fleet
inventory types out of request-path packages. A shared contract needed by both
sides should move to a small neutral package instead of making the router
depend on Fleet. Run:

```bash
go test ./internal/architecture ./internal/fleet \
  ./cmd/metrum-ai-router-fleetctl
python3 scripts/validate_package_contents_test.py
```

The package-content check is independent defense: source package separation
does not replace validation that customer images omit every Fleet lifecycle
binary and compatibility alias.

## Runtime bundle boundary

The manifest carries exactly one `runtime_bundle_ref`, an
`aws-ssm:///` or `aws-secretsmanager:///` reference without query data. It
never carries `config.yaml`, `env.json`, provider credentials, or any other
runtime value. The typed resolver reads the protected value only in memory and
accepts a single JSON object with exactly two string fields: `config.yaml`
(a YAML mapping) and `env.json` (a JSON string map). Malformed payloads,
unknown fields, empty values, and raw secret-shaped manifest content fail with
safe generic errors.

The adapter writes those two exact keys to the owned `router-runtime` Secret
and mounts it read-only at `/app/config`, the Router image's startup path.
Runtime licensing was removed in 3.0.0; Fleet no longer mounts a distinct license Secret.
Neither protected bundle values nor references enter plans, lifecycle records,
statuses, or error output.

## Dedicated RDS boundary

The default deployment path is SQLite state with one Router container and one
replica; it neither provisions nor binds RDS. A protected non-production
profile may permit an explicit approved `database_profile` manifest branch for
`database_mode: dedicated-rds`. That plan contains only deterministic database
identity and profile scalars—no DSN, hostname, port, credentials, secret
reference, or database response.
The normalized lifecycle records retries and ownership-safe retention exactly
as for the PVC path. `tenant_deployment_rds.go` provides the typed AWS RDS
adapter for a private, encrypted PostgreSQL instance with ownership tags,
RDS-Proxy-disabled policy, final-snapshot deletion, and scalar-only evidence.
It is unattached by the default EKS constructor. The existing `deploy` and
`delete` verbs can attach it only by consuming an externally issued,
mode-`0600`, time-bounded non-production disposable-E2E admission, signed by
the approved profile's `lifecycle_approval_public_key`, and bound to the exact
profile, deterministic job/intent, namespace, database profile, and manifest
digest. `metrum-ai-router-fleetctl` never creates, updates, emits, or persists that
admission or signing material. Invalid, unsigned, stale, or out-of-scope
records fail before registry or AWS/EKS clients are opened. Production-stage
Fleet tenants use the same lifecycle authority with an operator-owned production
profile, signed production-stage intent, and Release-approver authorization; see
[Customer instance operations](CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md).

## Review authority

The security/operations review needs one qualified reviewer. A
single-maintainer deployment may self-review, and the reviewer may be the
person who implemented the change. The external disposable-E2E admission
allows only the first bounded E2E after #818 preflight; it is neither a second
review nor a production authorization. Once the E2E evidence exists, the
single review is recorded before the authorized production-like non-production
rehearsal and cites the approved profile/intent IDs, immutable digest,
config/license revisions, passing suite and disposable-E2E results, isolation
and secret-handling checks, and the retention/rollback decision, per [Recorded
security and operations
review](CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md#recorded-security-and-operations-review).
Separate reviewers or additional scoped EKS roles may be used when more than
one qualified person is available or a customer contract demands separation of
duties. Production-stage operations authority remains the operator-owned
protected profile plus the recorded security/operations review in
[Customer instance operations](CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md#recorded-security-and-operations-review).

## Optional commerce and lifecycle boundary

Fleet, commerce, licensing, and customer-lifecycle packages remain in this
Apache-2.0 monorepo as **optional operator tooling**. They are not required to
run Community Metrum AI Router. Request-path packages (`cmd/metrum-ai-router`,
`internal/router`, customer-local `metrum-ai-routerctl`) must not import
`internal/commerce`, `internal/fleet`, or `internal/customerlifecycle`.
Customer Docker images continue to omit Fleet-only binaries. Local Stripe and
operator credential files such as `commerce.env.json` stay untracked and out of
Docker build context.

## Consequences

- A customer cannot accidentally use a support CLI to provision or activate an
  environment.
- A Fleet operator cannot obtain Router/provider credentials or DSNs from
  plans, registries, statuses, or error output.
- Packaged `customer` convenience verbs cannot select or attach dedicated RDS;
  that remains a core Fleet deploy branch with an external admission only.
- This ADR grants no DNS, secret, credential, or migration mutation authority
  outside the protected Fleet profile and signed-intent path.
- Commerce/fleet code in-tree does not imply a Community requirement to run
  payment or tenant-lifecycle services.
