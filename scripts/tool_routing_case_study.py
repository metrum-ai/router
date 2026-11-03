# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Run the BFCL tool-call case study through two router arms (#436 slice E).

BFCL v4 data (gorilla repo, pinned commit) is sent through two model groups in
one router: a static baseline that always picks the cheap target, and a
tool_aware group in enforce mode with a stronger escalation target. Each task is
answered from the router's OpenAI Chat surface and scored by BFCL's own AST
checker (imported from bfcl-eval), so the success metric is the official one and
the transport is the router.

Cost comes from the router's usage rows for the case-study caller, never from
list-rate arithmetic, and every failure is written out with its BFCL error type.
"""

from __future__ import annotations

import argparse
import json
import os
import sqlite3
import sys
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

DATA_BASE = "https://raw.githubusercontent.com/ShishirPatil/gorilla/{rev}/berkeley-function-call-leaderboard/bfcl_eval/data"
CATEGORIES = {
    "simple_python": "simple",
    "multiple": "multiple",
    "parallel": "parallel",
}


def load_jsonl(path: Path) -> list[dict]:
    entries = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line:
            entries.append(json.loads(line))
    return entries


def fetch_data(cache: Path, revision: str, category: str) -> tuple[list[dict], list[dict]]:
    cache.mkdir(parents=True, exist_ok=True)
    questions_path = cache / f"BFCL_v4_{category}.json"
    answers_path = cache / f"possible_answer_BFCL_v4_{category}.json"
    for path, folder in ((questions_path, ""), (answers_path, "possible_answer/")):
        if not path.exists():
            url = f"{DATA_BASE.format(rev=revision)}/{folder}{path.name.replace('possible_answer_', '')}"
            with urllib.request.urlopen(url, timeout=60) as response:
                path.write_bytes(response.read())
    questions = load_jsonl(questions_path)
    answers = load_jsonl(answers_path)
    if len(questions) != len(answers):
        raise RuntimeError(f"question/answer count mismatch for {category}")
    return questions, answers


def openai_tools(functions: list[dict]) -> list[dict]:
    """Build OpenAI-format tools, replacing "." in names as BFCL itself does.

    BFCL's own OpenAI handlers rewrite dotted function names to underscores
    because the OpenAI schema rejects ".", and its AST checker maps the names
    back (underscore_to_dot). Skipping the rewrite makes every dotted-name
    category a 400 instead of a model result.
    """
    tools = []
    for function in functions:
        parameters = json.loads(json.dumps(function.get("parameters", {"type": "object", "properties": {}})))
        tools.append({
            "type": "function",
            "function": {
                "name": function["name"].replace(".", "_"),
                "description": function.get("description", ""),
                "parameters": _normalize_schema(parameters),
            },
        })
    return tools


def _normalize_schema(schema: dict) -> dict:
    """Map BFCL's schema dialect onto the OpenAI subset the router forwards."""
    type_map = {"dict": "object", "float": "number", "tuple": "array", "any": "string",
                "integer": "integer", "string": "string", "array": "array", "boolean": "boolean"}
    if not isinstance(schema, dict):
        return schema
    out = {}
    for key, value in schema.items():
        if key == "type" and isinstance(value, str):
            out[key] = type_map.get(value, value)
        elif key == "properties" and isinstance(value, dict):
            out[key] = {name: _normalize_schema(spec) for name, spec in value.items()}
        elif key == "items":
            out[key] = _normalize_schema(value)
        elif key in {"anyOf", "oneOf", "allOf"} and isinstance(value, list):
            out[key] = [_normalize_schema(branch) for branch in value]
        elif key == "optional":
            continue
        else:
            out[key] = value
    return out


def call_router(base_url: str, token: str, group: str, question: list[dict], tools: list[dict],
                timeout: float) -> dict:
    body = {
        "model": group,
        "messages": question,
        "tools": tools,
        "tool_choice": "auto",
        "temperature": 0.001,
        "max_tokens": 512,
        "stream": False,
    }
    request = urllib.request.Request(
        base_url.rstrip("/") + "/v1/chat/completions",
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "Authorization": f"Bearer {token}"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return json.loads(response.read().decode())
    except urllib.error.HTTPError as exc:
        return {"__error__": f"http_{exc.code}", "body": exc.read().decode()[:400]}
    except (urllib.error.URLError, TimeoutError) as exc:
        return {"__error__": f"transport:{type(exc).__name__}"}


def model_output(response: dict) -> tuple[list[dict], str]:
    if "__error__" in response:
        return [], response["__error__"]
    try:
        message = response["choices"][0]["message"]
    except (KeyError, IndexError, TypeError):
        return [], "no_choices"
    calls = message.get("tool_calls") or []
    output = []
    for call in calls:
        name = call.get("function", {}).get("name", "")
        arguments = call.get("function", {}).get("arguments", "")
        try:
            parsed = json.loads(arguments)
        except json.JSONDecodeError:
            return output, "invalid_json"
        output.append({name: parsed})
    if not output:
        return [], "no_tool_call"
    return output, ""


def usage_rows(db_path: Path, token_id: str, since: float, groups: set[str]) -> dict:
    with sqlite3.connect(db_path, timeout=10) as db:
        rows = db.execute(
            "select resolved_group, count(*), coalesce(sum(total_cost_usd),0), "
            "coalesce(sum(input_tokens),0), coalesce(sum(output_tokens),0) "
            "from request_usage where token_id=? group by resolved_group",
            (token_id,),
        ).fetchall()
    out = {}
    for group, count, cost, input_tokens, output_tokens in rows:
        if group in groups:
            out[group] = {"requests": count, "cost_usd": round(cost, 8),
                          "input_tokens": input_tokens, "output_tokens": output_tokens}
    return out


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default=os.environ.get("CASE_STUDY_BASE_URL", "http://127.0.0.1:18136"))
    parser.add_argument("--token", default=os.environ.get("CASE_STUDY_TOKEN", ""))
    parser.add_argument("--static-group", default="bfcl-static")
    parser.add_argument("--tool-aware-group", default="bfcl-tool-aware")
    parser.add_argument("--revision", default="58f57e9124ea981403792dd51e00a6577e621fae")
    parser.add_argument("--categories", default="simple_python,multiple,parallel")
    parser.add_argument("--limit", type=int, default=0, help="per category; 0 means the whole category")
    parser.add_argument("--workers", type=int, default=4)
    parser.add_argument("--cache", type=Path, default=Path("bfcl-data"))
    parser.add_argument("--usage-db", type=Path, default=Path("tmp/case-study/usage.sqlite"))
    parser.add_argument("--token-id", default="rtr_case_study")
    parser.add_argument("--out", type=Path, default=Path("tmp/case-study/results.json"))
    parser.add_argument("--timeout", type=float, default=120.0)
    args = parser.parse_args()
    if not args.token:
        return _fail("set CASE_STUDY_TOKEN")

    from bfcl_eval.constants.enums import Language
    from bfcl_eval.eval_checker.ast_eval.ast_checker import ast_checker
    from bfcl_eval.constants.model_config import MODEL_CONFIG_MAPPING, ModelConfig

    answer_model = "case-study-checker"
    template = MODEL_CONFIG_MAPPING["gpt-4o-mini-2024-07-18-FC"]
    MODEL_CONFIG_MAPPING[answer_model] = ModelConfig(
        model_name=template.model_name,
        display_name=template.display_name,
        url=template.url,
        org=template.org,
        license=template.license,
        model_handler=template.model_handler,
        is_fc_model=template.is_fc_model,
        underscore_to_dot=True,
    )

    categories = [c.strip() for c in args.categories.split(",") if c.strip()]
    tasks = []
    ground_truth = {}
    for category in categories:
        questions, answers = fetch_data(args.cache, args.revision, category)
        if args.limit:
            questions = questions[: args.limit]
            answers = answers[: args.limit]
        for question, answer in zip(questions, answers):
            tasks.append({
                "id": question["id"],
                "category": category,
                "category_label": CATEGORIES.get(category, category),
                "question": question["question"][0],
                "tools": openai_tools(question["function"]),
                "functions": question["function"],
            })
            ground_truth[question["id"]] = answer.get("ground_truth", answer.get("possible_answer"))
    if not tasks:
        return _fail("no tasks loaded")
    print(f"loaded {len(tasks)} tasks from {len(categories)} categories at {args.revision}", flush=True)

    arms = {"static": args.static_group, "tool_aware": args.tool_aware_group}
    started = time.time()
    results: dict[str, dict] = {}
    for arm, group in arms.items():
        rows = []
        with ThreadPoolExecutor(max_workers=args.workers) as pool:
            responses = list(pool.map(
                lambda task: call_router(args.base_url, args.token, group, task["question"], task["tools"], args.timeout),
                tasks,
            ))
        for task, response in zip(tasks, responses):
            output, class_name = model_output(response)
            if class_name:
                verdict = {"valid": False, "error": [class_name], "error_type": class_name}
            else:
                verdict = ast_checker(
                    task["functions"], output, ground_truth[task["id"]],
                    Language.PYTHON, task["category"], answer_model,
                )
            rows.append({
                "id": task["id"],
                "category": task["category"],
                "valid": bool(verdict.get("valid")),
                "error": verdict.get("error"),
                "error_type": verdict.get("error_type"),
                "transport_error": class_name if class_name.startswith(("http_", "transport:")) else None,
            })
            print(f"{arm} {task['id']} {'pass' if verdict.get('valid') else 'FAIL'}", flush=True)
        results[arm] = {"group": group, "rows": rows}

    usage = usage_rows(args.usage_db, args.token_id, started, set(arms.values()))
    report = {
        "task_set": "BFCL v4",
        "revision": args.revision,
        "categories": categories,
        "tasks": len(tasks),
        "limit_per_category": args.limit,
        "arms": {},
        "usage": usage,
        "started_at": started,
        "finished_at": time.time(),
    }
    for arm, payload in results.items():
        rows = payload["rows"]
        passed = sum(1 for row in rows if row["valid"])
        report["arms"][arm] = {
            "group": payload["group"],
            "pass": passed,
            "total": len(rows),
            "by_category": {
                category: {
                    "pass": sum(1 for row in rows if row["category"] == category and row["valid"]),
                    "total": sum(1 for row in rows if row["category"] == category),
                }
                for category in categories
            },
            "failures": [row for row in rows if not row["valid"]],
        }
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({arm: {"pass": data["pass"], "total": data["total"]} for arm, data in report["arms"].items()}), flush=True)
    print(f"usage: {json.dumps(usage)}", flush=True)
    return 0


def _fail(message: str) -> int:
    print(f"case study stopped: {message}", file=sys.stderr)
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
