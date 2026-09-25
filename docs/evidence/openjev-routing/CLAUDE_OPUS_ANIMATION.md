# Claude Desktop → animated video (with our real OpenJev data)

Yes. In **Claude Desktop** you can ask for an animated video; under the hood Claude still writes motion (HTML/HyperFrames/Design timeline) and a renderer turns it into MP4 ([HyperFrames connector](https://claude.com/connectors/hyperframes-heygen), [how Claude “makes videos”](https://vuela.ai/blog/can-claude-make-videos)). Your job is to feed **measured data**, not vibes.

## What to do in Desktop (30 seconds)

1. Open **Claude Desktop** (Opus 5.5 if available).
2. Attach both files from this folder:
   - [`claude-animation-data.json`](claude-animation-data.json) — filmed scenes + suite stats
   - optional: [`ANNOUNCEMENT_FACTS.md`](ANNOUNCEMENT_FACTS.md) if you want more caption detail
3. Paste the **Desktop video prompt** below.
4. If Desktop offers HyperFrames / Design / “create video”, allow it.
5. After the draft: “Re-check every on-screen number against `claude-animation-data.json`. Do not invent.”

## Desktop video prompt (paste)

```text
Create an animated product video (MP4, ~24–30s, 16:9) explaining how Metrum AI Router uses OpenJev for model routing.

Use ONLY the attached claude-animation-data.json as the source of truth.
Do NOT invent models, prices, probabilities, latencies, accuracy, or prompts.
If a number is not in that file, omit it.

Story (must match DATA.scenes in order):
1. Title: Metrum AI Router × OpenJev — “a decision model that writes nothing chooses which GPT answers”
2. Scene s01: show the exact prompt; animate OpenJev probability bars from answers.task.probabilities; show confidence + openjev_ms; route arrow to gpt-5.6-luna with price from run.prices_per_m
3. Scene m01: same pattern → gpt-5.6-sol
4. Scene a01: same pattern → gpt-6-astra
5. Scene s03: OpenJev choice=simple but high_risk_noul is high → policy escalates to gpt-5.6-sol; label this as an honest escalation / fixture miss
6. End card: run.accuracy, run.openjev_p50_ms, run.repeat_flips, run.tier_mix, run.hardware (one line), run.license

Visual rules:
- OpenJev is NOT a chatbot: no fake streaming answer text. Show typed decision UI (choice + probability bars + noul).
- Prefer clean motion graphics; kinetic type OK; no purple neon cliché.
- Burn in exact floats from the JSON (e.g. 0.9977 / 0.0021 / 0.0002 on s01).
- Voiceover optional; if used, only say facts from the JSON / end card.

Deliver: the video file, plus a one-paragraph caption I can post, still using only measured facts.
```

## If Desktop asks clarifying questions

Answer with these fixed choices:

- Aspect: **16:9** (Twitter/X / LinkedIn); also ask for a **9:16** cut if you need Stories/Reels
- Length: **~25 seconds**
- Voice: **off** first (add VO later if needed)
- Brand: **Metrum AI Router** + **OpenJev** (disclose OpenJev ≠ hosted Jev; CC BY-NC)

## Why attach JSON instead of “read the repo”

Desktop video tools often render in a sandbox. Attached JSON is the reliable way to get **exact** prompts/probs/ms on screen ([artifact data must be inlined/attached](https://tools.inyourleague.net/en/claude-artifacts-best-practices-interactive-content-creation-en/)).

## Fallback if video render fails

Ask: “Output a self-contained HTML animation with the same storyboard and DATA, Play/Pause, then I’ll screen-record.”  
That path is also documented in the older paste block at the bottom of this file’s git history / `ANIMATION_AGENT_PROMPT.md`.

## Files

| file | role |
|---|---|
| `claude-animation-data.json` | **attach this** — 4 filmed scenes + suite stats |
| `ANNOUNCEMENT_FACTS.md` | optional — all 60 prompts for caption distillation |
| `routing-decisions.live.json` | optional — full machine table |
