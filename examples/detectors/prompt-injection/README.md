<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Prompt injection detector example

This standalone service implements `metrum.detector.v1` and scores only `tool_result` and `document` segments. It uses the open ProtectAI DeBERTa-v3-small prompt-injection classifier; see the [model card](https://huggingface.co/protectai/deberta-v3-small-prompt-injection-v2) for its license, intended use, and limitations. The upstream model card marks the project archived; it says the classifier targets English prompt injections and does not detect jailbreak attacks or non-English text. Treat its signal as one input to a security review, not a guarantee.

The router sends no text by default. Configure `include_segments` on this specific detector to opt into sending only those segment kinds to its allow-listed HTTPS endpoint. Prompts and user messages are never sent. The service does not log segment text, store it, or include it in signals. Keep this service on a trusted network; for production, terminate TLS at a trusted proxy and configure the router endpoint and `allow_hosts` for that HTTPS name. Loopback HTTP is intended only for local development.

## Run locally

Install the pinned, CPU-only dependency set with wheel hashes checked. The lock is for CPython 3.12 and uses the official PyTorch CPU wheel index (`https://download.pytorch.org/whl/cpu`):

```sh
python3 -m pip install --require-hashes -r examples/detectors/prompt-injection/requirements.txt
python3 examples/detectors/prompt-injection/server.py
```

The model is pinned to revision `d7c8842daf06de3179cc3aca76b7b3a057acc5e7`. Before loading, the service verifies the downloaded `model.safetensors` SHA-256 (`5f81f709c58b8e8a51d99e8382a152583e17847db3082922bcaf7a7ee80e91d0`). The model host requires accepting its access conditions; obtain and review the weights under your own model-host account. Do not place model-host credentials in the router configuration. Set `PROMPT_INJECTION_THRESHOLD` to a number from 0 through 1 (default `0.5`). The service binds to `127.0.0.1:8082`; `LISTEN_HOST` and `LISTEN_PORT` can override the bind address and port. A dependency, model download, checksum, or model-load failure prints a startup error and exits with status 1 before opening the listener.

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

Each selected segment is scored in 512-token windows with 64-token overlap; its signal uses the maximum injection score across all windows. Segment size and count remain bounded by the contract (8 KiB per segment, 16 segments per event). Detector work runs asynchronously after the request completes, so inference does not add latency to that request; a report signal cannot change its response. The router timeout bounds how long it waits for the service, and this example enforces a four-second socket timeout and per-request deadline, within the documented five-second sample router timeout. The configured `on_error` policy handles timeouts and classifier failures. Measure end-to-end result latency on the deployment hardware before choosing the router timeout. The example serializes classifier calls to keep model use predictable.

Run the tests without downloading model weights:

```sh
python3 -m unittest discover -s examples/detectors/prompt-injection -p 'test_*.py' -v
```
