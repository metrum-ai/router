# Sanitized run evidence (2026-10-28)

- `mock-output.json`: local mock upstream, router and policy. Two allowed requests
  reached the upstream; it verified the scoped request's synthetic email was
  replaced. Denied/quota/rate requests did not reach it.
- `oidc-smoke.txt`: separate synthetic issuer/handler tests, not the same demo
  session and not a real IdP.
- `fireworks-output.json`: two actual Fireworks DeepSeek V4.1 Flash calls, four
  rejected requests with zero attempts, no fallbacks. Six distinct request IDs
  and two attempt records were checked in the private usage DB. Router telemetry
  reports a PII rule applied to the scoped request; hosted received bytes were not
  independently examined.

Reported input/output tokens: rate 33/20, scoped 35/20; cached input was 0 for
both. At the configured 2026-10-28 list prices ($0.30/M input, $0.006/M cached
input, $1.20/M output; [source](https://fireworks.ai/models/deepseek-ai/deepseek-v4p1-flash-1017)),
router usage costs were $0.0000339 and $0.0000345, totaling **$0.0000684**.
This is usage-derived and not a provider invoice; fees or later adjustments are
not reconciled. Audit CSV content and private state were not published; the
loopback router and policy stopped, and the private tmpfs state was removed.
