# Branch protection for Launch A (manual repo-admin gate)

Protect `main` so the normal contributor path cannot merge with failing required checks. Enable the merge queue for `main`. GitHub runs required checks on the pull request before queueing and again on the combined merge-group commit before merging.

Required status checks (names must match workflow job names):

- `test-and-vet` from `.github/workflows/go.yml`. Pull requests run `make test-fast`. On a merge group this job reports success immediately because `test-full` includes the same fast checks.
- `test-full` from `.github/workflows/deterministic-gate.yml`. On a pull request this job only records that the full suite is deferred. On a merge group, or a manual dispatch, it runs `make test`, docs QA, admin typecheck, the credential-free LRP sandbox, and the source SBOM/vulnerability scan once.
- `signed-off-commits` from `.github/workflows/dco.yml`.
- `validate` from `.github/workflows/source-headers.yml`.

Do not require path-filtered jobs such as `docs-qa`, `admin-typecheck`, `synthetic`, or `live-unit`. GitHub treats a skipped required check as pending and blocks the pull request. Those jobs still run when their paths match, and the merge-group `test-full` job covers docs, admin typecheck, and LRP for the combined SHA.

Provider-backed workflows are not required checks. Dispatch them manually in the protected `evaluation` environment:

- API compatibility live: `.github/workflows/api-compat-live.yml`
- Inspect smoke or full: `.github/workflows/inspect-coding-evaluation.yml`
- Harbor promotion: `.github/workflows/harbor-promotion.yml`, capped at two agents, two groups, and four cells, with concurrency held at one

Harbor promotion needs repository secrets `HARBOR_ROUTER_TOKEN` and `HARBOR_ROUTER_BASE_URL` in that environment. The self-hosted runner also needs the `harbor` CLI on `PATH`. Missing credentials fail closed. Do not schedule these workflows and do not run them from pull requests or tags.

Also require:

- Verified commit signatures
- CodeQL results (default setup) with no high-or-higher alerts / errors
- Dismiss stale reviews on new commits when reviews are required
- Restrict who can push / bypass (admins only, or none)
- Do not allow force pushes or deletions
- Merge queue enabled, and "only merge non-failing pull requests" left enabled so a group merges only after its combined checks pass

Self-hosted runners for these checks must include the `router` label (`self-hosted,Linux,X64,router`). Use bare-metal (non-container) runners; do not register Docker-based Actions runners for this pool. The Go pull-request job installs `uv` via `astral-sh/setup-uv`. Host Docker is optional tooling for the LRP rootfs test case when available; it is not a runner requirement.

Negative gate test (closure evidence for U02/A2):

1. Open a pull request that fails `make test-fast`.
2. Confirm `test-and-vet` fails and the merge button remains blocked.
3. Confirm `test-full` on that pull request finishes without running `make test`; the log says the suite runs on the merge-group SHA.
4. Queue two ready pull requests and confirm one `test-full` run validates the combined SHA.
5. Record the pull request URL and settings screenshot/API dump with the release evidence pack.

GitHub CLI sketch (adjust team/org as needed). Applying this replaces the live protection rule, so review it before running:

```bash
gh api \
  --method PUT \
  -H "Accept: application/vnd.github+json" \
  "/repos/metrum-ai/router/branches/main/protection" \
  -f required_status_checks='{"strict":true,"contexts":["test-and-vet","test-full","signed-off-commits","validate"]}' \
  -F enforce_admins=true \
  -f required_pull_request_reviews='{"required_approving_review_count":1,"dismiss_stale_reviews":true}' \
  -F allow_force_pushes=false \
  -F allow_deletions=false
```

Enable the merge queue in the repository settings after that rule is in place. Settings are mutable and not captured by a git commit; keep evidence outside the tree.
