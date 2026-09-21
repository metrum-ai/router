# Product Capability Matrix

This matrix records what is visible in this repository and current docs. Keep it current when adding runtime behavior, config, or product claims.

| Area | Status | Notes |
|---|---|---|
| OpenAI Chat Completions | Implemented | `/v1/chat/completions` request shape supported. |
| OpenAI Responses | Implemented | Used by Codex CLI and Responses-compatible tool flows. |
| Anthropic Messages | Implemented | Used by Claude Code CLI and Anthropic-compatible clients. |
| Native same-dialect streaming | Implemented | OpenAI Chat, OpenAI Responses, and Anthropic Messages proxy incremental upstream SSE on same-dialect paths; cross-dialect bridges remain unary upstream with router-encoded caller streaming unless a bridge explicitly validates streaming. A committed native stream cannot fall back. Historical synthetic Responses SSE after a unary upstream was not native streaming proof; native Responses streaming landed with #103. |
| Gemini `generateContent` adapter | Implemented, evidence-gated | Outbound unary-text codec only; activation requires exact-model operator-attested direct and restricted router evidence. No caller-facing Gemini endpoint, tools, images, reasoning, structured output, or streaming. |
| Filtered model discovery | Implemented | `/v1/models` returns groups allowed for the caller token. |
| Caller tokens | Implemented | Raw tokens are generated once; config stores hashes. |
| Per-key allow lists | Implemented | Disallowed model requests return before provider routing. |
| Rate/usage limits | Implemented | RPM, TPM, concurrency, caller/server traffic shaping, quota, and lifetime budget fields exist in config/state behavior. |
| Weighted/failover/static routing | Implemented | Configured under deployment-defined model groups. |
| Dynamic-score routing | Implemented | In-process rolling observations for latency, throughput, reliability, plus catalog cost and evaluation metadata. Response-cache hits are excluded from adaptive observations. |
| Dynamic-score conversation affinity | Implemented | Caller-isolated, process-local, fixed-TTL prefix pins reorder only currently eligible targets; hits do not refresh TTL and successful fallback does not repin. |
| Model-group routing contracts | Implemented | Optional `models.<group>.contract` filters group-local targets by declared API surfaces, capability requirements, validation metadata, quality floors, and operational thresholds before strategy selection. |
| TypeScript routing | Implemented | Fresh synchronous Goja VM per decision with a bounded per-group admission cap and optional allowlisted HTTP helper; pure JavaScript has no execution deadline after admission. |
| External routing policy | Implemented | Trusted HTTP policy service receives derived context and deployment/caller inventory, then returns validated eligible targets. Baseline, shadow, and enforce modes support controlled promotion and rollback. |
| External provider keys | Implemented | Provider credentials are server-side env/config values. |
| Tool-aware routing | Implemented | Tool support metadata is dialect-specific. |
| Image/VLM-aware routing | Implemented | Image content is detected and target eligibility uses `input_modalities`. |
| Request-time cost accounting | Implemented | Usage rows/logs include configured price fields and calculated costs. |
| Target region diagnostics | Implemented | Optional bounded `targets[].region` is stored for the serving target, including successful fallback. It is metadata, not location enforcement or a routing-policy input. |
| Upstream-reported billed cost | Implemented where provider returns it | Stored separately from calculated router cost when available. |
| Usage DB/reporting | Implemented | GORM-backed relational usage storage and markdown report tool. |
| Browser admin reports | Implemented first slice | Disabled by default; browser-admin identity plus Casbin policy gates embedded report UI, domain-scoped JSON APIs, request drilldown/evidence bundles, spreadsheet-safe CSV, escaped Markdown export, and shared short-label chart legends for long category buckets under `/admin/reports/`. |
| Diagnostics tables | Implemented | Attempts, trace events, and sanitized request errors. |
| Governed content capture foundation | Implemented first slice | Disabled by default; enabled capture requires AES-256-GCM application encryption, a configured `local_key_id`, out-of-band local key material via `CONTENT_CAPTURE_LOCAL_KEY`, content-admin delete/purge, retention timestamps, and audit events. Export/read APIs remain follow-ups. |
| Commercial retention foundation | Implemented first slice | Disabled by default; config-derived policy/rule rows, legal holds and audits, dry-run status jobs, and batch purge for usage diagnostics, governed content capture, and rollup-gated usage detail. Archive/export, scheduler, remaining purge classes, and full admin UI/API workflows remain follow-ups. |
| Response cache | Implemented | In-process LRU/TTL for eligible non-tool requests. |
| Prometheus metrics | Implemented | Restricted to caller subjects authorized for `metrics` `read`; existing `metrics_admin: true` callers remain compatible. |
| Embedded hosted docs | Implemented | Docusaurus output embedded in release binaries. |
| Version metadata | Implemented | `/version`, health responses, docs badge, and headers. |
| Codex CLI smoke | Implemented operationally | Production smokes validate tool file creation. |
| Claude Code CLI smoke | Implemented operationally | Production smokes validate tool file creation. |
| Warp/OpenAI Chat tools | Supported by API shape | Validate with OpenAI Chat tool smoke when changing tool routes. |
| Signed license enforcement | Implemented | Signed Ed25519 envelope; capability, time, and volume entitlements enforced in-router. |
| Public model marketplace | Not a product goal | Can use marketplace providers as upstreams. |
| Enterprise web dashboard | Partial | Admin browser reporting is present; broader SSO/session administration and compliance workflow automation remain follow-ups. |
| Automated model quality oracle | Not implemented as a generic feature | Use explicit evals and smokes for model activation. |
| Fixed-model outcome gates | Implemented examples | Harbor and fixed-model OCR examples preregister scalar pass/error, latency, and cost thresholds; regression exits nonzero and live evidence remains outside the repository. |
| Optional commerce / licensing packages | Optional in monorepo | Commerce and licensing packages may remain in this Apache-2.0 tree as optional operator tooling. They are not required to run the Community router. Payment processing, card data, and private signing keys stay out of the request path; the router is not a billing ledger. |
