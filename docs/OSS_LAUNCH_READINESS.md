# OSS Launch Readiness Register (Launch A)

Product display name: **Metrum AI Router**. Snapshot for this register should be
recorded at release time with commit SHA, tag, and artifact checksums.

## Claim ledger

Allowed for Launch A:

- Open-source self-hosted AI gateway / router
- Configurable model groups with YAML providers
- TypeScript `route(ctx)` and HTTP external policy extensibility
- Governed request-path controls (auth, quotas, allow lists, usage)
- Same-dialect native SSE for OpenAI Chat and Anthropic Messages
- Process-local dynamic-score affinity
- Estimated spend ceilings on semantic/dynamic_score only

Withheld / separately gated:

- Universal decision traces on every request type
- Global hard-dollar budgets / production spend guarantees
- Fully intelligent model selection
- Data-residency enforcement from `targets[].region`
- Universal streaming (Responses / cross-dialect remain unary-backed)
- Learned-routing savings (U11)
- AMD/Dell XE7745+MI350P validation (U12)

## U / A closure status

| ID | Disposition | Evidence |
|---|---|---|
| U01 | Accepted; code closed for Launch A containment | Durable liability + persistence latch in `internal/router/quota.go`; cache after settlement; committed-stream settlement; tests in `quota_test.go` / `native_stream_test.go` |
| U02 | Accepted; settings remain repo-admin gate | Workflows enforce `make test` + docs QA + security; see `docs/BRANCH_PROTECTION.md` for required-check configuration and negative merge test |
| U03 | Accepted residual; default cache off | Caller/project + sampling fields keyed; unknown Raw fields bypass; README corrected |
| U04 | Accepted | Quickstart `cd router`; upgrade guide uses `metrum-ai-router` artifacts; legacy stubs declared non-functional |
| U05 | Accepted | Count-tokens acquires concurrency before body read; LLM path already did |
| U06 | Accepted | `spend_ceiling` / `tier_ceilings` examples in `config.example.yaml` with scope comments |
| U07 | Narrowed; unit evidence present | Native stream + affinity unit tests; provider-backed matrix remains A3 operator evidence |
| U08 | Accepted scoped | Loopback sidecar remains documented supported private-policy pattern |
| U09 | Accepted partial | Go module versions reconciled in `THIRD_PARTY_NOTICES.md`; release workflow publishes checksummed assets |
| U10 | Accepted | Public display name Metrum AI Router; docs QA blocks stale titles |
| U11 | Deferred promotion gate | Learned-routing case study failed cost gate remains visible |
| U12 | Deferred promotion gate | AMD MI355X ≠ XE7745/MI350P |
| U13 | Closed by monorepo boundary | Commerce/fleet optional; request path must not import them |
| A1 | Code ready; capture clean-install log at release | |
| A2 | Needs repo-admin protection evidence | `docs/BRANCH_PROTECTION.md` |
| A3 | Unit paths covered; live provider matrix operator-owned | |
| A4 | Cache off; learning opt-in; settlement fail-closed | |
| A5 | Ceiling example + Community rights published | |
| A6 | Release workflow present; verify public URLs/assets at publish | |

## Operator actions still outside this tree

1. Configure branch protection and run the failing-check negative merge test.
2. Rotate any real Stripe keys previously present in local `commerce.env.json`.
3. Publish signed/checksummed GitHub Release assets via the Release workflow.
4. Verify `https://metrum.ai/router` and docs redirects resolve.
5. Capture clean-clone quickstart and package smoke transcripts against the release tag.

## Local evidence captured during implementation

- `python3 scripts/check_env_example_secrets.py` / self-test: pass
- `python3 scripts/validate_docker_context.py`: pass (`commerce.env.json` excluded)
- `make docs-qa`: pass
- Targeted `go test` for quota, cache, security.txt, PII cache ordering, affinity, and stream settlement: pass
- `python3 scripts/local_dev_bootstrap.py --out-dir /tmp/metrum-ai-router-launch-smoke`: wrote config/license/token files (`BOOTSTRAP_OK`)

