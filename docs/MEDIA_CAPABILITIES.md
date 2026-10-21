<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Media and Anthropic format admission

Metrum AI Router targets may declare `modalities: [text, image, document, audio]`
and `structured_output: true`. These are operator assertions of provider support,
not transformations of provider-owned file identifiers or provider certification.
When `modalities` is absent, existing `input_modalities` remains the compatibility
source (default text). When present, `modalities` replaces that source. New
`structured_output` defaults to false; Anthropic `output_config.format` requires
it even for native Anthropic targets.

```yaml
targets:
  - provider: openai
    model: gpt-4.1-mini
    dialect: openai-chat
    modalities: [text, image, document]
    structured_output: true
    request_shape_support:
      supported_inbound_dialects: [anthropic]
      validation_status: passed
```

Unsupported media returns **400 `unsupported_modality`** before any upstream
attempt, rather than 200 with content discarded or the former 502 eligibility
error. Chat `file`, Responses `input_file` and Anthropic `document` parts use the
`document` modality. File IDs, URLs and base64 content are retained in the
appropriate upstream shape. Native dialect passthrough retains opaque metadata.
Translated text-source documents become base64 text files, not flattened prompt
text. Images/documents survive both stream and unary translation, with and
without client tools, including nested Anthropic tool-result media.

Translated Anthropic unknown content blocks (including provider-private blocks
without a portable representation) return **400 `unsupported_block`**. Native
Anthropic targets retain opaque blocks. Document support does not imply that a
translated target supports Anthropic citations or arbitrary provider file IDs.
The operator must configure a target that accepts the supplied media format.

`output_config.format` without `structured_output: true` returns
**400 `unsupported_parameter`**. A translated Chat target receives
`response_format: {type: json_schema, json_schema: {name: response, strict: true,
schema: ...}}`; a Responses target receives `text.format`. Native Anthropic
receives `output_config` verbatim. Existing OpenAI capability declarations remain
available for OpenAI callers.

Anthropic `web_search_*` and Responses hosted tools share one rejection policy:
**400 `provider-hosted-tools-forbidden`**. Generic search/image descriptors are
no longer silently removed. Client-executed tools remain unaffected.

## Validation evidence (2026-10-21)

- `make api-compat-mock`: 247 passed against working-tree localhost routers,
  including unary/stream document+image forwarding to Chat/Responses with and
  without tools, native preservation, format mapping, 400s with zero attempts,
  and tool continuation. See `test_media_capabilities.py` and
  `test_anthropic_capabilities.py`.
- Built `tmp/w2b/metrum-ai-router` and ran it on `127.0.0.1:18271`.
  A real OpenAI `gpt-4.1-mini` Chat request through Anthropic Messages returned
  200 with schema-conforming `{"ok":true}` and usage 38 input / 5 output tokens.
- Final-build Claude CLI runs through isolated Fireworks Chat and OpenAI
  Responses targets each created `proof.txt`, continued with tool results and
  read it back: both exit 0, exact `w2b-tool-ok` plus newline bytes. Credentials stayed environment-only
  or in ignored local configuration.
- Initial real `gpt-6-luna` Chat probe with legacy `max_tokens` returned provider
  400 (router 502); the standalone direct format probe succeeded, identifying
  a model/request-parameter limitation rather than format translation failure.

Commands run: `go build -o tmp/w2b/metrum-ai-router ./cmd/metrum-ai-router`,
`tmp/w2b/metrum-ai-router --config tmp/w2b/config.yaml` (localhost port 18271),
Python `urllib.request` POST to `/anthropic/v1/messages` with the schema above,
and `claude -p --dangerously-skip-permissions` in disposable fixture directories
with environment-only caller credentials and isolated group models. The driver
asserted schema output and exact file bytes, then stopped the router.
`make test-fast`, `uv run --with pyyaml make test`, and `make docs-qa` passed.

## Silent-drop audit boundary

The generic hosted-tool stripping helper was removed, and nested tool-result
flattening was corrected for translated media. Provider-private reasoning is
not portable; unknown Anthropic blocks reject instead. Other independent
lossy surfaces (system/developer block flattening, arbitrary OpenAI opaque item
translation, and provider-specific image file-ID conversion) are not certified
by this work and must not be inferred from a modality declaration. Native
passthrough is the appropriate route for provider-private fields. These remain
reported limitations rather than claims of broad cross-provider equivalence.
