<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Reference configuration: AMD Strix Halo edge

One Strix Halo mini-workstation (Ryzen AI MAX+ 395, 128 GB unified memory)
serving a small always-resident model and a 30B-class sparse model on the
official upstream vLLM ROCm image, with the router in front and an escalation group for
work that should not stay on the desk.

## Hardware

| Item | Requirement |
|---|---|
| System | 1× AMD Ryzen AI MAX+ 395 (Strix Halo, gfx1151, RDNA 3.5) with 128 GB unified LPDDR5X |
| GPU memory window | `amdgpu` GTT window large enough for ~110 GB (see `SIZING.md`); BIOS/firmware with a small VRAM carve-out and the rest reachable through GTT |
| Disk | ≥ 150 GB free for the checkpoint download, the image and the weight cache (`hf-cache` volume) |
| Driver | `amdgpu` kernel driver compatible with the container's ROCm runtime for gfx1151; `/dev/kfd` and `/dev/dri` present |
| Runtime | Docker Engine 27+ and Compose v2; the container gets `/dev/kfd`, `/dev/dri`, and the `video` and `render` groups |
| Network | Outbound HTTPS to `huggingface.co` and the escalation endpoint; inbound only to the router port |
| OS | Target: Ubuntu 24.04 LTS; the container supplies its ROCm userspace |
| Power | Mini-workstation envelope; no datacenter cooling or PDU work |

Bill of materials: the Strix Halo mini-workstation (128 GB configuration — the
64 GB variant does not fit the 30B-class slot), one NVMe drive, a network drop.

## Images

| Role | Image | Digest |
|---|---|---|
| Model server (both slots) | `vllm/vllm-openai-rocm:v0.33.0` | `sha256:4fcd39b758b660c4e9c345c021e2ea8f0ccdb31e3c32507d676cc3005ae83fba` |
| Router | operator-supplied `metrum-ai-router:<version>-linux-amd64` | digest of the release you deploy |

The official upstream image carries vLLM 0.33.0 and is outside the affected
range of GHSA-v97p-wpxp-px84. The pin moved from v0.25.0 for security
advisories; hardware revalidation on v0.33.0 is pending.

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

The checked-in `token_hash` value `REPLACE_WITH_GENERATED_CALLER_TOKEN_HASH` is an intentionally invalid placeholder; generate a real caller token before serving and write the generated caller entry into this configuration.

```sh
cd deploy/reference/amd-strix-halo-edge
mkdir -p state logs config
cp config.yaml config/config.yaml
# The runtime image runs as uid/gid 65532, so the directories it writes must
# belong to that identity (see BINARY_INSTALL.md).
sudo chown -R 65532:65532 config state logs
# Numeric host GIDs keep device access aligned with the host device-node owners.
export RENDER_GID="$(stat -c %g /dev/dri/renderD128)"
export VIDEO_GID="$(stat -c %g /dev/dri/card0)"

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
# The configuration is mounted read-only for the router, and a `compose run -v`
# with the same target does NOT replace that mount (verified on Compose 2.26.1 and
# v5.0.1), so compose.yaml takes the mode from ROUTER_CONFIG_MOUNT_MODE and this
# step sets it to rw.
ROUTER_CONFIG_MOUNT_MODE=rw docker compose run --rm --no-deps \
  --entrypoint /app/bin/metrum-ai-routerctl \
  router callers generate \
  --owner-user reference-operator --project reference --env demo --key k1 \
  --allow local-tiny,local-chat,escalate \
  --config /app/config/config.yaml --write --token-out /app/state/demo.token
chmod 0600 state/demo.token
```

Serving binaries do not apply application DDL, so the reviewed migration job runs
next, while the router is stopped
(see [BINARY_INSTALL.md](../../../docs/BINARY_INSTALL.md)).
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

First boot downloads ~64 GB of checkpoints and pulls ~30 GB of image; the model servers report healthy
only after that. `docker compose logs -f vllm-primary` shows progress.

Teardown: `docker compose down` (add `-v` to drop the weight cache).

The model servers publish on loopback (`TINY_VLLM_PORT`, `CHAT_VLLM_PORT`/`PRIMARY_VLLM_PORT`, `CODER_VLLM_PORT`) so the sweep can measure the
direct-lane baseline; nothing but the router listens on a non-loopback address.

## Measured results

**One hardware run so far, at commit `dd1d5aad` (before the edge bundle's container-listen
fix, which this tier does not use).** That run used image v0.25.0. The checked-in pin moved
to v0.33.0 for security advisories; hardware revalidation on v0.33.0 is pending. The
functional suite passed 8/0/2 (8 pass, 0 fail, 2 skipped) on the Strix Halo box. The
concurrency sweep did **not** reach the 8 s target on
this profile: the default `local-chat` sweep sustained **0 workers at the 8 s target**, and
a single worker measured **p95 9 494 ms through the router against 9 492 ms direct** — a
2 ms difference, so the miss is the model-server profile and the hardware, not routing
overhead. The 8 s target in `SIZING.md` is unchanged: it is the bar this tier is meant to
clear, and on this box the measured profile does not clear it at 1 worker. A profile that
does, or a documented decision to ship the slower one, is still pending; this paragraph
exists so nobody reads the target as a measurement.

**Diagnostic only, no claim: the `local-tiny` slot.** A separate sweep of the tiny slot on
the same box produced partial numbers instead of a summary, so treat them as a diagnostic
of that slot and not as a router or tier result. At 1 worker, p95 was about **6.8 s**
(direct 6 771.7 ms, through the router 6 779.8 ms); at 8 workers about **10.3 s** (direct
10 347.4 / routed 10 301.9); direct and routed agreed within **1%** in both cells, so the
latency is the model server's, not the router's. At 32 workers - above that slot's
configured `max-num-seqs` - the sweep aborted with a broken pipe on the direct lane and
committed no samples, so nothing is claimed for that point. The slot is a smoke and
classification slot: do not plan chat concurrency on these numbers.

Raw output for the run is held with the run records, not in this directory. The run records the
functional suite per group, the concurrency sweep (p95 latency and the
zero-failure worker count for the routed and direct lanes), the exact board,
BIOS/GTT setting, ROCm and kernel versions, image digest and model revisions, and
raw output under `results/`. `SIZING.md` holds the arithmetic the run is meant to
check.

## Actual cost

**Pending.** This tier's run is on hardware that is arranged rather than rented,
so the cost line will carry the agreed hourly rate and the hours used, or
`estimate (hours × list rate), invoice pending` while the invoice is
outstanding. No cost is claimed before it is known.

## Limits

- The vLLM 0.33.0 image pin moved for security advisories. Hardware revalidation on this image is pending.
- BF16 only: gfx1151 does not have the FP8 path this configuration would need for
  a second 30B-class slot, so there is no `local-coder` group.
- Unified memory is shared with the desktop and the CPU. A box also used for
  other work sustains fewer agents; the run must be measured on an idle box and
  say so.
- The measured 1-worker p95 is ~9.5 s, above the 8 s target in `SIZING.md` (see
  "Measured results"). Plan for agents that tolerate that latency, or size a
  different profile, until a faster one is measured here.
- The router binds to `127.0.0.1` with `allow_insecure_http: true` and
  `secure_cookies: false` for a loopback-only posture. Put a TLS proxy in front
  and flip both before other hosts reach it; use OIDC
  ([ADMIN_OIDC.md](../../../docs/ADMIN_OIDC.md)) for more than one operator.
- Anthropic Messages inbound to the local groups is not declared for these models
  (no `request_shape_support` evidence), so Claude-Code-style clients are out of
  scope until that is validated on this box.
- The vLLM 0.33.0 image has not been revalidated on this box yet.
- Digest pins were resolved on the date in `images.lock` and are not updated
  automatically.

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
deploy/reference/harness/functional_smoke.py amd-strix-halo-edge
DIRECT_VLLM_PORT=8001 deploy/reference/harness/concurrency_sweep.py amd-strix-halo-edge
python3 scripts/check_reference_configs.py
```
