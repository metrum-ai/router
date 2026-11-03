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
| Disk | ≥ 200 GB free for the checkpoint download, the image and the weight cache (`hf-cache` volume) |
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
it receives `404 model_not_found` (the group is not visible to that caller), and
its spend appears as its own row in reports. A missing
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

First boot downloads ~67 GB of checkpoints (Qwen3-32B is a BF16 repository; FP8 is applied at load, not at download) and pulls ~25 GB of image; the model servers report healthy
only after that. `docker compose logs -f vllm-chat` shows progress.

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
