# Honest comparison: an in-process open router and a proxy

This is a **small synthetic wiring comparison**, not a competitive benchmark.
It runs Metrum Router with the repository's length-based external policy against
[`llm-router-lite` 0.1.0](https://pypi.org/project/llm-router-lite/), an
MIT-licensed Python routing library. The latter runs **in process**, not as a
proxy; it is not vLLM Semantic Router. The library distribution is pinned by
SHA-256 in `requirements.txt`. Verify and review third-party packages before use.

Both arms receive the same six prompts and call the same fake backend with the
same two model IDs. The backend is deliberately programmed to return `FAIL`
when a pre-labelled hard case is sent to the cheap tier and `PASS` otherwise.
Those labels and the library's cost/quality inputs are **assumptions** in
`config.example.json`, not measurements of real models. Metrum's example
policy chooses local for ≤200 characters and frontier otherwise; the library
uses its own prompt-complexity and registered cost/quality heuristic. The
exercise measures agreement with those toy labels, not real answer quality.

## Run

From the repository root (Go, Python 3 and PyYAML required):

```sh
python3 -m pip install --no-deps --require-hashes -r examples/honest-comparison/requirements.txt
go build -o /tmp/router-comparison ./cmd/metrum-ai-router
python3 examples/honest-comparison/run.py --router /tmp/router-comparison
```

Expect 12 measured rows in six alternating-order pairs after one warm-up per
arm. Results contain per-pair tier and pass/fail plus median end-to-end client
latency. The local run and raw per-case scalars are in
[`evidence/mock-output.json`](evidence/mock-output.json): Metrum 4/6 and the
library 5/6 on these toy labels. The library was faster in that run; it avoided
Metrum's extra HTTP proxy and external-policy hops. This result is evidence of
that **specific configuration and topology**, not a general router-quality or
latency ranking. The sample is too small for a meaningful confidence interval;
it is deliberately not a representative workload.

## Feature coverage and limits

| Capability | Metrum example | Library example |
|---|---|---|
| Deployment | HTTP router + external policy | Python library in application process |
| Router-level scoped caller keys | Configured in this example | No HTTP server/key boundary; application must supply it |
| Tier decision | Length threshold | Configured cost, quality and complexity heuristic |
| PII filtering / audit | Available in Router, **not enabled or evaluated here** | No server-side filter/audit layer in this library run |
| Client latency | Includes proxy and local policy round trips | In-process route plus one backend HTTP call |

## Hosted and self-hosted smoke\n\nFor a paid Fireworks run, set `FIREWORKS_API_KEY` privately in the environment,\nthen run `python3 examples/honest-comparison/hosted.py --router\n/tmp/router-comparison --state-dir /absolute/private/new-dir`. Both aliases\npoint to the **same** Fireworks model. Six simple exact-answer tasks go through\neach arm; the direct-library requests and router requests use the same model,\nprompts and max-token setting. This tests the integration, not whether tier\nselection improves quality. It makes 12 paid requests, with no warmups.\n\nFor a loopback OpenAI-compatible model add `--backend local --local-url\nhttp://127.0.0.1:PORT/v1 --local-model MODEL`. CI exercises this path with a\nfake server, not a real model. Local compute cost is unknown and reported as\n`null`. The private state directory includes router credentials, request logs,\nusage and partial-request receipts. Protect it, reconcile all attempts including\nfailures, and remove it afterwards. Never publish private state.\n\n[`evidence/fireworks-output.json`](evidence/fireworks-output.json) is one\nhosted smoke: 6/6 exact answers for both arms using the same model. Configured\nlist-price usage costs were $0.0002676 and $0.0003060, not reconciled invoices.\nProvider variability and an in-process-versus-proxy topology confound the\nobserved latency and token totals. The run does not establish cost savings,\nquality parity or a general performance ranking.\n\nNo vLLM Semantic Router, real self-hosted model, or independent external\nreproduction was run. This small example is not a comprehensive feature or\nperformance evaluation. A stronger study needs representative reference-graded\nprompts, different real model tiers, comparable network topologies, failures\nand repeated paired trials. CI runs the actual pinned library against a fake\nupstream; it does not use paid credentials.
