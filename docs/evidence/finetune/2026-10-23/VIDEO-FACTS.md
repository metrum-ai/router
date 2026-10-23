<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Video facts — work in progress

## Read before using any headline

The first adapter is **superseded**: pre-outcome dataset QA found Python-literal
arguments rejected, leaving zero original test tool targets. Corrected parsing
and2:1 stratification now gives1333/2000train,333/500val,200/300test tool targets.
Corrected full training and real native serving on both stacks have passed.
**No valid held-out quality result or promotion claim yet.**

The first held-out attempt06:02→06:06UTC was aborted before reviewing any case
outputs/scores after a separate synthetic fixture exposed SDK array-variable
expansion. Canonical inputs, prompts, scorer/hash and thresholds are unchanged;
a runtime guard corrects transport. Raw partial files remain outside Git and
are excluded. See `eval/PREREGISTRATION.md` in the example.

## Measured: corrected training

- **Two-epoch BF16 LoRA on one96GB RTX PRO6000 completed.** Source:
  `training-runtime-v2.json`:2000train/500val,1333/333 native-tool targets,
  250steps, **2211s (36.85min)** wrapper, **66.17GiB** peak torch allocation;
  1Hz driver peak**72091MiB**. All800 adapter tensors /108,789,760 parameters
  are text-decoder-only; computeBF16, adapter fileF32. Actual prepared-cache QA:
  `prepared-tokenization-v2.json`. Max observed example3929tokens;8192 is a cap.
- **Wrapper-only GPU quote estimate$1.345025** at$2.19/h. NOT all-in spend:
  excludes failed first run, smoke/setup/idle/serve/storage/network, not invoice.
  Validation likelihood0.383500→0.155748 is not held-out tool accuracy or
  evidence of improvement.
- Adapter SHA256 local+remote:
  `1f1ce60dac65f01ba54601c557d821c490ff954a969fbb563425c7728d86bb2e`.
  Axolotl0.21.1.dev0 immutable official image; no Unsloth fallback needed.
- Historical initial512-token2-step smoke: `hardware-smoke-runtime.json`,
  53.11GiB peak,47.31s trainer/88.746s wrapper. Do not confuse with full-run peak.

## Measured: corrected live plumbing and synthetic serving

- Sources:`live-smoke-onprem.json`, `live-smoke-fireworks.json`,
  `live-smoke-base.json`:actual worktree-router chat, SSE, strict JSON schema,
  **real file creation and native tool-result continuation PASS**. Explicitly
  non-thinking. Same adapter artifact transferred to Fireworks. Fireworks uses
  vendor live merge on2×B300 BF16, not an independently trained adapter. Provider
  base packaging/implementation is not independently byte-verified against HF.
- Source:`live-governance.json`:configured90/5/5 canary; actual finite100-request
  sample **96base/1onpremFT/3FireworksFT**, NOT exactly10% FT. Shadow records FT
  recommendation while base serves. Synthetic prompt marker absent in logs/DB.
  This is controlled synthetic local-dev traffic, not production traffic.
- Source:`load-onprem-lora.json`:32 requests/profile atconcurrency1/8/32,
  **0errors**, **23.83/181.13/599.42 outputtokens/s**. At32:p95TTFT647ms,
  p95latency6.83s; peak1Hz driver memory84591MiB. Fully utilized quote estimate
  **$1.015/M outputtokens at32**, versus$25.53/M at1. Caveats: short synthetic
  prompt,128-token cap, finite sample, not productionSLO, invoice or quality ROI.
- Synthetic evaluator integration:4/8 assertions passed,0HTTP/provider errors,
  8/8 response request IDs joined usage DB. This verifies real grading/telemetry
  plumbing; NOT a held-out improvement result. Raw output remains outside Git.

## Measured: judge calibration (not domain validation)

- Source:`judge-calibration.json`:**60% agreement,95% bootstrapCI46–74%,n50**,
  one public MT-Bench human preference per distinct question; pinned
  `gpt-4o-2024-11-20`, temperature0, through local router. Weak agreement, not
  domain-specific validation or fine-tuning gain. No in-house human labeling.
- Same source:57,166input/98output tokens, estimated$0.143895 at$2.50/$10 per1M.
  Not invoice; excludes availability/synthetic smokes and subsequent grading.

## Quotes, safeguards and incomplete economics

- Shadeform quote$2.19/h,1×RTX PRO6000 Blackwell96GB,750GB disk. Source:track
  report and private `shade-current.json`. Full run fits; no independent network
  speed measurement. Serving cost is still accruing, not included in train quote.
- Vast failed rentals estimated$0.124168 total, both deleted/API-verified;
  private `instance-57316233-deleted.json`, `instance-57316282-deleted.json`.
  No training on Vast. Later B200 availability check did not create a rental.
- GPU serving-phase watchdog deadline07:41UTC, conservative total reservation
  including Vast$8.53. Fireworks corrected2×B300 quote$30/h,2h cleanup watchdog;
  new reservation$60 plus first superseded deployment≤$4.50 lifetime upper bound.
  These are quote-based bounds, not invoices. Delete immediately at check end.

## Still pending — do not claim

Held-out quality deltas/CI, full regression and sampling outcomes, promotion,
Fireworks/merged load profiles, measured break-even scenario and final teardown/
spend verification. Zero router token prices are placeholders, not free serving.
No reduced-retry savings, positive ROI or production deployment claim is justified.
