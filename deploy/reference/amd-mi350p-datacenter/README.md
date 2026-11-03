<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Reference configuration: AMD MI350P datacenter

One AMD Instinct MI350P (144 GB HBM3E) running a tiny slot, a 32B chat model and
a sparse coder model on the official ROCm vLLM image, with the router in front
and an escalation group off the box.

## Hardware

| Item | Requirement |
|---|---|
| GPU | 1× AMD Instinct MI350P, 144 GB HBM3E, PCIe dual-slot (gfx950) |
| Host | x86_64, ≥ 32 vCPU, ≥ 256 GB RAM, PCIe 4.0 x16 slot with adequate airflow |
| Disk | ≥ 300 GB free for the checkpoint downloads, the images and the weight cache (`hf-cache` volume) |
| Driver | `amdgpu` kernel driver with ROCm kernel support for gfx950, `/dev/kfd` and `/dev/dri` present |
| Runtime | Docker Engine 27+ and Compose v2; the container gets `/dev/kfd`, `/dev/dri`, and the `video` and `render` groups |
| Network | Outbound HTTPS to `huggingface.co` and the escalation endpoint; inbound only to the router port |
| OS | Tested target: Ubuntu 24.04 LTS with ROCm 7.13 userspace |

Bill of materials: the MI350P card, a host with a free dual-slot PCIe position
and the power/cooling budget for it, and one NVMe drive. This is a PCIe card, not
an OAM platform: there is no 8-GPU fabric assumption anywhere in this tier.

## Images

| Role | Image | Digest |
|---|---|---|
| Model server (all three slots) | `rocm/vllm:rocm7.13.0_gfx950-dcgpu_ubuntu24.04_py3.13_pytorch_2.10.0_vllm_0.19.1` | `sha256:3b20f0a32530aefa2416aa30f64011fdcab00c44676a915044b9a17379342fcc` |
| Router | operator-supplied `metrum-ai-router:<version>-linux-amd64` | digest of the release you deploy |

This is AMD's official ROCm vLLM image for gfx950. The architecture is part of
the tag, so the pin cannot silently resolve to a build for a different GPU
family. The vLLM version in this image (0.19.1) is older than the NVIDIA tier's
0.33.0; that is a property of AMD's published tag list, not a choice, and it is
noted again under Limits.

| Slot | Model | Revision |
|---|---|---|
| `local-tiny` | `Qwen/Qwen3-1.7B` (Apache-2.0) | `70d244cc86ccca08cf5af4e1e306ecf908b1ad5e` |
| `local-chat` | `Qwen/Qwen3-32B` (Apache-2.0) | `9216db5781bf21249d130ec9da846c4624c16137` |
| `local-coder` | `Qwen/Qwen3-Coder-30B-A3B-Instruct` (Apache-2.0) | `b2cff646eb4bb1d68355c01b18ae02e7cf42d120` |

## Model groups

| Group | Strategy | Target | Notes |
|---|---|---|---|
| `local-tiny` | static | `local-tiny` on `vllm-tiny` | smoke tests, cheap classification |
| `local-chat` | static | `local-chat` on `vllm-chat` | default group, Qwen3-32B FP8 |
| `local-coder` | static | `local-coder` on `vllm-coder` | sparse MoE coder, FP8 |
| `escalate` | static | `frontier-chat` on `frontier` | OpenAI-compatible frontend API, operator key |

Cost control on these static groups is the caller `quota` (2 000 requests and
4 M tokens per day, 100 M tokens per month, soft warning at 80 %).

## Deploy

```sh
cd deploy/reference/amd-mi350p-datacenter
mkdir -p state logs config
cp config.yaml config/config.yaml
# The runtime image runs as uid/gid 65532, so the directories it writes must
# belong to that identity (see DOCKER_COMPOSE_INSTALL.md).
sudo chown -R 65532:65532 config state logs

export ROUTER_IMAGE=metrum-ai-router:<version>-linux-amd64   # required, never latest
export LOCAL_VLLM_API_KEY="$(openssl rand -hex 24)"
export ROUTER_ID_TRANSFORM_KEY="$(openssl rand -hex 64)"
export REFERENCE_ADMIN_PASSWORD_HASH="$(htpasswd -bnBC 12 '' 'change-me' | tr -d ':\n')"
export OPENAI_API_KEY=            # optional; leave empty until escalation is wanted

docker compose config >/dev/null  # interpolation check before anything starts
```

Mint the caller before the stack starts. The router mounts `config.yaml` as a
single file, so rewriting it under a running router would leave that container
reading the old file (the request would fail with `401` until a restart). The
token file goes to `state/`, which is mounted as a directory:

```sh
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-routerctl \
  router callers generate \
  --owner-user reference-operator --project reference --env demo --key k1 \
  --allow local-tiny,local-chat,local-coder,escalate \
  --config /app/config/config.yaml --write --token-out /app/state/demo.token
chmod 0600 state/demo.token
```

Serving binaries do not apply application DDL, so the reviewed migration job runs
next, while the router is stopped
(see [DOCKER_COMPOSE_INSTALL.md](../../../docs/DOCKER_COMPOSE_INSTALL.md)).
`verify-serving` refuses to pass while a bound data job is pending, so the job is
resumed at checkpoint 0 first:

```sh
for action in plan apply; do
  docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate \
    router --action="$action" --driver=sqlite --db=/app/state/usage.sqlite --json
done
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate \
  router --action=resume --job=historical-usage-validation-v1 --checkpoint-ordinal=0 \
  --driver=sqlite --db=/app/state/usage.sqlite --json
for action in verify-serving status; do
  docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate \
    router --action="$action" --driver=sqlite --db=/app/state/usage.sqlite --json
done
```

Then start the stack and check the surface:

```sh
docker compose up -d
curl -fsS http://127.0.0.1:8080/readyz
TOKEN="$(cat state/demo.token)"
curl -fsS -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/v1/models
```

First boot downloads ~125 GB of checkpoints (both large models are BF16 repositories; FP8 is applied at load, not at download) and pulls ~30 GB of image; the model servers report healthy
only after that. `docker compose logs -f vllm-chat` shows progress.

Teardown: `docker compose down` (add `-v` to drop the weight cache).

The model servers publish on loopback (`TINY_VLLM_PORT`, `CHAT_VLLM_PORT`/`PRIMARY_VLLM_PORT`, `CODER_VLLM_PORT`) so the sweep can measure the
direct-lane baseline; nothing but the router listens on a non-loopback address.

## Measured results

**Pending.** No hardware run is committed for this tier yet. The run records the
functional suite per group, the concurrency sweep (p95 latency and the
zero-failure worker count for the routed and direct lanes), the exact card, ROCm
and driver versions, image digest and model revisions, and raw output under
`results/`. `SIZING.md` holds the arithmetic the run is meant to check.

## Actual cost

**Pending.** This tier's run is on hardware that is arranged rather than rented,
so the cost line will carry the agreed hourly rate and the hours used, or
`estimate (hours × list rate), invoice pending` while the invoice is
outstanding. No cost is claimed before it is known.

## Limits

- The pinned ROCm image carries vLLM 0.19.1 while the NVIDIA tier pins 0.33.0.
  Feature differences between those versions apply here; this tier does not claim
  parity with the NVIDIA tier's vLLM features.
- FP8 weights make three slots fit on one card. `VLLM_QUANTIZATION=` (empty)
  switches to BF16 and no longer fits this memory plan.
- One card is one bandwidth budget: the per-slot concurrency estimates do not
  add up under mixed traffic.
- The router binds to `127.0.0.1` with `allow_insecure_http: true` and
  `secure_cookies: false` for a loopback-only posture. Put a TLS proxy in front
  and flip both before other hosts reach it; use OIDC
  ([ADMIN_OIDC.md](../../../docs/ADMIN_OIDC.md)) for more than one operator.
- Anthropic Messages inbound to the local groups is not declared for these models
  (no `request_shape_support` evidence), so Claude-Code-style clients are out of
  scope until that is validated on this card.
- Multi-card scale-out, MI350X/MI355X OAM platforms and llm-d are out of scope for
  this tier.
- Digest pins were resolved on the date in `images.lock` and are not updated
  automatically.

## Rootless Podman

AMD development platforms often run Podman, rootless by default. The `chown -R
65532:65532` above is correct for rootful Docker and wrong for rootless Podman,
where container uid 65532 is a *mapped* host uid: use
`podman unshare chown -R 65532:65532 config state logs` instead, or run the stack
rootful. Mint the caller **before** the stack starts — the configuration is mounted
as a single read-only file, so a later `--write` has nothing to write beside it; if
you must mint afterwards, mount the configuration directory read-write for that step or
point `ROUTER_CONFIG_BACKUP_DIR` at a writable directory.

## Rerun

```sh
deploy/reference/harness/functional_smoke.py amd-mi350p-datacenter --group local-coder
DIRECT_VLLM_PORT=8001 deploy/reference/harness/concurrency_sweep.py amd-mi350p-datacenter
python3 scripts/check_reference_configs.py
```
