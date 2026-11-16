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
MAX_DIFF_BYTES = 1_000_000
INITIAL_DIFF_BYTES = 300_000
REQUEST_TIMEOUT_SECONDS = 900.0
MIN_DIFF_BYTES = 32 * 1024
MAX_OUTPUT_TOKENS = 32_000
MAX_REVIEW_BATCHES = 32
# Generated files are accepted only when a named CI check is explicitly mapped here.
GENERATED_FILE_VERIFICATION_ALLOWLIST: dict[str, str] = {}
REVIEW_CONTEXT_PATHS = ("CHANGELOG.md", "docs-site/docs/release-notes/upgrade-guide.md")
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
REVIEW_SYSTEM = """You are the independent release reviewer for Metrum AI Router. Review the supplied source diff since the previous production release. Every batch repeats the full changed diffs for CHANGELOG.md and the v5.0.0 upgrade guide, when those files changed in the range. Review only findings supported by files included in this batch; each finding location must refer to a file in this batch. Do not assume omitted source hunks are unchanged. Return only JSON with keys summary and findings. Each finding has severity (blocking or informational), category (exactly one of security, correctness, compatibility, release-integrity), title, rationale, and locations (a non-empty list of objects containing repository-relative file and positive integer line). Block only a concrete security, correctness, compatibility, or release-integrity issue that must be fixed before publish. Do not quote source, prompt text, credentials, tokens, or secret values. Mention only file paths and concise reasoning. A clean review has an empty findings array."""


class MaxTokensStopError(RuntimeError):
    """The provider stopped a valid review response at its output-token limit."""


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


def pack_diff_files(
    file_diffs: list[tuple[str, str]],
    *,
    max_bytes: int = MAX_DIFF_BYTES,
    max_batches: int = MAX_REVIEW_BATCHES,
    max_single_file_bytes: int | None = None,
) -> list[tuple[str, tuple[str, ...]]]:
    context_diffs = [(path, diff) for path, diff in file_diffs if path in REVIEW_CONTEXT_PATHS]
    context_paths = tuple(path for path, _ in context_diffs)
    context_bytes = sum(len(diff.encode("utf-8")) for _, diff in context_diffs)
    hard_limit = max_single_file_bytes or max_bytes
    if context_bytes > hard_limit:
        raise ValueError("release review context exceeds the per-request input limit")
    remaining_diffs = [(path, diff) for path, diff in file_diffs if path not in REVIEW_CONTEXT_PATHS]
    source_batches: list[tuple[list[str], list[str]]] = []
    current_diffs: list[str] = []
    current_paths: list[str] = []
    current_bytes = 0
    for path, diff in remaining_diffs:
        diff_bytes = len(diff.encode("utf-8"))
        if context_bytes + diff_bytes > max_bytes:
            if current_diffs:
                source_batches.append((current_diffs, current_paths))
                current_diffs = []
                current_paths = []
                current_bytes = 0
            if max_single_file_bytes is not None and context_bytes + diff_bytes <= hard_limit:
                source_batches.append(([diff], [path]))
                continue
            raise ValueError(f"release diff for {path} exceeds the per-request review input limit")
        if current_diffs and context_bytes + current_bytes + diff_bytes > max_bytes:
            source_batches.append((current_diffs, current_paths))
            current_diffs = []
            current_paths = []
            current_bytes = 0
            if len(source_batches) >= max_batches:
                raise ValueError("release diff exceeds the maximum number of review batches")
        current_diffs.append(diff)
        current_paths.append(path)
        current_bytes += diff_bytes
    if current_diffs:
        source_batches.append((current_diffs, current_paths))
    if not source_batches and context_diffs:
        source_batches.append(([], []))
    batches = [
        ("".join(diff for _, diff in context_diffs) + "".join(diffs), context_paths + tuple(paths))
        for diffs, paths in source_batches
    ]
    if not batches:
        raise ValueError("release diff is empty")
    if len(batches) > max_batches:
        raise ValueError("release diff exceeds the maximum number of review batches")
    return batches


def git_diff_batches(
    base: str,
    head: str,
    *,
    max_bytes: int = MAX_DIFF_BYTES,
    only_paths: set[str] | None = None,
    max_single_file_bytes: int | None = None,
) -> list[tuple[str, tuple[str, ...]]]:
    paths = git_diff_names(base, head)
    if only_paths is not None:
        paths = [path for path in paths if path in only_paths]
    file_diffs: list[tuple[str, str]] = []
    for path in paths:
        result = subprocess.run(
            ["git", "--literal-pathspecs", "diff", "--no-renames", "--no-ext-diff", "--unified=2", f"{base}..{head}", "--", path],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
        )
        if not result.stdout:
            raise ValueError(f"listed release diff file {path} produced an empty diff")
        file_diffs.append((path, result.stdout))
    return pack_diff_files(
        file_diffs,
        max_bytes=max_bytes,
        max_single_file_bytes=max_single_file_bytes,
    )


def git_diff_names(base: str, head: str) -> list[str]:
    names = subprocess.run(
        ["git", "--literal-pathspecs", "diff", "--no-renames", "--name-only", "-z", f"{base}..{head}"],
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
    ).stdout.split(b"\0")
    return [os.fsdecode(path) for path in names if path]


def read_streaming_response(response: Any, *, timeout: float, started: float | None = None) -> tuple[str, str]:
    if started is None:
        started = time.monotonic()
    event_name = ""
    data_lines: list[str] = []
    text_blocks: dict[int, list[str]] = {}
    stop_reason: str | None = None
    message_stopped = False

    def dispatch_event() -> None:
        nonlocal event_name, data_lines, stop_reason, message_stopped
        if not data_lines:
            event_name = ""
            return
        try:
            payload = json.loads("\n".join(data_lines))
        except json.JSONDecodeError as exc:
            raise RuntimeError("AI release review stream contained invalid event JSON") from exc
        kind = event_name or (payload.get("type") if isinstance(payload, dict) else None)
        if kind == "content_block_start" and isinstance(payload, dict):
            block = payload.get("content_block")
            index = payload.get("index")
            if isinstance(block, dict) and block.get("type") == "text" and isinstance(index, int):
                text_blocks.setdefault(index, []).append(str(block.get("text", "")))
        elif kind == "content_block_delta" and isinstance(payload, dict):
            index = payload.get("index")
            delta = payload.get("delta")
            if isinstance(index, int) and isinstance(delta, dict) and delta.get("type") == "text_delta":
                text_blocks.setdefault(index, []).append(str(delta.get("text", "")))
        elif kind == "message_delta" and isinstance(payload, dict):
            delta = payload.get("delta")
            if isinstance(delta, dict) and isinstance(delta.get("stop_reason"), str):
                stop_reason = delta["stop_reason"]
        elif kind == "message_stop":
            message_stopped = True
        elif kind == "error":
            raise RuntimeError("AI release review stream returned an error event")
        event_name = ""
        data_lines = []

    for raw_line in response:
        if time.monotonic() - started >= timeout:
            raise RequestTimeoutError(f"AI release review stream exceeded {timeout:g}-second request limit")
        line = raw_line.decode("utf-8") if isinstance(raw_line, bytes) else str(raw_line)
        line = line.rstrip("\r\n")
        if not line:
            dispatch_event()
        elif line.startswith(":"):
            continue
        elif line.startswith("event:"):
            event_name = line[6:].strip()
        elif line.startswith("data:"):
            data_lines.append(line[5:].lstrip())
    dispatch_event()
    if not message_stopped or stop_reason is None:
        raise RuntimeError("AI release review stream ended before message_delta/message_stop")
    final_text = ""
    if text_blocks:
        final_index = max(text_blocks)
        final_text = "".join(text_blocks[final_index]).strip()
    return final_text, stop_reason


def call_model(
    api_key: str,
    diff: str,
    *,
    batch_number: int = 1,
    batch_count: int = 1,
    timeout: float = REQUEST_TIMEOUT_SECONDS,
) -> dict[str, Any]:
    body = json.dumps({
        "model": MODEL,
        "max_tokens": MAX_OUTPUT_TOKENS,
        "stream": True,
        "output_config": {"format": {"type": "json_schema", "schema": REVIEW_OUTPUT_SCHEMA}},
        "system": REVIEW_SYSTEM,
        "messages": [{
            "role": "user",
            "content": f"Review release diff batch {batch_number} of {batch_count}. Findings must be grounded in this batch.\n\n{diff}",
        }],
    }).encode("utf-8")
    request = urllib.request.Request(
        API_URL,
        data=body,
        headers={
            "content-type": "application/json",
            "anthropic-version": API_VERSION,
            "x-api-key": api_key,
        },
        method="POST",
    )
    started = time.monotonic()
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            text, stop_reason = read_streaming_response(response, timeout=timeout, started=started)
    except urllib.error.HTTPError as exc:
        raise RuntimeError(f"AI release review request failed with HTTP status {exc.code}") from exc
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        if isinstance(exc, TimeoutError) or isinstance(getattr(exc, "reason", None), TimeoutError):
            raise RequestTimeoutError(f"AI release review request timed out after {timeout:g} seconds") from exc
        # Do not include provider response text, request data, or credentials.
        raise RuntimeError("AI release review request failed") from exc
    if stop_reason != "end_turn":
        # Stop reasons are bounded provider metadata; never print response/request text.
        safe_reason = stop_reason if isinstance(stop_reason, str) and re.fullmatch(r"[a-z_]{1,40}", stop_reason) else "unknown"
        message = f"AI release review response ended with stop_reason={safe_reason}"
        if safe_reason == "max_tokens":
            raise MaxTokensStopError(message)
        raise RuntimeError(message)
    if not text:
        raise RuntimeError("AI release review response had no text block (stop_reason=end_turn)")
    text = text.strip()
    if text.startswith("```"):
        text = re.sub(r"^```(?:json)?\s*|\s*```$", "", text, flags=re.DOTALL)
    try:
        result = json.loads(text)
    except json.JSONDecodeError as exc:
        raise RuntimeError("AI release review final text block was not valid JSON") from exc
    if not isinstance(result, dict):
        raise RuntimeError("AI release review final JSON value was not an object")
    return result


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
    normalized_coverage: list[dict[str, str]] = []
    seen_coverage: set[str] = set()
    for item in file_coverage or []:
        if not isinstance(item, dict):
            raise ValueError("AI release review file coverage entry must be an object")
        file_path = item.get("file")
        status = item.get("status")
        verification = item.get("verification", "")
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
            artifact = validate_artifact(artifact, args.base, args.head, expected_files)
            cache_hit = True
        else:
            api_key = os.environ.get("ANTHROPIC_API_KEY", "")
            if not api_key:
                print("AI release review failed: required API credential is unavailable", file=sys.stderr)
                return 2
            expected_files = set(git_diff_names(args.base, args.head))
            diff_limit = INITIAL_DIFF_BYTES
            batches = git_diff_batches(
                args.base,
                args.head,
                max_bytes=diff_limit,
                max_single_file_bytes=MAX_DIFF_BYTES,
            )
            batched_files = {path for _diff, paths in batches for path in paths}
            if batched_files != expected_files:
                raise ValueError("release review batches do not match git diff --name-only")
            findings_by_id: dict[str, dict[str, Any]] = {}
            batch_index = 0
            while batch_index < len(batches):
                if len(batches) > MAX_REVIEW_BATCHES:
                    raise ValueError("release diff exceeds the maximum number of review batches")
                diff, paths = batches[batch_index]
                batch_label = f"{batch_index + 1}/{len(batches)}"
                print(f"AI release review batch {batch_label} started", file=sys.stderr)
                try:
                    batch_review = call_model(
                        api_key, diff, batch_number=batch_index + 1, batch_count=len(batches)
                    )
                    artifact_batch = build_artifact(batch_review, args.base, args.head, allowed_files=set(paths))
                except (MaxTokensStopError, RequestTimeoutError) as exc:
                    failure_kind = "max_tokens" if isinstance(exc, MaxTokensStopError) else "timeout"
                    if diff_limit <= MIN_DIFF_BYTES:
                        raise type(exc)(
                            f"batch {batch_label} reached {failure_kind} at the {MIN_DIFF_BYTES}-byte minimum"
                        ) from exc
                    next_limit = max(MIN_DIFF_BYTES, diff_limit // 2)
                    try:
                        smaller_batches = git_diff_batches(
                            args.base,
                            args.head,
                            max_bytes=next_limit,
                            only_paths=set(paths),
                            max_single_file_bytes=MAX_DIFF_BYTES,
                        )
                    except ValueError as split_error:
                        raise ValueError(f"batch {batch_label} could not be split further: {split_error}") from split_error
                    if smaller_batches == [(diff, paths)]:
                        raise type(exc)(
                            f"batch {batch_label} still reaches {failure_kind} and cannot be split by file"
                        ) from exc
                    if len(batches) - 1 + len(smaller_batches) > MAX_REVIEW_BATCHES:
                        raise ValueError("release diff exceeds the maximum number of review batches after splitting") from exc
                    batches[batch_index:batch_index + 1] = smaller_batches
                    diff_limit = next_limit
                    print(
                        f"AI release review batch {batch_label} stopped with {failure_kind}; split into {len(smaller_batches)} smaller batch(es) (limit={diff_limit} bytes)",
                        file=sys.stderr,
                    )
                    continue
                except (RuntimeError, ValueError) as exc:
                    raise RuntimeError(f"batch {batch_label} failed: {exc}") from exc
                for finding in artifact_batch["findings"]:
                    existing = findings_by_id.get(finding["id"])
                    if existing is None or finding["severity"] == "blocking":
                        findings_by_id[finding["id"]] = finding
                print(f"AI release review batch {batch_label} completed", file=sys.stderr)
                batch_index += 1
            changed_files = len({path for _diff, paths in batches for path in paths})
            reviewed_paths = sorted({path for _diff, paths in batches for path in paths})
            artifact = build_artifact({
                "summary": f"Reviewed {changed_files} changed files across {len(batches)} bounded batches.",
                "findings": list(findings_by_id.values()),
            }, args.base, args.head, file_coverage=[
                {"file": path, "status": "reviewed"} for path in reviewed_paths
            ])
            artifact = validate_artifact(artifact, args.base, args.head, expected_files)
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
