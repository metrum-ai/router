# Animate with Claude Opus 5.5 — how + paste-ready prompt

## How Opus 5.5 “animates” (important)

Claude does **not** emit MP4/frames. It writes a **program** (HTML + CSS + JS Canvas/SVG) that draws the motion in a browser. People then screen-record or render that page to video.

Useful references:

- [Opus 5.5 Animation: Every Frame in JavaScript](https://www.iart.ai/blog/ai-javascript-animation) — `draw(ctx, t)` canvas pattern
- [Claude Artifacts best practices](https://tools.inyourleague.net/en/claude-artifacts-best-practices-interactive-content-creation-en/) — **paste real data into the prompt**; artifacts have **no network**, so JSON must be inlined
- [Claude Code artifacts](https://code.claude.com/docs/en/artifacts) — live HTML pages from a session
- Prefer animating `transform` + `opacity` only ([CSS animation guidance](https://claudecode-lab.com/en/blog/claude-code-css-animation-advanced/))

### Where to run it

| Surface | What to ask for | Export |
|---|---|---|
| **claude.ai** chat + Artifact | “Create a single-file HTML artifact …” | Play in artifact; screen-record, or Download HTML |
| **Claude Code** | Same, write `openjev-routing-anim.html` | Open in browser / Playwright screenshots → ffmpeg |
| **iArt Animation mode** (optional) | Same storyboard; Opus writes JS film | Product renders MP4 |

### How to feed **real** data (do this)

1. **Inline JSON** in the prompt (or attach [`claude-animation-data.json`](claude-animation-data.json)). Do not say “fetch the repo.”
2. Say explicitly: **use only these numbers; do not invent probabilities, latencies, models, or costs.**
3. Ask for a **self-contained** HTML file (no CDN APIs that need keys; Google Fonts OK if needed).
4. Storyboard with **timestamps** and which `scenes[]` id plays when.
5. Second message: “freeze at t=… and verify on-screen numbers match the JSON.”

Machine payload for this run: [`claude-animation-data.json`](claude-animation-data.json)  
Full receipts: [`ANNOUNCEMENT_FACTS.md`](ANNOUNCEMENT_FACTS.md)

---

## Paste into Claude Opus 5.5 (claude.ai or Claude Code)

## Paste into Claude Opus 5.5 (claude.ai or Claude Code)

Copy the fenced block below into a new **Opus 5.5** chat (attach `claude-animation-data.json` only if you prefer; it is already inlined).

````text
You are Claude Opus. Build a SELF-CONTAINED animated HTML demo (one file: HTML+CSS+JS) that visualizes a REAL OpenJev → Metrum AI Router routing decision.

CRITICAL DATA RULES
- Use ONLY the DATA JSON below. Do not invent models, prices, probabilities, latencies, accuracy, or prompts.
- Every on-screen number must appear verbatim in DATA.
- OpenJev does NOT generate prose answers. It returns typed decisions (choice / score / noul). Show probability bars, not a chatbot bubble with free text.
- OpenJev ≠ TypeSafe hosted Jev. Disclose CC BY-NC 4.0 for OpenJev weights on the end card.
- Output: a single downloadable HTML file artifact. Prefer transform/opacity animations. Support prefers-reduced-motion (jump to final state).
- No external network fetches. Inline all data.

PRODUCT STORY (measured 2026-09-25)
A request hits Metrum AI Router (strategy: external). The policy POSTs the prompt to OpenJev System One. OpenJev returns task∈{simple,medium,advanced} + complexity score + high_risk noul. Policy maps task → OpenAI model (luna/sol/astra), escalating one tier if highRisk or low confidence. Then the router would call that model.

Animate THREE successful routes + ONE honest escalation miss, then an end card with suite stats.

STORYBOARD (~24s loop with pause/replay)
0–3s: Title — “Metrum AI Router × OpenJev” / subtitle “A decision model that writes nothing chooses which GPT answers”
3–9s: Scene s01 — type prompt; packet to OpenJev; fill probability bars from answers.task.probabilities; show confidence + openjev_ms; arrow to gpt-5.6-luna + price
9–14s: Scene m01 — same pattern → gpt-5.6-sol
14–19s: Scene a01 — same pattern → gpt-6-astra
19–22s: Scene s03 — show choice=simple BUT high_risk_noul high → policy escalates to medium/sol; label “escalation (fixture miss)”
22–24s: End card — accuracy, openjev_p50_ms, repeat_flips, tier_mix, hardware one-liner, license

UI NOTES
- One composition, not a dashboard. Dark technical look OK; avoid purple glow clichés.
- Show chips: oj_ms, e2e_ms, confidenceBand, complexity score.
- Probability bars must use the exact floats from DATA.scenes[].answers.task.probabilities.
- Play / Pause / Restart controls.

DATA (authoritative):
{
  "run": {
    "date": "2026-09-25",
    "hardware": "Shadeform massedcompute RTXPro6000 / NVIDIA RTX PRO 6000 Blackwell Server Edition 97887 MiB / driver 580.126.09 / vLLM 0.29.0 fp8",
    "accuracy": "56/60 = 93.3%",
    "openjev_p50_ms": 193,
    "repeat_flips": 0,
    "tier_mix": {
      "gpt-5.6-luna": 19,
      "gpt-5.6-sol": 18,
      "gpt-6-astra": 23
    },
    "prices_per_m": {
      "gpt-5.6-luna": [
        0.2,
        1.2
      ],
      "gpt-5.6-sol": [
        4.0,
        20.0
      ],
      "gpt-6-astra": [
        10.0,
        50.0
      ]
    },
    "cost_projection_usd": {
      "always_cheap": 0.00114,
      "always_medium": 0.11928,
      "always_advanced": 0.30096,
      "openjev_routed_est": 0.151513
    },
    "license": "OpenJev weights CC BY-NC 4.0; OpenJev != TypeSafe hosted Jev"
  },
  "scenes": [
    {
      "id": "s01",
      "prompt": "Summarize this product update in one sentence: we shipped dark mode and fixed login timeouts.",
      "fixture_label": "simple",
      "openjev_ms": 177,
      "policy": {
        "openjev_choice": "simple",
        "final_task": "simple",
        "model": "gpt-5.6-luna",
        "escalated": false,
        "highRisk": false,
        "confidenceBand": "high",
        "complexity": 0.6854,
        "openjevLatencyMs": 186,
        "e2e_ms": 188,
        "correct": true
      },
      "answers": {
        "task": {
          "type": "choice",
          "choice": "simple",
          "probabilities": {
            "simple": 0.9953,
            "medium": 0.0043,
            "advanced": 0.0004
          },
          "confidence": 0.993
        },
        "complexity": {
          "score": 0.6159,
          "confidence": 0.6709,
          "probabilities": {
            "0": 0.3895,
            "1": 0.6055,
            "2": 0.0047,
            "3": 0.0001,
            "4": 0.0001
          }
        },
        "high_risk_noul": 0.0071,
        "usage": {
          "input_tokens": 330,
          "output_tokens": 0
        }
      }
    },
    {
      "id": "m01",
      "prompt": "Implement a Python function that merges two sorted lists and include a unit test.",
      "fixture_label": "medium",
      "openjev_ms": 191,
      "policy": {
        "openjev_choice": "medium",
        "final_task": "medium",
        "model": "gpt-5.6-sol",
        "escalated": false,
        "highRisk": false,
        "confidenceBand": "high",
        "complexity": 1.7661,
        "openjevLatencyMs": 194,
        "e2e_ms": 195,
        "correct": true
      },
      "answers": {
        "task": {
          "type": "choice",
          "choice": "medium",
          "probabilities": {
            "simple": 0.0005,
            "medium": 0.9989,
            "advanced": 0.0006
          },
          "confidence": 0.9984
        },
        "complexity": {
          "score": 1.7387,
          "confidence": 0.7723,
          "probabilities": {
            "0": 0.0032,
            "1": 0.2609,
            "2": 0.7305,
            "3": 0.0049,
            "4": 0.0005
          }
        },
        "high_risk_noul": 0.0065,
        "usage": {
          "input_tokens": 318,
          "output_tokens": 0
        }
      }
    },
    {
      "id": "a01",
      "prompt": "Prove the multi-constraint trade-offs between consistency and latency for a global ledger design.",
      "fixture_label": "advanced",
      "openjev_ms": 188,
      "policy": {
        "openjev_choice": "advanced",
        "final_task": "advanced",
        "model": "gpt-6-astra",
        "escalated": false,
        "highRisk": false,
        "confidenceBand": "high",
        "complexity": 3.6856,
        "openjevLatencyMs": 195,
        "e2e_ms": 196,
        "correct": true
      },
      "answers": {
        "task": {
          "type": "choice",
          "choice": "advanced",
          "probabilities": {
            "simple": 0.0012,
            "medium": 0.0028,
            "advanced": 0.996
          },
          "confidence": 0.994
        },
        "complexity": {
          "score": 3.6135,
          "confidence": 0.6779,
          "probabilities": {
            "0": 0.0119,
            "1": 0.0057,
            "2": 0.02,
            "3": 0.2817,
            "4": 0.6807
          }
        },
        "high_risk_noul": 0.0163,
        "usage": {
          "input_tokens": 327,
          "output_tokens": 0
        }
      }
    },
    {
      "id": "s03",
      "prompt": "Classify this ticket as billing, shipping, or technical: I was charged twice for order 4412.",
      "fixture_label": "simple",
      "openjev_ms": 188,
      "policy": {
        "openjev_choice": "simple",
        "final_task": "medium",
        "model": "gpt-5.6-sol",
        "escalated": true,
        "highRisk": true,
        "confidenceBand": "high",
        "complexity": 0.8724,
        "openjevLatencyMs": 254,
        "e2e_ms": 255,
        "correct": false
      },
      "answers": {
        "task": {
          "type": "choice",
          "choice": "simple",
          "probabilities": {
            "simple": 0.998,
            "medium": 0.0015,
            "advanced": 0.0004
          },
          "confidence": 0.997
        },
        "complexity": {
          "score": 1.0115,
          "confidence": 0.9005,
          "probabilities": {
            "0": 0.054,
            "1": 0.8821,
            "2": 0.0625,
            "3": 0.0013,
            "4": 0.0001
          }
        },
        "high_risk_noul": 0.9717,
        "usage": {
          "input_tokens": 345,
          "output_tokens": 0
        }
      }
    }
  ]
}

After generating the HTML, list every numeric claim you rendered and cite the DATA path (e.g. scenes[0].answers.task.confidence). If anything cannot be sourced from DATA, remove it.
````

## Shorter follow-up prompts (iterate)

**Tighten timing**

```text
Keep the same DATA. Slow scene s01 so probability bars take ~1.2s to fill. Keep total under 30s. Do not change numbers.
```

**Export for social**

```text
Add a 1080×1920 (9:16) layout variant toggled by a button, same DATA, for phone-screen recording.
```

**Verify fidelity**

```text
Freeze at the end of scene a01. Screenshot-level checklist: prompt text, choice, three probabilities, model id, price pair, latency ms — each must match DATA.scenes where id=a01.
```

## What not to do

- Don’t ask Opus to “look up OpenJev online” for numbers — use this measured run.
- Don’t ask for a native video file from Claude alone — ask for HTML/JS, then record/render.
- Don’t paste all 60 prompts into the first message; the 4 filmed scenes + suite stats are enough for motion. Keep `ANNOUNCEMENT_FACTS.md` for caption writing.
