# OpenJev routing evidence

## Live (real OpenJev on Shadeform)

See [`README.live.md`](README.live.md) and [`benchmark.live.json`](benchmark.live.json).

- Hardware: Shadeform `RTXPro6000` (NVIDIA RTX PRO 6000 Blackwell Server Edition, 96GB)
- Label accuracy: **93.3%** (56/60)
- OpenJev latency p50: **193 ms**
- Repeat flips: **0**

**For announcement writing (all prompts + decisions):**
[`ANNOUNCEMENT_FACTS.md`](ANNOUNCEMENT_FACTS.md) · [`routing-decisions.live.json`](routing-decisions.live.json) · distill guide [`SOCIAL_POST.md`](SOCIAL_POST.md)

**Animate with Claude Desktop / Opus 5.5:** attach [`claude-animation-data.json`](claude-animation-data.json) and paste the prompt in [`CLAUDE_OPUS_ANIMATION.md`](CLAUDE_OPUS_ANIMATION.md) (“Create an animated product video…”).

Animation handoff for a separate agent (observed reality only):
[`ANIMATION_AGENT_PROMPT.md`](ANIMATION_AGENT_PROMPT.md)

## Synthetic (fake OpenJev, CI wiring)

Earlier wiring evidence with a deterministic shim remains in [`benchmark.json`](benchmark.json).
Do not use that file for social claims about OpenJev accuracy.

## License

OpenJev weights are CC BY-NC 4.0. This repository example code is Apache-2.0.

