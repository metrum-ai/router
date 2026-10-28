<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# Run 3 single-GPU Shadeform fallback

The approved #426 Shadeform safety mechanism is adapted here for #425: one
H100/H200 with at least 80 GB GPU RAM and at least 300 GB storage. It generates
an Ed25519 key locally, posts **only its public half** to `/sshkeys/add`, and
passes the returned exact ID at create. It never calls GET `/sshkeys`, handles
Shadeform-managed keys, or sends a private key or provider credential in an
instance or Docker environment.

The $130 reservation and provider auto-delete spend threshold leave $20 of the
$150 authorization for Mac's $10 collection reserve and a further $10 buffer.
Collection's actual charge remains unattributable; elapsed-price and provider
`cost_estimate` are estimates, not posted bills or a hard cap. Stop early if
known actual combined charges approach the authorized limit.

Create a fresh private state intent with `shade425_lease.py init` and arm
`shade425_watcher.py` in a separate Herdr tab before key registration. Verify
its live listing, the simulated instance and key deletes, its live PID and tab.
On termination it deletes only the intent instance and our exact key ID; after
successful key deletion a second exact POST returning documented 404 verifies
key absence without listing keys. It verifies the instance absent and removes
local key files. Ambiguous registration/create is not treated as absent: stop
and have an operator reconcile the unique name and key fingerprint by an
approved channel. Signal `GPU-DONE #425` as soon as work or setup ends and
independently verify cleanup. No model download or training before SSH and GPU
proof. Follow the merged frozen recipe, data hashes, non-held-out hardware
smoke, LoRA inventory and unchanged outcome gate; report even a failed gate.
