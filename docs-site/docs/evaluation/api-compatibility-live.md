<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Live API compatibility: baseline (in progress)

This is a **partial baseline**, not a release-candidate result or a compatibility guarantee. The final release matrix and rerun target are not yet complete.

## Scope and evidence

- Run date: 2026-10-31; router main `62e3a603637b9b850338f2a25dfcb8c23f7d3119`.
- Testers: [OpenAI](https://github.com/beranekio/openai-compatibility-tester/tree/5935805af52c01059e5a166b01cf1c28077103f5) binary SHA-256 `9a4c9192a5e53a8f448cc0d5decba9377e2b9a4e4fc92cd4ee7a838dc3e490a1`; [Anthropic](https://github.com/beranekio/anthropic-compatibility-tester/tree/799b0bca29ee8a95913b096ecbb5c0e0960068d2) binary SHA-256 `ebc047f07a1eeb6ba0e1a2e72aa12f9a020ff0f2a2107ee9313f95c9955a08f3`. Both are built from the pinned sources.
- Matrix targets: OpenAI `gpt-5-nano` and native Anthropic `claude-haiku-4-5`. The test config has `limited` validation and the Anthropic-to-GPT eligibility gate rejects most calls before a provider attempt. It is not evidence that the cross-dialect path works.
- Redacted synthetic suite logs and usage rows: artifact snapshots `70f1c941` and `fdc762c0` (`router/v45-proof/536`); direct diagnostics `d422cd71`; unchanged starter `1a73f34a`.
- Router usage rows for both matrix subsets total **$0.00703245** at configured list rates, not an invoice; one usage row is estimated and some attempts have no reported usage. Direct diagnostic: **$0.00014965** list-rate from provider-reported 65 input and 366 output tokens; the rejected logprobs diagnostic had no reported usage. Unchanged starter: **$0.00000825** metered, plus an attempt without usage. These are not a complete provider bill.
- The quota is a 200,000-token admission budget with estimated reservations; reconciliation and missing usage can prevent it from being a strict bound on provider billing.

## Counts

| Client → target | Passing / attempted | Not run |
| --- | ---: | ---: |
| OpenAI → GPT | 15 / 73 | 11 |
| OpenAI → Claude | 8 / 73 | 11 |
| Anthropic → GPT | 1 / 31 | 1 |
| Anthropic → Claude | 12 / 31 | 1 |

Eleven OpenAI suites were omitted because they may create remote objects or invoke hosted tools; Anthropic `messages_code_execution` was omitted because it may allocate a hosted sandbox. Omissions are not passes. An unchanged generated starter config on `gpt-4o-mini` passed 2 / 6 core suites; it uses a different model from the matrix.

## Suite results

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
| `responses_tools_stream` | Tester expectation | Bug [#539](https://github.com/metrum-ai/router/issues/539) |
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

The current harness reruns only the 31-suite OpenAI and 27-suite Anthropic subsets, not the full tables above or the starter run. It can create provider-side stored response objects while testing stored-response suites; cleanup may fail when the corresponding router endpoint is unsupported. Use only disposable test data. First prepare a fresh local YAML config with `gpt` and `claude` groups, a single caller with day/month/lifetime token limits no higher than 200,000, configured prices, disabled content capture, native provider targets, and fresh absolute SQLite usage and state paths. Set `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` and `ROUTER_CALLER_TOKEN` in the environment. Then, from the repository root:

```sh
COMPAT_LIVE_CONFIG=/path/to/config.yaml COMPAT_LIVE_OUT=/path/to/new-results python3 scripts/compat_live.py
```

The harness builds pinned testers and the local router, records hashes, and exits nonzero if a selected suite fails. Its meter is list-rate accounting, not an invoice, and can miss unreported provider usage. The `make compat-live` target, complete catalog classification, reruns after compatibility merges, and the release-candidate run are pending. Do not treat this baseline as the release result.
