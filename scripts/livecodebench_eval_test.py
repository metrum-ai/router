#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline deterministic regression for the LiveCodeBench evaluator contract."""
from __future__ import annotations
import importlib.util
import subprocess
import types
import tempfile
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("livecodebench_eval", ROOT / "scripts/livecodebench_eval.py")
assert SPEC and SPEC.loader
LCB = importlib.util.module_from_spec(SPEC); SPEC.loader.exec_module(LCB)

def need(ok: bool, message: str) -> None:
    if not ok: raise AssertionError(message)

def rejects(fn, text: str) -> None:
    try: fn()
    except LCB.ContractError as exc: need(text in str(exc), str(exc))
    else: raise AssertionError("expected contract rejection")

class PinnedScorerTask(dict):
    def get_evaluation_sample(self):
        return {"safe": "fixture"}

def main() -> int:
    values=LCB.contract(); need(values["release_version"] == "release_v6" and values["task_count"] == 40 and values["prompt_formatter"] == "lcb_runner.prompts.code_generation.format_prompt_generation@28fef95e:OpenAIChat", "release/sample/prompt pin changed")
    tasks=[{"question_id": f"task-{i:03d}"} for i in range(80)]
    first=LCB.select_tasks(tasks, seed=values["sampling_seed"], count=40); second=LCB.select_tasks(reversed(tasks), seed=values["sampling_seed"], count=40)
    first_ids=[LCB.task_id(task) for task in first]; need(first_ids == [LCB.task_id(task) for task in second] and len(set(first_ids)) == 40, "sample is not deterministic and distinct")
    rejects(lambda: LCB.select_tasks(tasks[:39], seed="x", count=40), "fewer tasks")
    rejects(lambda: LCB.select_tasks(tasks + [tasks[0]], seed="x", count=40), "distinct")
    original_import, original_run = LCB.importlib.import_module, LCB.subprocess.run
    try:
        loaded=[]
        with tempfile.TemporaryDirectory() as data_dir:
            # The six JSONL files the official code_generation_lite.py script
            # maps for release_v6 and yields unmodified (every field is
            # already a JSON string).
            file_names=["test.jsonl", "test2.jsonl", "test3.jsonl", "test4.jsonl", "test5.jsonl", "test6.jsonl"]
            paths=[]
            built_ids=iter(range(85))
            for index, name in enumerate(file_names):
                count=80 if index == 0 else 1
                rows=[{"question_id": f"task-{next(built_ids):03d}"} for _ in range(count)]
                path=Path(data_dir) / name
                path.write_text("\n".join(json.dumps(row) for row in rows) + "\n", encoding="utf-8")
                paths.append(path)
            built=[]
            class FakeProblem(dict):
                def __init__(self, **kwargs):
                    super().__init__(**kwargs); built.append(kwargs["question_id"])
            def fake_download(repo_id, repo_type, filename):
                need(repo_id == values["dataset"] and repo_type == "dataset", "loader did not read the pinned official dataset repo")
                loaded.append(filename)
                return str(paths[file_names.index(filename)])
            LCB.importlib.import_module=lambda name: types.SimpleNamespace(CodeGenerationProblem=FakeProblem) if name == "lcb_runner.benchmarks.code_generation" else (types.SimpleNamespace(hf_hub_download=fake_download) if name == "huggingface_hub" else types.SimpleNamespace(__version__=values["datasets_version"]))
            official=LCB.load_official_tasks()
            need(len(official) == 85 and built[0] == "task-000" and built[-1] == "task-084", "official loader did not build rows from the pinned dataset files")
            need(loaded == file_names, "official loader did not read the contract file mapping")
            # A duplicate question_id across files is rejected.
            dupes=Path(data_dir) / "dup.jsonl"
            dupes.write_text(json.dumps({"question_id": "task-000"}) + "\n", encoding="utf-8")
            LCB.importlib.import_module=lambda name: types.SimpleNamespace(CodeGenerationProblem=FakeProblem) if name == "lcb_runner.benchmarks.code_generation" else (types.SimpleNamespace(hf_hub_download=lambda **kwargs: str(dupes)) if name == "huggingface_hub" else types.SimpleNamespace(__version__=values["datasets_version"]))
            rejects(LCB.load_official_tasks, "distinct")
            # A row without a question_id is rejected.
            blanks=Path(data_dir) / "blank.jsonl"
            blanks.write_text(json.dumps({"other": 1}) + "\n", encoding="utf-8")
            LCB.importlib.import_module=lambda name: types.SimpleNamespace(CodeGenerationProblem=FakeProblem) if name == "lcb_runner.benchmarks.code_generation" else (types.SimpleNamespace(hf_hub_download=lambda **kwargs: str(blanks)) if name == "huggingface_hub" else types.SimpleNamespace(__version__=values["datasets_version"]))
            rejects(LCB.load_official_tasks, "question_id")
        LCB.subprocess.run=lambda *args, **kwargs: subprocess.CompletedProcess(args, 0, values["livecodebench_revision"]+"\n", "")
        LCB.validate_environment(Path("/safe/lcb"))
        LCB.importlib.import_module=lambda name: types.SimpleNamespace(__version__="5.0.0")
        rejects(lambda: LCB.validate_environment(Path("/safe/lcb")), "datasets version")
        LCB.importlib.import_module=lambda name: (_ for _ in ()).throw(ImportError("missing"))
        rejects(LCB.load_official_tasks, "unavailable before inference")
    finally:
        LCB.importlib.import_module, LCB.subprocess.run = original_import, original_run
    formatter_calls=[]
    call_based_task=types.SimpleNamespace(question_content="question-marker", starter_code="def call_marker(value):", question_id="call-based-marker")
    def format_prompt_generation(task, style):
        formatter_calls.append((task, style))
        need(task is call_based_task and bool(task.starter_code), "official formatter did not receive call-based starter-code task")
        return [{"role":"system", "content":"system-marker"}, {"role":"user", "content":"starter-code-marker"}]
    try:
        LCB.importlib.import_module=lambda name: types.SimpleNamespace(format_prompt_generation=format_prompt_generation) if name == "lcb_runner.prompts.code_generation" else types.SimpleNamespace(LMStyle=types.SimpleNamespace(OpenAIChat="official-openai-chat"))
        messages=LCB.official_prompt_messages(call_based_task)
        need(messages[-1]["content"] == "starter-code-marker" and formatter_calls == [(call_based_task, "official-openai-chat")], "pinned official formatter path did not preserve starter-code context")
        with tempfile.TemporaryDirectory() as directory:
            command=Path(directory) / "runner"
            command.write_text("#!/bin/sh\nprintf generated\n", encoding="utf-8")
            command.chmod(0o700)
            received=[]
            original_run=LCB.subprocess.run
            LCB.subprocess.run=lambda args, **kwargs: received.append(kwargs["input"]) or subprocess.CompletedProcess(args, 0, "generated", "")
            try: need(LCB.protected_runner(command)(call_based_task) == "generated", "protected runner did not return generation")
            finally: LCB.subprocess.run=original_run
        need(json.loads(received[0]) == messages, "runner did not receive official OpenAI chat prompt messages")
        LCB.importlib.import_module=lambda name: types.SimpleNamespace(format_prompt_generation=lambda task, style: []) if name == "lcb_runner.prompts.code_generation" else types.SimpleNamespace(LMStyle=types.SimpleNamespace(OpenAIChat="official-openai-chat"))
        rejects(lambda: LCB.official_prompt_messages(call_based_task), "invalid OpenAI chat messages")
    finally:
        LCB.importlib.import_module=original_import
    calls=[]
    result=LCB.aggregate(first, lambda task: calls.append(LCB.task_id(task)) or "generated", lambda task, response: response == "generated")
    need(len(calls) == 40 and result == {"status":"completed", "release_version":"release_v6", "selected":40, "completed":40, "scored":40, "errors":0, "pass_at_1":1.0}, "40-task completed/scored aggregate changed")
    rejects(lambda: LCB.aggregate(first, lambda task: "", lambda task, response: True), "did not complete")
    rendered=str(result); need("task-" not in rendered and "generated" not in rendered, "aggregate leaked task or response content")

    # Pin the official return contract: [metrics, results, metadata].  For one
    # input and one generation, its results map contains one list of one
    # per-test result list.  The adapter must score all 40 inputs without
    # retaining the raw result map or metadata in the aggregate.
    original_import = LCB.importlib.import_module
    scorer_calls=[]
    def codegen_metrics(samples, generations, **kwargs):
        scorer_calls.append((samples, generations, kwargs))
        return [{"pass@1": 1.0, "detail": {"pass@1": {0: 1.0}}}, {0: [[1, True]]}, ["private scorer metadata"]]
    def extract_instance_results(results):
        need(results == {0: [[1, True]]}, "adapter did not use official results entry")
        return [[all(value > 0 for value in results[0][0])]]
    try:
        LCB.importlib.import_module=lambda name: types.SimpleNamespace(codegen_metrics=codegen_metrics, extract_instance_results=extract_instance_results) if name == "lcb_runner.evaluation" else original_import(name)
        scored_tasks=[PinnedScorerTask(question_id=f"fixture-{i:03d}") for i in range(40)]
        scored_result=LCB.aggregate(scored_tasks, lambda task: "generated", LCB.official_scorer())
        need(len(scorer_calls) == 40 and scored_result == {"status":"completed", "release_version":"release_v6", "selected":40, "completed":40, "scored":40, "errors":0, "pass_at_1":1.0}, "pinned official scorer shape did not score all 40 tasks")
        need("private scorer metadata" not in str(scored_result), "aggregate leaked scorer metadata")
        LCB.importlib.import_module=lambda name: types.SimpleNamespace(codegen_metrics=lambda *args, **kwargs: [{}, {0: [[True]]}], extract_instance_results=extract_instance_results) if name == "lcb_runner.evaluation" else original_import(name)
        rejects(lambda: LCB.official_scorer()(scored_tasks[0], "generated"), "invalid result shape")
    finally:
        LCB.importlib.import_module = original_import
    print("livecodebench evaluator tests passed")
    return 0

if __name__ == "__main__": raise SystemExit(main())
