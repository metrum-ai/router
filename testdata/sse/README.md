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
No bridge fixtures or P2 implementation are included.

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
