---
title: Edge Bundle
doc_type: howto
---

# Edge Bundle

`deploy/edge/` installs the router, a local model stack, the default model
groups, an escalation path and the admin UI on one edge box, then demonstrates
local-first routing, escalation, a budget hit and a spend report.

The bundle is for a single accelerator host: an AMD Strix Halo class system
(Ryzen AI MAX+ 395, 128 GB unified memory) or an NVIDIA GB10 class system. It
uses official upstream model-server images pinned by digest and the same image
pins as `deploy/reference/`, so a bundle installation and a reference
configuration describe the same software.

## What one command does

```sh
sudo ./install.sh --router-image metrum-ai-router:<version>-linux-amd64
./demo.sh
```

`install.sh` creates a data directory (default `$HOME/metrum-edge`) and, in order:

1. generates the secret environment (local model API key, identifier transform
   key, admin password hash) as mode `0600` files, never reusing a weak default;
2. renders `config/config.yaml` with the `local-tiny`, `local-chat` and
   `escalate` groups, caller quotas and the admin report surface;
3. copies the composed deployment for the detected accelerator;
4. generates two caller tokens and stores only their verifiers in the config;
5. runs the reviewed migration sequence (`plan`, `apply`, `resume`,
   `verify-serving`, `status`) before the router starts;
6. starts the stack, waits for `/readyz`, and checks that a caller token
   authenticates and that `local-chat` is advertised.

Re-running is safe: secrets and tokens are kept unless `--force` is given.

## The demo

| Step | What it proves |
|---|---|
| local-first | `local-chat` and `local-tiny` answer from the box |
| escalation | `escalate` leaves the box with the operator's key, and fails closed when there is no key instead of answering locally |
| budget hit | the small-budget caller is refused with `429 quota-exhausted` when its day budget is spent |
| spend report | `routerctl usage summary` reports tokens and cost from the router's own usage rows, with escalation separable |

A `SKIP` is printed with its reason and is not a pass.

## Sizing and limits

`deploy/edge/README.md` carries the hardware bill of materials, the KV-cache
arithmetic for both slots, the air-gapped install path, the troubleshooting table
and the observer checklist for a timed outside-engineer session. Validated
concurrency for the box is **not yet measured**: the README says which sweep
produces it and what it will report.

## Related

- [Self-hosted upstreams](../configuration/self-hosted-upstreams) for the
  provider shape behind the local groups.
- [Admin browser reports](../operations/admin-browser-reports) for the report
  surface the demo reads.
- [Usage reporting](../operations/usage-reporting) for the usage rows and cost
  columns.
