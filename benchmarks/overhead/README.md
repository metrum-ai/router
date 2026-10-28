<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Overhead benchmark preparation (#422)

This is **partial infrastructure**, not a measured router overhead result. It
adds a benchmark-only router wrapper for CPU and heap pprof capture and a local
mock tool-continuation smoke. It does not yet implement the direct-versus-router
load runner, decision-model deployment, cost/hardware recording or the required
real vLLM measurement. No GPU was used for the smoke.

## Mock-only smoke

From a clean checkout with Go, Python and `uv` installed:

```sh
make overhead-profile-smoke
```

This provisions the pinned local test dependencies, runs the profiler's race
tests, starts the wrapper and a synthetic localhost upstream, executes an OpenAI
Agents SDK streaming tool continuation, and parses the generated CPU and heap
profiles. It uses the `dev_no_license` build tag solely for this offline smoke.
It makes no hosted model calls and is not a performance benchmark.

## Profiling a load phase

`profiled-router` instantiates the same router service and HTTP timeout defaults
as the main binary but is a separate benchmark binary. Record its exact commit,
build flags and host; do not claim it is byte-identical to the production
binary. Build with the same intended licensing/build configuration as the
measured deployment. Use a private local state directory and a router config
with a loopback listener and environment references for credentials; never
commit credentials or config-expanded secrets.

```sh
go build -o /tmp/profiled-router ./benchmarks/overhead/profiled-router
# $WORK must already be an operator-owned private directory. The two named
# subdirectories must not exist: the wrapper creates them with mode 0700.
/tmp/profiled-router -config "$WORK/router.yaml" \
  -control-dir "$WORK/control" -profile-dir "$WORK/profiles"
# In a separate terminal, after readiness and warmup:
python3 benchmarks/overhead/profiled-router/control.py --socket "$WORK/control/control.sock" start
# Start the externally controlled fixed-concurrency load. While load is still
# in flight (not after all requests finish), stop sampling and take the heap:
python3 benchmarks/overhead/profiled-router/control.py --socket "$WORK/control/control.sock" stop
go tool pprof -top "$WORK/profiles/cpu.pprof"
go tool pprof -top "$WORK/profiles/heap.pprof"
```

Use a fresh wrapper process and output directory per profile. The `stop`
command stops CPU profiling before forcing GC and writing the heap snapshot, so
the forced GC does not contaminate the CPU profile. Sampling and forced GC
change workload timing; take unprofiled latency measurements separately and
label profiled runs. Default Go heap sampling can omit small allocations. The
control socket is restricted by its private directory and accepts only fixed
`start`/`stop` commands; do not expose or proxy it outside the local host.
Review profiles and logs for sensitive metadata before committing or archiving.

#422 still requires CPU and heap profiles under **32 and 128** concurrent
requests for both plain and decision-model paths, plus the specified shadow and
enforce comparisons, host details, repeated direct/routed vLLM results, and a
hot-path summary. Never treat this smoke as any of those results.
