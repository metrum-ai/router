<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Reference configuration: NVIDIA GB10 edge

One GB10-class desktop system (128 GB unified memory, arm64) serving a small
always-resident model and a 30B-class sparse model, with the router in front and
an escalation group for the work that should not stay on the desk.

## Hardware

| Item | Requirement |
|---|---|
| System | 1× NVIDIA GB10 system, 128 GB unified LPDDR5X, arm64 CPU |
| Disk | ≥ 150 GB free for the checkpoint download, the image and the weight cache (`hf-cache` volume) |
| Driver | NVIDIA driver and container runtime shipped with the system image, `nvidia-container-toolkit` installed |
| Network | Outbound HTTPS to `huggingface.co` and the escalation endpoint; inbound only to the router port |
| OS | Tested target: Ubuntu 24.04 LTS arm64 with Docker Engine 27+ and Compose v2 |
| Power | Desktop power envelope; no datacenter cooling or PDU work |

Bill of materials: the GB10 system itself, one NVMe drive of ≥ 200 GB free, a
network drop. No datacenter hardware is added.

## Images

| Role | Image | Digest |
|---|---|---|
| Model server (both slots) | `vllm/vllm-openai:v0.33.0` (arm64 member of the manifest list) | `sha256:d825baf9ce4b1b6bfc5ecc7b79880c7e6748c65a72d697ce3d47607aafa84c6e` |
| Router | operator-supplied `metrum-ai-router:<version>-linux-arm64` | digest of the release you deploy |

| Slot | Model | Revision |
|---|---|---|
| `local-tiny` | `Qwen/Qwen3-1.7B` (Apache-2.0) | `70d244cc86ccca08cf5af4e1e306ecf908b1ad5e` |
| slot `local-chat` | `Qwen/Qwen3-30B-A3B-Instruct-2507` (Apache-2.0) | `0d7cf23991f47feeb3a57ecb4c9cee8ea4a17bfe` |

## Model groups

| Group | Strategy | Target | Notes |
|---|---|---|---|
| `local-tiny` | static | `local-tiny` on `vllm-tiny` | smoke tests, cheap classification |
| `local-chat` | static | `local-chat` on `vllm-primary` | default group, 30B-class sparse MoE |
| `escalate` | static | `frontier-chat` on `frontier` | OpenAI-compatible datacenter or frontier API, operator key |

The primary slot is named for its role, so swapping the weights is one compose
variable (`PRIMARY_MODEL_ID`, `PRIMARY_MODEL_REVISION`) and the `local-chat`
group, its grants and every client stay as they are. Use
`Qwen/Qwen3-Coder-30B-A3B-Instruct` for the coder variant.

Cost control on these static groups is the caller `quota` (1 000 requests and
2 M tokens per day, 40 M tokens per month, soft warning at 80 %).

## Deploy

```sh
cd deploy/reference/nvidia-gb10-edge
mkdir -p state logs config
cp config.yaml config/config.yaml
# The runtime image runs as uid/gid 65532, so the directories it writes must
# belong to that identity (see DOCKER_COMPOSE_INSTALL.md).
sudo chown -R 65532:65532 config state logs

export ROUTER_IMAGE=metrum-ai-router:<version>-linux-arm64   # required, never latest
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
  --allow local-tiny,local-chat,escalate \
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

First boot downloads ~64 GB of checkpoints and pulls ~25 GB of image; the model servers report healthy
only after that. `docker compose logs -f vllm-primary` shows progress.

Teardown: `docker compose down` (add `-v` to drop the weight cache).

The model servers publish on loopback (`TINY_VLLM_PORT`, `CHAT_VLLM_PORT`/`PRIMARY_VLLM_PORT`, `CODER_VLLM_PORT`) so the sweep can measure the
direct-lane baseline; nothing but the router listens on a non-loopback address.

## Measured results

Check of record: [`results/2026-11-03-24a6bbfe9c/`](results/2026-11-03-24a6bbfe9c/)
on a rented **GB10 container** (Vast). The pinned arm64 member of
`vllm/vllm-openai:v0.33.0` loaded on GB10, captured CUDA graphs, served
`local-tiny`, and the tier's group answered through the router.

| Item | Result |
|---|---|
| Pinned arm64 image on GB10 | pass: vLLM 0.33.0, FlashAttention 2, `/health` 200, exact token accounting |
| `local-tiny` group through the router | pass: functional suite 6 passed, 0 failed, 3 skipped |
| `local-chat` slot (Qwen3-30B-A3B) | **not proven**: one host initialised the engine but never came up within 15 min; another had no egress |
| Co-located compose deployment | **not possible on that provider**: Vast GPU instances are unprivileged containers |
| Concurrency and latency | **not measured**: the direct lane needs loopback |

So the "kernel support unverified" limit below is now resolved for the tiny slot on
GB10 and remains open for the 30B slot.

## Actual cost

**estimate (hours × list rate), invoice pending: about $0.25** across four short
Vast containers, all destroyed; windows and rates are in
[`results/2026-11-03-24a6bbfe9c/cost.md`](results/2026-11-03-24a6bbfe9c/cost.md).

## Limits

- **Not validated on GB10 hardware.** This tier ships with a **functional check
  only**: the pinned arm64 image was run on a GB10 container offered by a rental
  provider, where it loaded and served the `local-tiny` slot through the router. No
  GB10 machine has run this tier's `compose.yaml`, no 30B `local-chat` slot has
  served from a GB10, and no latency or concurrency has been measured on this
  hardware. Hardware validation is tracked for v5.0.0; until then, treat every
  number in `SIZING.md` as arithmetic and the tier as unproven beyond that one
  functional check.
- Unified memory is shared with the CPU and the container runtime;
  `SIZING.md` assumes 85 % of the pool for the two slots.
- One 30B-class model fits beside the tiny slot. Two do not, so this tier has no
  `local-coder` group.
- The router binds to `127.0.0.1` with `allow_insecure_http: true` and
  `secure_cookies: false` for a loopback-only posture. Put a TLS proxy in front
  and flip both before other hosts reach it. Basic auth is a demo posture; use
  OIDC ([ADMIN_OIDC.md](../../../docs/ADMIN_OIDC.md)) for more than one operator.
- Anthropic Messages inbound to the local groups is not declared for these
  models (no `request_shape_support` evidence), so Claude-Code-style clients are
  out of scope until that is validated on this box.
- Digest pins were resolved on the date in `images.lock` and are not updated
  automatically.

## Rerun

```sh
deploy/reference/harness/functional_smoke.py nvidia-gb10-edge
DIRECT_VLLM_PORT=8001 deploy/reference/harness/concurrency_sweep.py nvidia-gb10-edge
python3 scripts/check_reference_configs.py
```
