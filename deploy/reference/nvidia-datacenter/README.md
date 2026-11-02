<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Reference configuration: NVIDIA datacenter (80 GB class)

One NVIDIA 80 GB datacenter GPU serving a small always-resident model and a
32B-class chat model, with the router in front and a separate escalation group
to a frontier API. Everything here is deployable as it stands; the only inputs
an operator supplies are the router image, three secrets and (optionally) a
frontier key.

## Hardware

| Item | Requirement |
|---|---|
| GPU | 1× NVIDIA H100 80 GB (H200/A100 80 GB also fit this table; adjust `SIZING.md` if the card differs) |
| Host | x86_64, ≥ 32 vCPU, ≥ 256 GB RAM |
| Disk | ≥ 500 GB free for weights and caches (`hf-cache` volume) |
| Driver | NVIDIA driver with container runtime ≥ 1.16 and `nvidia-container-toolkit` |
| Network | Outbound HTTPS to `huggingface.co` and the frontier API; inbound only to the router port |
| OS | Tested target: Ubuntu 24.04 LTS with Docker Engine 27+ and Compose v2 |

Bill of materials: this configuration assumes an existing single-GPU server and
adds no hardware. It is the tier to use when a team already owns a mid-range
datacenter GPU.

## Images

| Role | Image | Digest |
|---|---|---|
| Model server (both slots) | `vllm/vllm-openai:v0.33.0` | `sha256:d825baf9ce4b1b6bfc5ecc7b79880c7e6748c65a72d697ce3d47607aafa84c6e` |
| Router | operator-supplied `metrum-ai-router:<version>-linux-amd64` | digest of the release you deploy |

Model weights are pinned by Hugging Face commit revision in `images.lock` and
passed as `--revision`, so a redeploy resolves the same weights:

| Slot | Model | Revision |
|---|---|---|
| `local-tiny` | `Qwen/Qwen3-1.7B` (Apache-2.0) | `70d244cc86ccca08cf5af4e1e306ecf908b1ad5e` |
| `local-chat` | `Qwen/Qwen3-32B` (Apache-2.0) | `9216db5781bf21249d130ec9da846c4624c16137` |

Resolve or refresh a pin with `docker buildx imagetools inspect <image>`; update
`images.lock` and `compose.yaml` together, because the checker compares them.

## Model groups

| Group | Strategy | Target | Notes |
|---|---|---|---|
| `local-tiny` | static | `local-tiny` on `vllm-tiny` | smoke tests, cheap classification |
| `local-chat` | static | `local-chat` on `vllm-chat` | default group, Qwen3-32B FP8 |
| `escalate` | static | `frontier-chat` on `frontier` | OpenAI-compatible frontier API, operator key |

`escalate` is a separate group so a caller opts in explicitly, a caller without
it receives `403`, and its spend appears as its own row in reports. A missing
`OPENAI_API_KEY` does not fall back to a local answer; the request fails at the
upstream with the provider's authentication error.

Cost control that applies to these static groups is the caller `quota` in
`config.yaml` (2 000 requests and 4 M tokens per day, 100 M tokens per month,
soft warning at 80 %). `spend_ceiling` does not apply to static groups.

## Deploy

```sh
cd deploy/reference/nvidia-datacenter
mkdir -p state logs config
cp config.yaml config/config.yaml

export ROUTER_IMAGE=metrum-ai-router:<version>-linux-amd64   # required, never latest
export LOCAL_VLLM_API_KEY="$(openssl rand -hex 24)"
export ROUTER_ID_TRANSFORM_KEY="$(openssl rand -hex 64)"
export REFERENCE_ADMIN_PASSWORD_HASH="$(htpasswd -bnBC 12 '' 'change-me' | tr -d ':\n')"
export OPENAI_API_KEY=            # optional; leave empty until escalation is wanted

docker compose config >/dev/null  # interpolation check before anything starts
```

Serving binaries do not apply application DDL, so the reviewed migration job
runs first, while the router is stopped
(see [DOCKER_COMPOSE_INSTALL.md](../../../docs/DOCKER_COMPOSE_INSTALL.md)):

```sh
for action in plan apply verify-serving status; do
  docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate \
    router --action="$action" --driver=sqlite --db=/app/state/usage.sqlite --json
done
```

Then start the stack and generate the demo caller token. The checked-in
`token_sha256` is an all-zero placeholder that cannot authenticate anyone:

```sh
docker compose up -d
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-routerctl \
  router callers generate --config /app/config/config.yaml --id reference-demo \
  --write /app/config/config.yaml
curl -fsS http://127.0.0.1:8080/readyz
```

First boot downloads ~37 GB of weights; the model servers report healthy only
after that. `docker compose logs -f vllm-chat` shows progress.

Teardown: `docker compose down` (add `-v` to drop the weight cache).

The model servers publish on loopback (`TINY_VLLM_PORT`, `CHAT_VLLM_PORT`/`PRIMARY_VLLM_PORT`, `CODER_VLLM_PORT`) so the sweep can measure the
direct-lane baseline; nothing but the router listens on a non-loopback address.

## Measured results

**Pending.** No hardware run is committed for this tier yet, so this section
claims nothing. When the run happens it records, on a named commit:

- the functional suite result (`harness/functional_smoke.sh`), per group;
- the concurrency sweep (`harness/concurrency_sweep.sh`) with p95 latency and
  zero-failure worker counts for the router lane and the direct vLLM lane;
- the exact hardware, driver, image digests and model revisions;
- raw output committed under `results/`.

`SIZING.md` carries the arithmetic estimate that the run is meant to check.

## Actual cost

**Pending.** The tier is priced from the provider's billing API when the run
happens (hours × the invoice's hourly rate), and the number lands here with the
rental window. No cost is claimed before it is incurred; there is no list-rate
arithmetic in this section.

## Limits

- The 80 GB card is shared by both local slots. `SIZING.md` shows the KV pool
  that leaves for `local-chat`; a `local-coder` slot does not fit beside it on
  this tier. Swap the chat model instead (set `CHAT_MODEL_ID` and
  `CHAT_MODEL_REVISION`) — the `local-chat` group name and caller grants stay
  unchanged, so clients do not move.
- FP8 weights are what make a 32B model fit next to the tiny slot. On a card
  without FP8 support, set `VLLM_QUANTIZATION=` (empty) and a smaller
  `CHAT_MODEL_ID`; the sizing table then changes.
- The router binds to `127.0.0.1`. Exposing it to other hosts needs a TLS
  reverse proxy; `allow_insecure_http: true` and `secure_cookies: false` are set
  for loopback only and must be changed behind TLS.
- Admin basic auth is a demo posture. Use OIDC (see
  [ADMIN_OIDC.md](../../../docs/ADMIN_OIDC.md)) before more than one operator
  shares the box.
- Metrics, KV-aware routing, llm-d and multi-node scale-out are out of scope for
  this tier.
- Digest pins were resolved on the date in `images.lock`; they are not updated
  automatically, and a newer upstream tag is a deliberate change.

## Rerun

```sh
deploy/reference/harness/functional_smoke.sh nvidia-datacenter
deploy/reference/harness/concurrency_sweep.sh nvidia-datacenter
python3 scripts/check_reference_configs.py
```

The functional suite needs `ROUTER_BASE_URL`, `ROUTER_TOKEN` and
`LOCAL_VLLM_API_KEY`; the sweep needs `ROUTER_BASE_URL`, a router token and the
direct vLLM URL of the slot under test.
