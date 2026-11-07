<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Admin authorization dependency advisory gate

`ci / admin-authz-advisories` is additive: existing lane behavior, tests and secret scanning are unchanged. The required `ci` aggregate now depends on this job and rejects failure, cancellation or skipping on PR, merge-group and main-push events. A standalone optional check is not sufficient. It uses the HEAVY class, `ROUTER_HEAVY_RUNNER` with the existing `router-arc` fallback, pinned repository action versions, and pinned `govulncheck v1.9.0` against `./...` on the runner's source-build platform. Source analysis exceeds the small class's memory budget. `GOMAXPROCS=2` bounds Go CPU/build parallelism and `GOMEMLIMIT=8GiB` supplies a Go runtime soft memory target, leaving headroom on the heavy worker. This is not a hard process-memory limit and does not narrow package/symbol scope or change rejection criteria.

The scanner's JSON-mode exit status is **not** a clean-scan verdict. The standard-library-only parser requires the supported source/symbol protocol, actual Go version, SBOM and exact Casbin version from `go.mod`; it parses every finding. Imported-package or symbol findings block the job. Module-only findings remain explicitly listed as warnings/evidence, not suppressed and not described as a clean module graph. This does not prove safety on every cross-compilation target.

The job checks GitHub's affected-version advisory endpoint for the actual Casbin module/version and Casbin's published upstream repository advisories. Any returned advisory blocks for review, even an upstream entry not automatically classified as affecting this exact version. There is no automatic exception list. API failures or malformed responses fail closed. First-page collection is sufficient for this reject-any-entry policy; retained counts describe returned records, not a claim of exhaustive pagination or undisclosed-vulnerability coverage.

The current loader/store is router-owned; resolved dependency inventory establishes no selected external Casbin adapter/consumer module. The artifact records `go list -m` and `go mod graph`, including Casbin and govaluate versions. A newly selected external module requiring any Casbin engine is rejected until its adapter scope and upstream coverage are explicitly implemented; adapters are not silently assumed covered by the engine query. Casbin-related replacements also require source review. This is a conservative graph-based inventory, not proof about vendored or runtime-loaded code.

Evidence is uploaded even on failure and retained for 14 days: raw scanner stream, exact version, UTC scan-start time, resolved module inventory/graph, both advisory responses and parsed summary (when available). The summary records evaluation UTC separately and explicit selected-build triage for each retained module-only warning. No source is uploaded to an external scanner; the scanner fetches public vulnerability metadata. The GitHub token is restricted by the workflow's read-only permissions and is not recorded.

Run the offline parser regressions with:

```sh
python3 -m unittest discover -s scripts -p 'test_check_admin_authz_advisories.py'
```

A successful local replay of an old scan is parser evidence only. CI and release decisions require a fresh successful scan plus successful advisory fetches for the same dependency version. The gate does not replace code review, cross-platform analysis or the broader authorization acceptance suite.
