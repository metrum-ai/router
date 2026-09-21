# EKS Identity, Discovery, And Bootstrap

This internal runbook establishes the safe prerequisites for EKS delivery. It
does not deploy the router, modify AWS, or replace the validation-only staging
overlay. The shared operator and CI command wrapper is tracked separately; this
document defines the guarantees it must preserve.

**Authentication is a prerequisite, never a numbered delivery step.** Complete
AWS and Kubernetes login, SSO, MFA, or profile setup out of band. Delivery
and Make targets consume the currently authenticated session and fail
closed with a secret-free message when it is missing. They do not invoke login.

## Reauthenticate And Discover

Use an authorized short-lived federated AWS session. Do not copy credentials or
fall back to static keys. Supply every selection explicitly; the discovery
primitive never selects a current kube context, first account, or first cluster.

### Local Discovery Profile

Create the `genai-smart-router-eks-discovery` role from
`deploy/aws/genai-smart-router-eks-discovery-role.example.json` through
reviewed infrastructure-as-code. Its trust must name the approved MFA-protected
non-root operator explicitly. The current approved local identity name is
`smartrouter`; do not use an email address or account-root ARN as an IAM
principal. Account-root credentials cannot assume an AWS role and must never
be retained as a discovery source profile. The discovery policy is read-only:
it cannot create EKS resources, change an access entry, or create a Kubernetes
namespace.

After the platform administrator creates the `smartrouter` console login,
enrolls MFA, and grants only `sts:AssumeRole`, use a bounded local session; do
not retain a long-lived access key:

```ini
[profile smartrouter]
region = <approved-region>

[profile genai-smart-router-eks-discovery]
role_arn = arn:aws:iam::<ACCOUNT_ID>:role/genai-smart-router-eks-discovery
source_profile = smartrouter
region = <approved-region>
```

The dedicated `smartrouter` user and macOS bootstrap below are a legacy local
path for the read-only discovery role only. They are not the authorization
model for staging delivery or customer lifecycle operations. Delivery supports
two reviewed source paths:

- An exact organization-controlled federated/SSO role supplied as
  `AuthorizedOperatorRoleArn`, with separately reviewed `sts:AssumeRole`
  permission for the target role it uses.
- A permanent IAM user under `/smart-router-lifecycle/` with the principal tag
  `GenAISmartRouterLifecycle=true` that the platform owner has added to
  `genai-smart-router-eks-staging-lifecycle-operators`. Its group policy grants
  only entry to `genai-smart-router-eks-staging-lifecycle-operator`; that
  intermediary alone can assume the delivery, bootstrap, or image-publisher
  target roles.

No individual user is compiled into the CLI or stored in the protected target
policy. The lifecycle intermediary has no EKS, Kubernetes, Secret, ECR, or
workload authority. The bootstrap role uses Kubernetes `bind` and `escalate`
only on the exact named delivery Role, and only after the cluster-bootstrap
owner installs a fail-closed admission guard that requires the full reviewed
Role/RoleBinding specification. The recovery CLI server-side dry-runs and
readbacks those two namespace objects; it cannot read Secrets, mutate
workloads or admission policy, create resources, delete resources, or broaden
its own authority. The delivery process verifies the resulting account and
assumed-role name, protected target, immutable version-2 runtime/image
attestation, exact fail-closed admission policy/binding, and non-destructive
RBAC before selecting or mutating EKS. Removing the federated assignment or
the IAM-group membership revokes operator entry without changing deployment
state.

### Role assignment matrix

Protected tasks are granted by **role and group assignment**, never by
hardcoding IAM user ARNs in this repository:

| Assignment | Principal | Protected tasks |
| --- | --- | --- |
| Org attaches human to federated/SSO role passed as `AuthorizedPlatformIacRoleArn` | assumes `genai-smart-router-eks-staging-platform-iac` | Enroll/de-enroll path-tagged lifecycle users into the operators group only |
| Org adds IAM user under `/smart-router-lifecycle/` with tag `GenAISmartRouterLifecycle=true` to `genai-smart-router-eks-staging-lifecycle-operators` | assumes `genai-smart-router-eks-staging-lifecycle-operator` | Second hop only to delivery, bootstrap, or image-publisher |
| Federated/SSO role passed as `AuthorizedOperatorRoleArn` | may assume target roles per its reviewed source policy | Delivery / bootstrap / image-publisher as separately authorized |

No individual user name or ARN belongs in CloudFormation, the enrollment
helper source, tickets, or sanitized evidence.

### Generic IAM-User Enrollment

Federation/Identity Center is preferred for both platform-IaC and operator
entry. When the documented IAM-user fallback is necessary for operators, a
caller that has assumed `genai-smart-router-eks-staging-platform-iac` may use
the source-only helper to enroll one externally selected user. The principal
ARN is runtime input only: never commit it, place it in a ticket, save command
output containing it, or add it to a template.

Configure a local profile that assumes the platform-IaC role from the
organization-controlled federated source role (the same principal supplied as
`AuthorizedPlatformIacRoleArn` when the identity stack is deployed):

```ini
[profile <platform-iac-source-profile>]
region = us-east-1

[profile genai-smart-router-eks-staging-platform-iac]
role_arn = arn:aws:iam::<ACCOUNT_ID>:role/genai-smart-router-eks-staging-platform-iac
source_profile = <platform-iac-source-profile>
region = us-east-1
role_session_name = <change-id>
```

The helper requires that assumed-role caller. It rejects root, direct IAM-user
sessions, lifecycle-operator sessions, and delivery/bootstrap/image-publisher
sessions. It also rejects another account, users outside
`/smart-router-lifecycle/`, and users without `GenAISmartRouterLifecycle=true`.

See this document for the credential-free profile shape and verification
command. Historical staging Make delivery is retired; do not revive those
targets for live mutation.

Run the canonical bootstrap target. Before it creates a one-time source key,
it atomically reserves the mode-0600 recovery-record path. It then exchanges
the key with the macOS Keychain MFA seed for a one-hour STS session, verifies
the exact approved account and `genai-smart-router-eks-discovery` assumed-role
identity before reporting success, and deletes the source key even when
bootstrap fails:

```bash
make eks-session-bootstrap \
  EKS_ACCOUNT_ID=123456789012 \
  EKS_REGION=us-east-1 \
  EKS_MFA_SERIAL=arn:aws:iam::123456789012:mfa/smartrouter \
  EKS_MFA_KEYCHAIN_SERVICE=your-local-mfa-keychain-service \
  EKS_CLEANUP_RECORD=/secure/local/eks-source-key-cleanup.json
```

Verify the resulting identity is an `assumed-role/genai-smart-router-eks-discovery`
session in the approved account before running discovery. Bootstrap rejects a
wrong account or any other assumed role rather than reporting success. EKS
access entries plus namespace-scoped Kubernetes RBAC remain separately required
for `kubectl` reads.

If AWS_CONFIG_FILE or AWS_SHARED_CREDENTIALS_FILE is set, bootstrap writes the
session and role profiles to those exact selected files (otherwise the standard
~/.aws files) and pins its verification command to them. It ignores ambient AWS
credentials, profile selection, and web-identity overrides while performing
that verification, so it cannot validate an older same-named profile. Keep the
selected local files protected and distinct: they must be absolute,
non-symlink paths outside this repository with a parent directory that is not
group- or world-writable. Bootstrap takes an exclusive local lock tied to that
exact profile pair from its rollback snapshot through verification and rollback;
do not edit the same files concurrently. If another non-cooperating process has
changed either file, bootstrap refuses to overwrite it during rollback and
requires reconciliation instead. EKS_CLEANUP_RECORD is likewise an absolute,
non-symlink, repository-external file path under a non-group/world-writable
parent; bootstrap creates a missing parent privately before any AWS call. If
paired publication, exact-role verification, or later source-key cleanup fails
after profile publication, bootstrap restores both prior profile files (or
removes newly created ones) before it fails when they remain the exact files it
published; it never overwrites a concurrent update with stale rollback bytes.

Deletion is retried three times. If AWS remains unavailable, bootstrap fails
with a mode-0600 recovery record containing only safe state: either an exact
temporary access-key ID after a known create response, or a pre-create
reservation when the process was killed or the create result is ambiguous.
It never records the secret key. An existing recovery record blocks all new
source-key creation; the bootstrap tool never overwrites an unresolved record.

### Reconcile A Stale Recovery Record

Do not rerun bootstrap or delete a recovery record blindly. First inspect its
safe state locally:

```bash
make eks-session-recovery-status \
  EKS_CLEANUP_RECORD=/secure/local/eks-source-key-cleanup.json
```

If it reports `access_key_created`, use the approved non-root admin identity to
delete exactly the recorded key ID, verify deletion, then remove the recovery
record and retry. If it reports `reserved_before_create`, no key ID can be
safely inferred: the AWS create request may have succeeded after the client was
killed or disconnected. Using the approved admin identity, list access keys for
the dedicated source user, delete any unexpected bootstrap key, and verify that
no temporary source key remains before removing the reservation and retrying.
The dedicated `smartrouter` source user should not retain normal access keys,
which makes that reconciliation bounded and reviewable. Never create another
key until this reconciliation is complete.

```bash
# Read-only reconciliation inventory; it never reveals secret access-key material.
aws iam list-access-keys --profile <approved-admin-profile> --user-name smartrouter --output json

# After review, delete only an unexpected temporary key ID or the ID reported
# by access_key_created. Then re-run the list command and remove the local
# recovery record only when no temporary source key remains.
aws iam delete-access-key --profile <approved-admin-profile> --user-name smartrouter --access-key-id <temporary-key-id>
```

Use the Make targets to prevent implicit target selection and root-profile
access. They validate identifiers; `make eks-discover` then starts the locked
read-only discovery script. Before it rechecks the expected assumed role or
makes any live probe, it invalidates only a bounded, structurally recognizable
prior discovery report. An existing non-report file is refused and left
untouched. Keep the evidence output outside this repository:

```bash
make eks-discover \
  EKS_AWS_PROFILE=genai-smart-router-eks-discovery \
  EKS_ACCOUNT_ID=123456789012 \
  EKS_REGION=us-east-1 \
  EKS_CLUSTER=approved-cluster \
  EKS_NAMESPACE=tenant-approved-customer \
  EKS_LINKERD_NAMESPACE=linkerd \
  EKS_INGRESS_NAMESPACE=ingress-nginx \
  EKS_INGRESS_SERVICE_ACCOUNT=ingress-nginx \
  EKS_INGRESS_DEPLOYMENT=ingress-nginx-controller \
  EKS_LINKERD_TRUST_DOMAIN=cluster.local \
  EKS_ECR_REPOSITORY=approved-router-repository \
  EKS_DISCOVERY_OUTPUT=/secure/evidence/eks-discovery.json
```

`make eks-identity-check` is available for an identity-only preflight. Neither
Make target creates an AWS, EKS, or Kubernetes resource.

```bash
python3 scripts/eks_discover.py \
  --profile genai-smart-router-eks-discovery \
  --account-id 123456789012 \
  --region us-east-1 \
  --cluster approved-cluster \
  --namespace approved-namespace \
  --linkerd-namespace linkerd \
  --ingress-namespace ingress-nginx \
  --ingress-service-account ingress-nginx \
  --ingress-deployment ingress-nginx-controller \
  --linkerd-trust-domain cluster.local \
  --ecr-repository approved-router-repository \
  --output /secure/evidence/eks-discovery.json
```

`EKS_INGRESS_NAMESPACE` / `--ingress-namespace` is required for every
discovery, including a cluster that does not use Linkerd: it is the sole input
to the generated namespace allow policy. `EKS_LINKERD_NAMESPACE` /
`--linkerd-namespace` is optional. When Linkerd is omitted, omit the
service-account, Deployment, and trust-domain inputs too, then render only the
ingress NetworkPolicy. When Linkerd is selected, it is the explicit
control-plane namespace (not a product default), and discovery requires the
Linkerd policy CRDs and the `v1beta3` `Server` plus `v1beta1`
`ServerAuthorization` APIs used by the checked-in template. Select the actual
ingress service account, Deployment, and mesh trust domain as well.
Namespaces remain DNS labels; the selected ServiceAccount and Deployment use
their Kubernetes DNS-subdomain object names (up to 253 characters). Discovery
proves that the selected Deployment uses that service account, has its declared
replicas available, and has ready controller-owned Pods with a ready
`linkerd-proxy` sidecar. It derives the trust domain only from each ready
proxy's safe literal `_l5d_trustdomain` value and requires every selected Pod
to agree. It then requires the safe injected
`LINKERD2_PROXY_IDENTITY_LOCAL_NAME` value to exactly match that observed
domain, the service account, ingress namespace, and selected Linkerd
control-plane namespace before it records the derived Linkerd identity as
verified. It applies the same per-Pod trust-domain and local-identity checks to
the selected router workload, using each router Pod's service account and the
selected tenant namespace; readiness alone is not mesh evidence. This prevents
a policy from authorizing an identity that the ingress does not actually
present, including one with an operator-supplied but incorrect trust domain or a
router proxy injected by a different Linkerd control plane. The current contract
supports an ingress
`Deployment`; add a separately reviewed discovery contract before using a
different workload kind. For both the selected ingress and router
`Deployment`, discovery also requires durable injection evidence: the router
Pod template must explicitly set `linkerd.io/inject: enabled`; the selected
ingress Pod template may set `enabled` or Linkerd's `ingress` mode; or the
selected Namespace may set `enabled` when the template has no opt-out. The
checked-in
`tenant-router-linkerd-injection-patch.example.yaml` is the recommended router
deployment-template patch for a Linkerd EKS overlay; the platform-owned ingress
Deployment must retain equivalent template or Namespace evidence. Discovery deliberately
does not read Linkerd trust configuration payloads, trust anchors, certificates,
or tokens; review the derived identity before rendering.

The command creates a temporary kubeconfig and removes it on exit. It reads no
Kubernetes Secrets or ConfigMap payloads, and writes a machine-readable report
containing only safe names, booleans, versions, bounded Linkerd injection
state, and policy presence. It never retains arbitrary Namespace label or
annotation values. An expired session, wrong account, missing
namespace RBAC, inaccessible ECR, or missing EKS access fails before producing
a success report. When Linkerd was selected, missing Linkerd API/RBAC, a
mismatched ingress Deployment service account, an unready/non-meshed ingress
Pod, or a selected router Pod without a ready identity- and trust-domain-matched
`linkerd-proxy` also fails before success; otherwise the report records Linkerd
as not requested.
Discovery locally serializes use of one output path, validates that an existing
file is a bounded discovery report before invalidating it, and atomically
publishes only a fully successful replacement. A failed role check, probe, or
publish therefore leaves no reusable success report at that selected report
path, while an accidental non-report output path is preserved. Preserve
historical evidence under a different timestamped path before a rerun. Keep
evidence outside Git.

Review the report before bootstrap: EKS version/auth mode/access entries,
endpoint exposure, audit logging, ingress/storage names, bounded Linkerd
injection state and network policies, router resource names, ECR immutability/encryption/policy
presence, and Linkerd control-plane/policy API/identity service-account
inventory. It deliberately excludes endpoint values, trust-root bodies,
certificates, policy/config payloads, DSNs, and secrets.

## Identity Boundaries

Create distinct short-lived identities for read-only discovery, ECR build/push,
staging reconciliation, production promotion, and the router workload. GitHub
OIDC trust pins the repository/environment subject. The GitHub `staging`
environment must separately restrict deployment branches to the approved branch
before it grants an OIDC token. Production uses a distinct environment-gated
role. Prefer EKS access entries plus
namespace-scoped RBAC; document a dated migration plan if `aws-auth` is still
required.

Discovery requires a separate reviewed cluster-level read binding for exactly
the approved discovery identity: get Namespaces and CRDs, and get/list
IngressClasses and StorageClasses. Do not add those permissions to the tenant
provisioner Role or grant wildcard cluster administration.

It also requires the reviewed namespace-scoped read-only binding in
`deploy/kubernetes/bootstrap/eks-discovery-namespace-rbac.example.yaml` for
the selected tenant namespace: ServiceAccounts, NetworkPolicies, Deployments,
ReplicaSets, Services, PVCs, Ingresses, and Pods. The Pod and ReplicaSet reads
are used only to prove the selected router Deployment owns ready Linkerd
proxies whose safe identity and trust-domain fields match the selected control
plane when Linkerd is selected.
If Linkerd discovery is selected, apply the
separate `eks-discovery-linkerd-namespace-rbac.example.yaml` in the explicit
Linkerd control-plane namespace and
`eks-discovery-ingress-namespace-rbac.example.yaml` in the selected ingress
namespace. The ingress binding has only read access to ServiceAccounts,
Deployments, ReplicaSets, and Pods in that one namespace so discovery can trace
the selected Deployment to its ready Linkerd-proxy Pods; it grants no Secret or
ConfigMap reads. All templates bind the EKS access entry's configured Kubernetes
group, not its IAM principal ARN; none grants a write verb.

The router workload uses EKS Pod Identity or IRSA only after discovery confirms
cluster support. Its AWS policy may read only approved secret references and
write only required CloudWatch telemetry. It must not inherit node credentials.
Break-glass access uses a separately audited, MFA/federated, time-bounded role;
it is never a CI role.

## Tenant And Linkerd Bootstrap

`deploy/kubernetes/bootstrap/tenant-provisioner-rbac.yaml` and
`tenant-bootstrap-resources.example.yaml` are reviewed per-namespace templates.
A platform administrator binds/applies them only in an approved tenant namespace.
The provisioner cannot read Secrets, bind roles, operate in another namespace,
modify cluster-wide Linkerd policy, create/mutate Deployments, or create/mutate
service accounts. Router workload manifests use a distinct release identity and
must be constrained by admission policy to approved service accounts and Secret
references. Verify with `kubectl auth can-i` for allowed resources and explicit
denials for `secrets`, `deployments`, `serviceaccounts`, `clusterroles`, other
namespaces, and `pods/exec`.

Before rendering ingress access, deploy the router base resources and wait for
the selected router Pods to be Ready. The generic base NetworkPolicy is
egress-only so non-EKS deployments retain their deployment-owned ingress path.
An EKS overlay must add the reviewed
`tenant-router-ingress-guard.example.yaml` before the router is exposed; it
denies ingress during this state. Rerun the explicit-target discovery after
that readiness check; in Linkerd mode it records durable ingress and router
injection plus router proxy evidence needed by both renderers. For every EKS cluster, render
the companion selected-namespace allow policy from the successful scrubbed
report:

```bash
make eks-render-ingress-network-policy \
  EKS_DISCOVERY_OUTPUT=/secure/evidence/eks-discovery.json \
  EKS_INGRESS_NETWORK_POLICY_OUTPUT=/secure/evidence/tenant-ingress-network-policy.yaml
```

Review it, then activate it with an explicit deployment kubeconfig/context;
never use an ambient `kubectl` context. The validation target snapshots the
bounded kubeconfig and exact renderer-generated policy bytes into private local
files, then compares the discovery-selected AWS account and EKS endpoint with
that snapshot before it issues a server-side dry-run. The apply target repeats
that validation, dry-runs again, and requires an explicit confirmation; it
never rereads mutable caller artifact paths after validation. It accepts
discovery evidence only for 15 minutes after
its timestamp; rerun discovery after the window instead of activating stale
ingress or Linkerd identity evidence:

```bash
make eks-validate-tenant-network-policies \
  EKS_DISCOVERY_OUTPUT=/secure/evidence/eks-discovery.json \
  EKS_POLICY_AWS_PROFILE=<approved-deployment-profile> \
  EKS_POLICY_KUBECONFIG=/secure/kubeconfigs/approved-cluster.yaml \
  EKS_POLICY_CONTEXT=<approved-deployment-context> \
  EKS_INGRESS_NETWORK_POLICY_OUTPUT=/secure/evidence/tenant-ingress-network-policy.yaml

make eks-apply-tenant-network-policies \
  EKS_POLICY_APPLY_CONFIRM=apply \
  EKS_DISCOVERY_OUTPUT=/secure/evidence/eks-discovery.json \
  EKS_POLICY_AWS_PROFILE=<approved-deployment-profile> \
  EKS_POLICY_KUBECONFIG=/secure/kubeconfigs/approved-cluster.yaml \
  EKS_POLICY_CONTEXT=<approved-deployment-context> \
  EKS_INGRESS_NETWORK_POLICY_OUTPUT=/secure/evidence/tenant-ingress-network-policy.yaml
```

This is the complete ingress path for a non-Linkerd installation.

The discovery-owned companion policy keeps `app.kubernetes.io/name=smart-llmrouter`
only in `spec.podSelector`, not metadata. This keeps it outside the delivery
contract's label-selected overlay inventory; do not add a name-based inventory
exception or reuse the delivery label for separately managed resources.

Before using `tenant-linkerd-policy.example.yaml`, select the Linkerd
control-plane namespace during discovery and verify the discovered
Linkerd control plane, `policy.linkerd.io` CRDs/version, namespace injection
labels, and trust/identity readiness. The template uses `v1beta3` for `Server`
and `v1beta1` for `ServerAuthorization`, the separately served standard CRDs;
discovery must still confirm both versions and the ingress Deployment's actual
meshed service-account identity against the cluster before rendering. It also
requires durable ingress and router Namespace or Deployment-template injection
evidence before a policy can rely on current sidecars. The selected ingress
Deployment may use Linkerd's `linkerd.io/inject: ingress` template mode; router
workloads require ordinary `linkerd.io/inject: enabled` injection. The EKS ingress guard intentionally
denies ingress until a companion, selected-namespace allow policy is rendered.
Both artifacts must come only from the successful scrubbed discovery report,
never from hand-copied values:

```bash
make eks-render-linkerd-policy \
  EKS_DISCOVERY_OUTPUT=/secure/evidence/eks-discovery.json \
  EKS_INGRESS_NETWORK_POLICY_OUTPUT=/secure/evidence/tenant-ingress-network-policy.yaml \
  EKS_LINKERD_POLICY_OUTPUT=/secure/evidence/tenant-linkerd-policy.yaml
```

The Make target first renders the additive ingress NetworkPolicy for the
verified ingress namespace, then renders the Linkerd policy. It validates that
the reviewed EKS ingress guard retains its deny-ingress shape and rejects an
ingress policy template with a fixed namespace. In Linkerd mode it also refuses
discovery evidence unless both the selected ingress workload and selected router Pods
have ready `linkerd-proxy` sidecars with identity and trust-domain evidence
matching the selected Linkerd control plane, plus durable ingress and router
injection evidence. The Linkerd renderer derives
`service-account.namespace.serviceaccount.identity.linkerd-control-plane-namespace.trust-domain`
from the verified report and rejects mismatched identities or unrendered
placeholders. Add `EKS_LINKERD_POLICY_OUTPUT=/secure/evidence/tenant-linkerd-policy.yaml`
to both selection-bound validation/apply commands above. They verify and
server-side dry-run both outside-repository artifacts before any apply, then
activate the Linkerd `Server` and `ServerAuthorization` with the companion
NetworkPolicy so the namespace allow does not precede the identity policy.

## Idempotence, Drift, And Rollback

Render assets, server-side dry-run them, then apply only after review. Record
safe evidence: actor/workflow, selected account/region/cluster/namespace,
policy version/digest, resource names, validation result, and Linkerd injection
and policy API state. Do not record resource payloads, secrets, endpoint values,
or configuration contents. Detect RBAC, service-account, NetworkPolicy,
ResourceQuota/LimitRange, and Linkerd Server/authorization drift; require human
review before overwriting security-sensitive drift.

Rollback removes only the reviewed namespace-scoped resources or restores their
previous reviewed manifests. Do not delete a tenant namespace, Linkerd control
plane resource, Secret, PVC, or shared ingress/certificate dependency as part of
bootstrap rollback.
