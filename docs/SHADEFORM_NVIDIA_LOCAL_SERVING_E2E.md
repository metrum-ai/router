# Shadeform k3s NVIDIA local-serving e2e

**Profile:** `nvidia-local-serving`  
**KV cache:** off — do not install LMCache or Mooncake for this run.

This is the live, single-node k3s validation path for Metrum AI Router. It uses
only in-cluster OpenAI-compatible Services as router upstreams. Do not use Fleet,
OpenRouter, OpenAI, Anthropic, a router CRD controller, or a cloud inference API.

## Preconditions

Cloud and cluster authentication are prerequisites. Do not put login, MFA, or
credential-copy steps in automation. Stop with **`authenticate first, then retry`**
if a Shadeform API call, SSH connection, or `kubectl get nodes` is unauthorized.

Before provisioning, verify without printing values. Load
`SHADEFORM_API_KEY` from process environment first, then ignored `ops.env.json`,
then ignored `env.json`:

```bash
python3 -c 'import json, os; from pathlib import Path
v = os.environ.get("SHADEFORM_API_KEY")
if not v:
  for p in ("ops.env.json", "env.json"):
    path = Path(p)
    if not path.is_file():
      continue
    try:
      v = json.loads(path.read_text()).get("SHADEFORM_API_KEY")
    except Exception:
      v = None
    if v:
      break
assert v, "SHADEFORM_API_KEY missing"
print("SHADEFORM_API_KEY: present")'
command -v k3sup kubectl helm jq curl
test -r "${LICENSE_SIGNING_KEY_FILE:?LICENSE_SIGNING_KEY_FILE is required}"
```

The router image must be pullable by the chosen node and have an immutable tag or
digest. Keep the signed `license.json`, caller token, kubeconfig, runtime `env.json`,
approved entitlement, and any registry credentials outside Git. The normal operator
flow supplies `LICENSE_SIGNING_KEY_FILE` from the protected lifecycle intent; its
private material MUST NOT enter Helm values, Kubernetes, the router image, or a
generated safe summary.

A three-service model matrix needs **at least three schedulable NVIDIA GPUs**:
one GPU-requesting vLLM Deployment per model. Confirm this before downloading any
weights.

## Hardware and model matrix

Select in this order: B200, H200, then L40S. Record the exact Shadeform SKU,
node GPU name, driver/CUDA, k3s version, topology, disk plan, and allocatable
`nvidia.com/gpu` before deploying a model.

| Hardware profile | Required models | Do not deploy |
|---|---|---|
| `b200` / `h200` | one Qwen3.8-class 20–40B instruct/chat model plus at least two current <=9B instruct/coder models | a matrix without the 20–40B primary |
| `l40s` | three current small models: ~3B general, <=9B chat, <=9B coder | a 20–40B primary |

Checked-in intents:

- `deploy/kubernetes/intents/shadeform-nvidia-local-models.example.yaml` — conservative `l40s` fallback (four GPUs assumed).
- `deploy/kubernetes/intents/shadeform-nvidia-local-models-b200.example.yaml` — B200/H200 matrix with a 20–40B primary.

For B200/H200, set `hardware_profile: b200` or `h200`, provide a 20–40B model with
`model_size_billions` in that range, and retain two <=9B models. Blueprint render
rejects an external Service URL, fewer than three models, and an invalid hardware
matrix.

## 1. Create the Shadeform host and install k3s

Use the Shadeform instance-types API to select an *available* SKU/region and OS
with enough GPUs. GPU type and count live under `.configuration`; regions live
under `.availability[]`. Prefer a CUDA Shade OS so the node image owns the NVIDIA
driver and toolkit.

```bash
curl -fsS 'https://api.shadeform.ai/v1/instances/types?available=true&sort=price' \
  -H "X-API-KEY: ${SHADEFORM_API_KEY}" |
  jq -r '
    .instance_types[]
    | select(.configuration.gpu_type == "B200"
          or .configuration.gpu_type == "H200"
          or .configuration.gpu_type == "L40S")
    | select(.configuration.num_gpus >= 3)
    | . as $t
    | ($t.availability // [])[]
    | select(.available == true)
    | [$t.cloud, .region, $t.shade_instance_type, $t.configuration.num_gpus, $t.hourly_price, $t.configuration.gpu_type]
    | @tsv
  '

# Create the selected candidate with a pre-registered SSH key. Capture only the
# returned instance ID in the operator journal; never print the API key.
curl -fsS -X POST 'https://api.shadeform.ai/v1/instances/create' \
  -H "X-API-KEY: ${SHADEFORM_API_KEY}" -H 'Content-Type: application/json' \
  --data '{"cloud":"<cloud>","region":"<region>","shade_instance_type":"<sku>","shade_cloud":true,"name":"issue-943-k3s-e2e","os":"<selected-os>","ssh_key_id":"<registered-key-id>"}'
```

Poll `/v1/instances/<id>/info` until `status` is `active`, then install k3s with
k3sup using the returned IP, user, and SSH port:

```bash
k3sup install \
  --ip "$SHADEFORM_PUBLIC_IP" \
  --user "$SHADEFORM_SSH_USER" \
  --ssh-port "$SHADEFORM_SSH_PORT" \
  --ssh-key "$SHADEFORM_SSH_KEY" \
  --local-path "$HOME/.kube/shadeform-k3s.yaml" \
  --context shadeform-k3s
export KUBECONFIG="$HOME/.kube/shadeform-k3s.yaml"
kubectl get nodes -o wide
kubectl version --short
```

Use a k3s CNI that enforces `NetworkPolicy`; do not claim local-only egress from a
policy that the selected CNI does not enforce.

## 2. Confirm NVIDIA plumbing

Determine driver ownership from the node image. For an AMI/image-owned driver and
toolkit set `driver_owned_by_ami: true`; otherwise let GPU Operator own both.
Never enable DRA and the NVIDIA device plugin for the same device. Live installs
must use the **blueprint-rendered** GPU Operator values file (not a stale checked-in
copy): with `driver_owned_by_ami: true`, rendered values set `driver.enabled` and
`toolkit.enabled` to `false`.

```bash
# L40S fallback intent, or shadeform-nvidia-local-models-b200.example.yaml for B200/H200.
metrum-ai-routerctl blueprint render \
  --intent deploy/kubernetes/intents/shadeform-nvidia-local-models.example.yaml \
  --out /tmp/shadeform-blueprint
helm repo add nvidia https://helm.ngc.nvidia.com/nvidia
helm repo update
helm upgrade --install gpu-operator nvidia/gpu-operator \
  --namespace gpu-operator --create-namespace --version v26.7.0 \
  -f /tmp/shadeform-blueprint/overlays/nvidia-local-serving/gpu-operator-values.yaml
kubectl -n gpu-operator rollout status deploy/gpu-operator
kubectl get nodes -o json | jq -r '.items[] | "\(.metadata.name) allocatable_gpu=\(.status.allocatable["nvidia.com/gpu"] // "0")"'
```

Run `nvidia-smi` in a GPU debug Pod or on the node and record only GPU name,
driver version, CUDA version, and allocatable count. **Fail** if the count is less
than three for this concurrent three-service matrix.

## 3. Render and deploy local services plus router

The render output has two deployable units: a self-contained serving overlay and
a Helm chart for the router. Its `config.yaml` supplies only `*.svc.cluster.local`
provider URLs and one static local target for each model group.

```bash
# Create the caller file locally. The wrapper issues the license, creates the
# namespace, atomically replaces the runtime Secret, and runs Helm.
# Adjust --allow to the live model groups (local-tiny/... or local-qwen38/...).
metrum-ai-routerctl callers generate \
  --owner-user local-operator --project local --env dev \
  --allow local-tiny,local-small-chat,local-small-coder \
  --token-out /tmp/shadeform-caller.token \
  --config /tmp/shadeform-blueprint/config.yaml --write

# Serving overlay does not create the namespace. Create it before apply.
kubectl create namespace smart-llmrouter --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -k /tmp/shadeform-blueprint/overlays/nvidia-local-serving

# Self-managed entitlement signing.key_id MUST be "self-managed" to match the
# blueprint public_keys entry. Bind one concrete instance fingerprint in both
# the entitlement allowed_instances list and server.license.instance_fingerprint
# when the SKU sets instance_fingerprint_required / max_instances.
scripts/helm_install_with_license.sh \
  --kubeconfig "$KUBECONFIG" \
  --namespace smart-llmrouter \
  --release smart-llmrouter \
  --chart /tmp/shadeform-blueprint/charts/smart-llmrouter \
  --entitlement /secure/path/approved-entitlement.yaml \
  --valid-for 12h \
  --config /tmp/shadeform-blueprint/config.yaml \
  --env-file /secure/path/local-env.json \
  --image-repository metrum-ai-router \
  --image-tag issue-943
```

The router must have no `nvidia.com/gpu` request. Each vLLM Deployment must have
one. Check all three `rollout status` commands before any smoke.

## 4. Direct and router smokes

For every vLLM Service, run `GET /v1/models` and a non-empty
`POST /v1/chat/completions` with `max_tokens: 16`. Record only HTTP status, served
model ID, and request ID. Do not retain tokens or prompt bodies.

Port-forward the router and verify:

```bash
kubectl -n smart-llmrouter port-forward svc/smart-llmrouter 18080:80 &
TOKEN=$(cat /tmp/shadeform-caller.token)
curl -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18080/readyz
curl -fsS -H "Authorization: Bearer ${TOKEN}" http://127.0.0.1:18080/v1/models
# Repeat non-stream chat for each allowed local group.
# Verify safe attempt metadata names the expected local provider/model only.
# Repeat one model with stream:true and require a completed SSE stream.
curl -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18080/v1/models
# The unauthenticated status must be 401 or 403.
# A caller request for an unallowed model group must return a 4xx.
```

## LRP training note

Shadeform is optional for LRP evidence or configuration D training. Operators may
use any GPU host. When this Shadeform path is used for LRP train/eval, follow
[LRP_GPU_TRAINING.md](./LRP_GPU_TRAINING.md): skip CPU-only training, create the
instance only when the job is ready, and always terminate it when the job ends
or fails.

## Validation, evidence, and teardown

Run before PR creation:

```bash
make test-k8s-nvidia-local-serving
make secret-check
go test ./internal/smartrouterctl ./cmd/metrum-ai-routerctl
```

Attach safe scalars only: worktree/branch, Shadeform SKU, GPU/driver/CUDA,
allocatable GPUs, k3s version/topology, model HF and served IDs, Service DNS,
GPU requests, image tags/digests, smoke HTTP statuses/request IDs, local selected
target names, no-KV-cache confirmation, and `make`/test outcomes.

After a successful run, delete the serving overlay and router release, uninstall
GPU Operator only when this run installed it, terminate the Shadeform instance,
remove kubeconfig/token/runtime files, then remove the merged Git worktree. If a
live prerequisite or smoke fails and cannot be fixed in scope, record safe failure
evidence on the issue, leave the worktree, and do not merge a greenwashed PR.
