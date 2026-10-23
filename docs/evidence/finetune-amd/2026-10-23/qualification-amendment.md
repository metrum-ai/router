<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Shared qualification envelope e411fc9f — no mixed protocols

Training provenance remains shared96f851a7 with the documented ROCm toolkit/API
bootstrap amendments. **Current comparable qualification** uses the exact shared
`qualification-preregistration.json`, SHA256
`e411fc9f7c6216eb87e659d3b120561bc0e1b3f45ef2ef6c623637796e0ee1ea`,
FT signed commit `853dd5a85bf5dfb485fac016cdb0b95b5d3c8b44`.

The producer export contains duplicate tool declarations in2train/1test rows.
After a separate NVIDIA base batch infrastructure abort, the shared runner was
amended to count the ambiguous declaration as a deterministic failure/no dispatch
in EACH arm, rather than abort the entire300-task batch. No exported row, source
fixture, adapter, recipe, prompt/scorer or numerical threshold changed. All300
remain denominators; no score-driven refill or source-answer training fallback.

AMD's first native candidate batch DID start at18:32:36Z under96f851a7. It was
stopped18:37:34Z with294persisted episodes, BEFORE any native scores/comparisons
were inspected. That entire partial run and log remain private under
`/workspace/router-v430/amd-runtime/noncomparable-96f851a7/`, explicitly
**NON-COMPARABLE/excluded**, not silently merged with rerun rows. All its GPU and
request costs remain lifecycle costs. No AMD primary/regression/sampling batch
had started under96; prior synthetic load/development smokes are not qualification.

AMD restarted the ENTIRE300 native candidate batch at18:37:56Z under an immutable
private copy of e411fc9f. All remaining native/primary/standard/sampling/base/
teacher comparison measurements must use that same envelope. No outcomes were
used to choose this rerun, tune the trained adapter, change tests or select an
arm. Orchestrator notice and AMD acknowledgment are in the track report and
https://github.com/metrum-ai/router/issues/359#issuecomment-6372123399 .

This amendment does not claim any completed qualification score, gate acceptance,
like-for-like parity, final economics or resource destruction yet.
