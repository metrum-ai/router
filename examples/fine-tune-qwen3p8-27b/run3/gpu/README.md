<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# Historical run 3 single-GPU teardown guard

The Axolotl creation path is retired in v4.5 ([#509](https://github.com/metrum-ai/router/issues/509));
`lease.py create` fails closed without contacting a provider. Replacement
training and further run 3 work are deferred to v4.6.0. No new run is
implicitly authorized by these historical safety helpers.

The independent watcher remains available to reconcile an existing run-3
intent and delete only its unique `metrum-425-` label and returned instance ID.
An ambiguous creation must not be called absent before reconciliation; verify
exact-resource and local-key deletion. A reservation, hourly quote or elapsed
estimate is not an actual charge or a hard provider billing cap. Historical
training and results, including the original failed gate, are recorded in
[TRAINING.md](../TRAINING.md) and [run 3 evidence](../../../../docs/evidence/finetune/2026-10-28-run3/README.md).
