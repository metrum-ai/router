<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Billing reconciliation for the 2026-10-28 case-study runs

This note records how the provider-side charges behind the three #423 case
studies were reconciled, and what remains unattributed. It supplements the
per-study `usage.json` files, which hold request-time router rows; it does not
replace them.

## Method

- The provider's usage export for the study minutes was aggregated per model
  and compared with the router's own usage rows for the same arms: request
  count and input/output token totals matched **exactly** for every arm below.
  That match is what lets the provider charges be assigned to a specific arm
  even though both arms used the same provider credential.
- The billed line items reconciled exactly at the request-time rates already
  recorded in `usage.json` for `gpt-6.1-sol`: $2.00/M input, $2.50/M cache
  writes, $10.00/M output. These prompts were uncached and made no cache
  writes, so the OpenAI amounts below equal the router-recorded cost of the
  `gpt-6.1-sol` rows to the cent.
- Amounts are USD, actual charges for the study minutes on 2026-10-28, not
  list-rate arithmetic. The export was pulled on 2026-10-31.
- The routed arms also contain Fireworks `deepseek-v4p1-flash-1017` rows. No
  per-request billing export is available for them, so their amounts stay
  **usage-derived list-rate estimates** and are labelled as such everywhere.
  They are included in the routed totals only to keep an apples-to-apples
  request mix, and every saving figure states which part is billed.

## Per-study figures

Each study's window is the first to last router request of that study, so no
other traffic falls inside it.

The OpenAI columns cover only the `gpt-6.1-sol` requests; every other request
in a routed arm went to Fireworks.

| Study | Window (UTC) | Arm | OpenAI requests | OpenAI input / output tokens | OpenAI billed | Fireworks estimate | Routed total |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Document Q&A | 00:05:42–00:05:54 | frontier | 5 of 5 | 400 / 25 | $0.001050 | — | — |
| Document Q&A | 00:05:42–00:05:54 | routed | 2 of 5 | 167 / 10 | $0.000434 | $0.000177 | $0.000611 |
| Support agent | 00:05:37–00:06:16 | frontier | 15 of 15 | 5046 / 363 | $0.013722 | — | — |
| Support agent | 00:05:37–00:06:16 | routed | 7 of 15 | 2011 / 163 | $0.005652 | $0.002208756 | $0.007860756 |
| Coding agent | 00:19:44–00:21:41 | frontier | 3 of 3 | 522 / 3526 | $0.036304 | — | — |
| Coding agent | 00:19:44–00:21:41 | routed | 2 of 4 | 348 / 2192 | $0.022616 | $0.005021844 | $0.027637844 |

The document Q&A and support agent runs share the 00:05–00:06 minute buckets:
29 requests, 7624 input and 561 output tokens, which is exactly the provider
total for those minutes. The coding-agent minutes 00:19–00:21 contain 5
requests, 870 input and 5718 output tokens, also an exact match.

## Additional spend, excluded from the paired comparison

The first coding-agent attempt stopped early and produced
`coding-agent/evidence/incomplete-run.json`. It was billed: 3 requests,
522 input / 3616 output tokens, **$0.037204**. It is additional spend for the
experiment, not part of the paired result, and is excluded from the savings
percentages below.

OpenAI-attributed spend for the three completed comparisons is $0.051076
(frontier arms) plus $0.028702 (routed `gpt-6.1-sol` rows), i.e. **$0.079778**;
with the incomplete attempt the experiment totals **$0.116982** on that
provider. Fireworks charges for the routed rows are unknown, and the list-rate
estimate for them is $0.0074076 across the three studies.

## Resulting savings, stated precisely

| Study | Frontier billed | Routed billed (OpenAI) | Routed incl. Fireworks estimate | Reduction |
| --- | ---: | ---: | ---: | ---: |
| Document Q&A | $0.001050 | $0.000434 | $0.000611 | 41.8% (58.7% on the OpenAI-attributable portion) |
| Support agent | $0.013722 | $0.005652 | $0.007860756 | 42.7% (58.8% on the OpenAI-attributable portion) |
| Coding agent | $0.036304 | $0.022616 | $0.027637844 | 23.9% (37.7% on the OpenAI-attributable portion) |

The "OpenAI-attributable portion" figures compare only the requests both arms
sent to the same provider, which is the part with actual billed evidence. They
are not the headline saving, because the routed arm's remaining requests still
cost money at Fireworks.

## Limits

- Only one provider's charges are reconciled per arm. The Fireworks share of
  every routed arm is an estimate at request-time list rates, and no invoice
  confirms or contradicts it.
- The exact token match ties provider usage to the arms for these minutes, not
  to individual requests inside an arm. Per-request attribution is inferred
  from the router's usage rows.
- Amounts are small enough that per-minute usage granularity and rounding reach
  the same order of magnitude as the totals for the document Q&A study.
- Nothing here says anything about quality, latency or production volume. See
  the study READMEs for scored quality, which includes one quality loss.

Metrum AI
