<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Router-captured training loop (Acme technical example)

Metrum AI Router is the control boundary, not merely the endpoint used after
training. Serve → one-key opt-in capture → authorized harness labels → audited
export → external fine-tune → same-group key-scoped canary → replay through the
router → preregistered gate. A rejected candidate stays rejected.

## Run a bounded collection

Choose a teacher whose **model license and provider terms permit training on
outputs**, and verify permission before exporting training data. Our scoped
maintainer decision for synthetic technical traffic is recorded in
`collect.py::DECISION` and every example export manifest; it does not establish
permission for customer data. Teacher is Fireworks-hosted `gpt-oss-120b`, whose
model license is Apache-2.0. Keep provider secrets in the environment.

Start from licensed task-seed canonical JSONL produced by `../prepare_data.py`
from the pinned Apache-2.0 Glaive revision. This file supplies initial prompts,
tool schemas and public gold calls for a deterministic checker. **Its recorded
assistant answers are never model inputs or a direct training fallback.** The
only training answers in the export are the new router-captured teacher answers.
Use an operator-owned directory outside Git, with restrictive filesystem access.

From repository root:

```sh
go build -o /secure/router ./cmd/metrum-ai-router
uv run --with bcrypt --with jsonschema python \
  examples/fine-tune-qwen3p8-27b/router-loop/local.py \
  --binary /secure/router --tasks /secure/canonical/train.jsonl \
  --out /secure/router-loop --per-split 3
uv run --with jsonschema python -m unittest discover \
  -s examples/fine-tune-qwen3p8-27b/router-loop -v
```

`FIREWORKS_API_KEY` must already be in the environment. The launcher generates
fresh caller keys, admin password and encryption key in child environment only;
none enter command arguments. It starts a worktree router on localhost :18410,
then terminates it even on failure. Only `acme-support` enables encrypted,
redacted seven-day capture; `acme-it` remains uncaptured. Caller request/token
budgets and rate/concurrency limits are configured. Generated configuration,
logs, SQLite state and datasets are private artifacts, not repository fixtures.
Ephemeral encryption keys are intentionally not persisted; retaining encrypted
capture for later export requires deployment-managed key custody instead.

The harness performs up to six actual request/response turns per episode.
Returned model function calls are JSON-schema validated and executed by
`execute(call, tools, seed)`, a deterministic mock returning an explicit mock
completion object. Results go back through the router as paired tool messages.
The checker compares the ordered tool name/argument objects with the public gold
and requires a nonempty final response. This is **tool-call task correctness**,
not human judgment or semantic final-answer quality. Failed episodes remain in
`collection-evidence.json` denominators; success-only export excludes them.

Public dataset attribution: Glaive AI,
`glaiveai/glaive-function-calling-v2@e7f4b6456019f5d8bcb991ef0dd67d8ff23221ac`,
Apache-2.0. Preserve upstream notices when redistributing derived data.

## API and dataset contract

- `POST /admin/training/outcomes/{request_id}` requires deployment-wide admin
  `content:training` action `label`. Only an existing complete encrypted,
  unexpired capture can receive an outcome. Caller keys cannot label themselves.
- `POST /admin/training/export` requires deployment-wide admin `content:training`
  action `export`. An example explicit policy is
  `p, basic:trace-admin, *, content:training, export|label`.
  Ordinary report/export/delete permissions do not imply training permissions.
- Both require JSON, same-origin browser requests, security reporting enabled,
  and a durable security-event insert before access. Audit failure refuses export.
- Filters: `token_id` (public ID, never bearer key), `project`, `client`, `group`,
  UTC `from`/`to` (half-open), `success_only`, `min_quality`. Required explicit
  boundaries: `validation_from`, `test_from`, ordered inside the range. Also
  supply `teacher_terms_decision.decision` and `task_source.license`; these are
  admin attestations, not machine verification of legal rights.
- ZIP contains canonical `train.jsonl`, `val.jsonl`, `test.jsonl`, `manifest.json`.
  Rows: `{messages, tools, provenance, outcome, replay}`. Function arguments stay
  JSON strings. Full tool continuations remain paired. Source-task hashes cannot
  cross frozen time splits; ambiguous captures/labels fail closed. Export is
  bounded at 10,000 usage records and 64 MiB. Narrow filters for larger cohorts.
- Manifest records filters/boundaries, counts/SHA256, dataset hash/export ID,
  audit event ID, privacy policy, public task attribution, and teacher decision.
  Provenance hashes/timestamps are never rewritten by content PII filters.
- `router-outcome-v1`: source `task_hash`, `task_success`, `schema_valid`, quality
  in [0,1], `acme-support-v1` backend, integer seed, public `expected_calls`.
  Labels are harness attestations. Concurrent duplicate labels are ambiguous and
  fail export; they are never silently resolved to a preferred label.
- Existing storage redaction remains; export additionally masks email, phone
  and diagnostic-secret patterns on parsed content strings. It never restores
  PII. Regex filters are not a universal privacy guarantee. Headers, raw vendor
  request extensions and caller/provider credentials are not training fields.
  Operators must review tool schemas/results, consent, policy and retention.

Capture deletion/expiry prevents subsequent export. It cannot recall previously
downloaded datasets. Protect and expire copies separately. Legal holds preserve
records; they do not grant training rights. Historical IR-only or unsupported
multimodal captures are skipped rather than converted into partial conversations.

## Replay and route back

`router-replay-v1` supplies `initial_messages`, `tools`, allowlisted backend
`acme-support-v1`, seed and checker `expected-tool-calls-v1`/`expected_calls`.
Replay only the initial fixture, execute **new** model calls with `execute`, and
compare with the checker. Never supply recorded teacher assistant/tool answers
to candidate/teacher/base as an oracle continuation. Do not execute code or URLs
from exported data. Freeze scorer/fixture/gates before training or held-out calls.

`canary.ts` binds both the support public key ID and project to `ft-canary`
inside the same `support-agent` group; other callers keep `teacher`. The launcher
proves this using a localhost synthetic candidate **stand-in**, not a trained
model. `canary-evidence.json` joins actual request IDs to selected provider/model.
Replace that target with the real FT endpoint only after FT training/serving;
retain caller quotas, rollback and preregistered regression gates.

## Measured example, not a promotion

The initial real-provider sample collected nine tasks per key (18 episodes),
with eight real tool-result continuations and seven deterministic successes per
key. Export contained only seven support rows: 2 train / 2 val / 3 test.
The canary's second support request was denied by its caller budget (429).
Public list prices × observed router tokens estimate $0.00210315 per nine-task
caller cohort, $0.00030045 per success including failed-task tokens. These are
not invoices or production ROI. All debugging plus collection estimated $0.0172.
The tiny sample proves engineering integration, not useful training or quality
improvement. Candidate/base/teacher held-out comparison, actual trained-model
canary, latency distribution and production ROI are **not measured yet**.
See the shared README and evidence RESULTS.md for run 1's rejected candidate;
its narrow JSON-text results are not comparable with this native-tool checker.
