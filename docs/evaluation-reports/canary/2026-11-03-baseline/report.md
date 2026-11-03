# Direct-provider differential canary

- run: `canary-baseline-20261103` at 2026-11-03T18:31:00Z
- classification: both pass 8, router regression 0, provider-only fail 0, both fail 0
- cost: $0.000164 (provider usage x listed per-million prices (not invoice-reconciled))

| Cell | Probes | Passes | Error rate |
| --- | ---: | ---: | ---: |
| `canary-direct` | 4 | 4 | 0.00% |
| `canary-router-pinned` | 4 | 4 | 0.00% |
| `canary-direct-current-stable` | 4 | 4 | 0.00% |
| `canary-router-pinned-current-stable` | 4 | 4 | 0.00% |
