<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# Run 3 single-GPU teardown guard

The watcher is adapted from the reviewed #426 guard. It is restricted to a
fresh `metrum-425-` intent and deletes only its unique intent label and returned
instance ID. Before any create, start the watcher in a **separate Herdr tab**;
it lists owner instances and verifies a dry-run delete. An ambiguous create
must not be called absent before reconciliation. Never place credentials in
instance payloads, container environment flags or durable state.

Use at most one H100/H200 with at least 80 GB and the registered Axolotl image.
The private state directory must be new, mode 0700, and outside the worktree.
The guard caps the **reservation and observed-billing stop threshold** at $130,
leaving $20 below the $150 authorization for the approved $10 reserve and
unattributable/lagging collection charges. A reservation, hourly quote or
elapsed-time estimate is not an actual charge or a hard provider billing cap.
Monitor posted charges and stop early if the actual combined spend approaches
the authorized limit. Missing charge rows remain unknown, not zero.

`lease.py init --state-dir /absolute/private/state --minutes 180
--reservation-usd 130` creates an intent, not a cloud resource. Start
`watcher.py --state-dir /absolute/private/state` in a distinct Herdr tab; only
once it prints `WATCHER READY` may `lease.py create --state-dir ... --offer ID
--max-hourly PRICE` create an instance and attach its locally generated public
key. Confirm an SSH handshake and exact GPU before any model download or
training. This script does not authorize training or perform the recipe smoke;
the frozen inputs, matching non-held-out smoke, model/module inventory, gate,
and any required review remain mandatory. Send `GPU-DONE #425` in `done.txt`
as soon as training or evaluation is finished; separately verify deletion and
local-key absence, and reconcile actual charges. Failed/incomplete evaluations
are not passes.
