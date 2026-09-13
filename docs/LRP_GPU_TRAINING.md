# LRP GPU training hosts

Policy for Learned Routing Policy (LRP) **training** that produces evidence,
promotion candidates, or hardware-backed benchmarks (including issue #162
configuration D). This document is the operator source of truth. Customer-facing
summary:
[Train and evaluate](https://llm-api.apps.metrum.ai/docs/routing/lrp-train-and-evaluate)
(source: `docs-site/docs/routing/lrp-train-and-evaluate.md`).

## Rules

1. **Skip CPU-only training.** Do not run evidence or promotion training on
   CPU-only hosts. Assume GPU hosts are required for acceptable wall time and
   for hardware claims that match production LRP sidecars.
2. **Train on GPU.** Use at least one CUDA-capable NVIDIA GPU (or an equivalent
   operator-approved accelerator) for featurize/train/eval jobs that write
   bundles or public evidence under `docs/evidence/learned-routing-policy/`.
3. **Cloud GPU is optional.** Shadeform is one provisioning path used in Metrum
   development. Operators and customers may use any owned or rented GPU system
   that meets the job needs. Product docs must not require Shadeform.
4. **Ephemeral cloud instances.** When a cloud GPU instance is created for an
   LRP training or config-D benchmark job (Shadeform or similar):
   - Create the instance only when the job is ready to run.
   - Tear the instance down when the job finishes, fails, or is cancelled.
   - Do not leave idle training hosts billed overnight.
5. **CI synthetic exception.** `make lrp-test` and the synthetic demo may train
   LightGBM on CPU with **synthetic** embeddings and labels. Those runs are not
   hardware evidence and must not be cited as GPU or Shadeform results.

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

## BYO GPU

Any operator GPU host is fine if it can run the LRP uv project, the ONNX
embedding model used at serve time, and the train/eval CLI. Prefer the same
GPU class intended for the production LRP sidecar when measuring config D
throughput or latency.

## Related

- Operator runbook: [LEARNED_ROUTING_POLICY.md](./LEARNED_ROUTING_POLICY.md)
- Routing benchmark harness: [routing-benchmark.md](./evidence/learned-routing-policy/routing-benchmark.md)
- Issue #162 (GPU LRP / Shadeform topology for configuration D)
