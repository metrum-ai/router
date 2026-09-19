---
title: Deploy To Kubernetes
doc_type: howto
---

# Deploy To Kubernetes

Use Kubernetes when Metrum AI Router needs to run inside a **customer-operated** cluster with cluster-native ingress, Secrets, and operational controls. The generic base defaults to a serialized single-writer SQLite deployment on its `/app/state` PVC; PostgreSQL is an explicit option for multi-replica or externally managed database designs. This is the canonical Kubernetes installation page for teams that operate their own cluster; post-deployment topology guidance lives in [Enterprise Deployment Patterns](../operations/deployment-patterns).

The project maintains Kustomize-friendly manifests as a production-oriented
starting point. Review them against your cluster's ingress controller, network
policy engine, storage class, registry, and secret-management process before
production rollout.

The base and example overlay are deployment-neutral. Choose your own hostname,
ingress class, certificate workflow, registry, database topology, storage class,
resource sizing, and caller policy. A managed deployment can maintain a private
environment-specific overlay, but those values are not product defaults and do
not belong in public manifests or package documentation.

## Prerequisites

- A Kubernetes cluster with an ingress controller and TLS automation or a separate TLS termination plan.
- A private registry image tag such as `registry.example.com/metrum-ai-router:<version>-linux-amd64`.
- A fresh `ReadWriteOnce` PVC for the default SQLite bootstrap, or an explicit PostgreSQL deployment design for multi-replica/external database use.
- Provider credentials stored in a Kubernetes Secret or external secret manager.
- A router config reviewed for the deployment's model groups, callers, admin auth, and reporting settings.

For a managed deployment, Metrum's delivery team owns the environment-specific
automation and access controls. Customers should agree the target cluster,
namespace, registry, network, and approval requirements with that team before
release activation.

## Image And Architecture

Release Docker packages include per-architecture image tarballs:

| Node architecture | Package image tag example |
|---|---|
| `amd64` / `x86_64` | `<version>-linux-amd64` |
| `arm64` / Graviton | `<version>-linux-arm64` |

For private clusters, load the image tar into nodes or push it to a private registry:

```bash
docker load -i images/metrum-ai-router-<version>-linux-amd64.tar
docker tag metrum-ai-router:<version>-linux-amd64 registry.example.com/metrum-ai-router:<version>-linux-amd64
docker push registry.example.com/metrum-ai-router:<version>-linux-amd64
```

Mixed-architecture clusters need separate per-architecture tags or a registry-managed multi-architecture manifest. Do not use a per-architecture tarball as if it were a multi-architecture image.

## Runtime Contract

A Kubernetes deployment needs:

| Area | Required design |
|---|---|
| Namespace | A deployment-owned namespace with least-privilege RBAC. |
| Router runtime files | **One Kubernetes Secret** (for example `smart-llmrouter-secrets` or `router-runtime`) with keys `config.yaml` and `env.json`, mounted read-only under `/app/config/`. Do not store production `config.yaml` in a ConfigMap. |
| Provider keys | Included as `env.json` in that same Secret, or an equivalent secret-manager injection of the same file. |
| State and usage database | The generic base stores router state and `/app/state/usage.sqlite` on one `ReadWriteOnce` PVC. It remains one replica with `Recreate`; do not share SQLite between router writers. |
| Workload | A single-router Deployment for the generic SQLite path. Multi-replica deployments explicitly use PostgreSQL. |
| Network | Service, Ingress or Gateway, TLS, and NetworkPolicy for clients and upstream providers/private model services. The SQLite base has no database DSN or TCP/5432 egress. |
| Health | Readiness on `/readyz` and liveness on `/healthz`. |
| Resources | Requests and limits sized for request concurrency, streaming traffic, and admin report queries. |

Do not put provider keys, raw router tokens, token hashes, private signing material, or full production configs into public manifests, public docs, issue comments, or screenshots.

## Manifests

When the release includes Kubernetes examples, the base manifests live in:

```text
deploy/kubernetes/base/
deploy/kubernetes/overlays/sqlite-bootstrap/
deploy/kubernetes/overlays/example/
deploy/kubernetes/overlays/nvidia-local-serving/
deploy/kubernetes/overlays/k3s-amd-instinct-local-serving/
deploy/kubernetes/intents/shadeform-nvidia-local-models.example.yaml
```

### NVIDIA local-serving profile

When the cluster already runs OpenAI-compatible serving stacks on NVIDIA GPUs, use the
`nvidia-local-serving` overlay so Metrum AI Router points only at in-cluster Service DNS
names. The router Pod does **not** request `nvidia.com/gpu`; serving Deployments do.
LMCache and Mooncake stay off unless you enable them separately.

```bash
metrum-ai-routerctl blueprint render \
  --intent deploy/kubernetes/intents/shadeform-nvidia-local-models.example.yaml \
  --out /tmp/blueprint
# Review /tmp/blueprint/architecture.md and config.yaml, then:
kubectl apply -k deploy/kubernetes/overlays/nvidia-local-serving
```

Offline CI dry-run: `make test-k8s-nvidia-local-serving`. Live GPU-node steps stay in
operator runbooks; cloud/cluster login is a prerequisite, never a CLI workflow step.

### NVIDIA llm-d compatibility profile

Use `nvidia-llmd-compat` only when the cluster operator has separately chosen
llm-d and Gateway API Inference Extension. Metrum AI Router selects a model group
and sends OpenAI-compatible traffic to `llm-d-local-epp:8081`; llm-d then
selects a serving replica. Metrum AI Router is not an llm-d controller and does not
install cluster prerequisites.

Render from
`deploy/kubernetes/intents/shadeform-nvidia-llmd-compat.example.yaml`, review
the generated chart, serving manifest, and llm-d values, then follow the
operator runbook `docs/SHADEFORM_NVIDIA_LLMD_COMPAT_E2E.md`. Run the offline
gate before cluster work:

```bash
make test-k8s-nvidia-llmd-compat
```

Validate the exact model ID, Chat requests, streaming, health behavior, network
policy, and rollback in the target cluster before exposing the group.

### AMD Instinct local-serving profile

For operator-controlled k3s or Kubernetes clusters with AMD Instinct GPUs, the
manual `k3s-amd-instinct-local-serving` overlay deploys one vLLM/ROCm Service per
router model group. Serving Pods request `amd.com/gpu`; the Metrum AI Router Pod does
not. Upstreams remain in-cluster `*.svc.cluster.local` endpoints, and LMCache,
Mooncake, llm-d, and cloud LLM APIs are outside this profile.

Start with the one-GPU `local-tiny` milestone before applying the three-model
matrix. The checked-in AMD GPU Operator values pin `v1.5.1` in device-plugin
mode with host-owned drivers; do not enable DRA on the same DeviceConfig. The
node image may own the working driver; do not let the operator replace it unless
that driver lifecycle is intentional. The cluster CNI must enforce NetworkPolicy
before relying on the checked-in traffic restrictions.

Offline validation: `make test-k8s-amd-instinct-local-serving`. The internal
operator runbook is `docs/K3S_AMD_INSTINCT_LOCAL_SERVING_E2E.md`. The detailed
internal architecture reference is
`docs/amd-instinct-local-serving-reference-architecture.md`.

This profile is an operator-supplied integration example, not a blanket AMD or
ROCm compatibility commitment. The offline check validates manifests and
security invariants only. Support for a particular Instinct SKU, driver, ROCm
release, serving image, model, parser, or tool-call shape requires a successful
hardware-backed direct-upstream and router smoke for that exact combination.

For a fresh PVC, set the same immutable image in `sqlite-bootstrap/job.yaml` and `example/patch-image.yaml`, then run:

```bash
kubectl apply -k deploy/kubernetes/overlays/sqlite-bootstrap
kubectl -n smart-llmrouter wait --for=condition=complete job/smart-llmrouter-sqlite-bootstrap --timeout=10m
kubectl -n smart-llmrouter logs job/smart-llmrouter-sqlite-bootstrap -c migration-verify-serving
kubectl -n smart-llmrouter delete job smart-llmrouter-sqlite-bootstrap
kubectl apply -k deploy/kubernetes/overlays/example
kubectl -n smart-llmrouter rollout status deployment/smart-llmrouter --timeout=10m
```

The bootstrap Job removes the serving Deployment from its render so no serving pod contends for the PVC. It runs `version`, `plan`, `apply`, zero-row `resume`, then `verify-serving`; the last action fails unless the migration ledger is current/compatible and every bound data job is validated. Never run `delete -k` on this overlay: that can delete shared resources/PVCs. Use reviewed backup-bound migration jobs for existing database upgrades.

The suggested layout is:

```text
namespace/
  router-runtime Secret (config.yaml, env.json)
  router Deployment
  router Service
  router Ingress or Gateway
  NetworkPolicy
```

The router container should run the packaged image tag for the target release, not `latest`. Example `config.yaml` content for the Secret:

```yaml
server:
  listen: ":8080"
  usage_db:
    enabled: true
    driver: sqlite
    path: /app/state/usage.sqlite
    migration_policy: deployment-job

state_path: /app/state/router-state.json
```

## Secrets And Config

Create deployment-owned secrets before applying the router workload. Do not commit real secret files.

```bash
kubectl create namespace smart-llmrouter

kubectl -n smart-llmrouter create secret generic smart-llmrouter-secrets \
  --from-file=config.yaml=./config.yaml \
  --from-file=env.json=./env.json \
```

The router config is mounted at `/app/config/config.yaml`. Provider keys are mounted at `/app/config/env.json`. Durable router state is written under `/app/state`. Store the entire production runtime bundle in that Secret. Caller token hashes, browser-admin credentials, provider keys, and DSNs belong there, not in a ConfigMap.

For automated delivery evidence, bind a runtime Secret by its Kubernetes UID
and `resourceVersion`, never by its data or a captured content checksum. A
replacement or update must invalidate prior apply/smoke proof and require a
fresh reviewed rollout and smoke before promotion. Do **not** grant the
delivery identity any Secret verb: Kubernetes cannot authorize a
metadata-only Secret `get`. Instead, have a separate secret-bootstrap identity
publish a policy-pinned, immutable, non-secret ConfigMap containing only a
schema version, Secret name, UID, and resourceVersion, with a same-namespace
Secret owner reference matching the attested UID. Grant delivery only
name-scoped `get` on that ConfigMap; do not allow any other ConfigMap or
Secret verb. Keep
the attestation outside the workload Kustomize inventory. Delete it before a
Secret mutation and recreate it only after bootstrap has read the new Secret
metadata, so a partial rotation fails closed.

For an explicit PostgreSQL deployment, use TLS with hostname verification, keep its DSN in a deployment-owned Secret, mount any required CA bundle, and add narrowly scoped database egress. The generic SQLite path has no DSN or database network dependency. Keep raw provider keys, router tokens, token hashes, and DSNs out of tickets, screenshots, and public docs.

## Usage Database And State

The generic example uses its `ReadWriteOnce` PVC for router state and `/app/state/usage.sqlite`. Back it up atomically while the router is stopped, with `usage.sqlite` and any `-wal`/`-shm` sidecars together. The one-replica/Recreate contract is required for SQLite; multi-replica production reporting requires an explicitly configured PostgreSQL deployment.

## Deploy

The base kustomization intentionally does not apply `secret.example.yaml`; create `smart-llmrouter-secrets` through your secret-management process first, then render and inspect the manifests before applying them:

```bash
kubectl kustomize deploy/kubernetes/overlays/example > /tmp/smart-llmrouter.yaml
kubectl apply --dry-run=server -f /tmp/smart-llmrouter.yaml
kubectl apply -f /tmp/smart-llmrouter.yaml
```

If server-side dry-run is unavailable, use client-side dry-run as a syntax check:

```bash
kubectl apply --dry-run=client -f /tmp/smart-llmrouter.yaml
```

Check rollout:

```bash
kubectl -n smart-llmrouter rollout status deploy/smart-llmrouter
kubectl -n smart-llmrouter get pods,svc,ingress
```

Use **Recreate** when the router uses a single `ReadWriteOnce` SQLite PVC so only one pod mounts state.

## Network Policy

The generic base policy limits egress for HTTPS, DNS, and an example private
Postgres CIDR but intentionally leaves ingress to the deployment. Apply a
reviewed client/ingress NetworkPolicy before exposure.
Update the deployment for:

- approved provider endpoints or private upstream ranges;
- external Postgres address ranges;
- internal observability endpoints if required.

Network policy enforcement depends on the cluster CNI. Validate both allowed and denied flows in staging.

## Prerequisites

You must already have working `kubectl` access to the target cluster and any
cloud credentials your organization requires. Authenticate out of band; these
install steps do not run AWS SSO login, MFA bootstrap, or profile selection.

## Smoke Tests

Port-forward for an internal smoke before exposing ingress:

```bash
kubectl -n smart-llmrouter port-forward svc/smart-llmrouter 18080:80
curl -fsS http://127.0.0.1:18080/readyz
curl -fsS http://127.0.0.1:18080/docs/
curl -fsS http://127.0.0.1:18080/version
```

Then test with a deployment-issued caller token:

```bash
kubectl -n <namespace> rollout status deploy/<router-deployment>
kubectl -n <namespace> get pods,svc,ingress

export ROUTER_BASE_URL="https://<router-host>"
export ROUTER_TOKEN="replace-with-router-token"

curl -fsS "$ROUTER_BASE_URL/readyz"
curl -fsS "$ROUTER_BASE_URL/docs/"
curl -fsS -H "Authorization: Bearer $ROUTER_TOKEN" \
  "$ROUTER_BASE_URL/v1/models"
```

Run one small request through each client API shape that callers use:

- OpenAI Chat for `/v1/chat/completions`;
- OpenAI Responses or Codex CLI when enabled;
- Anthropic Messages or Claude Code when enabled;
- tool-call and image smokes for groups that advertise those capabilities.

When admin reports are enabled, verify browser-admin authentication and authorization separately. An unauthenticated `/admin/reports/` request should return a `401` challenge, not `404`; an authorized browser-admin request should load the report shell and a SQL-backed summary. Ordinary caller tokens must not access `/admin/reports/` or `/metrics`.

Fleet profiles can set `require_admin_reports: true` when browser reports are a
deployment requirement. Such a profile rejects a runtime bundle before Secret
mutation unless `/admin/reports`, Basic or OIDC admin authentication, and admin
authorization are all enabled.

Pair it with `admin_reports_proxy_cidrs`, set to the reverse-proxy networks that
front the router. Admin Basic Auth checks forwarded HTTPS before it compares the
password, so a `trusted_proxy_cidrs` list that does not cover the ingress
controller network returns `401` for correct credentials. With
`admin_reports_proxy_cidrs` set, Fleet rejects that bundle rather than deploying
it. In a cloud Kubernetes cluster the ingress controller pod address comes from
the cluster pod network, which is normally different from a local kind or Docker
bridge range:

```bash
kubectl get pods -n <ingress-namespace> -o wide
```

## Upgrade And Rollback

Use immutable image tags and reviewed config changes. Before rollout:

1. Back up the runtime Secret, PVC or state backup, and usage database.
2. Push the new per-architecture image tag to **your** private registry.
3. Update the overlay image patch.
4. Run `kubectl apply --dry-run=server`.
5. Apply and wait for rollout (`Recreate` for SQLite).
6. Smoke `/readyz`, `/docs/`, `/v1/models`, one chat request, and admin reports if enabled.

Rollback uses the previous image tag and previous Secret version:

```bash
kubectl -n smart-llmrouter rollout undo deploy/smart-llmrouter
kubectl -n smart-llmrouter rollout status deploy/smart-llmrouter
```

If a database migration or config change caused the failure, restore from the pre-upgrade backup before resuming traffic.

## Troubleshooting

- Pod not ready: check Postgres DSN, provider env file mount, and `/readyz` logs.
- `CrashLoopBackOff`: run `kubectl logs deploy/smart-llmrouter` and verify the mounted config parses.
- `/v1/models` empty: confirm the caller token allow-list and model group config.
- Provider errors: validate cluster egress, provider keys, and direct upstream smokes.
- Admin reports unavailable: confirm `server.admin_reports.enabled`, browser-admin auth, authorization grants, and usage DB connectivity.

The router never needs raw provider keys or router tokens in support screenshots. Share request IDs, status codes, sanitized logs, and safe configuration summaries instead.

Related pages:

- [Deployment Artifacts](./deployment-artifacts)
- [Package Validation And Security Checks](./package-validation)
- [Router Configuration](../configuration/router-config)
