<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Document Q&A

Five self-authored synthetic handbook and policy snippets form the corpus. A
fixed lexical overlap retriever selects one document, and the model must answer
the question from that document. Exact-match scoring requires both the correct
retrieved document and the reference answer in `tasks.json`. Each model arm
sees the same questions and retrieval results. This tiny, simple set measures
neither broad RAG quality nor production parity.

## 2026-10-28 run

| Arm | Exact-match tasks | Wilson 95% CI | Request-time usage cost | Request p50 / p95 |
| --- | ---: | --- | ---: | ---: |
| All frontier | 5/5 | 56.6%–100% | $0.001050 | 1261 / 1503 ms |
| Routed | 5/5 | 56.6%–100% | $0.000611 | 1053 / 1864 ms |

The routed arm used Fireworks Flash 1017 for 3/5 requests and OpenAI
GPT-6.1 Sol for 2/5; baseline used Sol for all five. This run's usage-derived
cost was 41.8% lower; there were no paired quality wins or losses. The OpenAI
charges for both arms were later reconciled against provider usage: **$0.001050
billed** for the frontier arm and **$0.000434 billed** for the routed arm's two
`gpt-6.1-sol` requests. The routed arm's three Fireworks requests add $0.000177
at list rates, an estimate with no invoice behind it, which is why the routed
total above is $0.000611 and not a fully billed figure. Source URLs and dated
prices are in `evidence/usage.json`; raw per-task outcomes are in `evidence/`,
and the reconciliation method is in
[`../common/evidence/billing-reconciliation-2026-10-28.md`](../common/evidence/billing-reconciliation-2026-10-28.md).
The confidence intervals are wide and do not establish quality parity.

See [`../common/README.md`](../common/README.md) for router setup, credentials,
usage extraction and cost interpretation. An engineer outside Metrum can use the
[external reproduction checklist](../common/REPRODUCTION.md) to record notes.
After preparing the private state:

```sh
python3 examples/case-studies/document-qa/run.py \
  --url http://127.0.0.1:18123 --token-file /dev/shm/case423/caller \
  --output /tmp/document-results.jsonl
python3 examples/case-studies/common/extract.py --workload document-qa \
  --results /tmp/document-results.jsonl --database /dev/shm/case423/usage.sqlite \
  --out-dir /tmp/document-evidence
```

Output paths must not already exist. On any request error the run stops; check
provider charges before restarting. Do not publish private router state or raw
provider logs.

```sh
python3 -m unittest discover -s examples/case-studies/document-qa -p 'test_*.py' -v
```

Metrum AI
