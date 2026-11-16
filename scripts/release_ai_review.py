# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Produce a fail-closed AI release review without logging request or response text."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


MODEL = "claude-fable-5-2"
API_URL = "https://api.anthropic.com/v1/messages"
API_VERSION = "2023-06-01"
MAX_DIFF_BYTES = 1_000_000
SENSITIVE = re.compile(r"(?i)(?:sk-[a-z0-9_-]{12,}|bearer\s+\S+|api[_ -]?key\s*[:=]|token\s*[:=]|password\s*[:=])")
REVIEW_SYSTEM = """You are the independent release reviewer for Metrum AI Router. Review the supplied source diff since the previous production release, CHANGELOG.md changes, and v5.0.0 upgrade-guide changes. Return only JSON with keys summary and findings. Each finding has severity (blocking or informational), category (exactly one of security, correctness, compatibility, release-integrity), title, rationale, and locations (a non-empty list of objects containing repository-relative file and positive integer line). Block only a concrete security, correctness, compatibility, or release-integrity issue that must be fixed before publish. Do not quote source, prompt text, credentials, tokens, or secret values. Mention only file paths and concise reasoning. A clean review has an empty findings array."""


def git_diff(base: str, head: str) -> str:
    result = subprocess.run(
        ["git", "diff", "--no-ext-diff", "--unified=2", f"{base}..{head}"],
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        text=True,
    )
    if not result.stdout.strip():
        raise ValueError("release diff is empty")
    if len(result.stdout.encode("utf-8")) > MAX_DIFF_BYTES:
        raise ValueError("release diff exceeds the review input size limit")
    return result.stdout


def call_model(api_key: str, diff: str, timeout: float = 120.0) -> dict[str, Any]:
    body = json.dumps({
        "model": MODEL,
        "max_tokens": 4096,
        "temperature": 0,
        "system": REVIEW_SYSTEM,
        "messages": [{"role": "user", "content": "Review this release diff:\n\n" + diff}],
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
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            data = json.loads(response.read())
    except (urllib.error.URLError, TimeoutError, OSError, json.JSONDecodeError) as exc:
        # Do not include provider response text, request data, or credentials.
        raise RuntimeError("AI release review request failed or timed out") from exc
    if not isinstance(data, dict) or not isinstance(data.get("content"), list):
        raise RuntimeError("AI release review returned an invalid report")
    text_blocks = [item.get("text", "") for item in data["content"] if isinstance(item, dict) and item.get("type") == "text"]
    text = "\n".join(text_blocks).strip()
    if text.startswith("```"):
        text = re.sub(r"^```(?:json)?\s*|\s*```$", "", text, flags=re.DOTALL)
    try:
        result = json.loads(text)
    except json.JSONDecodeError as exc:
        raise RuntimeError("AI release review returned an invalid report") from exc
    if not isinstance(result, dict):
        raise RuntimeError("AI release review returned an invalid report")
    return result


def safe_text(value: object, field: str) -> str:
    if not isinstance(value, str):
        raise ValueError(f"AI release review {field} must be text")
    value = " ".join(value.split())
    if not value or len(value) > 600 or SENSITIVE.search(value):
        raise ValueError(f"AI release review {field} is empty, too long, or contains sensitive-looking text")
    return value


def build_artifact(review: dict[str, Any], base: str, head: str) -> dict[str, Any]:
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
    payload = {
        "schema": "metrum-ai-router-release-review-artifact/v1",
        "base": base,
        "head": head,
        "summary": summary,
        "findings": normalized,
    }
    digest = hashlib.sha256(json.dumps(payload, ensure_ascii=True, separators=(",", ":"), sort_keys=True).encode("utf-8")).hexdigest()
    return {**payload, "artifact_digest": digest}


def validate_artifact(artifact: dict[str, Any], base: str, head: str) -> dict[str, Any]:
    if artifact.get("schema") != "metrum-ai-router-release-review-artifact/v1" or artifact.get("base") != base or artifact.get("head") != head:
        raise ValueError("stored AI release review artifact does not match the requested range")
    rebuilt = build_artifact(artifact, base, head)
    if rebuilt != artifact:
        raise ValueError("stored AI release review artifact digest or contents are invalid")
    return artifact


def evaluate(review: dict[str, Any], acceptances: dict[str, Any], base: str, head: str) -> tuple[dict[str, Any], int]:
    artifact = validate_artifact(review, base, head) if review.get("schema") == "metrum-ai-router-release-review-artifact/v1" else build_artifact(review, base, head)
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
        if args.artifact_cache and args.artifact_cache.exists():
            try:
                artifact = json.loads(args.artifact_cache.read_text(encoding="utf-8"))
            except json.JSONDecodeError as exc:
                raise ValueError("stored AI release review artifact is malformed") from exc
            artifact = validate_artifact(artifact, args.base, args.head)
            cache_hit = True
        else:
            diff = git_diff(args.base, args.head)
            api_key = os.environ.get("ANTHROPIC_API_KEY", "")
            if not api_key:
                print("AI release review failed: required API credential is unavailable", file=sys.stderr)
                return 2
            artifact = build_artifact(call_model(api_key, diff), args.base, args.head)
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
