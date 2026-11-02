#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

# Offline CI-safe gate for the manual AMD Instinct local-serving profile.
# It does not contact a cluster, pull models, or require credentials.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OVERLAY="$ROOT/deploy/kubernetes/overlays/k3s-amd-instinct-local-serving"

required=(
  "$OVERLAY/kustomization.yaml"
  "$OVERLAY/gpu-operator-values.yaml"
  "$OVERLAY/networkpolicies.yaml"
  "$OVERLAY/serving/vllm-tiny-deployment.yaml"
  "$OVERLAY/serving/vllm-chat-deployment.yaml"
  "$OVERLAY/serving/vllm-coder-deployment.yaml"
  "$ROOT/docs/K3S_AMD_INSTINCT_LOCAL_SERVING_E2E.md"
)
for path in "${required[@]}"; do
  test -f "$path" || { echo "missing required AMD local-serving artifact: $path" >&2; exit 1; }
done

echo "==> assert pinned ROCm serving image and model matrix"
grep -q 'vllm/vllm-openai-rocm:v0.25.0' "$OVERLAY/serving/vllm-tiny-deployment.yaml"
grep -q 'Qwen/Qwen3-1.7B' "$OVERLAY/serving/vllm-tiny-deployment.yaml"
grep -q 'local-tiny' "$OVERLAY/serving/vllm-tiny-deployment.yaml"
grep -q 'Qwen/Qwen3.5-4B' "$OVERLAY/serving/vllm-chat-deployment.yaml"
grep -q 'local-small-chat' "$OVERLAY/serving/vllm-chat-deployment.yaml"
grep -q 'Qwen/Qwen3-8B' "$OVERLAY/serving/vllm-coder-deployment.yaml"
grep -q 'local-small-coder' "$OVERLAY/serving/vllm-coder-deployment.yaml"

echo "==> assert GPU ownership and profile boundaries"
for deployment in "$OVERLAY"/serving/*-deployment.yaml; do
  grep -q 'amd.com/gpu' "$deployment"
  grep -q 'app.kubernetes.io/component: local-serving' "$deployment"
  if grep -q 'nvidia.com/gpu' "$deployment"; then
    echo "NVIDIA resources are forbidden in AMD serving deployment $deployment" >&2
    exit 1
  fi
done
if grep -R -E 'openrouter\.ai|api\.openai\.com|api\.anthropic\.com|LMCache|Mooncake|llm-d' \
  "$OVERLAY" --include='*.yaml'; then
  echo "cloud upstreams, external KV caches, and llm-d are forbidden in the AMD YAML profile" >&2
  exit 1
fi

echo "==> assert AMD operator device-plugin mode"
grep -q 'enableDevicePlugin: true' "$OVERLAY/gpu-operator-values.yaml"
grep -q 'draDriver:' "$OVERLAY/gpu-operator-values.yaml"
grep -A1 'draDriver:' "$OVERLAY/gpu-operator-values.yaml" | grep -q 'enable: false'
grep -A2 'driver:' "$OVERLAY/gpu-operator-values.yaml" | grep -q 'enable: false'

echo "==> assert local-only router egress policy"
grep -q 'metrum-ai-router-local-vllm-egress' "$OVERLAY/networkpolicies.yaml"
grep -q 'app.kubernetes.io/component: local-serving' "$OVERLAY/networkpolicies.yaml"
grep -q 'port: 8000' "$OVERLAY/networkpolicies.yaml"

echo "==> render kustomize overlay"
rendered=""
if command -v kubectl >/dev/null 2>&1; then
  rendered="$(kubectl kustomize "$OVERLAY")"
elif command -v kustomize >/dev/null 2>&1; then
  rendered="$(kustomize build "$OVERLAY")"
fi
if [[ -n "$rendered" ]]; then
  printf '%s\n' "$rendered" | grep -q 'name: vllm-tiny'
  printf '%s\n' "$rendered" | grep -q 'name: vllm-chat'
  printf '%s\n' "$rendered" | grep -q 'name: vllm-coder'
  printf '%s\n' "$rendered" | grep -q 'amd.com/gpu'
  printf '%s\n' "$rendered" | python3 -c '
import sys
for document in sys.stdin.read().split("---"):
    if "kind: Deployment" in document and "name: metrum-ai-router" in document:
        raise SystemExit("serving overlay must not deploy the router; Helm owns that workload")
'
else
  echo "kubectl and kustomize not found; skipping render check" >&2
fi

echo "OK test-k8s-amd-instinct-local-serving"
