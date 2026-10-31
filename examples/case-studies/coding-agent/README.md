<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Calculator coding agent

An agent receives a calculator specification, writes a Python program, and may
revise once after receiving independent test feedback. The judge sends 20
arithmetic and invalid-input examples to the program and scores its JSON output
outside the candidate process. Each arm has three paired trials. This is a new,
small synthetic calculator task, not the historical calculator build mentioned
in #423; it is not evidence of broad coding quality or production parity.

The first attempt stopped after three frontier passes when the first routed
response or sandbox interaction could not be scored. Its four request usage
rows, with $0.0397225 of usage-derived cost, are retained in
`evidence/incomplete-run.json`. It is not a valid paired comparison. The runner
was then changed to score an empty model response as a failed candidate and to
allow revision; the underlying error in the initial run was not conclusively
identified. Any subsequent run is a separate experiment, not a continuation of
that incomplete run.

## 2026-10-28 revised run

| Arm | Full task passes | Individual tests passed | Wilson 95% CI (tasks) | Usage cost | Request p50 / p95 |
| --- | ---: | ---: | --- | ---: | ---: |
| All frontier | 3/3 | 60/60 | 43.9%–100% | $0.036304 | 16731 / 18335 ms |
| Routed | 2/3 | 40/60 | 20.8%–93.9% | $0.027637844 | 11364 / 19127 ms |

**Quality dropped in this run:** routed lost one paired task and won none. The
final candidate on that task passed 0/20; only final candidate test scores are
retained. This small sample does not establish either general parity or a
statistically reliable gap. The routed arm sent 2/4 requests to Fireworks Flash
1017 and 2/4 to OpenAI GPT-6.1 Sol; all three baseline requests used Sol. The
revised run's usage-derived cost was 23.9% lower, but that saving did not hold
quality. The OpenAI charges for both arms were later reconciled against provider
usage: $0.036304 billed for the frontier arm and $0.022616 billed for its two
`gpt-6.1-sol` requests, with the two Fireworks requests adding $0.005022 at list
rates (estimate, no invoice). The $0.0397225 incomplete-run cost above is
separate and excluded from that comparison; reconciled to provider usage it is
$0.037204. Request-time prices and tokens are in `evidence/usage.json`, per-trial
scores are in `evidence/results.json`, and the method is in
[`../common/evidence/billing-reconciliation-2026-10-28.md`](../common/evidence/billing-reconciliation-2026-10-28.md).
Request latency excludes sandbox execution.

**Never run model-generated code on your workstation or CI runner.** The judge
must run on a dedicated disposable machine with Docker, no sensitive data and
an independent destruction deadline. It launches each candidate as an
unprivileged, network-disabled, read-only, resource-limited container with no
capabilities and a pinned official Python image. Containers and the disposable
host are not a perfect security boundary; assess the risks before reproducing.
No model or router credentials need to enter the sandbox host or container.

See [`../common/README.md`](../common/README.md) to set up the local router.
On a disposable sandbox, install Docker, pre-pull the official
`python:3.13-slim` image, record its immutable `sha256` repo digest, and copy
`judge.py` to `/root/case423/judge.py`. Verify the SSH host key out of band and
create a private `known_hosts` file. Prepare an isolated SSH key, and arrange
a watchdog that destroys the host even if the runner fails. Example below
assumes those steps and uses a pinned digest, not a mutable image tag:

```sh
python3 examples/case-studies/coding-agent/run.py \
  --url http://127.0.0.1:18123 --token-file /dev/shm/case423/caller \
  --ssh-host "$SANDBOX_IP" --ssh-key "$SANDBOX_KEY" --known-hosts "$KNOWN_HOSTS" \
  --image "python:3.13-slim@sha256:$PYTHON_IMAGE_DIGEST" \
  --output /tmp/coding-results.jsonl
python3 examples/case-studies/common/extract.py --workload coding-agent \
  --results /tmp/coding-results.jsonl --database /dev/shm/case423/usage.sqlite \
  --out-dir /tmp/coding-evidence
```

Output paths must not already exist. The run stops on request or sandbox
infrastructure errors. Reconcile potentially charged requests before restarting;
never discard failures. Destroy and verify deletion of the sandbox and its
associated resources immediately after use. The router state and token remain
private.

Offline tests use mocked Docker/SSH and do not execute generated code:

```sh
python3 -m unittest discover -s examples/case-studies/coding-agent -p 'test_*.py' -v
```

Metrum AI
