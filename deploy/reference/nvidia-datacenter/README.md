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

Mint the caller before the stack starts; that is the step that needs the writable
mount below. The router reads its configuration once at startup and does not watch
the file — verified by renaming a group in `config.yaml` while a router was running:
`/v1/models` kept the old set until the process restarted. Because the configuration
*directory* is mounted, a rewrite is visible inside the container, so
`docker compose up -d --force-recreate router` is enough to apply it (with a
single-file mount the container stayed pinned to the original file and a rewrite
could be missed even across a restart). A caller `allow` list that names a group you
remove stops the router from starting. The token file goes to `state/`, which is
mounted as a directory:

```sh
# The config directory is mounted read-only for the router. Override it read-write
# for this one run: the writer keeps a timestamped backup beside the file and then
# renames its temp file onto it, so a read-only mount fails even with the backup
# moved. A single-file mount could not be overridden this way, which is why
# compose.yaml mounts the directory.
docker compose run --rm --no-deps -v "$PWD/config:/app/config:rw" \
  --entrypoint /app/bin/metrum-ai-routerctl \
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

Run of record: [`results/2026-11-03-c2f077fece7d/`](results/2026-11-03-c2f077fece7d/)
on one rented **H100 PCIe 80 GB** (driver 570.195.03, commit `c2f077fe`), deployed
from this tier's `compose.yaml` unmodified with the router co-located.

| Item | Result |
|---|---|
| Deployment as committed | pass: both model servers healthy, `/readyz` 200, caller token authenticates |
| Functional suite | **8 passed, 0 failed**, 2 skipped (escalation not run on rented hardware; the restricted-caller check hit #755) |
| Admin UI auth | pass: `200` with credentials, `401` without |
| Concurrency sweep | **complete**: 1/8/32 workers × streaming and non-streaming × router and direct = 3 936 samples, zero failed requests |
| Sustained concurrency | **32 workers**, the largest cell, at p95 ≤ 8 000 ms |
| Suite token / group grants | not proven by the suite; `404 model_not_found` for a caller without the group was verified by hand (#755 dropped the harness caller when a second caller was minted) |

### Router overhead against the direct lane

256 output tokens, ~1 700-token prompt, temperature 0, three repetitions per cell.

| Workers | Lane | p50 | p95 | p99 |
|---|---|---|---|---|
| 1 | direct | 5 269 ms | 5 270 ms | 5 270 ms |
| 1 | router | 5 272 ms | 5 273 ms | 5 274 ms |
| 8 | direct | 5 695 ms | 5 698 ms | 5 702 ms |
| 8 | router | 5 695 ms | 5 702 ms | 5 720 ms |
| 32 | direct | 6 467 ms | 6 502 ms | 6 532 ms |
| 32 | router | 6 472 ms | 6 511 ms | 6 541 ms |

Streaming (time to first content token): p50/p95 **34/36 ms** at 1 worker, 65/73 ms at
8, and **77/129 ms** at 32 workers through the router (direct: 77/122 ms). The router's
added latency is **under 10 ms at every cell** — 9 ms at p95 with 32 concurrent
256-token requests.

These are one card, one commit, one context length. They say nothing about a
different GPU, a longer context or a different model revision.

Raw samples, the stack logs and the image digests are in the shared artifact library,
snapshot `dd5889b2` (product `router`, project `v47-package`, task
`433-h100-concurrency`).

## Actual cost

**estimate (hours × list rate), invoice pending: 1.317 h × $2.50/h = $3.29** for this
run, plus $5.18 for the earlier window on the same tier. The provider exposes no
per-instance billing endpoint, so the invoiced number has to come from the account
invoice; the arithmetic and the exact window are in
[`results/2026-11-03-c2f077fece7d/cost.md`](results/2026-11-03-c2f077fece7d/cost.md).

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

## Rootless Podman

AMD development platforms often run Podman, rootless by default. A hardware run found
that the `chown -R 65532:65532` above fails there: in a rootless container, uid 65532
is a *mapped* host uid, so the host directories do not belong to the identity the
container writes as. It also found that a caller mint after the stack is up fails on
the read-only configuration mount.

The remedies below are **not yet verified on hardware** (the same run stopped before
it could apply them). Until a rerun proves them, treat them as the starting point:

```sh
podman unshare chown -R 65532:65532 config state logs   # the ownership fix, in the namespace
```

or run the documented commands with `sudo`, i.e. rootful Podman, where the plain
`chown` above is correct. For a late caller mint, override the config mount
read-write for that one run:
`docker compose run --rm --no-deps -v "$PWD/config:/app/config:rw" ...`.

## Rerun

```sh
deploy/reference/harness/functional_smoke.sh nvidia-datacenter
deploy/reference/harness/concurrency_sweep.sh nvidia-datacenter
python3 scripts/check_reference_configs.py
```

The functional suite needs `ROUTER_BASE_URL`, `ROUTER_TOKEN` and
`LOCAL_VLLM_API_KEY`; the sweep needs `ROUTER_BASE_URL`, a router token and the
direct vLLM URL of the slot under test.
