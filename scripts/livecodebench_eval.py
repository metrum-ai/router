#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Sanitized, opt-in LiveCodeBench release_v6 evaluation contract.

This module deliberately keeps benchmark prompts and model generations in
memory.  Its persisted output is an allowlisted numeric aggregate only.
"""
from __future__ import annotations

import argparse
import contextlib
import hashlib
import importlib
import io
import json
import subprocess
import sys
from collections.abc import Mapping
from pathlib import Path
from typing import Any, Callable, Iterable

ROOT = Path(__file__).resolve().parents[1]
CONTRACT_PATH = ROOT / "evaluators/livecodebench/contract.json"
SAFE_FIELDS = ("status", "release_version", "selected", "completed", "scored", "errors", "pass_at_1")
PROMPT_FORMATTER = "lcb_runner.prompts.code_generation.format_prompt_generation@28fef95e:OpenAIChat"

class ContractError(ValueError):
    pass


def contract() -> dict[str, Any]:
    value = json.loads(CONTRACT_PATH.read_text(encoding="utf-8"))
    required = {"livecodebench_revision", "datasets_version", "dataset", "dataset_files", "release_version", "task_count", "sampling_seed", "generation", "request_timeout_seconds", "scorer_version", "prompt_formatter"}
    if not isinstance(value, dict) or required - value.keys():
        raise ContractError("LiveCodeBench contract is incomplete")
    if value["release_version"] != "release_v6" or value["task_count"] != 40:
        raise ContractError("LiveCodeBench contract must pin release_v6 and exactly 40 tasks")
    if value["prompt_formatter"] != PROMPT_FORMATTER:
        raise ContractError("LiveCodeBench contract must pin the official OpenAI-chat prompt formatter")
    return value


def task_id(task: Any) -> str:
    value = getattr(task, "question_id", task.get("question_id") if isinstance(task, dict) else None)
    if not isinstance(value, str) or not value:
        raise ContractError("dataset task is missing question_id")
    return value


def select_tasks(tasks: Iterable[Any], *, seed: str, count: int) -> list[Any]:
    by_id: dict[str, Any] = {}
    for task in tasks:
        identifier = task_id(task)
        if identifier in by_id:
            raise ContractError("dataset task IDs must be distinct before sampling")
        by_id[identifier] = task
    if len(by_id) < count:
        raise ContractError("dataset has fewer tasks than the required sample")
    ranked = sorted(by_id, key=lambda identifier: hashlib.sha256(f"{seed}:{identifier}".encode()).hexdigest())
    return [by_id[identifier] for identifier in ranked[:count]]


def load_official_tasks() -> list[Any]:
    values = contract()
    # datasets 4+ removed dataset loading scripts, and
    # livecodebench/code_generation_lite still ships code_generation_lite.py,
    # so the checkout's load_code_generation_dataset (a load_dataset(...,
    # trust_remote_code=True) call) cannot run on the pinned datasets 5 line
    # (metrum-ai/router#415). Load the exact JSONL files the official script
    # maps for the pinned release_version (contract dataset_files, copied from
    # the script's ALLOWED_FILES) and construct rows with the checkout's
    # official CodeGenerationProblem. Every field in those files is already a
    # JSON string, so no script-side casting is involved. The prompt formatter
    # and scorer identities remain pinned to the official checkout revision.
    try:
        module = importlib.import_module("lcb_runner.benchmarks.code_generation")
        problem_cls = module.CodeGenerationProblem
        hub_download = importlib.import_module("huggingface_hub").hf_hub_download
    except (ImportError, AttributeError) as exc:
        raise ContractError("official LiveCodeBench runner/dataset contract unavailable before inference") from exc
    files = (values.get("dataset_files") or {}).get(values["release_version"])
    if not isinstance(files, list) or not files or not all(isinstance(name, str) and "/" not in name for name in files):
        raise ContractError("pinned release_version has no dataset file mapping")
    tasks: list[Any] = []
    seen_ids: set[str] = set()
    for filename in files:
        path = Path(hub_download(repo_id=values["dataset"], repo_type="dataset", filename=filename))
        with open(path, encoding="utf-8") as handle:
            for line in handle:
                if not line.strip():
                    continue
                row = json.loads(line)
                identifier = task_id(row)
                if identifier in seen_ids:
                    raise ContractError("dataset task IDs must be distinct before sampling")
                seen_ids.add(identifier)
                tasks.append(problem_cls(**row))
    if not tasks:
        raise ContractError("official LiveCodeBench dataset files returned no tasks")
    return tasks


def validate_environment(lcb_root: Path) -> None:
    values = contract()
    completed = subprocess.run(["git", "-C", str(lcb_root), "rev-parse", "HEAD"], text=True, capture_output=True, check=False)
    if completed.returncode or completed.stdout.strip() != values["livecodebench_revision"]:
        raise ContractError("LiveCodeBench checkout revision does not match the pinned contract")
    try:
        datasets = importlib.import_module("datasets")
    except ImportError as exc:
        raise ContractError("pinned datasets dependency is unavailable before inference") from exc
    if getattr(datasets, "__version__", "") != values["datasets_version"]:
        raise ContractError("datasets version does not match the pinned contract")


def aggregate(tasks: Iterable[Any], infer: Callable[[Any], str], score: Callable[[Any, str], bool]) -> dict[str, Any]:
    selected = list(tasks)
    completed = scored = errors = passed = 0
    for task in selected:
        try:
            response = infer(task)
            if not isinstance(response, str) or not response.strip():
                raise ContractError("runner returned an empty placeholder output")
            completed += 1
            passed += int(bool(score(task, response)))
            scored += 1
        except Exception:
            errors += 1
    if completed != len(selected) or scored != len(selected) or errors:
        raise ContractError("runner did not complete and score every selected task")
    return sanitize({"status": "completed", "release_version": contract()["release_version"], "selected": len(selected), "completed": completed, "scored": scored, "errors": errors, "pass_at_1": passed / scored})


def official_prompt_messages(task: Any) -> list[dict[str, str]]:
    """Build the pinned official OpenAI-chat code-generation prompt in memory."""
    try:
        formatter = importlib.import_module("lcb_runner.prompts.code_generation").format_prompt_generation
        style = importlib.import_module("lcb_runner.lm_styles").LMStyle.OpenAIChat
    except (ImportError, AttributeError) as exc:
        raise ContractError("official LiveCodeBench prompt formatter is unavailable before inference") from exc
    messages = formatter(task, style)
    if not isinstance(messages, list) or not messages or not all(
        isinstance(message, dict)
        and isinstance(message.get("role"), str)
        and isinstance(message.get("content"), str)
        for message in messages
    ):
        raise ContractError("official LiveCodeBench prompt formatter returned invalid OpenAI chat messages")
    return messages


def protected_runner(path: Path) -> Callable[[Any], str]:
    if not path.is_file() or path.stat().st_mode & 0o077:
        raise ContractError("runner command file must be an owner-only regular file")
    def infer(task: Any) -> str:
        prompt = json.dumps(official_prompt_messages(task), separators=(",", ":"))
        result = subprocess.run([str(path)], input=prompt, text=True, capture_output=True, timeout=contract()["request_timeout_seconds"], check=False)
        if result.returncode:
            raise ContractError("runner command failed")
        return result.stdout
    return infer


def official_scorer() -> Callable[[Any, str], bool]:
    try:
        evaluation = importlib.import_module("lcb_runner.evaluation")
        codegen_metrics = evaluation.codegen_metrics
        extract_instance_results = evaluation.extract_instance_results
    except (ImportError, AttributeError) as exc:
        raise ContractError("official LiveCodeBench scorer is unavailable before inference") from exc

    def extract_single_score(scorer_result: Any) -> bool:
        # The pinned scorer returns [metrics, results, metadata].  Only the
        # second item carries per-instance test outcomes.  Keep its full shape
        # in memory, validate it before indexing, and return one boolean only.
        if not isinstance(scorer_result, (list, tuple)) or len(scorer_result) != 3:
            raise ContractError("official LiveCodeBench scorer returned an invalid result shape")
        results = scorer_result[1]
        if not isinstance(results, Mapping) or len(results) != 1:
            raise ContractError("official LiveCodeBench scorer returned an invalid per-instance result shape")
        instance_results = extract_instance_results(results)
        if (
            not isinstance(instance_results, list)
            or len(instance_results) != 1
            or not isinstance(instance_results[0], list)
            or len(instance_results[0]) != 1
            or type(instance_results[0][0]) is not bool
        ):
            raise ContractError("official LiveCodeBench scorer returned an invalid extracted score shape")
        return instance_results[0][0]

    def score(task: Any, response: str) -> bool:
        with contextlib.redirect_stdout(io.StringIO()):
            metrics = codegen_metrics([task.get_evaluation_sample()], [[response]], num_process_evaluate=1, timeout=contract()["request_timeout_seconds"])
        return extract_single_score(metrics)
    return score


def sanitize(result: dict[str, Any]) -> dict[str, Any]:
    safe = {key: result[key] for key in SAFE_FIELDS if key in result}
    if not all(isinstance(safe.get(key), int) and safe[key] >= 0 for key in ("selected", "completed", "scored", "errors")):
        raise ContractError("aggregate counts must be non-negative integers")
    if not isinstance(safe.get("pass_at_1"), (int, float)) or not 0 <= safe["pass_at_1"] <= 1:
        raise ContractError("pass_at_1 must be a bounded numeric aggregate")
    return safe


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=("validate", "sample", "run"))
    parser.add_argument("--lcb-root", type=Path, required=True)
    parser.add_argument("--runner-command-file", type=Path)
    args = parser.parse_args()
    try:
        validate_environment(args.lcb_root)
        selected = select_tasks(load_official_tasks(), seed=contract()["sampling_seed"], count=contract()["task_count"])
        if args.command == "run":
            if args.runner_command_file is None:
                raise ContractError("run requires an owner-only runner command file")
            print(json.dumps(aggregate(selected, protected_runner(args.runner_command_file), official_scorer()), sort_keys=True))
            return 0
    except ContractError as exc:
        print(f"LiveCodeBench blocked: {exc}", file=sys.stderr)
        return 2
    # Validation commands are deliberately pre-inference: they establish that an
    # approved runner can safely receive exactly this deterministic sample.
    print(json.dumps(sanitize({"status": "validated", "release_version": contract()["release_version"], "selected": len(selected), "completed": 0, "scored": 0, "errors": 0, "pass_at_1": 0.0}), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
