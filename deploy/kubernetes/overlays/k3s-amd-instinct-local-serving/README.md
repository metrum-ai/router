# AMD Instinct local-serving overlay

This is the manual-manifest path for issue #952. It mirrors the NVIDIA
local-serving topology without adding an AMD profile to router or blueprint core
logic:

- the router has no GPU request;
- each vLLM/ROCm Deployment requests one `amd.com/gpu`;
- downstream model groups map to in-cluster `*.svc.cluster.local` Services;
- LMCache, Mooncake, llm-d, and cloud LLM upstreams are not part of this profile.

The default Kustomization deploys the three core groups:

| Deployment | Served model ID | Weights |
|---|---|---|
| `vllm-tiny` | `local-tiny` | `Qwen/Qwen3-1.7B` |
| `vllm-chat` | `local-small-chat` | `Qwen/Qwen3.5-4B` |
| `vllm-coder` | `local-small-coder` | `Qwen/Qwen3-8B` |

For milestone 1, apply only `namespace.yaml`,
`serving/vllm-tiny-deployment.yaml`, and `serving/vllm-tiny-service.yaml`.
Apply `networkpolicies.yaml` only after confirming that the cluster CNI enforces
NetworkPolicy.

The checked-in `gpu-operator-values.yaml` selects AMD GPU Operator v1.5.1's
device-plugin mode and leaves the host-owned driver in place. Remove any
standalone AMD device-plugin DaemonSet before installing the operator; never let
DRA and the device plugin manage the same devices.

The accelerator-neutral router configuration is generated from the existing
local-serving blueprint and points to the same `vllm-tiny`, `vllm-chat`, and
`vllm-coder` Service names. Do not apply the generated NVIDIA serving overlay or
NVIDIA GPU Operator values on AMD nodes.

Serving Pods run as UID/GID 10001 with dropped capabilities, RuntimeDefault
seccomp and a read-only root. `/cache` and `/tmp` are ephemeral `emptyDir`
volumes; weights are downloaded again after Pod replacement. Provision enough
ephemeral storage or replace `/cache` with an appropriately owned PVC. AMD
`/dev/kfd` and render-node group ownership varies by node and device plugin:
operators must provide the appropriate supplemental groups or device-plugin
permissions for this non-root UID. Validate device access and model startup on
the target cluster before rollout; offline manifest checks do not exercise a GPU.

Run `make test-k8s-amd-instinct-local-serving` for the offline checks. Follow
`docs/K3S_AMD_INSTINCT_LOCAL_SERVING_E2E.md` for the live procedure. For Dell/AMD
aligned architecture, routing policies, metrics, and OpenTelemetry design, see
`docs/amd-instinct-local-serving-reference-architecture.md`.
