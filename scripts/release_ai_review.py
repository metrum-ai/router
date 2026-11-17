# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Produce a fail-closed AI release review without logging request or response text."""

from __future__ import annotations

import argparse
import collections
import hashlib
import json
import math
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


MODEL = "claude-fable-5-2"
API_URL = "https://api.anthropic.com/v1/messages"
API_VERSION = "2023-06-01"
REQUEST_TIMEOUT_SECONDS = 900.0
MAX_OUTPUT_TOKENS = 64_000
MAX_REVIEW_TURNS = 48
MAX_REVIEW_INPUT_TOKENS = 2_000_000
MAX_REVIEW_MESSAGE_BYTES = 8_000_000
MAX_CHANGED_FILE_LIST_BYTES = 2_000_000
MAX_REVIEW_TOOL_RESULT_BYTES = 8_000_000
MAX_TOOL_READ_BYTES = 256_000
MAX_TOOL_START = 5_000_000
# Generated files are accepted only when a named CI check is explicitly mapped here.
GENERATED_FILE_VERIFICATION_ALLOWLIST: dict[str, str] = {
    "docs/specs/admin-v1.openapi.json": "ci / generated-release-files",
    "internal/router/admindist/static/assets/admin-D7T4m6t1.js": "ci / generated-release-files",
    "internal/router/admindist/static/assets/admin-S7rpXrAb.css": "ci / generated-release-files",
    "internal/router/admindist/web/package-lock.json": "ci / generated-release-files",
    "tests/api_compat_ts/package-lock.json": "Supplemental offline checks / api-compat",
    "tests/agent_compat/package-lock.json": "Agent tool compatibility / local-agent-mock",
    "docs-site/package-lock.json": "Docs QA / docs-site-build",
    "examples/fine-tune-qwen3p8-27b/eval/package-lock.json": "Fine-tuning example / offline",
}
ALLOWED_DOC_ROOTS = ("docs/",)
ALLOWED_DOC_FILES = frozenset(("RELEASING.md", "CHANGELOG.md", "docs-site/docs/release-notes/upgrade-guide.md"))
REVIEW_OUTPUT_SCHEMA = {
    "type": "object",
    "properties": {
        "summary": {"type": "string", "description": "Non-empty text of at most 600 characters."},
        "findings": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "severity": {"type": "string", "enum": ["blocking", "informational"]},
                    "category": {"type": "string", "enum": ["security", "correctness", "compatibility", "release-integrity"]},
                    "title": {"type": "string", "description": "Non-empty text of at most 600 characters."},
                    "rationale": {"type": "string", "description": "Non-empty text of at most 600 characters."},
                    "locations": {
                        "type": "array",
                        "items": {
                            "type": "object",
                            "properties": {
                                "file": {"type": "string", "description": "Repository-relative path included in this review batch."},
                                "line": {"type": "integer", "description": "Positive source line number."},
                            },
                            "required": ["file", "line"],
                            "additionalProperties": False,
                        },
                        "minItems": 1,
                    },
                },
                "required": ["severity", "category", "title", "rationale", "locations"],
                "additionalProperties": False,
            },
        },
    },
    "required": ["summary", "findings"],
    "additionalProperties": False,
}
SENSITIVE_PREFIX = re.compile(r"(?i)\b(?:sk-ant-api\d{2}-|gh[pousr]_|github_pat_)([A-Za-z0-9_-]{24,})")
SENSITIVE_ASSIGNMENT = re.compile(
    r"(?i)\b(?:api[_ -]?key|token|password)\s*[:=]\s*([A-Za-z0-9_+/=-]{24,})|\bbearer\s+([A-Za-z0-9_+/=-]{24,})"
)
REVIEW_SYSTEM = """You are the independent release reviewer for Metrum AI Router. Review the changes since the previous production release using only the supplied read-only client tools. The first request contains the complete changed-file list and metadata, but no diff text. Read the diff of every changed non-generated file before returning a verdict. You may read relevant documentation with read_doc. Generated files have named CI verification and must not be read. Each finding must refer to a changed file whose diff you read, with a positive line number. Return only JSON with keys summary and findings. Each finding has severity (blocking or informational), category (exactly one of security, correctness, compatibility, release-integrity), title, rationale, and locations (a non-empty list of objects containing repository-relative file and positive integer line). Block only a concrete security, correctness, compatibility, or release-integrity issue that must be fixed before publish. Do not quote source, prompt text, credentials, tokens, or secret values. Mention only file paths and concise reasoning. A clean review has an empty findings array."""

REVIEW_TOOLS = [
    {"name": "list_changed_files", "description": "List changed paths, byte sizes, and added/deleted line counts for the fixed release range.", "input_schema": {"type": "object", "properties": {}, "additionalProperties": False}},
    {"name": "read_diff", "description": "Read a byte page of one changed file's unified diff. Paths must be in list_changed_files output.", "input_schema": {"type": "object", "properties": {"path": {"type": "string"}, "start": {"type": "integer"}, "max_bytes": {"type": "integer"}}, "required": ["path", "start", "max_bytes"], "additionalProperties": False}},
    {"name": "read_file", "description": "Read a byte page of a changed file at ref base or head.", "input_schema": {"type": "object", "properties": {"path": {"type": "string"}, "ref": {"type": "string", "enum": ["base", "head"]}, "start": {"type": "integer"}, "max_bytes": {"type": "integer"}}, "required": ["path", "ref", "start", "max_bytes"], "additionalProperties": False}},
    {"name": "read_doc", "description": "Read a byte page from RELEASING.md, CHANGELOG.md, the upgrade guide, or docs/.", "input_schema": {"type": "object", "properties": {"path": {"type": "string"}, "start": {"type": "integer"}, "max_bytes": {"type": "integer"}}, "required": ["path", "start", "max_bytes"], "additionalProperties": False}},
]


class RequestTimeoutError(RuntimeError):
    """The provider stream exceeded the per-request time limit."""


def _high_entropy_credential(value: str) -> bool:
    if len(value) < 24:
        return False
    counts = collections.Counter(value)
    entropy = -sum((count / len(value)) * math.log2(count / len(value)) for count in counts.values())
    return entropy >= 3.5


def contains_sensitive_credential(value: str) -> bool:
    if any(_high_entropy_credential(match.group(1)) for match in SENSITIVE_PREFIX.finditer(value)):
        return True
    return any(
        _high_entropy_credential(secret)
        for match in SENSITIVE_ASSIGNMENT.finditer(value)
        for secret in match.groups()
        if secret
    )


def git_diff_names(base: str, head: str) -> list[str]:
    names = subprocess.run(
        ["git", "--literal-pathspecs", "diff", "--no-renames", "--name-only", "-z", f"{base}..{head}"],
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
    ).stdout.split(b"\0")
    return [os.fsdecode(path) for path in names if path]


def read_streaming_message(response: Any, *, timeout: float) -> tuple[dict[str, Any], int]:
    """Read one streamed Messages response, retaining tool blocks without logging content."""
    started = time.monotonic()
    event_name = ""
    data_lines: list[str] = []
    blocks: dict[int, dict[str, Any]] = {}
    stop_reason: str | None = None
    input_tokens = 0
    output_tokens = 0
    stopped = False

    def dispatch() -> None:
        nonlocal event_name, data_lines, stop_reason, input_tokens, output_tokens, stopped
        if not data_lines:
            event_name = ""
            return
        payload = json.loads("\n".join(data_lines))
        kind = event_name or payload.get("type")
        index = payload.get("index")
        if kind == "message_start":
            usage = payload.get("message", {}).get("usage", {})
            input_tokens += sum(int(usage.get(field, 0) or 0) for field in (
                "input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"
            ))
        elif kind == "content_block_start" and isinstance(index, int):
            block = payload.get("content_block") or {}
            blocks[index] = dict(block)
        elif kind == "content_block_delta" and isinstance(index, int):
            delta = payload.get("delta") or {}
            block = blocks.setdefault(index, {})
            if delta.get("type") == "text_delta":
                block["text"] = str(block.get("text", "")) + str(delta.get("text", ""))
            elif delta.get("type") == "thinking_delta":
                block["thinking"] = str(block.get("thinking", "")) + str(delta.get("thinking", ""))
            elif delta.get("type") == "signature_delta":
                block["signature"] = str(block.get("signature", "")) + str(delta.get("signature", ""))
            elif delta.get("type") == "input_json_delta":
                block["partial_json"] = str(block.get("partial_json", "")) + str(delta.get("partial_json", ""))
        elif kind == "message_delta":
            delta = payload.get("delta") or {}
            stop_reason = delta.get("stop_reason", stop_reason)
            usage = payload.get("usage") or {}
            output_tokens = int(usage.get("output_tokens", output_tokens) or 0)
        elif kind == "message_stop":
            stopped = True
        elif kind == "error":
            raise RuntimeError("AI release review stream returned an error event")
        event_name, data_lines = "", []

    for raw_line in response:
        if time.monotonic() - started >= timeout:
            raise RequestTimeoutError(f"AI release review stream exceeded {timeout:g}-second request limit")
        line = raw_line.decode("utf-8") if isinstance(raw_line, bytes) else str(raw_line)
        line = line.rstrip("\r\n")
        if not line:
            dispatch()
        elif line.startswith(":"):
            continue
        elif line.startswith("event:"):
            event_name = line[6:].strip()
        elif line.startswith("data:"):
            data_lines.append(line[5:].lstrip())
    dispatch()
    if not stopped or not stop_reason:
        raise RuntimeError("AI release review stream ended before message_delta/message_stop")
    content = [blocks[index] for index in sorted(blocks)]
    for block in content:
        partial = block.pop("partial_json", None)
        if partial is not None and stop_reason != "max_tokens":
            try:
                block["input"] = json.loads(partial)
            except json.JSONDecodeError as exc:
                raise RuntimeError("AI release review tool arguments were invalid JSON") from exc
    return {"content": content, "stop_reason": stop_reason, "output_tokens": output_tokens}, input_tokens


def _safe_relative_path(path: object) -> str:
    if not isinstance(path, str) or not path or path.startswith("/") or "\\" in path or "\0" in path:
        raise ValueError("tool path is invalid")
    parts = Path(path).parts
    if any(part in {"", ".", ".."} for part in parts) or Path(path).as_posix() != path:
        raise ValueError("tool path is invalid")
    return path


def _git_bytes(args: list[str]) -> bytes:
    return subprocess.run(["git", *args], check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL).stdout


def changed_file_metadata(base: str, head: str) -> list[dict[str, Any]]:
    raw = _git_bytes(["--literal-pathspecs", "diff", "--no-renames", "--numstat", "-z", f"{base}..{head}"])
    metadata: list[dict[str, Any]] = []
    records = [record for record in raw.split(b"\0") if record]
    binary_paths = {
        os.fsdecode(record.split(b"\t", 2)[2])
        for record in records
        if record.split(b"\t", 2)[:2] == [b"-", b"-"]
    }
    binary_hashes = git_binary_hashes(base, head, binary_paths)
    for record in records:
        added, deleted, path_b = record.split(b"\t", 2)
        path = _safe_relative_path(os.fsdecode(path_b))
        size = 0
        for ref in (head, base):
            try:
                size = int(_git_bytes(["cat-file", "-s", f"{ref}:{path}"]).strip())
                break
            except subprocess.CalledProcessError:
                continue
        item = {"path": path, "size_bytes": size,
                "added_lines": None if added == b"-" else int(added),
                "deleted_lines": None if deleted == b"-" else int(deleted)}
        if path in binary_hashes:
            item.update({"binary": True, "coverage": "binary",
                         "sha256_before": binary_hashes[path]["before"],
                         "sha256_after": binary_hashes[path]["after"]})
        metadata.append(item)
    return metadata


def git_binary_paths(base: str, head: str) -> list[str]:
    raw = _git_bytes(["--literal-pathspecs", "diff", "--no-renames", "--numstat", "-z", f"{base}..{head}"])
    binary: list[str] = []
    for record in raw.split(b"\0"):
        if not record:
            continue
        added, deleted, path = record.split(b"\t", 2)
        if added == b"-" and deleted == b"-":
            binary.append(os.fsdecode(path))
    return binary


def git_binary_hashes(base: str, head: str, paths: set[str]) -> dict[str, dict[str, str | None]]:
    hashes: dict[str, dict[str, str | None]] = {}
    for path in sorted(paths):
        pair: dict[str, str | None] = {}
        for label, ref in (("before", base), ("after", head)):
            try:
                data = _git_bytes(["show", f"{ref}:{path}"])
            except subprocess.CalledProcessError:
                pair[label] = None
            else:
                pair[label] = hashlib.sha256(data).hexdigest()
        hashes[path] = pair
    return hashes


def _read_range(data: bytes, start: object, max_bytes: object) -> str:
    if not isinstance(start, int) or isinstance(start, bool) or not 0 <= start <= MAX_TOOL_START:
        raise ValueError("tool read offset is invalid")
    if not isinstance(max_bytes, int) or isinstance(max_bytes, bool) or not 1 <= max_bytes <= MAX_TOOL_READ_BYTES:
        raise ValueError("tool read size is invalid")
    return data[start:start + max_bytes].decode("utf-8", errors="replace")


def execute_client_tool(name: str, args: dict[str, Any], *, base: str, head: str,
                        changed: dict[str, dict[str, Any]], diff_reads: dict[str, list[tuple[int, int, int]]]) -> str:
    if name == "list_changed_files":
        if args:
            raise ValueError("list_changed_files takes no arguments")
        return json.dumps(list(changed.values()), ensure_ascii=True)
    if name == "read_diff":
        path = _safe_relative_path(args.get("path"))
        if path not in changed or path in GENERATED_FILE_VERIFICATION_ALLOWLIST or changed[path].get("binary"):
            raise ValueError("read_diff path is not an eligible changed file")
        diff = _git_bytes(["--literal-pathspecs", "diff", "--no-renames", "--no-ext-diff", "--unified=2", f"{base}..{head}", "--", path])
        if not diff:
            raise ValueError("listed release diff file produced an empty diff")
        start, max_bytes = args.get("start"), args.get("max_bytes")
        content = _read_range(diff, start, max_bytes)
        if not isinstance(start, int) or isinstance(start, bool) or not isinstance(max_bytes, int) or isinstance(max_bytes, bool):
            raise ValueError("tool read offset or size is invalid")
        actual_bytes = min(max_bytes, max(0, len(diff) - start))
        diff_reads.setdefault(path, []).append((start, start + actual_bytes, len(diff)))
        print(f"AI release review read_diff path={json.dumps(path, ensure_ascii=True)} start={start} bytes={actual_bytes}", file=sys.stderr)
        return content
    if name in {"read_file", "read_doc"}:
        path = _safe_relative_path(args.get("path"))
        if name == "read_file":
            if path not in changed or path in GENERATED_FILE_VERIFICATION_ALLOWLIST or changed[path].get("binary"):
                raise ValueError("read_file path is not an eligible changed file")
            ref_name = args.get("ref")
            if ref_name not in {"base", "head"}:
                raise ValueError("read_file ref must be base or head")
            ref = base if ref_name == "base" else head
        else:
            if path in GENERATED_FILE_VERIFICATION_ALLOWLIST:
                raise ValueError("read_doc path is generated and covered by its named CI check")
            if path not in ALLOWED_DOC_FILES and not path.startswith(ALLOWED_DOC_ROOTS):
                raise ValueError("read_doc path is outside the allowed documentation set")
            ref = head
        data = _git_bytes(["show", f"{ref}:{path}"])
        return _read_range(data, args.get("start"), args.get("max_bytes"))
    raise ValueError("unknown read-only tool")


def log_anthropic_http_error(exc: urllib.error.HTTPError, *, label: str) -> None:
    try:
        payload = json.loads(exc.read().decode("utf-8"))
        error = payload.get("error", {}) if isinstance(payload, dict) else {}
        error_type = error.get("type", "unknown") if isinstance(error, dict) else "unknown"
        error_message = error.get("message", "unknown") if isinstance(error, dict) else "unknown"
        error_type = re.sub(r"[^A-Za-z0-9_.-]", "", str(error_type))[:80] or "unknown"
        error_message = " ".join(str(error_message).split())[:500] or "unknown"
    except (UnicodeDecodeError, json.JSONDecodeError, OSError):
        error_type, error_message = "unknown", "unparseable provider error metadata"
    print(f"AI release review {label} HTTP {exc.code} error.type={error_type} error.message={error_message}", file=sys.stderr)


def _send_streaming_turn(api_key: str, messages: list[dict[str, Any]], *, timeout: float) -> tuple[dict[str, Any], int]:
    stable_tools = [dict(tool) for tool in REVIEW_TOOLS]
    stable_tools[-1]["cache_control"] = {"type": "ephemeral"}
    body = json.dumps({
        "model": MODEL,
        "max_tokens": MAX_OUTPUT_TOKENS,
        "stream": True,
        "output_config": {"effort": "medium", "format": {"type": "json_schema", "schema": REVIEW_OUTPUT_SCHEMA}},
        "system": [{"type": "text", "text": REVIEW_SYSTEM, "cache_control": {"type": "ephemeral"}}],
        "tools": stable_tools,
        "messages": messages,
    }).encode("utf-8")
    request = urllib.request.Request(API_URL, data=body, headers={
        "content-type": "application/json", "anthropic-version": API_VERSION, "x-api-key": api_key,
    }, method="POST")
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            message, input_tokens = read_streaming_message(response, timeout=timeout)
    except urllib.error.HTTPError as exc:
        log_anthropic_http_error(exc, label="tool-use turn")
        raise RuntimeError(f"AI release review request failed with HTTP status {exc.code}") from exc
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        if isinstance(exc, TimeoutError) or isinstance(getattr(exc, "reason", None), TimeoutError):
            raise RequestTimeoutError(f"AI release review request timed out after {timeout:g} seconds") from exc
        raise RuntimeError("AI release review request failed") from exc
    output_tokens = message["output_tokens"]
    print(f"AI release review turn usage.input_tokens={input_tokens} usage.output_tokens={output_tokens}", file=sys.stderr)
    return message, input_tokens


def run_agentic_review(api_key: str, base: str, head: str) -> tuple[dict[str, Any], set[str], int, int]:
    metadata = changed_file_metadata(base, head)
    changed = {item["path"]: item for item in metadata}
    if not changed:
        raise ValueError("release diff is empty")
    for item in metadata:
        check = GENERATED_FILE_VERIFICATION_ALLOWLIST.get(item["path"])
        if check:
            item["coverage"] = "generated-and-verified"
            item["verification_check"] = check
        elif not item.get("binary"):
            item["coverage"] = "model-review-required"
    generated = set(changed) & set(GENERATED_FILE_VERIFICATION_ALLOWLIST)
    binary = {path for path, item in changed.items() if item.get("binary")}
    changed_list_json = json.dumps(metadata, ensure_ascii=True)
    if len(changed_list_json.encode("utf-8")) > MAX_CHANGED_FILE_LIST_BYTES:
        raise ValueError("release review changed-file list exceeds its byte limit")
    messages: list[dict[str, Any]] = [{
        "role": "user",
        "content": "Review the release range " + base + ".." + head + ". The following is the complete changed-file list and metadata; no diff text has been supplied. Fetch the diffs and documentation you need with the read-only tools. Cover every changed non-generated text file by reading its diff. Binary files are covered by before/after SHA-256 hashes and generated files by their named CI checks; do not read either.\n" + changed_list_json,
    }]
    diff_reads: dict[str, list[tuple[int, int, int]]] = {}
    total_input_tokens = 0
    total_output_tokens = 0
    total_tool_result_bytes = 0
    retrying_max_tokens = False
    for turn in range(1, MAX_REVIEW_TURNS + 1):
        if len(json.dumps(messages, ensure_ascii=True).encode("utf-8")) > MAX_REVIEW_MESSAGE_BYTES:
            raise ValueError("release review conversation exceeds its byte limit")
        message, input_tokens = _send_streaming_turn(api_key, messages, timeout=REQUEST_TIMEOUT_SECONDS)
        total_input_tokens += input_tokens
        total_output_tokens += message["output_tokens"]
        if total_input_tokens > MAX_REVIEW_INPUT_TOKENS:
            raise ValueError("release review exceeded the maximum total input-token budget")
        stop_reason = message["stop_reason"]
        if stop_reason == "refusal":
            raise RuntimeError("AI release review was refused")
        if stop_reason == "max_tokens":
            if retrying_max_tokens:
                raise RuntimeError("AI release review repeatedly reached max_tokens")
            retrying_max_tokens = True
            print(f"AI release review turn {turn} hit max_tokens; retrying the same request once", file=sys.stderr)
            continue
        retrying_max_tokens = False
        if stop_reason == "tool_use":
            tool_results = []
            for block in message["content"]:
                if block.get("type") != "tool_use":
                    continue
                tool_name = block.get("name")
                tool_id = block.get("id")
                tool_input = block.get("input")
                if not isinstance(tool_name, str) or not isinstance(tool_id, str) or not tool_id or not isinstance(tool_input, dict):
                    raise RuntimeError("AI release review returned an invalid tool call")
                try:
                    result = execute_client_tool(tool_name, tool_input,
                                                 base=base, head=head, changed=changed, diff_reads=diff_reads)
                except (KeyError, TypeError, ValueError, subprocess.CalledProcessError) as exc:
                    result = "ERROR: " + str(exc)
                total_tool_result_bytes += len(result.encode("utf-8"))
                if total_tool_result_bytes > MAX_REVIEW_TOOL_RESULT_BYTES:
                    raise ValueError("release review tool results exceed their byte limit")
                tool_results.append({"type": "tool_result", "tool_use_id": tool_id, "content": result})
            if not tool_results:
                raise RuntimeError("AI release review requested tools without a tool call")
            messages.extend([
                {"role": "assistant", "content": message["content"]},
                {"role": "user", "content": tool_results},
            ])
            continue
        if stop_reason != "end_turn":
            raise RuntimeError("AI release review ended with an unsupported stop reason")
        text_blocks = [block.get("text", "") for block in message["content"] if block.get("type") == "text"]
        if not text_blocks or not text_blocks[-1].strip():
            raise RuntimeError("AI release review response had no final text block")
        try:
            verdict = json.loads(text_blocks[-1])
        except json.JSONDecodeError as exc:
            raise RuntimeError("AI release review final text block was not valid JSON") from exc
        covered = set()
        for path, ranges in diff_reads.items():
            total = ranges[0][2]
            intervals = sorted((start, end) for start, end, length in ranges if length == total)
            cursor = 0
            for start, end in intervals:
                if start > cursor:
                    break
                cursor = max(cursor, end)
            if cursor >= total:
                covered.add(path)
        uncovered = set(changed) - generated - binary - covered
        if uncovered:
            raise ValueError(f"AI release review coverage is incomplete (unread_files={len(uncovered)})")
        build_artifact(verdict, base, head, allowed_files=covered)
        print(f"AI release review totals usage.input_tokens={total_input_tokens} usage.output_tokens={total_output_tokens} turns={turn}", file=sys.stderr)
        return verdict, covered, total_input_tokens, total_output_tokens
    raise ValueError("AI release review exceeded the maximum turn limit")


def safe_text(value: object, field: str) -> str:
    if not isinstance(value, str):
        raise ValueError(f"AI release review {field} must be text")
    value = " ".join(value.split())
    if not value or len(value) > 600 or contains_sensitive_credential(value):
        raise ValueError(f"AI release review {field} is empty, too long, or contains sensitive-looking text")
    return value


def build_artifact(
    review: dict[str, Any],
    base: str,
    head: str,
    *,
    allowed_files: set[str] | frozenset[str] | None = None,
    file_coverage: list[dict[str, str]] | None = None,
) -> dict[str, Any]:
    if not isinstance(base, str) or not base or not isinstance(head, str) or not head:
        raise ValueError("AI release review range is invalid")
    summary = safe_text(review.get("summary"), "summary")
    raw_findings = review.get("findings")
    if not isinstance(raw_findings, list):
        raise ValueError("AI release review findings must be a list")
    normalized: list[dict[str, Any]] = []
    seen: set[str] = set()
    for finding in raw_findings:
        if not isinstance(finding, dict):
            raise ValueError("AI release review finding must be an object")
        severity = finding.get("severity")
        if severity not in {"blocking", "informational"}:
            raise ValueError("AI release review finding has an invalid severity")
        category = finding.get("category")
        if category not in {"security", "correctness", "compatibility", "release-integrity"}:
            raise ValueError("AI release review finding has an invalid category")
        title = safe_text(finding.get("title"), "finding title")
        rationale = safe_text(finding.get("rationale"), "finding rationale")
        raw_locations = finding.get("locations")
        if not isinstance(raw_locations, list) or not raw_locations:
            raise ValueError("AI release review finding locations must be a non-empty list")
        locations: set[tuple[str, int]] = set()
        for location in raw_locations:
            if not isinstance(location, dict):
                raise ValueError("AI release review finding location must be an object")
            file_path, line = location.get("file"), location.get("line")
            if (
                not isinstance(file_path, str)
                or not file_path
                or file_path.startswith("/")
                or ".." in Path(file_path).parts
                or not isinstance(line, int)
                or isinstance(line, bool)
                or line < 1
            ):
                raise ValueError("AI release review finding contains an invalid location")
            if allowed_files is not None and file_path not in allowed_files:
                raise ValueError("AI release review finding refers to a file outside its batch")
            locations.add((file_path, line))
        ordered_locations = sorted(locations)
        identity = json.dumps({
            "range": [base, head],
            "category": category,
            "locations": ordered_locations,
        }, ensure_ascii=True, separators=(",", ":"), sort_keys=True)
        finding_id = hashlib.sha256(identity.encode("utf-8")).hexdigest()[:12]
        if finding_id in seen:
            raise ValueError("AI release review returned duplicate findings")
        seen.add(finding_id)
        normalized.append({
            "id": finding_id,
            "severity": severity,
            "category": category,
            "title": title,
            "rationale": rationale,
            "locations": [{"file": file_path, "line": line} for file_path, line in ordered_locations],
            "files": sorted({file_path for file_path, _ in ordered_locations}),
        })
    normalized_coverage: list[dict[str, Any]] = []
    seen_coverage: set[str] = set()
    for item in file_coverage or []:
        if not isinstance(item, dict):
            raise ValueError("AI release review file coverage entry must be an object")
        file_path = item.get("file")
        status = item.get("status")
        verification = item.get("verification", "")
        sha256_before = item.get("sha256_before")
        sha256_after = item.get("sha256_after")
        if (
            not isinstance(file_path, str)
            or not file_path
            or file_path.startswith("/")
            or ".." in Path(file_path).parts
            or file_path in seen_coverage
        ):
            raise ValueError("AI release review file coverage contains an invalid or duplicate path")
        if status == "reviewed" and not verification:
            normalized_coverage.append({"file": file_path, "status": status})
        elif status == "generated-and-verified":
            required_check = GENERATED_FILE_VERIFICATION_ALLOWLIST.get(file_path)
            if not required_check or verification != required_check:
                raise ValueError("AI release review generated-file coverage is not in the verification allowlist")
            normalized_coverage.append({
                "file": file_path,
                "status": status,
                "verification": required_check,
            })
        elif status == "binary":
            valid_hash = lambda value: value is None or (isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value))
            if not valid_hash(sha256_before) or not valid_hash(sha256_after) or (sha256_before is None and sha256_after is None):
                raise ValueError("AI release review binary coverage has invalid before/after hashes")
            normalized_coverage.append({
                "file": file_path, "status": status,
                "sha256_before": sha256_before, "sha256_after": sha256_after,
            })
        else:
            raise ValueError("AI release review file coverage has an invalid status")
        seen_coverage.add(file_path)
    normalized_coverage.sort(key=lambda item: item["file"])
    payload = {
        "schema": "metrum-ai-router-release-review-artifact/v2",
        "base": base,
        "head": head,
        "summary": summary,
        "findings": normalized,
        "file_coverage": normalized_coverage,
    }
    digest = hashlib.sha256(json.dumps(payload, ensure_ascii=True, separators=(",", ":"), sort_keys=True).encode("utf-8")).hexdigest()
    return {**payload, "artifact_digest": digest}


def validate_artifact(
    artifact: dict[str, Any],
    base: str,
    head: str,
    expected_files: set[str],
    expected_binary: dict[str, dict[str, str | None]] | None = None,
) -> dict[str, Any]:
    if artifact.get("schema") != "metrum-ai-router-release-review-artifact/v2" or artifact.get("base") != base or artifact.get("head") != head:
        raise ValueError("stored AI release review artifact does not match the requested range")
    rebuilt = build_artifact(artifact, base, head, file_coverage=artifact.get("file_coverage"))
    if rebuilt != artifact:
        raise ValueError("stored AI release review artifact digest or contents are invalid")
    covered_files = {item["file"] for item in artifact["file_coverage"]}
    if covered_files != expected_files:
        missing = len(expected_files - covered_files)
        extra = len(covered_files - expected_files)
        raise ValueError(f"stored AI release review file coverage does not match the diff (missing={missing}, extra={extra})")
    binary_entries = {
        item["file"]: {"before": item.get("sha256_before"), "after": item.get("sha256_after")}
        for item in artifact["file_coverage"] if item["status"] == "binary"
    }
    if binary_entries != (expected_binary or {}):
        raise ValueError("stored AI release review binary hashes do not match the release range")
    return artifact


def evaluate(review: dict[str, Any], acceptances: dict[str, Any], base: str, head: str) -> tuple[dict[str, Any], int]:
    artifact = review if review.get("schema") == "metrum-ai-router-release-review-artifact/v2" else build_artifact(review, base, head)
    digest = artifact["artifact_digest"]
    raw_acceptance = acceptances.get(digest, "")
    acceptance = safe_text(raw_acceptance, "artifact acceptance") if raw_acceptance else ""
    unanswered = sum(1 for item in artifact["findings"] if item["severity"] == "blocking" and not acceptance)
    findings = [{
        **item,
        "maintainer_response": acceptance if item["severity"] == "blocking" else "",
    } for item in artifact["findings"]]
    return {
        "summary": artifact["summary"],
        "findings": findings,
        "artifact_digest": digest,
        "decision": "pass" if unanswered == 0 else "block",
    }, unanswered


def render(report: dict[str, Any], base: str, head: str, tag: str) -> str:
    lines = [
        "# AI release review",
        "",
        f"- Release tag: `{tag}`",
        f"- Reviewed range: `{base}..{head}`",
        f"- Model: `{MODEL}`",
        f"- Review artifact SHA-256: `{report['artifact_digest']}`",
        f"- Decision: **{report['decision'].upper()}**",
        "",
        report["summary"],
        "",
    ]
    if not report["findings"]:
        lines.append("No findings.")
    for item in report["findings"]:
        lines.extend([
            f"## {item['severity'].title()}: {item['title']} (`{item['id']}`)",
            "",
            item["rationale"],
            "",
            "Files: " + (", ".join(f"`{path}`" for path in item["files"]) or "none specified"),
        ])
        if item["severity"] == "blocking":
            lines.extend(["", "Maintainer response: " + (item["maintainer_response"] or "**not answered**")])
        lines.append("")
    return "\n".join(lines).rstrip() + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True, help="previous release tag")
    parser.add_argument("--head", required=True, help="release commit")
    parser.add_argument("--tag", required=True, help="release tag")
    parser.add_argument("--responses", type=Path, default=Path("docs/release-review-responses.json"))
    parser.add_argument("--artifact-cache", type=Path)
    parser.add_argument("--artifact-output", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        response_data = json.loads(args.responses.read_text(encoding="utf-8"))
        if not isinstance(response_data, dict):
            raise ValueError("maintainer response file must be an object")
        if response_data.get("schema") != "metrum-ai-router-release-review-responses/v1":
            raise ValueError("maintainer response file has an unsupported schema")
        acceptances = response_data.get("acceptances", {})
        if not isinstance(acceptances, dict):
            raise ValueError("maintainer artifact acceptances must be an object")
        cache_hit = False
        artifact = None
        expected_files: set[str] | None = None
        if args.artifact_cache and args.artifact_cache.exists():
            try:
                artifact = json.loads(args.artifact_cache.read_text(encoding="utf-8"))
            except json.JSONDecodeError as exc:
                raise ValueError("stored AI release review artifact is malformed") from exc
            expected_files = set(git_diff_names(args.base, args.head))
            changed_metadata = changed_file_metadata(args.base, args.head)
            expected_binary = {
                item["path"]: {"before": item["sha256_before"], "after": item["sha256_after"]}
                for item in changed_metadata if item.get("binary")
            }
            artifact = validate_artifact(artifact, args.base, args.head, expected_files, expected_binary)
            cache_hit = True
        else:
            api_key = os.environ.get("ANTHROPIC_API_KEY", "")
            if not api_key:
                print("AI release review failed: required API credential is unavailable", file=sys.stderr)
                return 2
            expected_files = set(git_diff_names(args.base, args.head))
            verdict, reviewed_paths, total_input_tokens, total_output_tokens = run_agentic_review(api_key, args.base, args.head)
            changed_metadata = changed_file_metadata(args.base, args.head)
            generated_paths = set(expected_files) & set(GENERATED_FILE_VERIFICATION_ALLOWLIST)
            binary_metadata = {item["path"]: item for item in changed_metadata if item.get("binary")}
            binary_paths = set(binary_metadata)
            generated_paths -= binary_paths
            artifact = build_artifact({
                "summary": safe_text(verdict.get("summary"), "summary"),
                "findings": verdict.get("findings"),
            }, args.base, args.head, file_coverage=[
                *({"file": path, "status": "reviewed"} for path in sorted(reviewed_paths)),
                *({"file": path, "status": "generated-and-verified", "verification": GENERATED_FILE_VERIFICATION_ALLOWLIST[path]} for path in sorted(generated_paths)),
                *({"file": path, "status": "binary", "sha256_before": binary_metadata[path]["sha256_before"],
                   "sha256_after": binary_metadata[path]["sha256_after"]} for path in sorted(binary_paths)),
            ])
            expected_binary = {
                path: {"before": item["sha256_before"], "after": item["sha256_after"]}
                for path, item in binary_metadata.items()
            }
            artifact = validate_artifact(artifact, args.base, args.head, expected_files, expected_binary)
        report, unanswered = evaluate(artifact, acceptances, args.base, args.head)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(render(report, args.base, args.head, args.tag), encoding="utf-8")
        args.artifact_output.parent.mkdir(parents=True, exist_ok=True)
        args.artifact_output.write_text(json.dumps(artifact, ensure_ascii=True, sort_keys=True, indent=2) + "\n", encoding="utf-8")
        github_output = os.environ.get("GITHUB_OUTPUT")
        if github_output:
            with open(github_output, "a", encoding="utf-8") as output:
                output.write(f"cache_hit={'true' if cache_hit else 'false'}\n")
    except (OSError, subprocess.CalledProcessError, json.JSONDecodeError, RuntimeError, ValueError) as exc:
        print(f"AI release review failed: {exc}", file=sys.stderr)
        return 2
    print(f"AI release review recorded; findings={len(report['findings'])}, unanswered_blockers={unanswered}")
    return 1 if unanswered else 0


if __name__ == "__main__":
    raise SystemExit(main())
