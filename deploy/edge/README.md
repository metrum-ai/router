<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Edge bundle: one command, local-first with an escalation path

Installs, in one command:

- the **router** (`metrum-ai-router`, operator-supplied image);
- a **local model stack** on official upstream images, sized for a Strix Halo
  class box (128 GB unified memory) and pinned by digest;
- the **default model groups** `local-tiny`, `local-chat` and `escalate`;
- the **escalation path** to any OpenAI-compatible endpoint (frontier API or a
  datacenter router) with the operator's own key;
- the **admin UI and spend reports** at `/admin/reports/`;
- two **caller tokens**, one of them with a deliberately small daily budget so a
  real budget hit can be demonstrated.

```sh
sudo bash install.sh --router-image metrum-ai-router:<version>-linux-amd64
bash demo.sh          # local-first, escalation, budget hit, spend report
```

Everything lives under `--data-dir` (default `$HOME/metrum-edge`):
`config/config.yaml` (groups), `secrets/` (tokens and environment, mode 0600),
`state/` (usage database), `logs/`, `compose/`, `install.env`.

## Two modes

| Mode | What runs | Use it for |
|---|---|---|
| `--mode docker` (default) | vLLM model servers in containers on the GPU, the router in a container | the real deployment |
| `--mode native` | the router binary plus three **CPU mock upstreams**; no containers, no GPU | evaluation, air-gapped trials, CI install test |

Native mode's upstreams are a scripted OpenAI-compatible mock. They prove the
install path, group routing, quotas and reports; they produce no model output and
are not quality or performance evidence. Native mode also refuses to upgrade
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
   docker compose run --rm --no-deps -v "$PWD/config:/app/config:rw" \
     --entrypoint /app/bin/metrum-ai-routerctl router callers generate ...
   ```

Compose under Podman (`podman compose`, or `docker compose` where `docker` is Podman's
emulation) reads the same files, so every overlay must survive the same merge;
`docker compose config`/`podman compose config` is the check, and CI runs an
equivalent structural check for each overlay.

## Troubleshooting

| Symptom | What to check |
|---|---|
| `install.sh` stops at "admin UI needs a bcrypt password hash" | Install `apache2-utils` (for `htpasswd`), or pass `--admin-password-hash <bcrypt>`. A password generated by the installer is at `secrets/admin-password`. |
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
| 4 | Admin UI at `/admin/reports/` | Report page renders with the four groups | ≤ 5 min |
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
