# Branch protection for Launch A (manual repo-admin gate)

Protect `main` so the normal contributor path cannot merge with failing required checks. Enable the merge queue for `main`. GitHub runs required checks on the pull request before queueing and again on the combined merge-group commit before merging.

The intended required status checks (names must match workflow job names) are:

- `ci` from `.github/workflows/ci.yml`, for pull requests and merge groups.
- `signed-off-commits` from `.github/workflows/dco.yml`.

The repository administrator owns the ruleset and must update it before the retirement PR is enqueued; this document does not change live settings. Source-header validation runs inside `ci` and separately on pushes to main; other path-specific checks continue to run separately. `supplemental-checks.yml` retains merge-group and manual checks not covered by `ci` (including docs, TypeScript compatibility, Harbor, D3 and LRP), but they are **not** part of the `ci` result and are no longer required by the intended two-check ruleset. Monitor their results independently. LRP remains path-filtered for merge groups and always runs on manual dispatch. SBOM generation and high-severity vulnerability scanning run as part of the required `ci` aggregate on merge groups and manual dispatch; on pull requests that job is skipped.

CodeQL currently uses GitHub-managed default setup. After the advanced CodeQL workflow reaches main, a repository administrator must disable default setup and set `CODEQL_ADVANCED_ENABLED=true`; until that coordinated switch, default setup continues to run and the new workflow intentionally skips. Advanced setup scans on merge groups and weekly, not pull requests. Verify an actual successful analysis after the switch.

Provider-backed workflows are not required checks. Dispatch them manually in the protected `evaluation` environment:

- API compatibility live: `.github/workflows/api-compat-live.yml`
- Inspect smoke or full: `.github/workflows/inspect-coding-evaluation.yml`
- Harbor promotion: `.github/workflows/harbor-promotion.yml`, capped at two agents, two groups, and four cells, with concurrency held at one

Harbor promotion needs repository secrets `HARBOR_ROUTER_TOKEN` and `HARBOR_ROUTER_BASE_URL` in that environment. The ephemeral `router-arc` job also needs the `harbor` CLI on `PATH`; provision it in the job/image rather than assuming a persistent host installation. See [ARC runner operations](ci-arc-runners.md). Missing credentials fail closed. Do not schedule these workflows and do not run them from pull requests or tags.

Also require:

- Verified commit signatures
- CodeQL results with no high-or-higher alerts / errors, subject to the coordinated setup transition above
- Dismiss stale reviews on new commits when reviews are required
- Restrict who can push / bypass (admins only, or none)
- Do not allow force pushes or deletions
- Merge queue enabled, and "only merge non-failing pull requests" left enabled so a group merges only after its combined checks pass

Self-hosted runners for these checks must include the `router` label (`self-hosted,Linux,X64,router`). Runners must persist `~/.cache/go-build`, `~/go/pkg/mod`, and `~/.npm` between jobs. If runners are ephemeral containers, bind-mount these directories from the host. setup-go and setup-node network cache archives are disabled: the persistent local caches avoid transferring gigabytes on every job. Metrum AI's weekly `runner-cache-trim.yml` cleans only the Go build cache when it exceeds 4 GiB, printing sizes before and after and reporting (without trimming) module and npm cache sizes. Operators must add a unique label per runner (for example `host-<name>`) and list every runner's label in the repository variable `RUNNER_CACHE_TRIM_LABELS` as a JSON array. The matrix runs independently for every label with no parallelism limit; each job trims only its selected runner. Without this variable, the `["router"]` fallback trims only whichever runner picks up the single job, not the whole pool. Jobs have a ten-minute execution timeout, but this does not bound queue time: cancel queued jobs for missing/offline labels and repair the label inventory. The Go pull-request job installs `uv` via `astral-sh/setup-uv`. Host Docker is optional tooling for the LRP rootfs test case when available; it is not a runner requirement.

Negative gate test: confirm a failing `ci` blocks a PR and that `ci` runs on the combined merge-group SHA. Record the PR URL and settings evidence. Settings are mutable and not captured by a git commit; keep evidence outside the tree.
