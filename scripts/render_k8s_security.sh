#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
# Render complete overlays and a representative Linkerd patch; never scan fragments.
set -euo pipefail

if [[ $# != 1 ]]; then
  echo "usage: $0 EMPTY_OUTPUT_DIRECTORY" >&2
  exit 2
fi
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="$1"
mkdir -p "$out"
if [[ -n "$(find "$out" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
  echo "output directory must be empty" >&2
  exit 2
fi
out="$(cd "$out" && pwd)"
command -v kustomize >/dev/null
count=0
while IFS= read -r -d '' file; do
  dir="${file%/*}"
  name="${dir#"$root/deploy/kubernetes/overlays/"}"
  name="${name//\//__}"
  if [[ -f "$out/$name.yaml" ]]; then
    echo "duplicate rendered name: $name" >&2
    exit 2
  fi
  kustomize build "$dir" > "$out/$name.yaml"
  count=$((count + 1))
done < <(find "$root/deploy/kubernetes/overlays" -type f \( -name kustomization.yaml -o -name kustomization.yml -o -name Kustomization \) -print0 | sort -z)
if [[ $count -eq 0 ]]; then
  echo "no Kubernetes overlays found" >&2
  exit 2
fi
# The Linkerd example is a patch, not an independently deployable resource.
# Build a temporary overlay against the real base instead of scanning the patch.
if [[ -e "$out/linkerd-example.yaml" || -e "$out/Dockerfile" ]]; then
  echo "reserved rendered filename already exists" >&2
  exit 2
fi
linkerd="$(mktemp -d)"
cp -R "$root/deploy/kubernetes/base" "$linkerd/base"
cp "$root/deploy/kubernetes/bootstrap/tenant-router-linkerd-injection-patch.example.yaml" "$linkerd/linkerd-patch.yaml"
printf '%s\n' 'apiVersion: kustomize.config.k8s.io/v1beta1' 'kind: Kustomization' 'resources:' '  - base' 'patches:' '  - path: linkerd-patch.yaml' > "$linkerd/kustomization.yaml"
kustomize build "$linkerd" > "$out/linkerd-example.yaml"
cp "$root/deploy/github-runners-router/Dockerfile" "$out/Dockerfile"
echo "Rendered $count overlays, the composed Linkerd example, and runner Dockerfile into $out"
