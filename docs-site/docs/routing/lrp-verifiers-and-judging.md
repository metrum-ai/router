---
title: LRP verifiers and judging
doc_type: explanation
---

# Verifiers and Judging

LRP quality labels are assigned offline. Deterministic verifiers run only
inside the isolated judge worker. There is no host-process fallback.

Allowlisted verifier kinds include `exact`, `regex`, `json_schema`, `pytest`,
`sql_result`, `plugin`, and `none`. Unknown kinds produce missing evidence,
not host execution.

Seed tooling adds allowlisted tool-call plugins for presence, name, args schema,
and normalized arg match. Those labels stay in separate nullable columns. They
are not silently written into training quality. A tool mismatch is agreement
evidence with a reference turn, not proof of task failure.

LLM judging and human audit are separate outcome classes. Verifier success,
pairwise preference, and rubric scores have different meanings. Third-party
judging requires operator approval for that content transfer.

Passing synthetic verifier tests does not authorize live routing activation.

Operator contracts: [LRP_VERIFIERS.md](https://github.com/metrum-ai/router/blob/main/docs/LRP_VERIFIERS.md).
Human audit: [LRP_HUMAN_JUDGE.md](https://github.com/metrum-ai/router/blob/main/docs/LRP_HUMAN_JUDGE.md).
Train pipeline: [Train and evaluate](lrp-train-and-evaluate.md).
