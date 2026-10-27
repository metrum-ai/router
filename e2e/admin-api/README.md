# Admin Config API e2e suite

Scripted, repeatable real-upstream end-to-end validation for the Admin Config
API (#395) and the dedicated admin key (#350).

## What it does

`run_e2e.py` (Python 3, stdlib only):

1. Builds `router`, `routerctl`, and `router-migrate` from the current tree.
2. Migrates and seeds a SQLite config control-plane DB with a canonical
   document covering four real upstreams (OpenAI, Fireworks, Gemini,
   OpenRouter), three groups (plain, multi-shape, contract-tools), three admin
   keys (full, read-only, writer without `config:force`/`shadow:write`), and
   one high-limit caller.
3. Starts three DB-mode router processes on one shared SQLite config DB
   (multi-replica) plus one file-mode router, then runs the case matrix:
   - Positive: reads in both modes, dry-run diff, a live swap chain across
     four providers and back with continuous unary + streaming client traffic
     (zero errors, in-flight streams finish on the old target, per-replica
     propagation timing), Responses + Anthropic Messages shapes after a swap,
     add/remove/reorder with a deliberately broken first target proving
     failover, one-call group rollback, export/diff, CLI `groups` over HTTP,
     idempotent retry, multi-replica convergence via `/admin/config/status`,
     and the shadow lifecycle (start at `sample_rate` 0.5, comparison, promote
     with the audit link, then a second shadow stopped without promoting).
   - Negative: unknown provider/model, ad hoc credentials in the payload (a
     synthetic canary, then grepped out of logs and DB), contract-ineligible
     target, last-target removal, stale `If-Match`, validation policy refusal
     with `force` gating (403 without `config:force`, audited `forced=true`
     with it), no-token/caller-key/provider-key rejection, read-only admin
     write 403, missing `shadow:write` 403, file-mode 409
     `config_source_mode_file`, malformed body, oversized target list, and
     concurrent conflicting writes (exactly one wins).
4. Writes `evidence.json` + `SUMMARY.md` (per-case request, expected vs actual
   status, served provider/model, latency) and a cost estimate from usage
   rows.

## Run

```sh
# keys come from the environment and are never printed or written to evidence
OPENAI_API_KEY=... FIREWORKS_API_KEY=... GEMINI_API_KEY=... OPENROUTER_API_KEY=... \
  python3 e2e/admin-api/run_e2e.py [--evidence-dir DIR] [--keep]
```

The suite uses tiny prompts and small token caps; a full run costs a few
cents at most. It exits nonzero if any case fails.

Bedrock, Groq, xAI, Moonshot, Minimax, and Baseten are intentionally skipped:
the four covered upstreams already exercise OpenAI-Chat, OpenAI-Responses,
and Gemini dialects and cross-provider swaps; Bedrock Converse additionally
needs SigV4 request-signing setup.
