<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# v4.7.2 release-candidate verification

RC: `fc77797c245516e2a6fb3f98de5ee22ee4d3202a`.
Run date: 2026-11-15. One sequential invocation of `make release-gate`, unchanged
$15 configured cap, no target/model/suite/validation exceptions added.
The overall gate exits nonzero because the recorded red cells remain red.
This is bounded release evidence, not certification of untested surfaces.

Exact archived configuration SHA-256:
- Stage A: `41a3dc29746c2728fec3633db05a1c8c40bd59dae6076b60f41eefec3d451b13`.
- Stage C: `cafaee3d2dab566205463863c844fa3b707904b4c7dba46896102912d00dd7f0`.

## Gate comparison

| Stage | RC result | v4.7.1 result |
| --- | --- | --- |
| A | 20/24 | 20/24 |
| B | not run: needs a Docker host | pass: 5/5 in both API styles, plus offline replay |
| C | fail, fully classified; 63/108 suites pass | fail, fully classified; 63/108 suites pass |
| D | pass: 6/6 | pass: 6/6 |

Stage A red cells: routed Responses-to-Claude quote (`429 tpm-exceeded`), and
routed Chat-to-GPT file/edit/quote (`400 upstream-rejected`; edit retries also
observed TPM admission refusals). These are failures, not unsupported passes.
All other routed and direct file/edit/quote cells passed their tool and file
or independent unit-test checks.

Stage C ran the same pinned tester commits and selected suites. All current
per-suite verdicts and error messages equal the archived comparison reports
used by v4.7.1 after normalizing only request IDs and the loopback port. Every
current failure is listed below; none was reclassified from an earlier run.

| Tester/target | Passing suites | Current failing suites |
| --- | --- | --- |
| openai-gpt | 11/27 | `chat_completions`, `chat_completions_json`, `chat_completions_logprobs`, `chat_completions_multi_turn`, `chat_completions_reasoning`, `chat_completions_stream`, `chat_completions_stream_usage`, `chat_completions_tools`, `chat_completions_tools_stream`, `chat_completions_vision`, `completions`, `completions_stream`, `embeddings`, `embeddings_batch`, `moderations`, `responses_compact` |
| openai-claude | 18/27 | `chat_completions_logprobs`, `completions`, `completions_stream`, `embeddings`, `embeddings_batch`, `moderations`, `responses_compact`, `responses_input_tokens`, `responses_reasoning` |
| anthropic-gpt | 16/27 | `beta_files`, `completions`, `completions_stream`, `message_batches_cancel`, `message_batches_create`, `message_batches_delete`, `message_batches_get`, `message_batches_list`, `message_batches_results`, `messages_document`, `messages_thinking` |
| anthropic-claude | 18/27 | `beta_files`, `completions`, `completions_stream`, `message_batches_cancel`, `message_batches_create`, `message_batches_delete`, `message_batches_get`, `message_batches_list`, `message_batches_results` |

The failures retain typed HTTP 400 or 501 responses. The full error text and
per-request diagnostics are archived with the run. The model/surface-specific
failure list is retained rather than described as a complete API pass.
Stage D completed six direct/routed/native/translated parallel-tool cells with
no reported problems and no halted dispatch.

The unchanged Stage A harness does not rewrite a quoted absolute usage path
in the baseline file. Its nominal counter reads an alternate nonexistent
file and reports zero; that value is not asserted as spend. The actual usage
database was independently captured and metered. Stage C and D have their own
meter records. Direct Stage A provider cost is not metered by that harness.
No invoice-cost claim is made here.

## Additional streaming proof (not a gate stage)

An additional **non-gate** RC streaming check wrote a 200,000-byte file
from 200,632 bytes of tool arguments and completed the actual tool-result
continuation. Its largest upstream SSE line was 209,948 bytes. Replaying
the identical provider stream against the released v4.7.1 binary reproduced
`upstream_frame_too_large` and a missing terminal marker after HTTP commitment.
This focused proof supplements the gate; it does not add a passing gate cell.

The file SHA-256 is `05ece9bde690bf39239aca4213a06d0a5ddb2eb6ec9ce0c2a5593bb1832f5b2a`.
The client decoded the returned arguments, wrote the file, then sent the tool
result back through the RC. Both fixed-router responses ended with their
terminal markers. The released v4.7.1 replay had already committed HTTP 200
before truncation; its audit class was `upstream_frame_too_large`, status 502.
No request or response body is included in this record.

## Upgrade and rollback check

The release asset `metrum-ai-router-v4.7.1-linux-amd64.tar.gz` was verified
against its released SHA256SUMS entry:
`8a1736411420be7947bc13037a36603457b31cedb86f0cd31fb528f4669a0216`.
The released executable reports v4.7.1, commit 661dcbf6, Go 1.26.10.

A fresh SQLite fixture was created by that released server and one mock-backed
request wrote its first usage row. Both migration CLIs reported schema 12,
zero pending migrations, state current. The RC opened that database and a
second mock-backed request wrote row 2. All 13 migration-ledger entries and
the schema DDL digest were identical before and after the RC write:
`5bf268e730d233428fe0e7432fa6362efd401236371a017117ed0a5c8665484a`.
The released v4.7.1 server reopened it, and its released usage-report command
read the resulting usage rows. This proves the tested SQLite path; no
production database or PostgreSQL upgrade was exercised by this fixture.

Commands, using a temporary local fixture configuration and database:

```sh
released/metrum-ai-router --config old-create.yaml
released/metrum-ai-router-migrate --db usage.sqlite --action status --json
rc/metrum-ai-router-migrate --db usage.sqlite --action status --json
rc/metrum-ai-router --config rc-write.yaml
rc/metrum-ai-router-migrate --db usage.sqlite --action status --json
released/metrum-ai-router --config old-reopen.yaml
released/metrum-ai-router-migrate --db usage.sqlite --action status --json
released/metrum-ai-router-usage-report --db usage.sqlite --since 24h
```

All listeners for these fixtures were separate loopback instances and were
stopped afterwards. No existing service was restarted or reconfigured.

## Evidence digests

These hashes bind the archived reports and summaries to this record. They do
not substitute another commit's results for the RC run.

| Report | SHA-256 |
| --- | --- |
| canonical-results | `04acbb511cca73598c79490fb9973dcbf368982ba022b7d304c2d919a50ea4f3` |
| A-monitor-rows | `d79676397e4ff67b1898792742358218288ba7ea6e5fae5703ffd10450f4686a` |
| A-actual-usage-summary | `127b4582695f4ede32cc23bede3a4390f5dbeccfe4ca83466d8021b4d7969b5c` |
| C-comparison | `776581c8d2ee92b6119a07b33bc2e76f61822f540e4d23e893a54b7732e4993d` |
| D-result | `8a66f1b8708a80226725323554121037ade0ae48db79886fd526a88f43a5275b` |
| non-gate-streaming | `3c4add91cc844dd2a0fc57f3863a38e03981da6fc2c7b2c0d6f7cd3d5e205149` |
| upgrade-summary | `f2e0d7c3d03132738c5f1af43c01f90c3d02513aa2527f0403956933574ece52` |
| C-openai-gpt | `c3df88c942f44465a30672ac5d39d61c9c69a2cf77897f80d6ba518792fdfae0` |
| C-openai-claude | `2a68bb9dc982e783b51033ab579bd968840d8912c7cd48d474d947cf9746b23c` |
| C-anthropic-gpt | `f95cdea8f04156939d012a817d28f994bef432efac42f7fe1da5c03510293865` |
| C-anthropic-claude | `adb9c69374bb5dbe81a24d06a88344a18f29e377f69a4ba9a600d0f593de2932` |
