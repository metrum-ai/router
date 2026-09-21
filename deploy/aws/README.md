# AWS EKS Delivery Identity Templates

> **Ops cutover:** Example RoleName values and filenames use `metrum-ai-router-*`. Live IAM roles, groups, ECR repositories, and CloudFormation stacks still named `genai-smart-router-*` (or older) must be renamed or rebound by operators; this tree does not mutate account identity.

Files ending in `.example.json` are reviewed examples, not deployable account
infrastructure. `metrum-ai-router-eks-staging-identity.yaml` is the single
deployable non-production identity stack for the checked-in Metrum staging
target. Supply its exact authorized federated operator-role ARN only as a
protected deployment parameter; never commit that principal, AWS keys, cluster
endpoints, repository policies, secret values, session data, or credentials.

`github-oidc-trust-policy.example.json` is a staging-role trust template. Give
production promotion its own role and replace the subject with the exact
`repo:<OWNER>/<REPOSITORY>:environment:production` subject. Do not broaden a
trust policy to `repo:*`, a branch wildcard, tags, pull requests, or a generic
repository claim. The workflow must use a pinned action revision and request
only `id-token: write` plus the minimum repository permissions.

`metrum-ai-router-eks-discovery-role.example.json` defines the separately
named, read-only EKS discovery role. Replace the account and approved
IAM-user placeholder through reviewed infrastructure as code. The approved
local operator is named `smartrouter`; an organization may substitute a
federated role only through a separately reviewed trust-policy change.
Do not trust the account root, issue long-lived access keys, or use a root
session as the role source: AWS root credentials cannot assume roles. The
bootstrap administrator must create or nominate an MFA-protected non-root
principal, grant it only `sts:AssumeRole` for this role, and then replace any
temporary bootstrap trust before discovery begins.

The discovery role intentionally grants no EKS/Kubernetes mutation. EKS access
entries and namespace-scoped Kubernetes RBAC are separately required for
read-only API discovery. A tenant provisioner is a distinct workload identity;
it must be granted only the approved tenant namespace and bootstrap resources.

Define separate policies/roles for:

- discovery: `sts:GetCallerIdentity`, approved `eks:DescribeCluster`,
  `eks:ListAccessEntries`, `eks:ListNodegroups`, ECR repository inspection,
  and only the Kubernetes access entry/RBAC required for reads;
- image push: only the approved ECR repository upload and digest-read actions;
- staging reconciliation and production promotion: `eks:DescribeCluster` plus
  namespace-scoped Kubernetes RBAC, in separate environment-gated roles;
- workload identity: only the approved Secrets Manager ARN path and optional
  CloudWatch log/metric resources. Never use node-role credentials.

Require ECR tag immutability, private repository access, retention lifecycle,
scan visibility, and digest retrieval before a deployment pipeline consumes an
image. The pipeline story owns provenance, SBOM, signature enforcement, and the
shared command contract.

## Deployable Staging Delivery Identity

`metrum-ai-router-eks-staging-identity.yaml` is the deployable
CloudFormation definition for the single reviewed Metrum staging target. It
creates exact delivery and bootstrap roles, a separate reusable staging
image-publisher role, a fixed lifecycle-operator role, and the permanent
`metrum-ai-router-eks-staging-lifecycle-operators` IAM group. It also creates
the private `metrum-ai-router` ECR repository, protected non-secret target
Parameter, and EKS access entries mapped to the
`metrum-ai-router-eks-staging-delivery` and
`metrum-ai-router-eks-staging-bootstrap` Kubernetes groups. The repository
has immutable tags, scan-on-push, and a retained resource deletion policy.
Lifecycle expiration applies only to images carrying an explicit
`cleanup-approved-` tag for at least seven days. Active deployment digests and
rollback-approved ReplicaSet digests must remain unmarked, so age or image
count alone cannot remove them during the supported rollback window.

The required `AuthorizedOperatorRoleArn` remains one exact
organization-controlled federated or SSO role. It may assume the target roles
directly through its separately reviewed source-role policy. The required
`AuthorizedPlatformIacRoleArn` is a separate exact federated or SSO role that
may assume only `metrum-ai-router-eks-staging-platform-iac`. That platform-IaC
role may enroll or remove path-tagged IAM users under `/smart-router-lifecycle/`
into `metrum-ai-router-eks-staging-lifecycle-operators` and has no EKS,
Secret, ECR, or PassRole authority. The permanent IAM-user operator path is
group membership: after enrollment, group members may only assume
`metrum-ai-router-eks-staging-lifecycle-operator`; the intermediary may only
assume the three reviewed target roles. The IAM-user path, required
`MetrumAIRouterLifecycle=true` principal tag, and group policy are all
required. The template names no individual user, creates no user, and creates
no credentials. Users outside that path, missing the tag, or outside the group
cannot enter the lifecycle role. Do not pass an IAM user ARN, account root,
wildcard principal, access key, session token, or MFA value as either federated
role parameter.

Deploy or update it only from an approved non-root identity that can update the
stack. After deploy, assign humans to the SSO/federated role used as
`AuthorizedPlatformIacRoleArn`, assume platform-IaC, and enroll operators with
runtime ARNs only:

```bash
aws cloudformation deploy \
  --profile <approved-stack-deploy-profile> \
  --region us-east-1 \
  --stack-name metrum-ai-router-eks-staging-identity \
  --template-file deploy/aws/metrum-ai-router-eks-staging-identity.yaml \
  --parameter-overrides \
    AuthorizedOperatorRoleArn=<exact-federated-operator-role-arn> \
    AuthorizedPlatformIacRoleArn=<exact-federated-platform-iac-role-arn> \
    ClusterName=metrum \
  --capabilities CAPABILITY_NAMED_IAM \
  --no-fail-on-empty-changeset
```

### Scoped Bootstrap Recovery

The bootstrap role has one AWS permission: describe the reviewed cluster. Its
Kubernetes group has namespace-scoped `get`, `patch`, and `update` rights on
the exact named delivery `Role` and `RoleBinding`, plus the Kubernetes `bind`
and `escalate` checks needed to restore that known role. The separately owned
`eks-staging-bootstrap-rbac-admission.yaml` denies every bootstrap request
except the exact reviewed delivery objects, so those verbs cannot authorize
arbitrary rules, subjects, names, namespaces, workload mutation, or privilege
expansion. The role cannot read Secrets or other ConfigMaps, read Pod logs,
execute Pods, create resources, delete resources, or act outside
`metrum-ai-router-staging`.

The platform/bootstrap owner must install the fail-closed admission guard and
bootstrap RBAC once through an explicit temporary kubeconfig after the identity
stack creates the access entry. It creates the unbound delivery Role at this
stage and delays its RoleBinding until workload admission is active.
Afterwards an authorized federated operator can assume
`metrum-ai-router-eks-staging-bootstrap` and use the guarded recovery CLI
to reconcile only the reviewed namespace delivery RBAC. The CLI first
reads the two named admission-guard objects and compares their exact canonical
specifications, then server-side dry-runs both objects and force-claims their
fields before mutation. It emits a bounded exact-object readback after every
outcome. Forced ownership is safe only because the separately owned admission
policy denies every divergent result. The CLI never applies the separately
owned cluster-scoped admission-read RBAC.

```bash
aws eks update-kubeconfig \
  --profile <staging-bootstrap-profile> \
  --region us-east-1 \
  --name metrum \
  --kubeconfig <explicit-temporary-kubeconfig>
python3 scripts/reconcile_staging_delivery_rbac.py \
  --kubeconfig <explicit-temporary-kubeconfig> \
  --confirm RECOVER_STAGING_DELIVERY_RBAC
```

The initial platform-owned installation creates the recovery guard and
bootstrap RBAC, then follows the attestation and workload-admission sequence
in [Staging Image Admission](#staging-image-admission) **before** it grants the
delivery group any RoleBinding. The platform owner may create the unbound Role
at this stage:

```bash
KUBECONFIG=<explicit-temporary-kubeconfig> \
  kubectl apply --server-side \
  -f deploy/kubernetes/bootstrap/eks-staging-bootstrap-rbac-admission.yaml
KUBECONFIG=<explicit-temporary-kubeconfig> \
  kubectl apply --server-side \
  -f deploy/kubernetes/bootstrap/eks-staging-bootstrap-rbac.yaml
KUBECONFIG=<explicit-temporary-kubeconfig> \
  kubectl apply --server-side \
  -f deploy/kubernetes/bootstrap/eks-staging-delivery-rbac.yaml
KUBECONFIG=<explicit-temporary-kubeconfig> \
  kubectl apply --server-side \
  -f deploy/kubernetes/bootstrap/eks-staging-delivery-namespace-rbac.yaml
```

Only after the immutable runtime attestation exists and the delivery admission
policy and binding have passed server-side dry-run and live readback may the
platform owner bind the delivery group:

```bash
KUBECONFIG=<explicit-temporary-kubeconfig> \
  kubectl apply --server-side \
  -f deploy/kubernetes/bootstrap/eks-staging-delivery-rolebinding.yaml
```

### Staging Image Publisher

The publisher role trusts the same `AuthorizedOperatorRoleArn`; it does not
name an individual user. Assign operators to that federated/SSO role and grant
its source role `sts:AssumeRole` for
`metrum-ai-router-eks-staging-image-publisher`. The publisher can request an
ECR authorization token and upload, inspect, and resolve images only in the
stack-owned `metrum-ai-router` repository. It cannot mutate EKS, Secrets,
parameters, or any other repository.

Publish a reviewed current commit under an immutable staging tag, then resolve
the resulting `@sha256:` digest before creating the runtime attestation:

```bash
aws ecr get-login-password --profile <staging-publisher-profile> --region us-east-1 \
  | docker login --username AWS --password-stdin <account>.dkr.ecr.us-east-1.amazonaws.com
docker buildx build --platform linux/amd64 --load \
  -t <account>.dkr.ecr.us-east-1.amazonaws.com/metrum-ai-router:staging-<commit> .
docker push <account>.dkr.ecr.us-east-1.amazonaws.com/metrum-ai-router:staging-<commit>
aws ecr describe-images --profile <staging-publisher-profile> --region us-east-1 \
  --repository-name metrum-ai-router --image-ids imageTag=staging-<commit>
```

The separately reviewed cluster-bootstrap identity must first create the
immutable version-2 runtime/admission attestation described below, then apply
`deploy/kubernetes/bootstrap/eks-staging-delivery-admission.yaml` and
`deploy/kubernetes/bootstrap/eks-staging-bootstrap-rbac-admission.yaml`.
After the identity stack and one-time bootstrap RBAC installation, the scoped
bootstrap role runs `scripts/reconcile_staging_delivery_rbac.py` through an
explicit kubeconfig. The delivery-admission policy denies human requests unless
the final Deployment uses the bootstrap-approved router and Linkerd images plus
the reviewed pod security, process, environment, mount, and host-isolation
contract. Its missing parameter and evaluation failures deny. The namespace
Role grants non-destructive desired-state reconciliation and name-scoped
attestation reads. The separate cluster-scoped
`eks-staging-delivery-rbac.yaml` grants `get` only on named policies and
bindings so preflight can verify the live admission contract; it grants no
mutation. There is no Secret access, other ConfigMap access, Pod logs/exec,
resource deletion, wildcard verb, broad cluster role, or cross-namespace
authority.

Run `python3 scripts/validate_eks_bootstrap_assets.py` before deploying the
stack, policy, or RBAC. Review the CloudFormation change set and Kubernetes
server-side dry-runs before apply. Stack deletion revokes the delivery,
bootstrap, and image-publisher roles with their EKS access entries but does not
delete the Router workload, RDS, PVC, runtime Secret, or customer data; those
resources retain their own reviewed lifecycle.
## EKS Staging Target Bootstrap

`metrum-ai-router-eks-staging-target.json` is a checked-in bootstrap policy
for the delivery-contract test suite. Its ECR repository URI is **not** proof
of a currently approved live AWS/EKS target.

Before any EKS delivery command can be used outside the offline contract tests:

1. Renew a least-privilege, non-root AWS session through the secure login
   flow and confirm the approved non-production account, region, ECR
   repository, cluster, namespace, runtime Secret, non-secret Secret
   attestation ConfigMap, overlay, `image_architecture`, workload, and delivery
   role.
2. Update the reviewed JSON and the exact protected SSM Parameter named by the
   policy through approved infrastructure-as-code in one reconciliation
   change. The delivery role may read that Parameter but must not update it.
3. Run `make eks-preflight` with the approved role. Any schema, value, or hash
   mismatch fails closed before cluster selection, rendering, or mutation.

The current schema is version 6. It pins the full tagless source image name
that Kustomize must replace, `image_architecture` used to validate the
release-binding statement and its SBOM/provenance/signature/scan evidence, the
single runtime Secret name, the separately bootstrap-owned runtime Secret
attestation ConfigMap, and the workload target. The supply-chain verifier
derives architecture only from the reviewed JSON; delivery then requires its
complete canonical policy hash to match the protected Parameter. Never accept
`EKS_IMAGE_ARCHITECTURE` or any other caller-controlled architecture input.
The delivery identity receives name-scoped `get` access only to that ConfigMap
and must have **no** other ConfigMap or Secret verbs. Kubernetes RBAC cannot
make a Secret `get` metadata-only: JSONPath filters output after the API has
authorized and returned the complete Secret.

The bootstrap identity alone creates a fresh immutable attestation ConfigMap
after it creates or updates the Secret and after release approval resolves the
exact router and Linkerd images. Its `data` must contain exactly
`schema_version: v2`, `secret_name`, `secret_uid`,
`secret_resource_version`, `approved_router_image`,
`approved_linkerd_proxy_image`, `approved_linkerd_init_image`, and the exact
observed ReplicaSet-controller `approved_pod_creator_username`. The router
value must be the approved immutable ECR `@sha256:` reference. Linkerd values
must be the exact injector-owned images; use the literal `none` for the
initializer only with Linkerd CNI—the native `linkerd-proxy` sidecar remains
required.
The ConfigMap must have no `binaryData` and exactly one same-namespace `v1`
`Secret` owner reference matching the attested name and UID.

The admission binding uses that ConfigMap as a native policy parameter with
`parameterNotFoundAction: Deny`. Before changing the Secret or any approved
image, delete the old attestation; after the reviewed inputs are ready, create
the fresh immutable ConfigMap, server-side dry-run and apply the admission
policy, and only then grant or use delivery RBAC. The delivery preflight compares
the live policy and binding to the checked-in contract, proves the role cannot
mutate either, validates all attestation fields, and requires the requested
router digest to equal `approved_router_image`. Missing, malformed, deleting,
mutable, or mismatched state blocks delivery.

The policy evaluates every Deployment create/update by the delivery group in
the staging namespace. Its first validation permits only the reviewed
`metrum-ai-router` Deployment name, so using an alternate workload name cannot
bypass the container, image, mount, or pod-security checks.

The delivery contract records only safe UID/resource-version fields and
admission-spec hashes, never Secret contents or raw ConfigMap data. Do not add
the attestation ConfigMap or cluster-scoped admission objects to the Kustomize
workload inventory: they are privileged bootstrap state, not workload desired
state.

Do not infer authorization from an account number, repository URI, local AWS
profile, or this file alone. The protected Parameter and least-privilege
identity are the live approval boundary.
