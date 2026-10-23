<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Gemini usage fixture

`thinking-response.json` reproduces the response shape reported in Metrum AI
Router issue #335. It contains no provider credentials or customer data.
Gemini's candidate count excludes thoughts; the output count is therefore
20 + 500 = 520. The same unary response is used for synthesized Chat streaming:
the router still calls `generateContent`, not `streamGenerateContent`.
