<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Partial overhead load driver (#422)

This is **preparation, not a benchmark result**. No real vLLM, decision model,
GPU, cloud VM or edge hardware measurements have been performed by these
smokes. The driver does not provision or validate infrastructure, and its output
alone is not sufficient for #422 acceptance.

Run the CPU-only checks (the integration smoke builds the router with
`dev_no_license` and uses one scripted localhost backend):

```sh
make overhead-load-smoke
```

For an operator-controlled environment, the driver accepts direct and routed
OpenAI Chat completion URLs and one model name, sent identically on both paths.
Use a synthetic prompt, and provide credentials only via environment variables
named with `--direct-token-env` and `--router-token-env`. URLs must not contain
user information, query strings or fragments. Credential-bearing requests require
HTTPS or a loopback HTTP endpoint; credentials are never sent to non-loopback
cleartext endpoints. Output goes to a **new**, private
directory; credentials, endpoint URLs, prompt text, and response content are
not included in its manifest, summaries or per-request samples. Verify and
redact all generated artifacts and router logs before sharing.

```sh
python3 benchmarks/overhead/load.py \
  --direct-url "$DIRECT_CHAT_URL" --router-url "$ROUTER_CHAT_URL" \
  --direct-token-env BACKEND_TOKEN --router-token-env ROUTER_TOKEN \
  --model "$SERVED_MODEL_AND_GROUP" --prompt-file synthetic-prompt.txt \
  --output-tokens 32 --output-dir private-results
```

This command alone does not establish that the routes use the same vLLM server:
the operator must verify the router config, vLLM identity, served model,
backend image digest, prompt and output settings, hardware and versions, and
record those independently without credentials. It must be run separately for
plain, decision shadow and decision enforce routes against that same backend.
The fixed model-group name must also be a valid served model name on direct
vLLM. Provisioning, approvals, spending limits, watchdogs and teardown remain
external responsibilities; in particular, `--timeout` is a per-socket
inactivity timeout, not an absolute workload or spend deadline. Do not run
GPU workloads without the project's required budget and lifecycle controls.

## Method and limits

- By default each phase has 1, 8, 32 or 128 concurrent workers, three
  repetitions and eight timed requests per worker. Each worker first warms its
  persistent connection with one unmeasured request. Warm the backend itself
  separately; one connection warmup is not evidence of a warm model.
- Within each concurrency and response mode, the runner alternates direct-first
  and router-first order across repetitions. Both routes receive identical
  request bodies. Non-streaming and streaming use different bodies as required
  by their respective APIs. Failures stop the run without writing a completed
  summary; partial samples are not valid benchmark results.
- The driver requires reported `prompt_tokens` to be identical across all
  completed phases and `completion_tokens` to equal `--output-tokens` on every
  request. Reported counts are a consistency check, not independent tokenization
  or proof of backend identity. An early-EOS response fails instead of silently
  mixing output sizes.
- `latency_ms` is full non-streaming client round trip. Streaming
  `ttft_content_ms` is client-observed time to the first nonempty content SSE
  event, not upstream generation time; role-only events do not count.
  `content_event_gaps_ms` are gaps between nonempty content events, **not
  necessarily inter-token latency**: events can contain multiple tokens and
  client/network buffering affects their timing. Token-level latency requires
  additional instrumentation.
- Nearest-rank p50 and p99 are calculated separately per route and repetition;
  each delta is the router quantile minus the direct quantile, not a percentile
  of paired request differences. `delta_variance` reports mean, sample standard
  deviation, minimum and maximum across repetitions. Defaults provide only eight
  samples per repetition at concurrency 1, so p99 there is merely the maximum;
  choose and disclose a larger request count for a meaningful study.
- The finite phase's achieved requests/second is reported, but it is not the
  host's maximum sustained throughput. CPU/RAM per 1,000 req/s, decision-model
  deployment/identity, profiled 32/128 runs, CPU/heap hot-path summaries,
  hardware/config provenance and the published comparison remain to be done.
  Profiled and unprofiled phases must be kept separate because profiling
  perturbs latency and resource usage.
