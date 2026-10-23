<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Usage provenance

Metrum AI Router records `request_usage.usage_source` as:

- `reported`: upstream usage (including usage carried by a response-cache entry).
- `partial`: upstream usage obtained before a committed stream failed.
- `estimated`: router-estimated usage; this is not an upstream tokenizer result.
- `none`: no usage was settled.

Historical records retain the empty string (unknown). Migration 2026102202
adds the column without guessing provenance from token counts. The warning
that previously identified estimates was not durably stored. Existing rollups
start with zero known estimated counters; zero therefore does not establish
that historical usage was entirely reported. New daily, hourly, and monthly
billing rollups retain estimated request, input-token, and output-token counts.

Native Chat streams request `stream_options.include_usage=true` when the client
omits `stream_options`. Explicit client options are preserved, including false.
Targets that cannot accept the injection can declare `stream_options` in
`request_shape_support.unsupported_request_features`. If streamed usage remains
absent, the router estimates output from observed text (Unicode characters / 4,
rounded up), not the requested `max_tokens` ceiling, and settles quota at that
estimate. Tool-only output with no textual content has zero estimated output;
this heuristic is not an exact count of tool arguments or hidden reasoning.

Request-list reports accept `usage_source=reported|partial|estimated|none`.
Request details expose `usageSource`. The overview and cost summary expose
`estimatedRequests`, `estimatedInputTokens`, `estimatedOutputTokens`, and
`estimatedTokenShare` (percentage of total tokens). The admin metric grid shows
an Estimated usage tile. Unknown historical tokens remain in the denominator;
use a post-migration time window for a complete provenance split.

Prometheus series include a bounded `usage_source` label. In particular,
`metrum_ai_router_input_tokens_total`, `metrum_ai_router_output_tokens_total`, and
`metrum_ai_router_tokens_total` can be filtered by provenance. Sum across this
label to reproduce the earlier aggregate. Quota settlement trace messages also
include the source and settled input/output counts.
