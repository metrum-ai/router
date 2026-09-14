# LRP GPU training hosts

Policy for Learned Routing Policy (LRP) **training** that produces evidence,
promotion candidates, or hardware-backed benchmarks (including issue #162
configuration D). This document is the operator source of truth. Customer-facing
summary:
[Train and evaluate](https://llm-api.apps.metrum.ai/docs/routing/lrp-train-and-evaluate)
(source: `docs-site/docs/routing/lrp-train-and-evaluate.md`).

## Rules

1. **No local laptop/desktop ML.** Do not run evidence, promotion, featurize,
   train, eval, Hugging Face model loads, or large-corpus jobs on a developer
   laptop or desktop workstation (OOM risk). Local workstation training for
   those jobs is forbidden. Example evidence uses at most 100 traces on
   Shadeform or BYO remote GPU; do not load the full upstream parquet on a
   laptop.
2. **Remote GPU host only.** Those jobs run only on an ephemeral remote GPU
   host with ample RAM and GPU. Shadeform (or equivalent) is the usual path;
   BYO remote GPU is fine. Create the host when needed; always tear it down.
3. **Skip CPU-only training.** Do not run evidence or promotion training on
   CPU-only hosts. Assume GPU hosts are required for acceptable wall time and
   for hardware claims that match production LRP sidecars.
4. **Train on GPU.** Use at least one CUDA-capable NVIDIA GPU (or an equivalent
   operator-approved accelerator) for featurize/train/eval jobs that write
   bundles or public evidence under `docs/evidence/learned-routing-policy/`.
5. **Shadeform is optional.** Shadeform is one provisioning path used in Metrum
   development. Operators may use any owned or rented **remote** GPU system
   that meets the job needs. Product docs must not require Shadeform.
6. **Ephemeral cloud instances.** Create a cloud GPU instance only when the job
   is ready to run. Always tear it down when the job finishes, fails, or is
   cancelled. Do not leave idle training hosts billed overnight. Applies to
   Shadeform and similar providers.
7. **CI synthetic exception only.** `make lrp-test` and the synthetic demo may
   train LightGBM on CPU with **tiny synthetic fixtures only**. Those runs are
   not hardware evidence and must not be cited as GPU or Shadeform results.
   Do not expand local ML beyond this non-evidence CI path.

## Embedding backends and devices (#156 A/C)

ONNX Runtime (`onnxruntime`) is the **default** embedding backend, not the only
path. Operators may select `sentence-transformers` with a local directory or an
already-cached Hugging Face snapshot. Serving never downloads models or runs
remote model code.

| Stage | onnxruntime | sentence-transformers |
| --- | --- | --- |
| Embedding | CPU EP, CUDA EP (`cuda:N`), ROCm EP when present | torch `cpu` or `cuda:N` (HIP uses cuda strings; never pass literal `rocm`) |
| Quality / token fit | LightGBM on host CPU (unchanged; GPU LightGBM is out of scope here) | same |
| Serve | Same resolver as train via `compute.device` | same |

Shared settings:

```yaml
embedding:
  backend: onnxruntime  # or sentence-transformers
  model: /operator-owned/embed/model.onnx
  max_seq_len: 512
  batch_size: 1
compute:
  device: cpu  # auto | cpu | cuda:N | rocm
  strict_device: false
```

CLI mirrors these with `--embedding-backend`, `--embedding-model`,
`--tokenizer` (ONNX), `--device`, and `--strict-device`.

### Optional dependencies

- Default lockfile keeps CPU `onnxruntime`. Do not replace it in the shared
  project lock for CI.
- CUDA ONNX Runtime is **operator-installed**: uninstall CPU `onnxruntime` on
  the GPU host and install a matching `onnxruntime-gpu` wheel from upstream.
  LRP selects `CUDAExecutionProvider` only when that provider is available and
  `compute.device` requests CUDA or `auto`.
- sentence-transformers + torch: install the optional extra without changing
  the default lock resolution for other environments:

```bash
uv sync --project services/learned-routing-policy --extra embed-st
```

ROCm ONNX packaging remains operator-verified. Upstream removed
`ROCMExecutionProvider` starting in some ORT 1.23+ builds. Confirm the installed
runtime exposes the provider before claiming ROCm support.

### Device mismatch policy

Manifests record `train_device_class` and `intended_serve_device_class`. Loading
on a different device class warns by default. `strict_device: true` refuses.
Semantic fingerprint mismatches (backend, model hash, tokenizer/normalization,
max_seq_len, precision, configured device_class) always refuse.

## Shadeform (optional example)

Shadeform steps mirror the create-then-always-teardown pattern in
[SHADEFORM_NVIDIA_LOCAL_SERVING_E2E.md](./SHADEFORM_NVIDIA_LOCAL_SERVING_E2E.md):

1. Load `SHADEFORM_API_KEY` from process environment or ignored ops/env files.
   Never print the key.
2. Create an available GPU SKU only after the training dataset and commands are
   ready.
3. Run train/eval on the instance; copy only approved scalar evidence off-host.
4. Terminate the Shadeform instance and remove local kubeconfig/SSH/runtime
   secrets even if the job failed.

Record SKU, GPU name, driver/CUDA, and wall time in protected evidence. Do not
commit credentials or private hostnames.

## BYO remote GPU

A BYO **remote** GPU host is fine if it has ample RAM and GPU, can run the LRP
uv project, the ONNX embedding model used at serve time, and the train/eval
CLI. Laptop and desktop workstations are not BYO hosts for evidence jobs.
Prefer the same GPU class intended for the production LRP sidecar when
measuring config D throughput or latency. Tear down rented or ephemeral BYO
hosts when the job ends.

## Related

- Operator runbook: [LEARNED_ROUTING_POLICY.md](./LEARNED_ROUTING_POLICY.md)
- Routing benchmark harness: [routing-benchmark.md](./evidence/learned-routing-policy/routing-benchmark.md)
- Issue #162 (GPU LRP / Shadeform topology for configuration D)
- Issue #156 sections A/C (pluggable embedders and device selection)
