<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Agent canary smoke — 2026-11-03

AGENT-06 real-client validation for the v4.7.0 evaluation scope of #330: a real
`pi` client (1.4.0) drove **image input** and **reasoning** through a local
router pinned to a cheap model (`gpt-5.4-nano`, OpenAI Chat). Bounded: two
successful turns and two refusals, all hosted calls together **$0.00096**
(usage-derived, from the router's own usage rows).

| run | shape | result |
|---|---|---|
| image reading | `@nonce-image.png` + pi tools, `--thinking off` | `200`; pi answered `739182`, the exact six-digit nonce printed in the image — the image traversed the router |
| reasoning only | `--thinking high --no-tools` | `200`; telemetry bucket `reasoning_effort=high` reached the upstream |
| declared refusal | default pi shape (`reasoning_effort=medium` + pi tools) on a target declaring `reasoning.with_tools: false` (#703) | `400 unsupported_capability`, `param: reasoning_effort`, **0 upstream attempts** |
| undeclared control | same shape on the identical target without the declaration | provider `400 upstream-rejected` (the #697 limitation), recorded, not counted as a pass |

The undeclared control is why the declaration matters: a real coding client sets
reasoning effort by default and sends tools, and on a Chat-dialect reasoning
model that combination is provider-rejected unless the router fails closed first.

`evidence.json` holds the sanitized record: client and router shape, per-run
commands and outcomes, request-shape telemetry (reasoning bucket, image and tool
counts) and the usage rows with token counts and cost. `nonce-image.png` is the
generated fixture; its sha256 is in the evidence file.

Reproduce (requires `OPENAI_API_KEY` and a local router with an `agent-smoke`
group pinned to the same model, `input_modalities: [text, image]` and
`tool_support.openai_chat: [tools, tool_choice]`):

```sh
PI_CODING_AGENT_DIR=<dir with models.json pointing at the router> \
  pi -p --no-session --provider metrum --model agent-smoke --thinking off --mode json \
  "@nonce-image.png" "What six-digit number is printed in this image? Reply with only the number."
```

This is a smoke of the AGENT-06 gaps (images and reasoning with a real client),
not a certification: one turn each, one model, one client.
