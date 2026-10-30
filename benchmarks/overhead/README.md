<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Overhead benchmark preparation (#422)

This is **partial infrastructure**, not a measured router overhead result. It
includes a benchmark-only router wrapper, a paired load driver and a separately
profiled load phase. It does not yet implement decision-model deployment,
cost/hardware recording or the required real vLLM measurement. No GPU was
used for the smokes.

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

`make overhead-decision-smoke` exercises the paired load driver against the
same synthetic backend with plain, shadow and enforce configs. The latter two
use the real d3 adapter and a **fake** d3 service, not a real decision model.
The test verifies upstream model/prompt/output settings and per-request policy
telemetry at concurrency 2. It does not establish decision accuracy, real model
cost/latency, high-concurrency behavior or evidence for #422 acceptance.

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
commands; do not expose or proxy it outside the local host. Manual `stop` has no
concurrency guard and is a diagnostic only; it is not evidence of a #422
profile under load. Review profiles and logs for sensitive metadata before
committing or archiving.

## Guarded profiled load phase

Use a fresh wrapper process, control directory and profile directory for each
profile. With the wrapper ready and the backend independently warmed, run a
**separate profiled** phase (not the unprofiled latency measurement):

```sh
python3 benchmarks/overhead/profile_phase.py \
  --router-url "$ROUTER_CHAT_URL" --router-token-env ROUTER_TOKEN \
  --control-socket "$WORK/control/control.sock" \
  --model "$SERVED_MODEL_AND_GROUP" --path-kind plain \
  --prompt-file synthetic-prompt.txt --output-tokens 32 \
  --concurrency 32 --output-dir "$WORK/profile-phase"
# Inspect the profiles only after the phase exits successfully:
go tool pprof -top "$WORK/profiles/cpu.pprof"
go tool pprof -top "$WORK/profiles/heap.pprof"
```

Repeat independently for concurrency 128 and for verified decision shadow and
decision-enforce configurations; `--path-kind` is an operator label, not proof
of the actual config. Use `--streaming` if that is the desired phase. Each
worker warms its connection, then CPU profiling starts just before the load
barrier releases. The driver polls for at least the requested number of active
chat handlers and the requested observation duration. At CPU stop and through
forced-GC heap capture, the wrapper records the lowest active handler count;
any drop below 32 or 128 invalidates the capture. These are router HTTP
handlers, not proof of concurrent GPU generation or downstream work. Isolate
this experiment from other clients: unrelated chat requests could inflate that
count. The sampled observation period is polled, so its count is not guaranteed
continuous between polls; the capture guard itself tracks every handler exit.

For exploratory captures when normal gaps between client requests make exact
occupancy unattainable, `--minimum-active-percent 90` opts into a lower floor:
29 active handlers for 32 clients or 116 for 128 clients. The same floor is
polled in the observation window and tracked through CPU stop/heap capture; the
metadata records both client concurrency and the selected floor. This does
**not** prove exact 32/128 active handlers or satisfy a stricter exact-occupancy
acceptance criterion. The default remains 100%, with no silent fallback.

If the load ends too early, any request fails, token usage is inconsistent, or
the guard fails, no `profile-phase.json` is written and **both profile files
must be treated as invalid**, even if they exist. Use sufficiently long fixed
outputs and `--requests-per-worker` to maintain the target until capture. The
successful metadata contains only operator declarations and workload properties;
it does not independently validate the backend or route. Socket inactivity
timeouts and the phase deadline are not an absolute resource or process
deadline: an independent watchdog and the project GPU budget and teardown
controls remain mandatory.

`python3 benchmarks/overhead/profile_integration_smoke.py` validates this
orchestration at 32 and 128 clients with both strict and opt-in 90% floors
against one delayed localhost mock, parses both profiles, and checks that short
load is rejected. It is not a measurement.

#422 still requires CPU and heap profiles under **32 and 128** concurrent
requests for both plain and decision-model paths, plus the specified shadow and
enforce comparisons, host details, repeated direct/routed vLLM results, and a
hot-path summary. Never treat this smoke as any of those results.
