<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# AMD Instinct video facts — preparation only

- Issue #359 is not complete: no AMD droplet, training, inference, measured
  throughput, quality comparison or cost-per-success result exists yet.
- Spend: $0; no resource requires teardown.
- Only #358 router-exported training data is permitted. The seven-row mixed
  real-teacher engineering export passes FT consumer validation but is not a
  training/quality cohort. Full final data is now delivered and verified:
  1186train/221val/300test, with98 historical teacher failures retained in test.
  `shared-freeze.json` binds identical NVIDIA/AMD bytes, recipe and evaluation
  protocol before student training/qualification; direct-download data is not used.
- Teacher gate is cleared by the maintainer's scoped synthetic Acme/mock-tool
  technical-test decision (issue358 comment6366186842), retained as
  `teacher_terms_decision` in the actual manifest and evidence. It is not general
  customer-output permission; users must verify both model and provider terms.
- Official ROCm command planning, security/env capture and provisional Axolotl
  configs exist. CPU tests cannot prove MI325X/Qwen3.8/LoRA compatibility.
- Future serving must stay on loopback over SSH through a worktree localhost
  router. Held-out replay must execute new tool continuations, not replay teacher
  answers as candidate output. NVIDIA/AMD parity remains unmeasured.
- Do not narrate a successful AMD fine-tune, performance win, quality gain,
  validated fallback toolkit, production promotion or completed end-to-end loop.
