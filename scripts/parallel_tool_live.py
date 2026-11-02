#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Live parallel-tool gate (#656 Phase B).

Runs a bounded matrix against real providers, both directly and through a local
router, and records per-cell results plus the router's own usage-row cost.

Safety rules enforced here:
  * only cheap models, and every request must bound output tokens (>= 256);
  * every dispatch is preceded by a hard budget reservation (`parallel_live_budget`);
  * at most one dispatch per (cell, turn): any ambiguity halts the cell;
  * provider keys are read from the environment and never printed or written;
  * captured bodies are evidence, not logs, and contain no credentials.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from decimal import Decimal
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from parallel_live_budget import Budget, read_usage  # noqa: E402

ROOT = Path(__file__).resolve().parents[1]
TOKEN = "live-parallel-gate-token"
MAX_OUTPUT = 256
MARKERS = {"a": "GATE-A-7731", "b": "GATE-B-4192", "c": "GATE-C-6605"}


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def post(url: str, payload: dict, headers: dict[str, str], timeout: float = 90.0) -> tuple[int, bytes]:
    body = json.dumps(payload).encode()
    request = urllib.request.Request(url, data=body, method="POST", headers={"Content-Type": "application/json", **headers})
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read()


def sse_events(raw: bytes) -> list[dict]:
    events = []
    for line in raw.splitlines():
        if not line.startswith(b"data:"):
            continue
        data = line[5:].strip()
        if not data or data == b"[DONE]":
            continue
        try:
            payload = json.loads(data)
        except ValueError:
            continue
        if isinstance(payload, dict):
            events.append(payload)
    return events


def chat_calls(payload: dict) -> list[dict]:
    message = (payload.get("choices") or [{}])[0].get("message", {})
    return message.get("tool_calls") or []


def chat_stream_calls(raw: bytes) -> list[dict]:
    calls: dict[int, dict] = {}
    for event in sse_events(raw):
        for choice in event.get("choices") or []:
            for call in (choice.get("delta") or {}).get("tool_calls") or []:
                entry = calls.setdefault(call.get("index", 0), {"id": "", "name": "", "arguments": ""})
                entry["id"] = call.get("id") or entry["id"]
                function = call.get("function") or {}
                entry["name"] += function.get("name") or ""
                entry["arguments"] += function.get("arguments") or ""
    return [calls[k] for k in sorted(calls)]


def message_calls(payload: dict) -> list[dict]:
    return [block for block in payload.get("content") or [] if block.get("type") == "tool_use"]


def message_stream_calls(raw: bytes) -> list[dict]:
    calls: dict[int, dict] = {}
    for event in sse_events(raw):
        if event.get("type") == "content_block_start":
            block = event.get("content_block") or {}
            if block.get("type") == "tool_use":
                calls[event.get("index", 0)] = {"id": block.get("id", ""), "name": block.get("name", ""), "arguments": "", "start_input": block.get("input") or {}}
        elif event.get("type") == "content_block_delta":
            delta = event.get("delta") or {}
            if delta.get("type") == "input_json_delta" and event.get("index", 0) in calls:
                calls[event.get("index", 0)]["arguments"] += delta.get("partial_json") or ""
    for entry in calls.values():
        if not entry["arguments"]:
            entry["arguments"] = json.dumps(entry.pop("start_input", {}))
        else:
            entry.pop("start_input", None)
    return [calls[k] for k in sorted(calls)]


def check_calls(calls: list[dict], label: str) -> list[str]:
    problems = []
    if len(calls) < 3:
        problems.append(f"{label}: expected >=3 parallel calls, got {len(calls)}")
    ids = [call.get("id") for call in calls]
    if len(set(ids)) != len(ids) or any(not value for value in ids):
        problems.append(f"{label}: call ids are not distinct: {ids}")
    for call in calls:
        arguments = call.get("arguments") or ""
        try:
            parsed = json.loads(arguments) if isinstance(arguments, str) else arguments
        except ValueError:
            problems.append(f"{label}: call {call.get('id')} arguments are not valid JSON")
            continue
        if not isinstance(parsed, dict) or "path" not in parsed:
            problems.append(f"{label}: call {call.get('id')} arguments missing path")
    return problems


def chat_tool_schema() -> list[dict]:
    return [{"type": "function", "function": {"name": "read_file", "description": "Read a file and return its exact contents.", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"], "additionalProperties": False}}}]


def message_tool_schema() -> list[dict]:
    return [{"name": "read_file", "description": "Read a file and return its exact contents.", "input_schema": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"], "additionalProperties": False}}]


def parallel_prompt() -> str:
    return ("Read these three files in one assistant turn using three separate read_file calls, "
            "one per file, all at once: notes/a.txt, notes/b.txt, notes/c.txt. Do not summarise and do not call them one at a time.")


def followup_prompt() -> str:
    return ("Here are the three results, deliberately returned out of order and one of them empty. "
            "Reply with the exact text of each of the three markers you received, in the order A, B, C, "
            "and say EMPTY for the empty file. Do not use any tool.")


class Gate:
    def __init__(self, budget: Budget, out: Path):
        self.budget = budget
        self.out = out
        self.cells: list[dict] = []
        self.usage_unknown = 0

    def dispatch(self, provider: str, url: str, payload: dict, headers: dict[str, str], stream: bool) -> tuple[int, bytes, Decimal | None]:
        reservation = self.budget.reserve(provider, json.dumps(payload).encode())
        status, raw = post(url, payload, headers)
        usage = read_usage(provider, raw, stream)
        cost = self.budget.settle(provider, usage, reservation)
        if usage is None:
            self.usage_unknown += 1
        return status, raw, cost

    def cell(self, name: str, provider: str, base_url: str, headers: dict[str, str], routed: bool, model: str, group: str = "gate") -> None:
        result = {"cell": name, "provider": provider, "model": model, "routed": routed, "stream": True, "problems": [], "cost_usd": 0.0}
        request_model = group if routed else model
        result["request_model"] = request_model
        result["group"] = group if routed else None
        try:
            if provider == "anthropic":
                turn1 = {"model": request_model, "max_tokens": MAX_OUTPUT, "stream": True, "system": "You are a tool-using agent.", "messages": [{"role": "user", "content": parallel_prompt()}], "tools": message_tool_schema()}
                status, raw, cost = self.dispatch(provider, base_url + "/v1/messages", turn1, headers, True)
                result["turn1_status"] = status
                result["cost_usd"] += float(cost or 0)
                calls_raw = message_stream_calls(raw)
            else:
                turn1 = {"model": request_model, "max_tokens": MAX_OUTPUT, "stream": True, "stream_options": {"include_usage": True}, "messages": [{"role": "user", "content": parallel_prompt()}], "tools": chat_tool_schema(), "parallel_tool_calls": True}
                status, raw, cost = self.dispatch(provider, base_url + "/v1/chat/completions", turn1, headers, True)
                result["turn1_status"] = status
                result["cost_usd"] += float(cost or 0)
                calls_raw = chat_stream_calls(raw)
            result["turn1_sse"] = raw.decode("utf-8", "replace")[:200000]
            result["call_count"] = len(calls_raw)
            if status != 200:
                result["problems"].append(f"turn 1 HTTP {status}")
            result["problems"] += check_calls(calls_raw, "turn1")
            if result["problems"]:
                return
            calls = [{"id": call["id"], "name": call["name"], "arguments": json.loads(call["arguments"]) if isinstance(call["arguments"], str) else call["arguments"]} for call in calls_raw]
            ordered = sorted(calls, key=lambda call: str(call.get("arguments", {}).get("path", "")))
            first, second, third = ordered[0], ordered[1], ordered[2]
            results = [
                {"id": third["id"], "text": f"error: {MARKERS['c']}"},
                {"id": first["id"], "text": [MARKERS["a"][:6], MARKERS["a"][6:]]},
                {"id": second["id"], "text": ""},
            ]
            if provider == "anthropic":
                blocks = []
                for entry in results:
                    block = {"type": "tool_result", "tool_use_id": entry["id"]}
                    if isinstance(entry["text"], list):
                        block["content"] = [{"type": "text", "text": part} for part in entry["text"]]
                    elif entry["text"].startswith("error: "):
                        block["content"] = entry["text"]
                        block["is_error"] = True
                    else:
                        block["content"] = entry["text"]
                    blocks.append(block)
                turn2 = {"model": request_model, "max_tokens": MAX_OUTPUT, "stream": True, "system": "You are a tool-using agent.",
                         "messages": [{"role": "user", "content": parallel_prompt()}, {"role": "assistant", "content": [{"type": "tool_use", "id": call["id"], "name": call["name"], "input": call["arguments"]} for call in ordered]}, {"role": "user", "content": blocks}],
                         "tools": message_tool_schema()}
            else:
                messages = [{"role": "user", "content": parallel_prompt()},
                            {"role": "assistant", "content": None, "tool_calls": [{"id": call["id"], "type": "function", "function": {"name": call["name"], "arguments": json.dumps(call["arguments"])}} for call in ordered]}]
                for entry in results:
                    content = "".join(entry["text"]) if isinstance(entry["text"], list) else entry["text"]
                    messages.append({"role": "tool", "tool_call_id": entry["id"], "content": content})
                turn2 = {"model": request_model, "max_tokens": MAX_OUTPUT, "stream": True, "stream_options": {"include_usage": True},
                         "messages": messages + [{"role": "user", "content": followup_prompt()}], "tools": chat_tool_schema()}
            status, raw, cost = self.dispatch(provider, base_url + ("/v1/messages" if provider == "anthropic" else "/v1/chat/completions"), turn2, headers, True)
            result["turn2_status"] = status
            result["cost_usd"] += float(cost or 0)
            result["turn2_sse"] = raw.decode("utf-8", "replace")[:200000]
            if status != 200:
                result["problems"].append(f"turn 2 HTTP {status}")
            text = ""
            for event in sse_events(raw):
                if provider == "anthropic" and event.get("type") == "content_block_delta":
                    delta = event.get("delta") or {}
                    if delta.get("type") == "text_delta":
                        text += delta.get("text") or ""
                elif provider != "anthropic":
                    for choice in event.get("choices") or []:
                        text += (choice.get("delta") or {}).get("content") or ""
            result["final_text"] = text[:4000]
            compact = "".join(text.split())
            # Record exact (whitespace-sensitive) marker presence too: a model may
            # reformat a multi-part result, which is different from a router
            # changing the text, and both facts belong in the report.
            result["markers_exact"] = {key: MARKERS[key] in text for key in ("a", "c")}
            for key in ("a", "c"):
                if MARKERS[key] not in compact:
                    result["problems"].append(f"final answer missing marker {key}")
            # The empty result must be visible either as the requested word or as
            # an explicitly empty block; inventing text for it is the failure case.
            if "EMPTY" in compact.upper():
                result["empty_result_shown_as"] = "word"
            elif re.search(r"```\s*```", text):
                result["empty_result_shown_as"] = "empty-block"
            else:
                result["problems"].append("final answer did not show the empty result")
        finally:
            self.cells.append(result)


def router_config(temp: Path, openai_key: str | None, anthropic_key: str | None) -> tuple[Path, int]:
    port = free_port()
    providers = {}
    targets = []
    if openai_key:
        providers["openai"] = {"base_url": "https://api.openai.com/v1", "dialect": "openai-chat", "api_key": "${OPENAI_API_KEY}", "api_key_env": "OPENAI_API_KEY"}
        targets.append({"provider": "openai", "model": "gpt-4o-mini", "input_price_per_million_usd": 0.15, "output_price_per_million_usd": 0.6, "pricing_source": "https://platform.openai.com/docs/pricing", "pricing_updated_at": "2026-11-02", "request_shape_support": {"supported_inbound_dialects": ["openai-chat", "openai-responses", "anthropic"], "validation_status": "passed"}, "tool_support": {"openai_chat": ["tools", "tool_choice"], "openai_responses": ["function", "tool_choice"], "anthropic_messages": ["client_tools", "tool_choice"]}})
    if anthropic_key:
        providers["anthropic"] = {"base_url": "https://api.anthropic.com", "dialect": "anthropic", "api_key": "${ANTHROPIC_API_KEY}", "api_key_env": "ANTHROPIC_API_KEY"}
        targets.append({"provider": "anthropic", "model": "claude-haiku-4-5", "input_price_per_million_usd": 1, "output_price_per_million_usd": 5, "pricing_source": "https://platform.claude.com/docs/en/about-claude/pricing", "pricing_updated_at": "2026-11-02", "request_shape_support": {"supported_inbound_dialects": ["anthropic", "openai-chat", "openai-responses"], "validation_status": "passed"}, "tool_support": {"anthropic_messages": ["client_tools", "tool_choice"], "openai_chat": ["tools", "tool_choice"], "openai_responses": ["function", "tool_choice"]}})
    token_sha = hashlib.sha256(TOKEN.encode()).hexdigest()
    config = {
        "server": {"listen": f"127.0.0.1:{port}", "identifiers": {"mode": "passthrough"}, "default_model_group": "gate", "cache": {"enabled": False}, "usage_db": {"enabled": True, "driver": "sqlite", "path": str(temp / "usage.sqlite"), "migration_policy": "auto-safe"}, "logging": {"path": str(temp / "requests.jsonl")}},
        "state_path": str(temp / "state.json"),
        "providers": providers,
        "models": {
            "gate": {"strategy": "static", "targets": targets},
            "gate-chat-to-messages": {"strategy": "static", "targets": [t for t in targets if t["provider"] == "anthropic"]},
            "gate-messages-to-chat": {"strategy": "static", "targets": [t for t in targets if t["provider"] == "openai"]},
        },
        "callers": [{"id": "live-gate", "token_sha256": token_sha, "allow": ["gate", "gate-chat-to-messages", "gate-messages-to-chat"], "rate": {"rpm": 60, "tpm": 400000, "concurrent": 2}, "quota": {"day": {"requests": 100, "tokens": 1000000}, "month": {"tokens": 5000000}}, "key": {"lifetime_tokens": 5000000}}],
    }
    path = temp / "config.yaml"
    import yaml  # noqa: PLC0415 - optional dependency for this harness only
    path.write_text(yaml.safe_dump(config, sort_keys=False), encoding="utf-8")
    return path, port


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--router-bin", type=Path, default=None)
    parser.add_argument("--cap", default="5")
    args = parser.parse_args()

    openai_key = os.environ.get("OPENAI_API_KEY", "").strip()
    anthropic_key = os.environ.get("ANTHROPIC_API_KEY", "").strip()
    if not openai_key and not anthropic_key:
        raise SystemExit("no provider keys in the environment")

    budget = Budget(Decimal(args.cap))
    gate = Gate(budget, args.out)
    with tempfile.TemporaryDirectory(prefix="parallel-live-") as raw:
        temp = Path(raw)
        binary = args.router_bin
        if binary is None:
            binary = temp / "router"
            subprocess.run(["go", "build", "-o", str(binary), "./cmd/metrum-ai-router"], cwd=ROOT, check=True)
        config, port = router_config(temp, openai_key, anthropic_key)
        log = (temp / "router.log").open("w", encoding="utf-8")
        process = subprocess.Popen([str(binary), "-config", str(config)], cwd=temp, stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise SystemExit("router exited before readiness")
                try:
                    with urllib.request.urlopen(f"http://127.0.0.1:{port}/readyz", timeout=1) as response:
                        if response.status == 200:
                            break
                except OSError:
                    time.sleep(0.1)
            else:
                raise SystemExit("router did not become ready")
            routed_headers_anthropic = {"x-api-key": TOKEN, "anthropic-version": "2023-06-01"}
            routed_headers_openai = {"Authorization": f"Bearer {TOKEN}"}
            if openai_key:
                gate.cell("openai-chat-direct", "openai-chat", "https://api.openai.com", {"Authorization": f"Bearer {openai_key}"}, False, "gpt-4o-mini")
                gate.cell("openai-chat-routed", "openai-chat", f"http://127.0.0.1:{port}", routed_headers_openai, True, "gpt-4o-mini")
            if anthropic_key:
                gate.cell("anthropic-messages-direct", "anthropic", "https://api.anthropic.com", {"x-api-key": anthropic_key, "anthropic-version": "2023-06-01"}, False, "claude-haiku-4-5")
                gate.cell("anthropic-messages-routed", "anthropic", f"http://127.0.0.1:{port}", routed_headers_anthropic, True, "claude-haiku-4-5")
            if openai_key and anthropic_key:
                # Translated directions through the router: the caller keeps its
                # own dialect and the router translates to the other provider.
                gate.cell("chat-caller-to-messages-target", "openai-chat", f"http://127.0.0.1:{port}", routed_headers_openai, True, "claude-haiku-4-5", "gate-chat-to-messages")
                gate.cell("messages-caller-to-chat-target", "anthropic", f"http://127.0.0.1:{port}", routed_headers_anthropic, True, "gpt-4o-mini", "gate-messages-to-chat")
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
            log.close()
            usage_rows = []
            db = temp / "usage.sqlite"
            if db.exists():
                import sqlite3
                connection = sqlite3.connect(db)
                try:
                    for row in connection.execute("SELECT request_id, target_provider, target_model, stream, status, input_tokens, output_tokens, total_cost_usd FROM request_usage ORDER BY rowid"):
                        usage_rows.append({"request_id": row[0], "provider": row[1], "model": row[2], "stream": row[3], "status": row[4], "input_tokens": row[5], "output_tokens": row[6], "total_cost_usd": row[7]})
                except sqlite3.Error as exc:
                    usage_rows.append({"error": type(exc).__name__})
                finally:
                    connection.close()
        report = {"cells": gate.cells, "router_usage_rows": usage_rows, "reserved_usd": float(budget.reserved), "list_rate_usage_usd": float(budget.list_usage_cost), "unknown_usage_turns": budget.unknown_usage, "halted": budget.blocked, "cap_usd": float(budget.cap)}
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    failed = [cell["cell"] for cell in gate.cells if cell["problems"]]
    print(f"cells={len(gate.cells)} failed={len(failed)} routed_cost={budget.list_usage_cost}")
    for cell in gate.cells:
        print(f"  {cell['cell']}: {'FAIL ' + '; '.join(cell['problems']) if cell['problems'] else 'pass'}")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
