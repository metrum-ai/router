<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Support agent

This is a synthetic support workload, not a customer dataset. Three isolated
order fixtures exercise four sequential tools: look up an order, calculate its
policy credit, apply it, and open a ticket. A separate deterministic checker
requires the observed four-step chain, no tool errors, and the correct final
JSON. Each arm receives the same tasks. Baseline sends all requests to
`frontier`; `routed` uses the weighted cheap/frontier group. A task passes
only if the checker passes, not merely if HTTP succeeds. The tiny hand-built
fixture set is a smoke test, not proof of parity in production.

## 2026-10-28 run

| Arm | Successful tasks | Wilson 95% CI | Request-time usage cost | Request p50 / p95 |
| --- | ---: | --- | ---: | ---: |
| All frontier | 3/3 | 43.9%–100% | $0.013722 | 1488 / 1740 ms |
| Routed | 3/3 | 43.9%–100% | $0.007860756 | 1201 / 2290 ms |

The routed arm used Fireworks Flash 1017 for 8/15 requests and OpenAI
GPT-6.1 Sol for 7/15; baseline used Sol for all 15. This run's usage-derived
cost was 42.7% lower; there were no paired quality wins or losses. The OpenAI
charges for both arms were later reconciled against provider usage: **$0.013722
billed** for the frontier arm and **$0.005652 billed** for the routed arm's seven
`gpt-6.1-sol` requests. The routed arm's eight Fireworks requests add $0.002209
at list rates, an estimate with no invoice behind it, which is why the routed
total above is $0.007860756 and not a fully billed figure. Source URLs and dated
prices are in `evidence/usage.json`; individual outcomes are in `evidence/`, and
the reconciliation method is in
[`../common/evidence/billing-reconciliation-2026-10-28.md`](../common/evidence/billing-reconciliation-2026-10-28.md).
The confidence intervals are wide and do not establish quality parity.

See [`../common/README.md`](../common/README.md) for router setup, credentials,
rate provenance, usage extraction and cost interpretation. Run on a local
router after preparing the private state directory:

```sh
python3 examples/case-studies/support-agent/run.py \
  --url http://127.0.0.1:18123 --token-file /dev/shm/case423/caller \
  --output /tmp/support-results.jsonl
python3 examples/case-studies/common/extract.py --workload support-agent \
  --results /tmp/support-results.jsonl --database /dev/shm/case423/usage.sqlite \
  --out-dir /tmp/support-evidence
```

Results and evidence output paths must not already exist. A network or HTTP
error stops the run: reconcile the provider's potentially charged request
before restarting. Do not publish private router state, logs or caller tokens.

Credential-free tests, including a mock router and negative verifier controls:

```sh
python3 -m unittest discover -s examples/case-studies/support-agent -p 'test_*.py' -v
```

Metrum AI
