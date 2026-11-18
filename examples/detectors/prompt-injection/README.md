<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Prompt injection detector example

This standalone service implements `metrum.detector.v1` and scores only `tool_result` and `document` segments. It uses the open ProtectAI DeBERTa-v3-base prompt-injection classifier; see the [model card](https://huggingface.co/protectai/deberta-v3-base-prompt-injection-v2) for its Apache-2.0 license, intended use, and limitations. The model card says the classifier targets English prompt injections and does not detect jailbreak attacks or non-English text. The model card also warns about false positives on system prompts. Treat its signal as one input to a security review, not a guarantee.

The router sends no text by default. Configure `include_segments` on this specific detector to opt into sending only those segment kinds to its allow-listed HTTPS endpoint. Prompts and user messages are never sent. The service does not log segment text, store it, or include it in signals. Keep this service on a trusted network; for production, terminate TLS at a trusted proxy and configure the router endpoint and `allow_hosts` for that HTTPS name. Loopback HTTP is intended only for local development.

## Run locally

Install the pinned, CPU-only dependency set with wheel hashes checked. The lock is for CPython 3.12 and uses the official PyTorch CPU wheel index (`https://download.pytorch.org/whl/cpu`):

Direct inference pins are `torch==2.10.0+cpu`, `transformers==5.18.0`, and `sentencepiece==0.2.1`. `requirements.in` also pins patched `httpx2`, `httpcore2`, `setuptools`, and `idna` versions; `requirements.txt` contains hashes for the full dependency set.

```sh
python3 -m pip install --require-hashes -r examples/detectors/prompt-injection/requirements.txt
python3 examples/detectors/prompt-injection/server.py
```

The model is pinned to revision `90c9989b1a342275dd0d1a95aad283c04e075671`. Before loading, the service verifies the downloaded `model.safetensors` SHA-256 (`6521cb8d0ac08148c81464899c424e6148fcc62befa371089fa4061d8b6e0424`). The pinned model is public and downloads anonymously; no account, access grant, or model-host credential is required. Set `PROMPT_INJECTION_THRESHOLD` to a number from 0 through 1 (default `0.5`). The service binds to `127.0.0.1:8082`; `LISTEN_HOST` and `LISTEN_PORT` can override the bind address and port. A dependency, model download, checksum, or model-load failure prints a startup error and exits with status 1 before opening the listener.

Example router configuration for local report mode:

```yaml
server:
  detectors:
    - id: prompt_injection
      url: http://127.0.0.1:8082/v1/detect
      allow_hosts: [127.0.0.1]
      mode: report
      timeout_ms: 5000
      on_error: fallback
      sample_rate: 1
      include_segments: [tool_result, document]
```

This opt-in sends segment text to this one allow-listed detector. Config loading warns when segment inclusion is enabled. The detector returns only detector ID, score, severity, segment kind/index, and request ID. No prompt or user-message text enters the event.

## Latency and operation

The base classifier is larger than the previous small reference: its safetensors weights are 737,719,272 bytes (about 704 MiB), excluding tokenizer files and runtime memory. Expect higher CPU latency and memory use. CPU throughput depends on thread count, hardware, segment length and number of windows; benchmark the real-weight smoke below before sizing a deployment.

Each selected segment is scored in 512-token windows with 64-token overlap; its signal uses the maximum injection score across all windows. Segment size and count remain bounded by the contract (8 KiB per segment, 16 segments per event). Detector work runs asynchronously after the request completes, so inference does not add latency to that request; a report signal cannot change its response. The router timeout bounds how long it waits for the service, and this example enforces a four-second socket timeout and per-request deadline, within the documented five-second sample router timeout. The configured `on_error` policy handles timeouts and classifier failures. Measure end-to-end result latency on the deployment hardware before choosing the router timeout. The example serializes classifier calls to keep model use predictable.

Run the tests without downloading model weights:

```sh
python3 -m unittest discover -s examples/detectors/prompt-injection -p 'test_*.py' -v
```

Run the opt-in real-weight HTTP smoke (downloads the pinned public model anonymously and verifies its checksum):

```sh
OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 PROMPT_INJECTION_REAL_WEIGHTS=1 \
  python3 -m unittest discover -s examples/detectors/prompt-injection -p 'test_real_weights.py' -v
```

The synthetic fixtures in `smoke_fixtures.py` are a benign museum description and an instruction to ignore prior instructions and reveal the system prompt. At the unchanged default threshold, the benign fixture must return no signal and the positive fixture one high-severity signal. The smoke reports per-fixture HTTP latency and always loads real weights, even if test mode is set elsewhere. These two fixtures check integration, not detection accuracy across a representative dataset. Start in report mode and evaluate false positives and false negatives on your own data.
