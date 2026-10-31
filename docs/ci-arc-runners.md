<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Router CI on ephemeral ARC runners

Workflow jobs target the repository-restricted `router-arc` Actions Runner
Controller scale set on DigitalOcean Kubernetes (DOKS).
`runner-cache-trim.yml` is the sole exception: it still maintains the pikachu
host and must not run in disposable pods. Each ARC job starts a single-use
agent-workstation pod on an 8-vCPU node. No prior job's HOME, tool installation,
workspace, module/build cache or `/var/tmp` contents are assumed to survive.

## Cold starts and cache policy

- Go setup explicitly enables its Actions-backed module/build cache (including
  tests/vet, Windows cross-compiles, docs, release, fuzz, FT and deterministic
  lanes). This replaces the persistent-host `cache: false` policy from #341.
  A cache miss still downloads/builds normally; a cache hit is an optimization,
  never a test prerequisite. Go tests retain their existing timeouts.
- npm setup caches downloads by the tracked admin or FT lockfile; each job
  still runs `npm ci`. Never cache node_modules or assume generated FT promptfoo
  workspace files exist: Make creates the directory, copies locks and installs.
- uv setup explicitly enables Actions-backed caching in consuming lanes. It
  must still resolve/sync on cache misses. LRP's heavier dependencies benefit
  particularly from restoration; no Docker dependency is added by this switch.
- API-compat and Harbor Make targets intentionally allocate disposable caches,
  provision locked dependencies with network access, then run fail-closed
  offline conformance. They already tolerate empty caches. Their private cache
  roots intentionally bypass normal restored caches; measure provisioning
  separately before proposing an optimization that preserves offline isolation.
- Workflow/Makefile audit found no hardcoded `/home/...` paths, `host-pikachu`
  or `fix/ci-local-runner-caches` references. Existing LRP `/var/tmp` artifacts
  are created within each job, not inherited; uploads reference that job's files.
  Existing optional sandbox/tool probes remain unchanged. Do not repair missing
  tools, HOME or permission errors by adding privileged/Docker fallbacks or skips.

## Acceptance and timing

PR and merge-group checks exercise the consolidated `ci` workflow. The
additional offline checks in `supplemental-checks.yml` run on merge groups or
manual dispatch and are not included in the required `ci` result. Report queue
delay separately from runner execution, each lane's conclusion, cache-hit/miss
evidence and `internal/router` test duration. Compare with the
maintainer's pikachu baseline: deterministic gate approximately9–10minutes,
internal/router approximately520seconds. First cold starts and later restored
runs are distinct measurements. Manual/protected provider lanes, release and
scheduled fuzz are not silently dispatched as benchmarks.

Record exact environment failures from runner logs; do not infer success from
local tool availability or hide the failure with timeout/permission changes.
