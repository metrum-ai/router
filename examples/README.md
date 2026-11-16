# Examples

Start here for operator-owned demos of Metrum AI Router extensibility:

| Example | What it shows |
|---|---|
| [external-routing-policy](external-routing-policy/) | HTTP policy services (`shadow` / `enforce` / `baseline`), including OpenJev task-class routing |
| [typescript-pii-policy](typescript-pii-policy/) | In-process TypeScript `route(ctx)` policy |
| [typescript-request-shape](typescript-request-shape/) | Request-shape aware TypeScript selection |
| [cli-e2e-c](cli-e2e-c/) | Coding-agent client smoke harness |
| [harbor-algotune-pca](harbor-algotune-pca/) | Outcome-gated coding evaluation template |
| [fixed-model-ocr-baseline](fixed-model-ocr-baseline/) | Fixed-model OCR baseline template |
| [fine-tune-qwen3p8-27b](fine-tune-qwen3p8-27b/) | Work-in-progress licensed LoRA training, private serving and router-backed outcome evaluation |

Optional commerce examples under this tree are optional
operator tooling. They are not required to run the Community router.

## Router startup budget

Demos that start a local router and poll `/readyz` share one readiness budget
(`examples/router_readiness.py`). The default is 50 s: a measured run of 40
starts on the shared-CPU CI runner class (governance and tokenomics demo
configs, with the timeout raised to 120 s for the measurement) gave p50 11.87 s,
p95 13.31 s and max 24.65 s, and the rule is twice the maximum rounded up to the
next 5 s. For comparison, uncensored local starts of the same config shape are
p50 2.5 s / max 4.1 s even pinned to one CPU; the shared runners, not the startup
work, set the budget. Each successful start prints `router ready in <n>s`, and a
timeout prints the elapsed time plus the tail of the router log before failing,
so a real startup regression is not hidden behind a larger timeout.

Set `METRUM_ROUTER_READY_TIMEOUT_S` to override the budget in seconds (positive
number). Tests: `python3 -m unittest discover -s examples/tests -p 'test_*.py' -v`.
