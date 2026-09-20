# SSE fixture corpus

Captured upstream SSE and golden caller-side streams for
`go test ./internal/stream/...` replay. Responses Phase B fixtures are synthetic and reproducible.

## Layout

### Upstream captures

```text
testdata/sse/<provider>/<dialect>/<shape>.sse
```

- `<provider>` — live upstream (for example `openai`, `anthropic`, a named
  vLLM/proxy label).
- `<dialect>` — `openai-chat`, `openai-responses`, or `anthropic`.
- `<shape>` — matrix shape id / short name (for example `001-plain-text-short`).

One file per provider × dialect × shape. Captured via `make sse-capture`
(`scripts/sse_capture.py`; currently synthetic generation only).

### Golden caller streams

```text
testdata/sse/golden/<upstream>-to-<caller>/<shape>.jsonl
```

- `<upstream>` / `<caller>` — dialect tokens (`openai-chat`,
  `openai-responses`, `anthropic`).
- One JSON event per line, field-for-field against translator output.
- Router-owned ids normalized to stable placeholders (`mr_TEST`,
  `mr_TEST_0`). Caller-side goldens must not contain upstream id prefixes
  such as `chatcmpl-`, `msg_01`, `toolu_`, `call_`, or `fc_` (ID-008).

The current `synthetic` corpus and goldens are generated together from
protocol-shaped synthetic events, not captured from a live provider. They prove
chunk-invariant replay and ID transformation, not live provider compatibility.
P2 covers `responses_to_chat` (Responses caller, Chat upstream). Its synthetic
`openai-chat` fixtures and `openai-chat-to-openai-responses` goldens cover UTF-8
text, interleaved parallel tools, mixed text/tools, and length-limited output.
Replay checks gap-free sequence numbers, stable encoded tool item IDs, content
part ordering, terminal usage, and byte/JSON/random chunk boundaries. To refresh
only these goldens, run `UPDATE_GOLDEN=1 go test ./internal/stream -run TestChatUpstreamToResponsesCallerReplay`.

P3 covers `chat_to_responses` (Chat caller, Responses upstream). Its synthetic
`responses-to-chat` fixtures contain Responses SSE, with full framed Chat SSE
goldens in `openai-responses-to-openai-chat/*.sse` (including `[DONE]`). They cover
UTF-8 text, empty output, refusal, interleaved tools with sparse upstream indexes,
mixed text/tools, length/content-filter termination, and detailed usage. Lifecycle
snapshots do not duplicate deltas; upstream sequence numbers are ignored.
Replay uses byte, fixed-size, and seeded random reads. Refresh only P3 goldens
with `UPDATE_GOLDEN=1 go test ./internal/stream -run TestResponsesUpstreamToChatCallerReplay`.

## Secret and content rules

At capture time, strip:

- `Authorization`
- `x-api-key`
- any `*-request-id` headers

Fixtures are committed. They must contain:

- **no credentials** (API keys, bearer tokens, cookies)
- **no customer content** — synthetic / canary prompts only

`make secret-check` must stay clean for this tree. Do not check in live
production traces.

## Phase B coverage

Run `make sse-capture` then `go test ./internal/stream/...`. Replay covers
whole-event, byte, split-UTF-8, split-JSON, and random-seed-42/1337 reads.
Shapes include plain/large text (>64 KiB), structured output, parallel function
calls, reasoning summaries, refusal, unknown events, incomplete and failed
responses. Protocol IDs normalize to `mr_TEST` placeholders; content is untouched.

Router fake-upstream tests cover first-delta delivery, HTTP failure before
commit, unknown-only fallback, no fallback after a delta, truncated streams,
malformed JSON, size limits, write failure, client cancellation propagated to
the attempt, Begin/Next/Finish panic recovery, exactly-once Finish, terminal
usage, ID rewriting, and synthesized configuration. Existing identifier tests
cover tool-result round trips and the F-011 restoration restriction.

The detailed incremental plan with numbered P1 shapes and F-001–F-010 definitions
is not present in this checkout, so these tests use descriptive names instead
of asserting an unverified mapping to those IDs. Live provider capture, client
SDK validation, and any plan-specific cases beyond the coverage above remain
unverified. No production traces or credentials are used.
