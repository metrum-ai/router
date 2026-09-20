# Shadeform k3s llm-d compatibility smoke

**Profile:** `nvidia-llmd-compat`  
**Issue:** GitHub #948  
**KV cache:** off — do not install LMCache or Mooncake for this run.

This run validates Metrum AI Router against an in-cluster **llm-d v0.9+** standalone
OpenAI-compatible frontend. Metrum AI Router does **not** install or control llm-d;
llm-d owns replica selection inside its `InferencePool`.

Reuse Shadeform host provisioning, k3sup, and GPU Operator steps from
[SHADEFORM_NVIDIA_LOCAL_SERVING_E2E.md](SHADEFORM_NVIDIA_LOCAL_SERVING_E2E.md).
This document only covers the llm-d delta.

## Preconditions

Same as the vLLM local-serving runbook: `SHADEFORM_API_KEY` from `ops.env.json`,
`LICENSE_SIGNING_KEY_FILE`, `k3sup`, `kubectl`, `helm`, and a router image with an
immutable tag.

**GPU budget:** at least **two** schedulable NVIDIA GPUs on the node — one for the
vLLM model server and one for llm-d control-plane/proxy pods. The router Pod must
not request `nvidia.com/gpu`.

Offline gate before live work:

```bash
make test-k8s-nvidia-llmd-compat
```

Intent: `deploy/kubernetes/intents/shadeform-nvidia-llmd-compat.example.yaml`

## 1. Blueprint render

```bash
INTENT=deploy/kubernetes/intents/shadeform-nvidia-llmd-compat.example.yaml
OUT=/tmp/shadeform-llmd-blueprint
go run ./cmd/metrum-ai-routerctl blueprint render --intent "$INTENT" --out "$OUT"
```

Confirm `config.yaml` references only `*.svc.cluster.local` upstreams and exposes
`local-llmd-chat` only.

## 2. Cluster baseline

Follow sections 1–3 of [SHADEFORM_NVIDIA_LOCAL_SERVING_E2E.md](SHADEFORM_NVIDIA_LOCAL_SERVING_E2E.md)
(Shadeform host, k3s, GPU Operator, namespace `smart-llmrouter`).

## 3. vLLM model server (llm-d backend)

Apply the generated model-server manifests from blueprint output:

```bash
kubectl apply -f "$OUT/overlays/nvidia-llmd-compat/serving/vllm-llmd-backend-deployment.yaml"
kubectl apply -f "$OUT/overlays/nvidia-llmd-compat/serving/vllm-llmd-backend-service.yaml"
kubectl wait --for=condition=Available deployment/vllm-llmd-backend -n smart-llmrouter --timeout=45m
```

Direct smoke (vLLM must advertise the router model id `local-llmd-chat` via `--served-model-name`):

```bash
kubectl run curl-smoke --rm -i --restart=Never -n smart-llmrouter --image=curlimages/curl:8.5.0 -- \
  curl -fsS "http://vllm-llmd-backend:8000/v1/models"
```

## 4. Install Gateway API Inference Extension CRDs

Install the **v1** InferencePool CRDs required by llm-d **v0.9+** before the Helm release:

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/download/v1.5.0/v1-manifests.yaml
```

Do not add a Metrum AI Router controller for llm-d.

## 5. Install llm-d standalone

After CRDs are Ready, install from the generated artifacts:

```bash
bash "$OUT/overlays/nvidia-llmd-compat/llm-d/install.example.sh"
kubectl -n smart-llmrouter rollout status deploy/llm-d-local-epp --timeout=20m
```

Standalone mode exposes the OpenAI-compatible frontend on Service **`llm-d-local-epp` port `8081`** (not port 8000). Direct llm-d smoke:

```bash
kubectl run curl-llmd --rm -i --restart=Never -n smart-llmrouter --image=curlimages/curl:8.5.0 -- \
  curl -fsS "http://llm-d-local-epp:8081/v1/models"
```

## 6. Metrum AI Router

Build/import the router image, render Helm values from blueprint output, install
with `helm upgrade --install` after creating the runtime Secret (`config.yaml` + `env.json`), and wait for `/readyz`.

Router smokes (in-cluster or port-forward):

- `GET /readyz` → 200
- `GET /v1/models` → only `local-llmd-chat`
- `POST /v1/chat/completions` non-stream → 200, non-empty content
- `POST /v1/chat/completions` `stream:true` → SSE completes
- Unauthorized → 401; disallowed model → 403

Usage rows must show a local in-cluster target, not a cloud provider.

## 7. Teardown

Same cleanup contract as #943:

- Uninstall Metrum AI Router Helm release
- Delete serving manifests / llm-d release
- Uninstall GPU Operator if installed only for this test
- Terminate the Shadeform instance
- Remove local kubeconfig/token artifacts

## Non-goals

- Mooncake / LMCache / disaggregated prefill-decode topology
- Router `InferencePool` auto-discovery schema
- AMD GPU path (#946)
