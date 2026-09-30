# Claude Desktop → animated video (decision realism)

Prior drafts that only animate probability bars then flash a model name feel fake. The point of this clip is **why that upstream was chosen** and **how the policy got there** — using measured live numbers only.

## What to do in Desktop

1. Open **Claude Desktop** (Opus 5.5 if available).
2. Attach [`claude-animation-data.json`](claude-animation-data.json).
3. Paste the **Desktop video prompt** below (replace any older prompt).
4. Allow HyperFrames / Design / “create video” if offered.
5. After the draft: “Re-check every on-screen number and rule outcome against `claude-animation-data.json`. Do not invent.”

## Desktop video prompt (paste)

```text
Create an animated product video (MP4, ~40–50s, 16:9) that feels like a live routing audit — not a marketing montage.

SOURCE OF TRUTH: attached claude-animation-data.json only.
Do NOT invent models, prices, probabilities, latencies, accuracy, or prompts.
If a number is not in that file, omit it.

CORE IDEA (must land in the first 8 seconds):
OpenJev does not write answers. It returns typed decisions. A small policy applies explicit rules, then Metrum AI Router maps the surviving task class onto a priced OpenAI ladder. Show the machinery.

MANDATORY STRUCTURE — do not collapse this into “bars → arrow → model”:

A. SYSTEM COLD OPEN (~6s)
   - One horizontal flow, labels from system.pipeline:
     Client → Metrum AI Router → OpenJev policy → OpenJev → policy rules → Router → OpenAI
   - Super: system.openjev_role (short paraphrase OK; keep meaning)
   - Super: system.router_role (short paraphrase OK)

B. LADDER + RULES CARD (~5s)
   - Show system.ladder as three priced slots (model + $/1M in/out)
   - Show system.policy_rules as checklist chips: R1 map, R2 noul≥0.5 escalate, R3 confidence<0.45 escalate
   - Viewer must understand: choice alone is not enough; risk/confidence can bump the tier

C. FOUR LIVE DECISIONS — for EACH scene in DATA.scenes (s01, m01, a01, s03), spend ~7–8s and ALWAYS show this same panel layout:

   LEFT: exact prompt text (verbatim from scene.prompt)
   CENTER: OpenJev typed readout filling in:
     - task.choice + three probability bars (exact floats)
     - complexity.score + legend hint (trivial→expert)
     - high_risk noul as a gauge against 0.5 threshold
     - chip: openjevLatencyMs / e2e_ms
   RIGHT: RULE TRACE from scene.rule_trace — animate each rule as pass/fail/select in order
   BOTTOM BEAT: one line of scene.why_this_model (may shorten for VO/title, but keep the causal claim)
   OUTCOME: final_task → model → price_per_m_in_out, with escalated/highRisk badges when true
   For s03: flash “fixture miss / deliberate escalate” because correct=false

D. END CARD (~5s)
   - run.accuracy, run.accuracy_by_label, run.openjev_p50_ms, run.repeat_flips, run.escalations, run.tier_mix
   - one line hardware from system.hardware
   - system.license
   - optional: cost_projection always_advanced vs openjev_routed_est (absolute $, not “saved %”)

VISUAL RULES:
- Look like an operator / decision console: monospace for model ids and floats, clear pass/fail on rules
- OpenJev is NOT a chatbot: never show fake streamed answer prose
- No purple neon cliché; no floating emoji stickers
- Burn in exact floats (e.g. 0.9953 / 0.0043 / 0.0004 on s01; noul 0.9717 on s03)
- Prefer one composition that updates in place across scenes (same panel) so the “why” stays readable
- Motion should teach causality: bars first → noul vs 0.5 → rule lights → ladder highlight → selected model

VOICEOVER (preferred ON for this cut):
- Narrate the causal chain, not hype. Example cadence for s01:
  “OpenJev says simple at 0.995. Risk 0.007 — under threshold. Map simple to luna.”
- For s03:
  “Choice is still simple — but risk is 0.972. Rule escalates one tier. Sol, not luna.”
- Only state facts present in the JSON.

Deliver: the MP4, plus a 2–3 sentence caption using only measured facts.
```

## If Desktop asks clarifying questions

- Aspect: **16:9** (also request a **9:16** cut if needed)
- Length: **~45 seconds** (not 20s — the rule traces need air)
- Voice: **on** (causal narration); captions burned in either way
- Brand: **Metrum AI Router** + **OpenJev** (disclose ≠ hosted Jev; CC BY-NC)
- Tone: **audit / engineering demo**, not launch hype

## Iteration prompts (if the first render is still shallow)

Paste as follow-ups:

1. `The last cut still doesn’t show WHY. For every scene, hold the rule_trace panel until each rule resolves, then highlight the ladder slot. Do not cut to the model name before the rules finish.`
2. `Show noul as a threshold crossing against 0.5, not a decorative dial. On s03 the needle must cross and R2 must light “escalate”.`
3. `Add a persistent side strip: final_task vs openjev_choice. When they differ, draw the escalate arrow.`
4. `Re-check every float against claude-animation-data.json. Delete any invented copy.`

## Fallback if video render fails

Ask: “Output a self-contained HTML decision-console animation with the same storyboard and DATA, Play/Pause, then I’ll screen-record.”  
Same panel layout (prompt / OpenJev readout / rule_trace / ladder).

## Files

| file | role |
|---|---|
| `claude-animation-data.json` | **attach this** — pipeline, rules, ladder, 4 scenes with `why_this_model` + `rule_trace` |
| `ANNOUNCEMENT_FACTS.md` | optional — caption distillation |
| `routing-decisions.live.json` | optional — full machine table |
