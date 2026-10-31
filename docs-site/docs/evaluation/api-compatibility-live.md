---
title: Live API compatibility
doc_type: explanation
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Live API compatibility: baseline (in progress)

This is a **partial historical baseline**, not a release-candidate result or a compatibility guarantee. Current-model validation, the final release matrix and rerun target are not yet complete.

## Scope and evidence

- Run date: 2026-10-31; router main `62e3a603637b9b850338f2a25dfcb8c23f7d3119`.
- Testers: [OpenAI](https://github.com/beranekio/openai-compatibility-tester/tree/5935805af52c01059e5a166b01cf1c28077103f5) binary SHA-256 `9a4c9192a5e53a8f448cc0d5decba9377e2b9a4e4fc92cd4ee7a838dc3e490a1`; [Anthropic](https://github.com/beranekio/anthropic-compatibility-tester/tree/799b0bca29ee8a95913b096ecbb5c0e0960068d2) binary SHA-256 `ebc047f07a1eeb6ba0e1a2e72aa12f9a020ff0f2a2107ee9313f95c9955a08f3`. Both are built from the pinned sources.
- Matrix targets: OpenAI `gpt-5-nano` and native Anthropic `claude-haiku-4-5`. These are historical results: the [GPT model page](https://developers.openai.com/api/docs/models/gpt-5-nano) marks the former deprecated and [Anthropic's model overview](https://platform.claude.com/docs/en/about-claude/models/overview) lists the latter as legacy. The test config has `limited` validation and the Anthropic-to-GPT eligibility gate rejects most calls before a provider attempt. It is not evidence that the cross-dialect path works.
- The configured standard input/output prices were $0.05/$0.40 per million tokens for [GPT-5 nano](https://developers.openai.com/api/docs/models/gpt-5-nano) and $1/$5 for [Claude Haiku 4.5](https://www.anthropic.com/news/claude-haiku-4-5). Cached input and cache-write rates are recorded in the redacted config and usage evidence.
- Redacted synthetic suite logs and usage rows: artifact snapshots `70f1c941` and `fdc762c0` (`router/v45-proof/536`); raw request and server logs `e62a2874`; direct diagnostics `d422cd71`; unchanged starter `1a73f34a`.
- Router usage rows for both matrix subsets total **$0.00703245** at configured list rates, not an invoice; one usage row is estimated and some attempts have no reported usage. Direct diagnostic: **$0.00014965** list-rate from provider-reported 65 input and 366 output tokens; the rejected logprobs diagnostic had no reported usage. Unchanged starter: **$0.00000825** metered, plus an attempt without usage. These are not a complete provider bill.
- The quota is a 200,000-token admission budget with estimated reservations; reconciliation and missing usage can prevent it from being a strict bound on provider billing.

## Current-model subset (prior main)

On 2026-10-31, the same tester binaries and router main `62e3a603637b9b850338f2a25dfcb8c23f7d3119` ran a restricted 27-suite selection in each lane against [GPT-6 Luna](https://developers.openai.com/api/docs/models/gpt-6-luna) and [Claude Haiku 5.5](https://platform.claude.com/docs/en/models/haiku-5-5/overview). Both model metadata checks returned 200. The targets use `limited` validation; OpenAI storage is forced off. This is not the full catalog and is not a release-candidate result. Omitted suites are not passes.

For these short-prompt tests the configured list rates were $0.10 input / $0.50 output per million tokens for each model; consult the linked model pages and [Anthropic pricing](https://platform.claude.com/docs/en/about-claude/pricing) for cache and long-prompt rates. The router usage rows total **$0.00069810**, not an invoice. Thirty-four target attempts reported usage; seven upstream attempts had no usage. Evidence: snapshot `e3b5e635` (`router/v45-proof/536`); direct diagnostics `3e3cf1ef`. Bounded direct diagnostics returned provider 400s without usage for GPT chat tools at default reasoning, GPT chat logprobs, and Claude's legacy enabled-thinking shape. Those diagnostics used a 128-token output cap for GPT, unlike the tester; unreported provider charges cannot be inferred.

Legend: **P** = pass; **M** = model lookup/catalog bug ([#529](https://github.com/metrum-ai/router/issues/529), [#538](https://github.com/metrum-ai/router/issues/538)); **S** = stream bug ([#528](https://github.com/metrum-ai/router/issues/528)); **X** = cross-dialect translation bug ([#539](https://github.com/metrum-ai/router/issues/539)); **U** = bare unsupported endpoint error ([#531](https://github.com/metrum-ai/router/issues/531)); **E** = error-response bug ([#530](https://github.com/metrum-ai/router/issues/530)); **D** = document admission bug ([#537](https://github.com/metrum-ai/router/issues/537), [#530](https://github.com/metrum-ai/router/issues/530)); **L** = limited config blocked provider dispatch, error shape tracked in [#530](https://github.com/metrum-ai/router/issues/530) and [#539](https://github.com/metrum-ai/router/issues/539); **V** = provider limitation confirmed by direct probe, with router error mapping tracked in [#530](https://github.com/metrum-ai/router/issues/530); **V\*** = likely same limitation, not separately probed. GPT chat tool-stream and multi-turn attempts both reported upstream `reasoning_effort` errors; the direct nonstream tool probe confirmed the parameter restriction for that request shape. Individual suite passes are not comprehensive compatibility claims.

After #564, the same current-model subset was rerun on main `4b1a87e9f3a068680166c90855541c413945439d`. All four counts and individual pass/fail outcomes matched the table below; #564 changed docs dependencies and a CI timing guard, not runtime code. Router usage rows total **$0.00073210** at list rates, not an invoice; seven upstream attempts reported no usage. Evidence snapshot `84fe8f05` (`router/v45-proof/536`). The unchanged starter result below was not rerun at this head.

### OpenAI SDK → GPT / Claude: 13 / 27, 7 / 27

| Suite | GPT | Claude |
| --- | --- | --- |
| `models` | P | P |
| `models_get` | M | M |
| `chat_completions` | P | P |
| `chat_completions_stream` | S | P |
| `chat_completions_stream_usage` | P | X |
| `chat_completions_logprobs` | V | X |
| `chat_completions_json` | P | X |
| `chat_completions_vision` | P | P |
| `chat_completions_reasoning` | P | P |
| `chat_completions_tools` | V | X |
| `chat_completions_tools_stream` | V\* | X |
| `chat_completions_multi_turn` | V\* | X |
| `completions` | U | U |
| `completions_stream` | U | U |
| `embeddings` | U | U |
| `embeddings_batch` | U | U |
| `responses` | P | P |
| `responses_stream` | P | X |
| `responses_tools` | P | X |
| `responses_tools_stream` | P | X |
| `responses_json` | P | X |
| `responses_input_tokens` | U | U |
| `responses_compact` | U | U |
| `responses_vision` | P | P |
| `responses_reasoning` | P | X |
| `moderations` | U | U |
| `error_responses` | E | E |

### Anthropic SDK → GPT / Claude: 1 / 27, 11 / 27

| Suite | GPT | Claude |
| --- | --- | --- |
| `models` | M | M |
| `models_get` | M | M |
| `messages` | L | P |
| `messages_stream` | L | P |
| `messages_tools` | L | P |
| `messages_tools_stream` | L | P |
| `messages_json` | L | P |
| `messages_multi_turn` | L | P |
| `messages_count_tokens` | P | P |
| `messages_vision` | L | P |
| `completions` | U | U |
| `completions_stream` | U | U |
| `message_batches_create` | U | U |
| `message_batches_get` | U | U |
| `message_batches_cancel` | U | U |
| `message_batches_list` | U | U |
| `message_batches_results` | U | U |
| `message_batches_delete` | U | U |
| `messages_thinking` | L | V |
| `messages_prompt_cache` | L | P |
| `messages_document` | D | D |
| `beta_files` | U | U |
| `error_responses` | E | E |
| `beta_models` | M | M |
| `beta_models_get` | M | M |
| `beta_messages` | L | P |
| `beta_messages_stream` | L | P |

## Historical counts

| Client → target | Passing / attempted | Not run |
| --- | ---: | ---: |
| OpenAI → GPT | 15 / 73 | 11 |
| OpenAI → Claude | 8 / 73 | 11 |
| Anthropic → GPT | 1 / 31 | 1 |
| Anthropic → Claude | 12 / 31 | 1 |

Eleven OpenAI suites were omitted because they may create remote objects or invoke hosted tools; Anthropic `messages_code_execution` was omitted because it may allocate a hosted sandbox. Omissions are not passes. An unchanged generated starter config on `gpt-4o-mini` passed 2 / 6 core suites; it uses a different model from the matrix.

## Historical suite results

Each failure links its owning issue. “Investigating” is not a final classification. Unsupported APIs returning bare 404/405 are bugs rather than accepted unsupported results. “Limited config” means the failure was not a live provider test. Results reflect individual tester assertions; a pass does not establish all schema variants.

### OpenAI SDK

| Suite | GPT | Claude |
| --- | --- | --- |
| `models` | Pass | Pass |
| `models_get` | Bug [#529](https://github.com/metrum-ai/router/issues/529) | Bug [#529](https://github.com/metrum-ai/router/issues/529) |
| `chat_completions` | Pass | Pass |
| `chat_completions_stream` | Bug [#528](https://github.com/metrum-ai/router/issues/528) | Pass |
| `chat_completions_stream_usage` | Pass | Bug [#539](https://github.com/metrum-ai/router/issues/539) |
| `chat_completions_logprobs` | Provider denial; 503 mapping under review [#530](https://github.com/metrum-ai/router/issues/530) | Bug [#539](https://github.com/metrum-ai/router/issues/539) |
| `chat_completions_json` | Pass | Bug [#539](https://github.com/metrum-ai/router/issues/539) |
| `chat_completions_vision` | Pass | Pass |
| `chat_completions_reasoning` | Pass | Pass |
| `chat_completions_audio` | Bug [#576](https://github.com/metrum-ai/router/issues/576) | Bug [#576](https://github.com/metrum-ai/router/issues/576) |
| `chat_completions_audio_input` | Bug [#530](https://github.com/metrum-ai/router/issues/530) | Bug [#530](https://github.com/metrum-ai/router/issues/530) |
| `chat_completions_tools` | Pass | Bug [#539](https://github.com/metrum-ai/router/issues/539) |
| `chat_completions_tools_stream` | Bug [#528](https://github.com/metrum-ai/router/issues/528) | Bug [#539](https://github.com/metrum-ai/router/issues/539) |
| `chat_completions_multi_turn` | Pass | Bug [#539](https://github.com/metrum-ai/router/issues/539) |
| `chat_completions_get` | Not run | Not run |
| `chat_completions_list` | Not run | Not run |
| `chat_completions_delete` | Not run | Not run |
| `chat_completions_messages` | Not run | Not run |
| `chat_completions_update` | Not run | Not run |
| `completions` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `completions_stream` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `embeddings` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `embeddings_batch` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `responses` | Pass | Pass |
| `responses_stream` | Pass | Bug [#539](https://github.com/metrum-ai/router/issues/539) |
| `responses_tools` | Pass | Bug [#539](https://github.com/metrum-ai/router/issues/539) |
| `responses_tools_stream` | Tester expectation ([source](https://github.com/beranekio/openai-compatibility-tester/blob/5935805af52c01059e5a166b01cf1c28077103f5/internal/suites/responses_tools_stream.go), [#536](https://github.com/metrum-ai/router/issues/536)) | Bug [#539](https://github.com/metrum-ai/router/issues/539) |
| `responses_json` | Pass | Bug [#539](https://github.com/metrum-ai/router/issues/539) |
| `responses_get` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `responses_get_stream` | Not run | Not run |
| `responses_delete` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `responses_cancel` | Not run | Not run |
| `responses_input_items` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `responses_compact` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `responses_input_tokens` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `responses_prewarm` | Not run | Not run |
| `responses_vision` | Pass | Pass |
| `responses_reasoning` | Pass | Bug [#539](https://github.com/metrum-ai/router/issues/539) |
| `responses_web_search` | Not run | Not run |
| `responses_file_search` | Not run | Not run |
| `responses_code_interpreter` | Not run | Not run |
| `responses_multi_turn` | Pass | Pass |
| `moderations` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `moderations_image` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `images_generations` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `images_edits` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `images_generations_stream` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `images_edits_stream` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `images_variations` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `audio_speech` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `audio_speech_stream` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `audio_transcriptions` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `audio_transcriptions_stream` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `audio_translations` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `files` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `content_provenance_checks` | Bug [#580](https://github.com/metrum-ai/router/issues/580) | Bug [#580](https://github.com/metrum-ai/router/issues/580) |
| `uploads` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `uploads_cancel` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `batches_create` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `batches_get` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `batches_cancel` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `batches_list` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `conversations` | Bug [#580](https://github.com/metrum-ai/router/issues/580) | Bug [#580](https://github.com/metrum-ai/router/issues/580) |
| `vector_stores` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `vector_store_files` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `vector_store_file_batches` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `realtime_client_secrets` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `realtime_transcription_client_secrets` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `live_sessions` | Bug [#580](https://github.com/metrum-ai/router/issues/580) | Bug [#580](https://github.com/metrum-ai/router/issues/580) |
| `containers` | Bug [#580](https://github.com/metrum-ai/router/issues/580) | Bug [#580](https://github.com/metrum-ai/router/issues/580) |
| `container_files` | Bug [#580](https://github.com/metrum-ai/router/issues/580) | Bug [#580](https://github.com/metrum-ai/router/issues/580) |
| `videos` | Bug [#580](https://github.com/metrum-ai/router/issues/580) | Bug [#580](https://github.com/metrum-ai/router/issues/580) |
| `videos_variants` | Bug [#580](https://github.com/metrum-ai/router/issues/580) | Bug [#580](https://github.com/metrum-ai/router/issues/580) |
| `videos_characters` | Bug [#580](https://github.com/metrum-ai/router/issues/580) | Bug [#580](https://github.com/metrum-ai/router/issues/580) |
| `skills` | Bug [#580](https://github.com/metrum-ai/router/issues/580) | Bug [#580](https://github.com/metrum-ai/router/issues/580) |
| `skill_versions` | Bug [#580](https://github.com/metrum-ai/router/issues/580) | Bug [#580](https://github.com/metrum-ai/router/issues/580) |
| `fine_tuning` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `chatkit_sessions` | Bug [#580](https://github.com/metrum-ai/router/issues/580) | Bug [#580](https://github.com/metrum-ai/router/issues/580) |
| `chatkit_threads` | Bug [#580](https://github.com/metrum-ai/router/issues/580) | Bug [#580](https://github.com/metrum-ai/router/issues/580) |
| `agents` | Bug [#580](https://github.com/metrum-ai/router/issues/580) | Bug [#580](https://github.com/metrum-ai/router/issues/580) |
| `agents_sessions` | Bug [#580](https://github.com/metrum-ai/router/issues/580) | Bug [#580](https://github.com/metrum-ai/router/issues/580) |
| `assistants` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `assistants_threads` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `error_responses` | Bug [#530](https://github.com/metrum-ai/router/issues/530) | Bug [#530](https://github.com/metrum-ai/router/issues/530) |

### Anthropic SDK

| Suite | GPT | Claude |
| --- | --- | --- |
| `models` | Bug [#538](https://github.com/metrum-ai/router/issues/538) | Bug [#538](https://github.com/metrum-ai/router/issues/538) |
| `models_get` | Bug [#538](https://github.com/metrum-ai/router/issues/538) | Bug [#538](https://github.com/metrum-ai/router/issues/538) |
| `messages` | Limited config; error bug [#530](https://github.com/metrum-ai/router/issues/530) / [#539](https://github.com/metrum-ai/router/issues/539) | Pass |
| `messages_stream` | Limited config; error bug [#530](https://github.com/metrum-ai/router/issues/530) / [#539](https://github.com/metrum-ai/router/issues/539) | Pass |
| `messages_tools` | Limited config; error bug [#530](https://github.com/metrum-ai/router/issues/530) / [#539](https://github.com/metrum-ai/router/issues/539) | Pass |
| `messages_tools_stream` | Limited config; error bug [#530](https://github.com/metrum-ai/router/issues/530) / [#539](https://github.com/metrum-ai/router/issues/539) | Pass |
| `messages_json` | Limited config; error bug [#530](https://github.com/metrum-ai/router/issues/530) / [#539](https://github.com/metrum-ai/router/issues/539) | Pass |
| `messages_multi_turn` | Limited config; error bug [#530](https://github.com/metrum-ai/router/issues/530) / [#539](https://github.com/metrum-ai/router/issues/539) | Pass |
| `messages_count_tokens` | Pass | Pass |
| `messages_vision` | Limited config; error bug [#530](https://github.com/metrum-ai/router/issues/530) / [#539](https://github.com/metrum-ai/router/issues/539) | Pass |
| `messages_thinking` | Limited config; error bug [#530](https://github.com/metrum-ai/router/issues/530) / [#539](https://github.com/metrum-ai/router/issues/539) | Pass |
| `messages_prompt_cache` | Limited config; error bug [#530](https://github.com/metrum-ai/router/issues/530) / [#539](https://github.com/metrum-ai/router/issues/539) | Pass |
| `messages_document` | Bug [#537](https://github.com/metrum-ai/router/issues/537) / [#530](https://github.com/metrum-ai/router/issues/530) | Bug [#537](https://github.com/metrum-ai/router/issues/537) / [#530](https://github.com/metrum-ai/router/issues/530) |
| `completions` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `completions_stream` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `message_batches_create` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `message_batches_get` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `message_batches_cancel` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `message_batches_list` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `message_batches_results` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `message_batches_delete` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `beta_files` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `beta_skills` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `beta_skill_versions` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `beta_skill_version_download` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `beta_models` | Bug [#538](https://github.com/metrum-ai/router/issues/538) | Bug [#538](https://github.com/metrum-ai/router/issues/538) |
| `beta_models_get` | Bug [#538](https://github.com/metrum-ai/router/issues/538) | Bug [#538](https://github.com/metrum-ai/router/issues/538) |
| `beta_messages` | Limited config; error bug [#530](https://github.com/metrum-ai/router/issues/530) / [#539](https://github.com/metrum-ai/router/issues/539) | Pass |
| `beta_messages_stream` | Limited config; error bug [#530](https://github.com/metrum-ai/router/issues/530) / [#539](https://github.com/metrum-ai/router/issues/539) | Pass |
| `beta_message_batches_create` | Bug [#531](https://github.com/metrum-ai/router/issues/531) | Bug [#531](https://github.com/metrum-ai/router/issues/531) |
| `messages_code_execution` | Not run | Not run |
| `error_responses` | Bug [#530](https://github.com/metrum-ai/router/issues/530) | Bug [#530](https://github.com/metrum-ai/router/issues/530) |

## Rerun and limits

The current harness reruns only 27 OpenAI and 27 Anthropic suites per lane, not the full tables above or the starter run. It excludes stored-response and hosted-code suites to avoid creating provider-side resources, so it does not reproduce the complete baseline matrix. Earlier baseline runs requested stored responses; attempted cleanup through the unsupported router endpoint failed, and test logs did not retain their provider IDs. Use only disposable test data. First prepare a fresh local YAML config with `gpt` and `claude` groups, a single caller with day/month/lifetime token limits no higher than 200,000, configured prices, disabled content capture, native provider targets, `force_store_false: true` on every OpenAI target, and fresh absolute SQLite usage and state paths. Set `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` and `ROUTER_CALLER_TOKEN` in the environment. Then, from the repository root:

```sh
COMPAT_LIVE_CONFIG=/path/to/config.yaml COMPAT_LIVE_OUT=/path/to/new-results python3 scripts/compat_live.py
```

The harness builds pinned testers and the local router, records hashes, and exits nonzero if a selected suite fails. To test a particular PR head, set `COMPAT_LIVE_ROUTER_SOURCE` to a clean worktree at that commit; review that revision's handling of the safety settings before running. The harness and router commits are recorded separately. Its meter is list-rate accounting, not an invoice, and can miss unreported provider usage. The `make compat-live` target, complete catalog classification, reruns after compatibility merges, and the release-candidate run are pending. Do not treat this baseline as the release result.
