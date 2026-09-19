# SSE fixture corpus

Captured upstream SSE and golden caller-side streams for
`go test ./internal/stream/...` replay. Scaffold only — no fixtures yet.

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
(placeholder today: `scripts/sse_capture.py`).

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

Same-dialect goldens are captured from the native provider for that dialect,
not hand-written. Bridge goldens must be indistinguishable from native to a
real client (Claude Code, Codex).

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
