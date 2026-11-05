<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Reference configuration: AMD Strix Halo edge

One Strix Halo mini-workstation (Ryzen AI MAX+ 395, 128 GB unified memory)
serving a small always-resident model and a 30B-class sparse model on the
official ROCm vLLM image, with the router in front and an escalation group for
work that should not stay on the desk.

## Hardware

| Item | Requirement |
|---|---|
| System | 1× AMD Ryzen AI MAX+ 395 (Strix Halo, gfx1151, RDNA 3.5) with 128 GB unified LPDDR5X |
| GPU memory window | `amdgpu` GTT window large enough for ~110 GB (see `SIZING.md`); BIOS/firmware with a small VRAM carve-out and the rest reachable through GTT |
| Disk | ≥ 150 GB free for the checkpoint download, the image and the weight cache (`hf-cache` volume) |
| Driver | `amdgpu` kernel driver with ROCm 7.x userspace for gfx1151; `/dev/kfd` and `/dev/dri` present |
| Runtime | Docker Engine 27+ and Compose v2; the container gets `/dev/kfd`, `/dev/dri`, and the `video` and `render` groups |
| Network | Outbound HTTPS to `huggingface.co` and the escalation endpoint; inbound only to the router port |
| OS | Tested target: Ubuntu 24.04 LTS with ROCm 7.13 userspace |
| Power | Mini-workstation envelope; no datacenter cooling or PDU work |

Bill of materials: the Strix Halo mini-workstation (128 GB configuration — the
64 GB variant does not fit the 30B-class slot), one NVMe drive, a network drop.

## Images

| Role | Image | Digest |
|---|---|---|
| Model server (both slots) | `rocm/vllm:rocm7.13.0_gfx1151_ubuntu24.04_py3.13_pytorch_2.10.0_vllm_0.19.1` | `sha256:394194d36edcf9b36bcb563e143b21b80e64e7d04f33a447b448c0c0c00c04a8` |
| Router | operator-supplied `metrum-ai-router:<version>-linux-amd64` | digest of the release you deploy |

The tag names gfx1151, so the pin cannot resolve to a build for another GPU
family. It carries vLLM 0.19.1, which is older than the NVIDIA tier's pin; see
Limits.

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
cd deploy/reference/amd-strix-halo-edge
mkdir -p state logs config
cp config.yaml config/config.yaml
# The runtime image runs as uid/gid 65532, so the directories it writes must
# belong to that identity (see DOCKER_COMPOSE_INSTALL.md).
sudo chown -R 65532:65532 config state logs
# The pinned ROCm image has no `render` group in its /etc/group, so the container
# needs the numeric host GIDs of the device nodes rather than group names.
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

First boot downloads ~64 GB of checkpoints and pulls ~30 GB of image; the model servers report healthy
only after that. `docker compose logs -f vllm-primary` shows progress.

Teardown: `docker compose down` (add `-v` to drop the weight cache).

The model servers publish on loopback (`TINY_VLLM_PORT`, `CHAT_VLLM_PORT`/`PRIMARY_VLLM_PORT`, `CODER_VLLM_PORT`) so the sweep can measure the
direct-lane baseline; nothing but the router listens on a non-loopback address.

## Measured results

**Pending.** No hardware run is committed for this tier yet. The run records the
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

- The pinned ROCm image carries vLLM 0.19.1 while the NVIDIA tier pins 0.33.0;
  feature differences between those versions apply here.
- BF16 only: gfx1151 does not have the FP8 path this configuration would need for
  a second 30B-class slot, so there is no `local-coder` group.
- Unified memory is shared with the desktop and the CPU. A box also used for
  other work sustains fewer agents; the run must be measured on an idle box and
  say so.
- The router binds to `127.0.0.1` with `allow_insecure_http: true` and
  `secure_cookies: false` for a loopback-only posture. Put a TLS proxy in front
  and flip both before other hosts reach it; use OIDC
  ([ADMIN_OIDC.md](../../../docs/ADMIN_OIDC.md)) for more than one operator.
- Anthropic Messages inbound to the local groups is not declared for these models
  (no `request_shape_support` evidence), so Claude-Code-style clients are out of
  scope until that is validated on this box.
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
