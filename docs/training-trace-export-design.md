<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Governed training trace export — draft design (#358)

Metrum AI Router remains the capture, authorization and selection boundary. This
is a work-in-progress design, not a declaration that the complete loop is ready.

## Existing foundation

`content_capture.go` encrypts redacted content in dedicated relational tables,
with explicit operator opt-in at server/caller/group scope. Request IDs join
usage identity (caller public token ID, project, client, resolved group), usage
and cost. Retention and deletion remove exportability; legal holds preserve
records but do not grant training consent. Metadata-only operational reports and
LRP collection must remain metadata-only. Captured headers are never training
inputs. The capture-time identity, rather than current caller configuration,
must govern historical selection and authorization.

The text-oriented IR discards OpenAI tool-call IDs/arguments. New opt-in captures
therefore preserve an allowlisted `chat_messages` representation alongside IR,
only when tool capture is enabled. Raw vendor request extensions are excluded.
Text/chat-function calls are supported initially; unsupported multimodal shapes
omit the canonical representation rather than inventing a partial transcript.
Old captures without the representation are ineligible for canonical export.

## Export and outcome contract

Proposed endpoints: `POST /admin/training/export` and
`POST /admin/training/outcomes/{request_id}`. Require explicit deployment-wide
admin policy for `content:training` actions `export` and `label` respectively;
ordinary report viewers, caller keys and content deletion permissions do not
implicitly grant training export. A security event must be durably written
before privileged access proceeds. No payloads or credentials belong in audit
rows. Outcome attachment requires an existing encrypted, complete, unexpired
captured request, not an uploaded transcript.

The export response is a ZIP containing `manifest.json`, `train.jsonl`,
`val.jsonl`, and `test.jsonl`. Effective filters: public token ID, project, client,
model group, half-open UTC time range, success-only, minimum quality. Split
boundaries are explicit UTC times supplied before training/evaluation. Export
must fail rather than put one source task in multiple splits. Deduplicate
canonical transcript hashes, retain provenance hashes, file counts and SHA256s.

Rows use `{messages, tools, provenance, outcome, replay}`. Canonical OpenAI
assistant `tool_calls[].function.arguments` remain JSON strings, with subsequent
`tool_call_id` results. Export the final complete request history plus the final
assistant response, not isolated first-turn requests. Never feed recorded
teacher answers to candidate replay. Replay requires a versioned allowlisted
backend/checker and frozen initial fixture, never arbitrary code/URLs.

Draft outcome version `router-outcome-v1`: source `task_hash`, boolean
`task_success`, boolean `schema_valid`, numeric `quality` in [0,1], allowlisted
`backend` and integer seed. These are authorized harness attestations, not router
inferences from HTTP 200 or claims of independent judgment. Missing labels are
ineligible, never default success. Success/quality filters apply to train/val,
not test: frozen test preserves eligible labeled teacher failures. Evaluation
must also retain every task in its denominator. General/text completion labels
assert completed transport/schema validity, not semantic answer correctness.
The importable backend.py interface is initialize/execute/success with isolated
per-episode state and no remote schema references.

Export never restores PII. Preserve storage redaction and add export filtering
on parsed strings. Header contents, raw metadata, caller credentials, provider
keys and encryption material are excluded. Regex filtering is not a guarantee
against all possible sensitive data; operators still require reviewed policy,
consent and secured downstream retention. Export artifacts are customer-owned
copies: capture retention/deletion cannot recall an already downloaded dataset.

## Canary and downstream integration

Existing script routing exposes `caller.id`, project and public `tokenId` and
can select targets within the same group. Prefer that mechanism over introducing
another routing configuration surface; caller quotas/budgets remain in force.
A demonstrated two-key canary and real-provider collection are still required.
The exporter aligns with FT's `metrum-router-ft-export-v1` consumer contract:
splits/boundaries, audit reference, teacher decision/privacy attestations,
trace/task SHA256, tool-schema validity and replay initial fixtures. The actual
FT `run2/router_export.py` consumer validated a real seven-row sample without
rewriting its manifest. This proves schema integration, not useful cohort size
or training authorization. Full cohort mix/counts and semantic evaluation/gate
mapping must be frozen with FT before expanded collection/training. No actual
trained-model promotion is claimed.

## Teacher-license decision: maintainer-authorized synthetic technical test

Do not assume an open-weight license overrides hosted-service restrictions.
On 2026-10-23, `https://fireworks.ai/terms-of-service` returned a PDF whose SHA256
was `78c0dfb857eb860d15cfc2dc7296f3b19950d3145b0e5a40bc46e28cfbd19d30`.
Section 2.2(c) prohibits using content available on/via the service for machine
learning/AI training or development; 2.2(i) also restricts benchmarking and
competitive analysis. Section 3.2 says users own Content, but that does not by
itself resolve the explicit use restriction. No applicable negotiated account
exception has been supplied. Do not collect teacher training outputs through
Fireworks under an assumed permission.

Maintainer decision, 2026-10-23: proceed with a Fireworks-hosted open-weight
teacher for this synthetic technical test of the loop (illustrative Acme
scenario and deterministic mock tools), not real customer outputs. Decision:
https://github.com/metrum-ai/router/issues/358#issuecomment-6366186842 . This is a
scoped maintainer decision, not a claim that public provider terms generally
permit output training. Record it and the pinned snapshot SHA in each example
manifest as `teacher_terms_decision`. Collection cap remains approximately $20.

Users must select a teacher whose model license AND provider terms permit
training on its outputs and verify permission before enabling training export.
The example decision does not grant permission for customer traffic.
