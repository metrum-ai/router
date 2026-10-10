#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Live pi tool-continuation smoke for a running local Metrum AI Router.

Drives the `pi` coding agent against a router group (default big-coder):
pi writes proof.txt with a unique token through the write tool, reads it
back through the read tool, and replies `DONE <contents>`. Then it sends one
unary and one streaming Chat Completions request with urllib.

Checks:
  - pi exits 0 and runs the write and read tools
  - proof.txt holds the token and the final assistant text has DONE <token>
  - every response `model` (pi turns, unary body, every SSE chunk) matches
    --expect-model-identity: the group for requested_group, or the
    request's request_usage.target_model for upstream
  - every new request_usage row has status=200 and requested_model=<group>

pi runs with a temporary PI_CODING_AGENT_DIR whose `metrum` provider reads
the caller token with `!cat <token-file>`. ~/.pi is never read or written,
and the token is never printed. Stdlib only.

Example (local router from `make local-router`):
  python3 scripts/pi_tool_continuation_smoke.py \\
    --base-url http://127.0.0.1:18081/v1 \\
    --token-file tmp/local-pi/METRUM_API_KEY \\
    --usage-db tmp/local-pi/usage.sqlite \\
    --expect-model-identity requested_group
"""

from __future__ import annotations

import argparse
import json
import os
import secrets
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

IDENTITIES = ("upstream", "requested_group")
REQUIRED_TOOLS = ("write", "read")
PROOF_FILE = "proof.txt"


def make_token() -> str:
    return "PROOF-" + secrets.token_hex(6)


def build_prompt(token: str) -> str:
    return (
        f"Use the write tool to create the file {PROOF_FILE} containing exactly: {token} . "
        f"Then use the read tool to read {PROOF_FILE} back. "
        "Finally reply with DONE followed by the file contents."
    )


def build_pi_models(base_url: str, token_file: Path, group: str) -> dict:
    """Return an isolated pi models.json with one `metrum` provider."""
    return {
        "providers": {
            "metrum": {
                "baseUrl": base_url.rstrip("/"),
                "api": "openai-completions",
                "apiKey": f"!cat {token_file}",
                "authHeader": True,
                "compat": {
                    "supportsStore": False,
                    "supportsDeveloperRole": False,
                    "supportsReasoningEffort": False,
                },
                "models": [
                    {
                        "id": group,
                        "name": f"Metrum AI Router {group}",
                        "contextWindow": 124518,
                        "maxTokens": 8192,
                        "reasoning": False,
                        "input": ["text"],
                    }
                ],
            }
        }
    }


def _message_text(message: dict) -> str:
    content = message.get("content")
    if isinstance(content, str):
        return content
    if not isinstance(content, list):
        return ""
    return "".join(
        str(part.get("text", ""))
        for part in content
        if isinstance(part, dict) and part.get("type") == "text"
    )


def parse_pi_events(lines: list[str]) -> dict:
    """Summarize pi --mode json output.

    Returns successful tool names in order, the final assistant text, the
    caller-facing model of each assistant message, and the count of lines
    that were not JSON. pi records `responseModel` only when the response
    `model` differs from the requested one, so the effective model is
    `responseModel`, falling back to `model`.
    """
    tools: list[str] = []
    tool_errors: list[str] = []
    final_text = ""
    models: list[str] = []
    bad_lines = 0
    for line in lines:
        line = line.strip()
        if not line:
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            bad_lines += 1
            continue
        if not isinstance(event, dict):
            continue
        kind = event.get("type")
        if kind == "tool_execution_end":
            name = str(event.get("toolName", ""))
            if event.get("isError"):
                tool_errors.append(name)
            else:
                tools.append(name)
        elif kind == "message_end":
            message = event.get("message")
            if not isinstance(message, dict) or message.get("role") != "assistant":
                continue
            effective = message.get("responseModel") or message.get("model")
            if effective:
                models.append(str(effective))
            text = _message_text(message)
            if text.strip():
                final_text = text
    return {
        "tools": tools,
        "tool_errors": tool_errors,
        "final_text": final_text,
        "models": models,
        "bad_lines": bad_lines,
    }


def check_pi_result(exit_code: int, parsed: dict, token: str, file_contents: str | None) -> list[str]:
    failures: list[str] = []
    if exit_code != 0:
        failures.append(f"pi exited {exit_code}")
    for tool in REQUIRED_TOOLS:
        if tool not in parsed["tools"]:
            failures.append(f"pi did not run the {tool} tool")
    if file_contents is None:
        failures.append(f"{PROOF_FILE} was not created")
    elif file_contents.strip() != token:
        failures.append(f"{PROOF_FILE} contents do not match the token")
    if f"DONE {token}" not in " ".join(parsed["final_text"].split()):
        failures.append("final assistant text does not contain DONE <token>")
    return failures


def parse_sse_models(body: str) -> tuple[list[str], int]:
    """Return the `model` of every SSE data chunk and the chunk count."""
    models: list[str] = []
    chunks = 0
    for raw in body.splitlines():
        line = raw.strip()
        if not line.startswith("data:"):
            continue
        data = line[len("data:"):].strip()
        if not data or data == "[DONE]":
            continue
        chunks += 1
        try:
            event = json.loads(data)
        except json.JSONDecodeError:
            models.append("<invalid-json>")
            continue
        if isinstance(event, dict) and "model" in event:
            models.append(str(event["model"]))
    return models, chunks


def model_matches(identity: str, group: str, seen: str, target_model: str) -> bool:
    """Report whether a caller-facing model fits the expected identity.

    requested_group must be the group exactly. upstream must be the serving
    target model, or a dated snapshot of it such as `<target>-2026-10-01`.
    """
    if identity == "requested_group":
        return seen == group
    if not target_model or seen == group:
        return False
    return seen == target_model or seen.startswith(target_model + "-")


def check_models(identity: str, group: str, seen: list[str], target_models: list[str], label: str) -> list[str]:
    failures: list[str] = []
    if not seen:
        return [f"{label}: no model values seen"]
    for value in sorted(set(seen)):
        if not any(model_matches(identity, group, value, t) for t in (target_models or [""])):
            failures.append(f"{label}: model {value!r} does not match {identity} (targets {sorted(set(target_models))})")
    return failures


def check_usage_rows(rows: list[dict], group: str, minimum: int) -> list[str]:
    failures: list[str] = []
    if len(rows) < minimum:
        failures.append(f"expected at least {minimum} new request_usage rows, got {len(rows)}")
    for row in rows:
        if row.get("status") != 200:
            failures.append(f"request_usage {row.get('request_id')} status {row.get('status')}")
        if row.get("requested_model") != group:
            failures.append(f"request_usage {row.get('request_id')} requested_model {row.get('requested_model')!r}")
    return failures


def usage_high_water(db: Path) -> int:
    with sqlite3.connect(f"file:{db}?mode=ro", uri=True) as conn:
        row = conn.execute("SELECT COALESCE(MAX(rowid), 0) FROM request_usage").fetchone()
    return int(row[0])


def usage_rows_since(db: Path, rowid: int) -> list[dict]:
    with sqlite3.connect(f"file:{db}?mode=ro", uri=True) as conn:
        conn.row_factory = sqlite3.Row
        cur = conn.execute(
            "SELECT request_id, status, requested_model, target_provider, target_model "
            "FROM request_usage WHERE rowid > ? ORDER BY rowid",
            (rowid,),
        )
        return [dict(r) for r in cur.fetchall()]


def wait_usage_rows(db: Path, rowid: int, request_ids: set[str], minimum: int, timeout: float) -> list[dict]:
    """Poll until the usage writer has flushed the expected rows."""
    deadline = time.monotonic() + timeout
    while True:
        rows = usage_rows_since(db, rowid)
        have = {r["request_id"] for r in rows}
        if (len(rows) >= minimum and request_ids <= have) or time.monotonic() >= deadline:
            return rows
        time.sleep(0.5)


def run_pi(pi: str, base_url: str, token_file: Path, group: str, prompt: str, timeout: float) -> tuple[int, list[str], str | None, str]:
    with tempfile.TemporaryDirectory(prefix="metrum-pi-agent-") as agent_dir, tempfile.TemporaryDirectory(
        prefix="metrum-pi-work-"
    ) as work_dir:
        models_path = Path(agent_dir) / "models.json"
        models_path.write_text(json.dumps(build_pi_models(base_url, token_file, group), indent=2) + "\n", encoding="utf-8")
        os.chmod(models_path, 0o600)
        env = dict(os.environ)
        env["PI_CODING_AGENT_DIR"] = agent_dir
        cmd = [pi, "-p", "--no-session", "--provider", "metrum", "--model", group, "--mode", "json", prompt]
        try:
            result = subprocess.run(
                cmd,
                cwd=work_dir,
                env=env,
                stdin=subprocess.DEVNULL,
                capture_output=True,
                text=True,
                timeout=timeout,
            )
            code, stdout, stderr = result.returncode, result.stdout, result.stderr
        except subprocess.TimeoutExpired as err:
            code = 124
            stdout = err.stdout.decode() if isinstance(err.stdout, bytes) else (err.stdout or "")
            stderr = "pi timed out"
        proof = Path(work_dir) / PROOF_FILE
        contents = proof.read_text(encoding="utf-8") if proof.is_file() else None
        return code, stdout.splitlines(), contents, stderr[-2000:]


def chat_request(base_url: str, token: str, group: str, stream: bool, timeout: float) -> tuple[int, str, str]:
    payload: dict = {
        "model": group,
        "messages": [{"role": "user", "content": "Reply with exactly: smoke ok"}],
        "max_tokens": 64,
    }
    if stream:
        payload["stream"] = True
        payload["stream_options"] = {"include_usage": True}
    req = urllib.request.Request(
        base_url.rstrip("/") + "/chat/completions",
        data=json.dumps(payload).encode("utf-8"),
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status, resp.headers.get("X-Request-Id", ""), resp.read().decode("utf-8", errors="replace")
    except urllib.error.HTTPError as err:
        return err.code, err.headers.get("X-Request-Id", ""), err.read().decode("utf-8", errors="replace")


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", required=True, help="router OpenAI base URL, e.g. http://127.0.0.1:18081/v1")
    parser.add_argument("--token-file", required=True, type=Path, help="file holding the caller token (never printed)")
    parser.add_argument("--usage-db", required=True, type=Path, help="router SQLite usage DB")
    parser.add_argument("--expect-model-identity", required=True, choices=IDENTITIES)
    parser.add_argument("--group", default="big-coder", help="router model group (default: big-coder)")
    parser.add_argument("--pi", default="pi", help="pi executable (default: pi on PATH)")
    parser.add_argument("--timeout", type=float, default=600, help="pi timeout in seconds")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    token_file = args.token_file.expanduser().resolve()
    usage_db = args.usage_db.expanduser().resolve()
    if not token_file.is_file():
        print(f"token file not found: {token_file}", file=sys.stderr)
        return 2
    if not usage_db.is_file():
        print(f"usage DB not found: {usage_db}", file=sys.stderr)
        return 2
    caller_token = token_file.read_text(encoding="utf-8").strip()
    failures: list[str] = []
    start_rowid = usage_high_water(usage_db)

    proof_token = make_token()
    code, lines, contents, pi_stderr = run_pi(
        args.pi, args.base_url, token_file, args.group, build_prompt(proof_token), args.timeout
    )
    parsed = parse_pi_events(lines)
    failures += check_pi_result(code, parsed, proof_token, contents)
    pi_rowid = usage_high_water(usage_db)

    hops: dict[str, dict] = {}
    for name, stream in (("unary", False), ("stream", True)):
        status, request_id, body = chat_request(args.base_url, caller_token, args.group, stream, 120)
        if stream:
            seen, chunks = parse_sse_models(body)
        else:
            try:
                seen, chunks = [str(json.loads(body).get("model", ""))], 1
            except (json.JSONDecodeError, AttributeError):
                seen, chunks = [], 0
        hops[name] = {"status": status, "request_id": request_id, "models": seen, "chunks": chunks}
        if status != 200:
            failures.append(f"{name} chat request returned {status}")
        if not request_id:
            failures.append(f"{name} chat request has no X-Request-Id")

    curl_ids = {h["request_id"] for h in hops.values() if h["request_id"]}
    rows = wait_usage_rows(usage_db, start_rowid, curl_ids, len(curl_ids) + 1, 15)
    by_id = {r["request_id"]: r for r in rows}
    failures += check_usage_rows(rows, args.group, len(curl_ids) + 1)

    pi_targets = [r["target_model"] for r in rows if r["request_id"] not in curl_ids]
    failures += check_models(args.expect_model_identity, args.group, parsed["models"], pi_targets, "pi")
    for name, hop in hops.items():
        target = by_id.get(hop["request_id"], {}).get("target_model", "")
        hop["target_provider"] = by_id.get(hop["request_id"], {}).get("target_provider", "")
        hop["target_model"] = target
        failures += check_models(args.expect_model_identity, args.group, hop["models"], [target], name)

    summary = {
        "schema": "metrum.ai/router-pi-tool-continuation-smoke/v1",
        "ok": not failures,
        "expect_model_identity": args.expect_model_identity,
        "group": args.group,
        "proof_token": proof_token,
        "pi": {
            "exit_code": code,
            "tools": parsed["tools"],
            "tool_errors": parsed["tool_errors"],
            "final_text": parsed["final_text"][:200],
            "models": parsed["models"],
            "usage_rows": [
                {k: r[k] for k in ("request_id", "status", "target_provider", "target_model")}
                for r in rows
                if r["request_id"] not in curl_ids
            ],
            "new_rows_during_pi": max(pi_rowid - start_rowid, 0),
        },
        "chat": {
            name: {
                "status": h["status"],
                "request_id": h["request_id"],
                "chunks": h["chunks"],
                "models": sorted(set(h["models"])),
                "target_provider": h.get("target_provider", ""),
                "target_model": h.get("target_model", ""),
            }
            for name, h in hops.items()
        },
        "failures": failures,
    }
    if failures and code != 0:
        summary["pi"]["stderr_tail"] = pi_stderr
    print(json.dumps(summary, indent=2))
    return 0 if not failures else 1


if __name__ == "__main__":
    raise SystemExit(main())
