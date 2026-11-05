<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Edge bundle: one command, local-first with an escalation path

Installs, in one command:

- the **router** (`metrum-ai-router`, operator-supplied image);
- a **local model stack** on official upstream images, sized for a Strix Halo
  class box (128 GB unified memory) and pinned by digest;
- the **default model groups** `local-tiny`, `local-chat` and `escalate` (three;
  a reference tier that adds `local-coder` has four);
- the **escalation path** to any OpenAI-compatible endpoint (frontier API or a
  datacenter router) with the operator's own key;
- the **admin UI and spend reports** at `/admin/reports/`;
- two **caller tokens**, one of them with a deliberately small daily budget so a
  real budget hit can be demonstrated.

```sh
sudo bash install.sh --router-image metrum-ai-router:<version>-linux-amd64
bash demo.sh          # local-first, escalation, budget hit, spend report
```

Everything lives under `--data-dir` (default `/var/lib/metrum-edge` when the
installer runs as root, otherwise `$HOME/metrum-edge`; `lib/common.sh` holds that rule
and all four scripts use it, so `install.sh`, `demo.sh`, `upgrade.sh` and `uninstall.sh`
find the same directory without being told again):
`config/config.yaml` (groups), `secrets/` (tokens and environment, mode 0600),
`state/` (usage database), `logs/`, `compose/`, `install.env`.

## Get the bundle

Everything is public, and the installer does not fetch anything for you:

```sh
# the bundle files (no build step; they are scripts and manifests)
git clone --depth 1 https://github.com/metrum-ai/router
cd router/deploy/edge

# the router binaries: an official release archive, not a container image
ARCH=amd64                       # or arm64
VERSION=v4.6.0                   # pin one; record it in your run notes
curl -fLO "https://github.com/metrum-ai/router/releases/download/${VERSION}/metrum-ai-router-${VERSION}-linux-${ARCH}.tar.gz"
curl -fLO "https://github.com/metrum-ai/router/releases/download/${VERSION}/SHA256SUMS"
sha256sum -c --ignore-missing SHA256SUMS
tar -xzf "metrum-ai-router-${VERSION}-linux-${ARCH}.tar.gz"   # yields metrum-ai-router-${VERSION}-linux-${ARCH}/bin/
```

`--bin-dir` takes that extracted **`bin/`** directory — the archive unpacks to
`metrum-ai-router-<version>-linux-<arch>/bin/`, not to a bare `bin/` — and it must
contain `metrum-ai-router`, `metrum-ai-routerctl` and `metrum-ai-router-migrate`.

### The router image for docker mode

**No container image is published**: releases ship binary archives only (checked
anonymously against Docker Hub and GHCR, 2026-11-03). Docker mode therefore needs an
image you build yourself from public inputs, and there are two ways to do it. Both
build from the directory that *contains* the extracted package, because the COPY
paths below start with the package directory name.

*Wrap the release binaries* — seconds, no toolchain:

```dockerfile
# Dockerfile.router
FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY metrum-ai-router-<version>-linux-<arch>/bin/ /app/bin/
COPY metrum-ai-router-<version>-linux-<arch>/LICENSE metrum-ai-router-<version>-linux-<arch>/NOTICE /app/
WORKDIR /app
USER 65532:65532
ENTRYPOINT ["/app/bin/metrum-ai-router"]
```

```sh
# run from the directory that contains the extracted package, next to Dockerfile.router
docker build -f Dockerfile.router -t metrum-ai-router:<version>-linux-<arch> .
```

Keep `ca-certificates`: the router calls HTTPS upstreams, including the escalation
provider, and a scratch image without them fails those calls.

*Build the shipped Dockerfile* — the canonical image, including the bundled docs site
and admin UI, needs Go and Node, which the Dockerfile's builder stage supplies:

```sh
git clone https://github.com/metrum-ai/router && cd router
git checkout <tag>                    # the release tag you are deploying
docker build --build-arg VERSION=<tag> --build-arg COMMIT=<sha> \
  --build-arg BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  -t metrum-ai-router:<tag>-linux-amd64 .
```

Either way, pass that tag to `--router-image` and record its image id in your run
notes.

Version notation here: a **release tag** looks like `v4.6.0`, a **router image tag**
looks like `<version>-linux-<arch>` (`4.7.0-linux-amd64`), and examples may show
`<version>`, `<tag>` or a concrete `4.7.0`. Substitute consistently and record the
release tag and the commit you cloned.

The bundle files and the release are separately versioned. What is known today: the
bundle scripts in this directory were exercised end to end against **v4.6.0** (an
unassisted install and demo by an outside agent, see the reference run notes) and the
4.7.x tree is what the reference deployments use. Older or newer releases are not
claimed. Pin both sides in your run notes — the release tag and the commit you cloned.
If you download individual files with `curl` instead of cloning, remember that the
scripts also need `lib/common.sh` (the shared data-directory and path defaults), so
fetch that too and keep it at `lib/common.sh` relative to them; pinning the commit in
each URL rather than using `main` keeps the set consistent.

## Host prerequisites

On a stock Ubuntu image, before the installer:

```sh
sudo apt-get update
sudo apt-get install -y ca-certificates curl git jq python3 openssl apache2-utils
```

`apache2-utils` supplies `htpasswd` for the admin password hash; without it (or a
local `python3 -c 'import bcrypt'`) the installer stops with instructions.

Docker Engine 27+ with the Compose plugin is required for docker mode and is not
installed by the bundle; on a stock Ubuntu host the upstream instructions are
<https://docs.docker.com/engine/install/ubuntu/> (or the convenience script at
<https://get.docker.com>), and the Compose plugin normally arrives with the `docker-ce`
package. On an NVIDIA host the container toolkit is also required — follow
<https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html>
and run the `nvidia-ctk runtime configure --runtime=docker` step it describes, then
`sudo systemctl restart docker`; `docker run --rm --gpus all <image> nvidia-smi` proves
it. On AMD the container needs `/dev/kfd` and `/dev/dri` access, which the installer
detects and writes into the secret environment as group ids (override with
`EDGE_RENDER_GID` / `EDGE_VIDEO_GID`). The runtime image runs as uid 65532 and
the installer hands it the directories it mounts.

In docker mode the router listens on `0.0.0.0` **inside** its container while the host
publishes that port on `--listen` (default `127.0.0.1`), so the API is not exposed
beyond the host. The two addresses are different on purpose: a process bound to
container loopback never answers the published port, and `install.sh` would then wait
for `/readyz` until the 360 s timeout even though the logs show it listening.

## Two modes

| Mode | What runs | Use it for |
|---|---|---|
| `--mode docker` (default) | vLLM model servers in containers on the GPU, the router in a container | the real deployment |
| `--mode native` | the router binary plus three **CPU mock upstreams**; no containers, no GPU | evaluation, air-gapped trials, CI install test |

Native mode's upstreams are a scripted OpenAI-compatible mock, including the third
one behind `escalate`: the demo's escalation step exercises routing, group
separation and reporting, and **it does not leave the box or call a provider**, no
matter what key or `--escalation-url` you supply. Native mode proves the install
path, group routing, quotas, budgets and reports; it produces no model output, and
its token counts and costs are mock arithmetic rather than inference or billing. Native mode also refuses to upgrade
(`upgrade.sh`) because there is no managed image to replace.

## Hardware bill of materials

| Item | Requirement |
|---|---|
| System | 1× AMD Ryzen AI MAX+ 395 (Strix Halo, gfx1151) **128 GB** unified memory. The 64 GB variant cannot hold the 30B-class slot. |
| GPU memory window | `amdgpu` GTT window large enough for ~110 GB (BIOS/`amdgpu.gttsize`); see Sizing |
| Disk | ≥ 200 GB free for weights (`hf-cache` volume) and state |
| Driver | `amdgpu` with ROCm 7.x userspace for gfx1151; `/dev/kfd` and `/dev/dri` present |
| Runtime | Docker Engine 27+ with the Compose v2 plugin; the installer adds `/dev/kfd`, `/dev/dri`, `video` and `render` groups |
| CPU keyboard/monitor | none after install; the admin UI is reachable at the router address |
| Network | outbound HTTPS to `huggingface.co` and the escalation endpoint (not needed in air-gapped mode) |

NVIDIA (GB10 class) hosts use `--accelerator nvidia`, which selects the
multi-arch `vllm/vllm-openai` pin and the NVIDIA device reservation. The pins in
`compose.amd.yaml` and `compose.nvidia.yaml` are the same ones the reference
configurations pin, and the install test fails if they drift.

The AMD overlay sets `entrypoint: ["vllm", "serve"]` explicitly: the ROCm image
ships vLLM, but its default command is a wheel-pin helper rather than the server,
and AMD's published run form is `vllm serve ...`.

## Sizing

Method (the same as `deploy/reference/amd-strix-halo-edge/SIZING.md`):

```
kv_bytes_per_token = 2 × layers × kv_heads × head_dim × bytes_per_element
kv_pool            = gpu_memory_utilization × capacity − weights − overhead
requests_at_ctx    = kv_pool / (kv_bytes_per_token × context_tokens)
```

| Slot | Model | Weights (BF16) | Layers | KV heads | Head dim | KV bytes/token | Utilization | KV pool | Requests at 8K |
|---|---|---|---|---|---|---|---|---|---|
| `local-tiny` | Qwen3-1.7B | 3.4 GB | 28 | 8 | 128 | 112 KiB | 0.05 | 1.0 GiB | 1 |
| `local-chat` (slot `local-primary`) | Qwen3-30B-A3B-Instruct-2507 | 61 GB | 48 | 4 | 128 | 96 KiB | 0.80 | 39.4 GiB | 52 |

Reproduce the rows with `deploy/reference/harness/kv_math.py`:

```sh
python3 deploy/reference/harness/kv_math.py --layers 48 --kv-heads 4 --head-dim 128 \
  --context 8192 --capacity-gb 128 --util 0.80 --weights-gb 61
```

**Validated concurrency: not yet measured.** The table is arithmetic and says
what memory allows; shared LPDDR5X bandwidth is what decides the real number. The
measurement is the concurrency sweep in
`deploy/reference/amd-strix-halo-edge/SIZING.md` on the hardware run, and this
section will carry the measured figure with its p95 latency and the box it ran
on. Until then the bundle claims no concurrency number.

Swap the primary model without touching the group, the grants or any client:

```sh
EDGE_PRIMARY_MODEL=Qwen/Qwen3-Coder-30B-A3B-Instruct \
EDGE_PRIMARY_REVISION=b2cff646eb4bb1d68355c01b18ae02e7cf42d120 \
  docker compose --env-file ../secrets/env -f compose.yaml -f compose.amd.yaml up -d vllm-primary
```

## The demo

`bash demo.sh` runs four steps and prints `PASS`, `SKIP` (with its reason) or `FAIL`
for each; a skip is not a pass, and the script exits non-zero if anything failed.

| Step | What it shows |
|---|---|
| 1. local-first | `local-chat` and `local-tiny` answer from the box, with the upstream model named |
| 2. escalation | `escalate` leaves the box through the operator's endpoint; without a usable key it fails closed with an upstream error (`upstream-access-denied` for a rejected key, `upstream-rejected` when no key is sent) instead of quietly answering locally |
| 3. budget hit | the `demo-budget` caller is refused with `429 quota-exhausted` once its day request budget is spent |
| 4. spend report | `routerctl usage summary` prints tokens and cost from the router's own usage rows, with the escalation spend separable from the on-box groups |

## AMD hosts: GPU group ids

The pinned ROCm image has no `render` entry in its `/etc/group`, so a compose
`group_add: [video, render]` fails with "Unable to find group render: no matching
entries in group file" — on Docker as well as Podman. The AMD overlay therefore takes
numeric host GIDs, and `install.sh` reads them from the device nodes:

```sh
stat -c %g /dev/dri/renderD128     # render
stat -c %g /dev/dri/card0          # video
```

On a typical Strix Halo host that is `992` and `44`. If the installer cannot see those
nodes (a container or VM that maps the devices differently), pass them explicitly:
`EDGE_RENDER_GID=992 EDGE_VIDEO_GID=44 sudo -E bash install.sh ...`.

## Supplying the escalation key (docker mode)

The bundle writes its secret environment to `secrets/env` (mode 0600) and passes it
to the router container. For a real provider, put the key in that file under
**`OPENAI_API_KEY`** — the rendered configuration's escalation provider reads
`${OPENAI_API_KEY}` — and recreate the router so it re-reads it:

```sh
DATA_DIR="/var/lib/metrum-edge"         # the --data-dir you installed with (root default)
sudoedit "$DATA_DIR/secrets/env"        # OPENAI_API_KEY='sk-...'

# install.env records the accelerator and the paths, so this works on either vendor
. "$DATA_DIR/install.env"
docker compose --env-file "$EDGE_ENV_FILE" \
  -f "$EDGE_DATA_DIR/compose/compose.yaml" \
  -f "$EDGE_DATA_DIR/compose/compose.${EDGE_ACCELERATOR}.yaml" \
  up -d --no-deps --force-recreate router
```

`--escalation-key-env NAME` changes which variable the *renderer* points the
provider at, but **docker mode can only deliver two names into the router
container**: `compose.yaml` passes `OPENAI_API_KEY` and `EDGE_ESCALATION_API_KEY`,
and nothing else reaches that process. Any other `NAME` is written into the
configuration and then never resolves, so the escalation group fails closed with an
upstream authentication error. Use `OPENAI_API_KEY` (the default) or
`EDGE_ESCALATION_API_KEY`; if you need a third name, add it to the router's
`environment:` in the compose file as well. A missing key leaves the group failing
closed with an upstream error, which is the intended behaviour — never a silent
local answer. (Native mode ignores all of this: its escalation target is a mock.)

## Upgrade and uninstall

```sh
bash upgrade.sh --router-image metrum-ai-router:<new-version>-linux-amd64
bash upgrade.sh --config-only          # re-render groups from the current template
bash uninstall.sh                      # stop and remove containers; keep data
bash uninstall.sh --purge --yes        # also delete config, secrets, state, logs
```

`upgrade.sh` snapshots the configuration and secret environment to
`backups/<timestamp>/`, switches the router image, re-renders the groups with the
existing tokens, runs the migration sequence, and only then recreates the router.
A failed migration leaves the router stopped and the previous groups on disk.
Model servers are not restarted unless `--restart-models` is given.

Uninstall keeps the usage database by default: the spend history is evidence.
`--purge` prints what it will delete and refuses without `--yes`.

**If the install failed before it finished**, `install.env` does not exist yet and
`demo.sh`, `upgrade.sh` and `uninstall.sh` stop with "install.env not found" — by
design, not a second fault. Inspect the stack with the explicit compose files, then
re-run `install.sh` (it is idempotent and keeps existing secrets and tokens):

```sh
D="/var/lib/metrum-edge"              # or your --data-dir
docker compose --env-file "$D/secrets/env" \
  -f "$D/compose/compose.yaml" -f "$D/compose/compose.${EDGE_ACCELERATOR:-nvidia}.yaml" ps
docker compose --env-file "$D/secrets/env" \
  -f "$D/compose/compose.yaml" -f "$D/compose/compose.${EDGE_ACCELERATOR:-nvidia}.yaml" logs router | tail -40
```

A router reporting `open /app/config/...: permission denied` is the ownership case:
re-run `install.sh`, which hands the data directories to the image's uid 65532.

## Air-gapped install

The bundle never calls out during install except to pull images and weights, so
an air-gapped install is the same command with the inputs pre-staged:

1. On a connected host, pull the router image and the model-server image from the
   overlay (`compose.amd.yaml` or `compose.nvidia.yaml`) and save them:
   `docker save <image> -o edge-images.tar`. Bring the tar inside.
2. Pre-populate the weight cache on the target: run
   `docker run --rm -v metrum-router-edge_hf-cache:/cache -e HF_HOME=/cache <model-server image> python3 -c "from huggingface_hub import snapshot_download as s; s('Qwen/Qwen3-30B-A3B-Instruct-2507', revision='0d7cf23991f47feeb3a57ecb4c9cee8ea4a17bfe')"`
   (and the tiny model), or copy a prepared cache volume in.
3. `docker load -i edge-images.tar`, then
   `bash install.sh --air-gapped --router-image <image>`.
4. `--air-gapped` writes `HF_HUB_OFFLINE=1` into the secret environment, so the
   model servers fail closed on a missing weight instead of hanging on a download.
   The escalation group is unavailable until a key is supplied, which is the normal
   air-gapped posture.

## Smaller GPU: the 48 GB profile

The default stack is sized for 128 GB of unified memory because of the 30B-class
primary model. On a 48 GB card the bundle runs the same groups with a smaller
primary; nothing else changes (group names, caller grants, demo, reports):

```sh
export EDGE_PRIMARY_MODEL=Qwen/Qwen3-8B
export EDGE_PRIMARY_REVISION=b968826d9c46dd6066d109eabc6255188de91218
export EDGE_PRIMARY_UTILIZATION=0.70
export EDGE_TINY_UTILIZATION=0.26
# Cap the tiny slot's concurrency and context: vLLM sizes CUDA graphs and
# activation buffers per sequence, and the default 256 is what starved the KV
# allocation on this card.
export EDGE_TINY_MAX_NUM_SEQS=8
export EDGE_TINY_MAX_MODEL_LEN=4096
sudo -E bash install.sh --router-image metrum-ai-router:<version>-linux-amd64
```

| Slot | Model | Weights (BF16) | Utilization | Budget | KV pool | Requests (computed, not measured) |
|---|---|---|---|---|---|---|
| `local-tiny` | Qwen3-1.7B | 3.4 GB | 0.26 | 12.5 GB | 6.1 GiB | 13 at 4K |
| `local-chat` | Qwen3-8B | 16.4 GB | 0.70 | 33.6 GB | 14.2 GiB | 12 at 8K |

Why the cap rather than a bigger utilization: the model server captures CUDA graphs
and allocates activation buffers for `--max-num-seqs` sequences *before* it hands the
remaining budget to the KV cache, so an unbounded queue on a small card is what
produced `ValueError: No available memory for the cache blocks` on this profile. Eight
concurrent sequences is ample for the smoke tests and classification traffic the tiny
slot takes, and it makes the KV arithmetic above match what the slot can actually
allocate. If a slot still fails to start, set `EDGE_TINY_MAX_NUM_SEQS=4` (or add
`--enforce-eager` for the tiny slot, which trades latency for a fixed memory profile)
before reaching for utilization.

Arithmetic from the sizing method above, with a 3 GiB per-slot overhead allowance;
`deploy/reference/harness/kv_math.py` reproduces the rows. Disk: ~20 GB of
checkpoints plus the ~25 GB model-server image, so ≥ 100 GB free.

Both request columns are **computed, not measured** (`kv_math.py` reproduces them
from the sizing method above, with a 3 GiB per-slot overhead allowance).

Two things to expect on a card this size: the tiny slot keeps only a few 8K-context
requests in flight (it is a smoke and classification slot, not a chat slot), and the
primary is an 8B model rather than the default 30B-class one — the `local-chat` group
name, its grants and every client stay exactly the same. On a cold start vLLM also
profiles CUDA graphs, which needs headroom beyond the weights; a tiny slot configured
too tightly fails with `No available memory for the cache blocks` and only recovers
after container restarts. The values above leave that headroom; if your card behaves
differently, raise `EDGE_TINY_UTILIZATION` in 0.02 steps and take the difference from
`EDGE_PRIMARY_UTILIZATION`.

## Rootless Podman

Podman is the default container runtime on several AMD platforms and is **rootless** by
default. A hardware run found two differences; the remedies are **not yet verified on
hardware** — that run stopped before it could apply them — so treat them as the
starting point rather than a tested path:

1. **Ownership.** A rootless container sees container uid 65532 as a *mapped* host uid,
   so the host `chown -R 65532:65532 config state logs` yields `permission denied`.
   Chown inside the user namespace instead, or run everything with `sudo` (rootful
   Podman), where the documented `chown` is correct:

   ```sh
   podman unshare chown -R 65532:65532 config state logs
   ```
2. **Minting a caller after the stack is up.** The configuration is mounted read-only,
   so the writer has nothing writable to rename its temp file onto — minting before
   `up` does not help either, because `compose run` uses the service's mounts. Override
   the mount for that one run:

   ```sh
   . "$EDGE_DATA_DIR/install.env"
   ROUTER_CONFIG_MOUNT_MODE=rw docker compose --env-file "$EDGE_ENV_FILE" \
     -f "$EDGE_DATA_DIR/compose/compose.yaml" \
     -f "$EDGE_DATA_DIR/compose/compose.${EDGE_ACCELERATOR}.yaml" \
     run --rm --no-deps \
     --entrypoint /app/bin/metrum-ai-routerctl \
     router callers generate --owner-user ops --project p --env demo --key k1 \
     --allow local-tiny,local-chat --config /app/config/config.yaml --write \
     --token-out /app/state/demo.token
   ```

Compose under Podman (`podman compose`, or `docker compose` where `docker` is Podman's
emulation) reads the same files, so every overlay must survive the same merge;
`docker compose config`/`podman compose config` is the check, and CI runs an
equivalent structural check for each overlay.

## Troubleshooting

| Symptom | What to check |
|---|---|
| `install.sh` stops at "admin UI needs a bcrypt password hash" | Install `apache2-utils` (for `htpasswd`), or pass `--admin-password-hash <bcrypt>`. A password generated by the installer is at `secrets/admin-password`. |
| Router logs `listening on 0.0.0.0:8080` but the installer still fails `router did not become ready at http://127.0.0.1:8080/readyz within 360s` | The host is not reaching the published port; check `docker compose ps` shows the port mapping and that nothing else holds 8080 on the host. On a build older than 4.7.x the rendered config could carry `127.0.0.1` into the container, which no published port can reach. |
| Router never becomes ready (the installer waits 360 s, then exits) | First read `logs router`: a startup error is logged long before the wait expires. `initialize router: mkdir /path: permission denied` means the rendered configuration carried a host path the container cannot reach — docker mode renders `/app/state` and `/app/logs`, so a custom `--data-dir` under a directory the container's uid 65532 cannot traverse (for example `/root`, mode 0700) is the cause; use `/var/lib/metrum-edge` or another searchable path. |
| Router never becomes ready | `docker compose --env-file secrets/env -f compose/compose.yaml -f compose/compose.amd.yaml ps`; the router starts only after both model servers are healthy, and first boot downloads ~64 GB of weights. `docker compose ... logs vllm-primary` shows progress. |
| `vllm-primary` restart-loops with an out-of-memory error | The GTT window is smaller than the plan. Raise `amdgpu.gttsize` (MB) in the boot loader, reboot, re-check with `journalctl -k \| grep -i gtt`. Lower `EDGE_PRIMARY_UTILIZATION` only with the sizing table in front of you. |
| `migration action verify-serving failed` | The bound data job is still pending; the installer resumes it at checkpoint 0 before verifying. Re-run `install.sh` to repeat the sequence, and read `state/` before deleting anything. |
| `401` from the router with the demo token | Tokens are generated at install time and hashed in `config/config.yaml`. If the config was edited or restored from a different data directory, re-run `install.sh --force` (rotates both tokens). |
| Admin UI returns 401 | Basic auth user is `admin`; the password is the one in `secrets/admin-password` or the one you passed with `--admin-password`. |
| Escalation returns an upstream error (`upstream-access-denied`/`upstream-rejected`) | Put the key in `secrets/env` under the variable named by `EDGE_ESCALATION_KEY_ENV` (default `OPENAI_API_KEY`) and restart the router. Failing closed is intended. |
| `docker: permission denied` | Run with `sudo`, or add the user to the `docker` group and start a new session. |
| Port already in use | `--port` changes the router port; `EDGE_TINY_VLLM_PORT`, `EDGE_PRIMARY_VLLM_PORT` change the model-server ports. |

## What CI tests, and what it does not

`make test-edge-bundle` (wired into `ci-python`) builds the three binaries, runs
`install.sh --mode native` on a clean runner, then asserts: the rendered
configuration loads in the router's own loader, secret files are `0600` and no
secret appears in installer output, groups are advertised, unauthenticated
requests are refused, the demo passes every step, a re-install does not rotate
tokens, a docker-mode `--dry-run` renders a valid configuration and pinned
overlay, and uninstall keeps data then purges it.

It does **not** run containers, touch a GPU, download weights or measure
throughput. Those belong to the hardware runs. The docker-mode path is covered by
rendering plus the compose files' pinned digests, not by a live container start in
CI, because the CI runners have no container runtime.

## One-hour outside-engineer session (Gate 2)

The timed session needs a person, so the bundle ships the script and the
checklist instead of claiming the result. Observer checklist:

| # | Step | Pass condition | Time |
|---|---|---|---|
| 1 | Read only this README | No questions needed to start | — |
| 2 | `install.sh` with the router image and the box's key | Ends with "Edge bundle installed" | ≤ 20 min (weights already cached) |
| 3 | `demo.sh` | Four PASS lines, or SKIP with the escalation key missing | ≤ 5 min |
| 4 | Admin UI at `/admin/reports/` | Report page renders with the three groups | ≤ 5 min |
| 5 | One real request through the local group from the engineer's own client | Answer returns, usage row appears | ≤ 10 min |
| 6 | `upgrade.sh --config-only` then `demo.sh` | Router ready again, demo passes | ≤ 10 min |
| 7 | `uninstall.sh`, then reinstall | Data kept, reinstall reuses tokens | ≤ 10 min |

Total budget: 60 minutes. Record the actual timings and every point where the
engineer needed help; that list is the next documentation change.

## Files

| File | Purpose |
|---|---|
| `install.sh` | the one command; renders config, generates secrets and tokens, migrates, starts, verifies |
| `config.template.yaml` | router configuration template (groups, quotas, admin surfaces) |
| `compose.yaml`, `compose.amd.yaml`, `compose.nvidia.yaml` | deployment and the two accelerator overlays |
| `demo.sh` | local-first, escalation, budget hit, spend report |
| `upgrade.sh`, `uninstall.sh` | the lifecycle paths |
| `mock/mock-upstream.py` | CPU-only OpenAI-compatible mock for native mode and CI; not a model |
| `test/install_e2e.py` | the install test that CI runs |
