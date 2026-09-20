#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Generate deterministic synthetic Responses SSE fixtures; no network or secrets."""
from __future__ import annotations
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1] / "testdata/sse"

def event(kind, **fields):
    return {"type": kind, **fields}

def main() -> int:
    shapes = {
        "plain-text": [event("response.output_text.delta", item_id="msg_test", delta='synthetic café 🌍'), event("response.output_text.done", item_id="msg_test", text='synthetic café 🌍')],
        "large-text": [event("response.output_text.delta", item_id="msg_test", delta="x" * 70000)],
        "structured-output": [event("response.output_text.delta", item_id="msg_test", delta='{"ok":true}')],
        "refusal": [event("response.refusal.delta", item_id="msg_test", delta="Synthetic refusal"), event("response.refusal.done", item_id="msg_test", refusal="Synthetic refusal")],
        "reasoning": [event("response.reasoning_summary_text.delta", item_id="rs_test", summary_index=0, delta="Synthetic summary")],
        "tools-parallel": [event("response.output_item.added", output_index=i, item={"id": f"fc_{i}", "call_id": f"call_{i}", "type": "function_call", "name": "lookup", "arguments": ""}) for i in range(2)] + [event("response.function_call_arguments.delta", item_id=f"fc_{i}", output_index=i, delta='{"q":"café"}') for i in range(2)] + [event("response.output_item.done", output_index=i, item={"id": f"fc_{i}", "call_id": f"call_{i}", "type": "function_call", "name": "lookup", "arguments": '{"q":"café"}'}) for i in range(2)],
        "unknown-event": [event("vendor.future", secret="synthetic"), event("response.output_text.delta", delta="ok")],
        "incomplete": [],
        "failed": [],
    }
    upstream = ROOT / "synthetic/openai-responses"
    golden = ROOT / "golden/openai-responses-to-openai-responses"
    upstream.mkdir(parents=True, exist_ok=True)
    golden.mkdir(parents=True, exist_ok=True)
    for shape, middle in shapes.items():
        status = shape if shape in ("failed", "incomplete") else "completed"
        frames = [event("response.created", response={"id": "resp_test", "status": "in_progress"}), *middle, event("response." + status, response={"id": "resp_test", "status": status, "usage": {"input_tokens": 2, "output_tokens": 3, "total_tokens": 5}})]
        ids = {}
        def normalize(value):
            if isinstance(value, dict):
                return {k: ids.setdefault(v, "mr_TEST" + ("_" + str(len(ids)) if ids else "")) if k in ("id", "item_id", "call_id") and isinstance(v, str) else normalize(v) for k, v in value.items()}
            if isinstance(value, list):
                return [normalize(v) for v in value]
            return value
        raw = "".join("event: " + f["type"] + "\ndata: " + json.dumps(f, ensure_ascii=False, separators=(",", ":")) + "\n\n" for f in frames)
        (upstream / (shape + ".sse")).write_text(raw)
        (golden / (shape + ".jsonl")).write_text("".join(json.dumps(normalize(f), ensure_ascii=False, sort_keys=True) + "\n" for f in frames if not f["type"].startswith("vendor.")))
    print(f"Wrote {len(shapes)} synthetic SSE/golden pairs under {ROOT}")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
