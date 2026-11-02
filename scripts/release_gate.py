#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Scripted v4.6 release gate.

One command runs the release-candidate checks that need real providers and
writes a single result table with actual cost from the router's usage rows:

  A. pi agent end to end in both API styles (OpenAI Responses and Anthropic
     Messages) against a GPT target and a Claude target, plus direct baselines.
  B. Harbor tasks direct and routed in both styles (needs a container runtime).
  C. The pinned live SDK compatibility suites (`scripts/compat_live.py`).
  D. The live parallel tool-call gate (`scripts/parallel_tool_live.py`).

Safety rules:
  * keys come from the environment only and are never printed or written;
  * every stage is bounded and the whole run stops at --cap (default $15);
  * the router usage rows are the cost source of record; list price, not an
    invoice;
  * a stage that cannot run is reported as skipped with its reason, never as a
    pass.

Usage:
  RELEASE_GATE_CONFIG=/path/to/full-capability.yaml \
  ROUTER_CALLER_TOKEN=... make release-gate
"""
from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import re
import shlex
import shutil
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
TASKS = ("file", "edit", "quote")
LANES = (("openai-responses", "gpt"), ("openai-responses", "claude"), ("anthropic-messages", "gpt"), ("anthropic-messages", "claude"))
DIRECT = (("openai-responses", "gpt"), ("anthropic-messages", "claude"))
API = {"openai-responses": "openai-responses", "anthropic-messages": "anthropic-messages"}
MARKER = "RELEASE-GATE-TOOL-4917"


class GateError(RuntimeError):
    pass


def log(message):
    print(message, flush=True)


def env(name):
    return os.environ.get(name, "").strip()


def sha256(text):
    return hashlib.sha256(text.encode()).hexdigest()


def token_hash(token):
    """pbkdf2-sha3-512 verifier in the router's self-describing form."""
    salt = os.urandom(24)
    key = hashlib.pbkdf2_hmac("sha3_512", token.encode(), salt, 20000, 64)
    enc = lambda raw: base64.urlsafe_b64encode(raw).decode().rstrip("=")  # noqa: E731
    return f"pbkdf2-sha3-512:i=20000:{enc(salt)}:{enc(key)}"


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def wait_ready(process, port, timeout=90):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise GateError("router exited before readiness")
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/readyz", timeout=1) as response:
                if response.status == 200:
                    return
        except (urllib.error.URLError, OSError):
            pass
        time.sleep(0.2)
    raise GateError("router readiness timeout")


def meter(db):
    if not Path(db).is_file():
        return 0.0, 0, 0
    with sqlite3.connect(db, timeout=10) as conn:
        cost, input_tokens, output_tokens = conn.execute(
            "select coalesce(sum(total_cost_usd),0), coalesce(sum(input_tokens),0), coalesce(sum(output_tokens),0) from request_usage"
        ).fetchone()
    return float(cost), int(input_tokens), int(output_tokens)


def usage_rows(db, since):
    if not Path(db).is_file():
        return []
    with sqlite3.connect(db, timeout=10) as conn:
        return conn.execute(
            "select target_provider, target_model, status, input_tokens, output_tokens, total_cost_usd from request_usage where rowid > ? order by rowid",
            (since,),
        ).fetchall()


def last_rowid(db):
    if not Path(db).is_file():
        return 0
    with sqlite3.connect(db, timeout=10) as conn:
        return conn.execute("select coalesce(max(rowid),0) from request_usage").fetchone()[0]


GPT_GROUP = "gpt"
GPT_PINNED_DIALECT = "openai-responses"


def ensure_gpt_responses_pinned(source):
    """Require the GPT group to target the Responses dialect only.

    A Chat-dialect GPT target rejects reasoning-with-tools requests that arrive
    from an Anthropic Messages caller upstream (issue #697), which turns a
    router translation choice into an opaque `upstream-rejected`. The gate
    therefore refuses a GPT group that still lists a non-Responses target and
    names the fix, instead of reporting that upstream rejection as the only
    symptom.
    """
    lines = Path(source).read_text(encoding="utf-8").splitlines()
    header = re.compile(rf"^(\s*){re.escape(GPT_GROUP)}:\s*$")
    start = None
    indent = 0
    for index, line in enumerate(lines):
        match = header.match(line)
        if match:
            start, indent = index + 1, len(match.group(1))
            break
    if start is None:
        raise GateError(f"the config must declare a {GPT_GROUP!r} model group for the gate lanes")
    block = []
    for line in lines[start:]:
        if not line.strip():
            block.append(line)
            continue
        if len(line) - len(line.lstrip()) <= indent:
            break
        block.append(line)

    # Only list items under the group's own `targets:` key are targets; other
    # nested lists (tags, modality lists, tool support) are irrelevant here.
    targets_indent = None
    target_lines = []
    for position, line in enumerate(block):
        match = re.match(r"^(\s*)targets:\s*$", line)
        if match:
            targets_indent = len(match.group(1))
            target_lines = block[position + 1:]
            break
    if targets_indent is None:
        raise GateError(f"the {GPT_GROUP!r} group must declare a targets list")
    trimmed = []
    for line in target_lines:
        if not line.strip():
            trimmed.append(line)
            continue
        line_indent = len(line) - len(line.lstrip())
        is_item = line.strip().startswith("- ")
        if not is_item and line_indent <= targets_indent:
            break
        trimmed.append(line)

    targets, current = [], None
    item_indent = None
    for line in trimmed:
        stripped = line.strip()
        line_indent = len(line) - len(line.lstrip())
        body = stripped
        if stripped.startswith("- "):
            if item_indent is None:
                item_indent = line_indent
            if line_indent != item_indent:
                # A nested list inside a target (tool support, modalities, tags).
                continue
            current = {"provider": "", "dialect": ""}
            targets.append(current)
            body = stripped[2:].strip()
        if current is None:
            continue
        if item_indent is not None and line_indent > item_indent + 2:
            continue
        provider = re.match(r"^provider:\s*(\S+)\s*$", body)
        if provider:
            current["provider"] = provider.group(1)
            continue
        dialect = re.match(r"^dialect:\s*(\S+)\s*$", body)
        if dialect:
            current["dialect"] = dialect.group(1)
    if not targets:
        raise GateError(f"the {GPT_GROUP!r} group must declare at least one target")
    strays = [f"{t['provider'] or '?'}:{t['dialect'] or 'unset'}" for t in targets if t["dialect"] != GPT_PINNED_DIALECT]
    if strays:
        raise GateError(
            f"the {GPT_GROUP!r} group must target only {GPT_PINNED_DIALECT!r}; "
            f"pin or remove these targets: {', '.join(strays)} (issue #697)"
        )
    return len(targets)


def prepare_config(source, workdir, port, db_path):
    text = Path(source).read_text(encoding="utf-8")
    text = re.sub(r"(?m)^(\s*)listen:\s*\S+", lambda m: f"{m.group(1)}listen: 127.0.0.1:{port}", text, count=1)
    text = re.sub(r"(?m)^(\s*)path:\s*\S*usage[^\s]*\.sqlite\s*$", lambda m: f"{m.group(1)}path: {db_path}", text)
    # The release-candidate config guard requires absolute state and log paths,
    # so the gate pins them inside its own output directory.
    workdir_path = Path(workdir)
    text = re.sub(r"(?m)^(\s*)state_path:\s*\S+", lambda m: f"{m.group(1)}state_path: {workdir_path / 'router-state.json'}", text, count=1)
    text = re.sub(r"(?m)^(\s*)path:\s*requests\.jsonl\s*$", lambda m: f"{m.group(1)}path: {workdir_path / 'requests.jsonl'}", text, count=1)
    token = env("ROUTER_CALLER_TOKEN")
    if not token:
        raise GateError("ROUTER_CALLER_TOKEN is required")
    # token_hash lookup requires the shaped rtr_metrum_<...>_<secret> token; the
    # gate accepts any operator token, so it writes the unsalted verifier and
    # leaves that deprecation warning visible in the router log.
    text, replaced = re.subn(r"(?m)^(\s*)token_hash:\s*\S+", lambda m: f"{m.group(1)}token_sha256: {sha256(token)}", text, count=1)
    if not replaced:
        text = re.sub(r"(?m)^(\s*)token_sha256:\s*\S+", lambda m: f"{m.group(1)}token_sha256: {sha256(token)}", text, count=1)
    target = Path(workdir) / "config.yaml"
    target.write_text(text, encoding="utf-8")
    return target


def write_models(agent, api, base_url, model_id, window=16384, max_tokens=1024):
    agent.mkdir(parents=True, exist_ok=True)
    models = {
        "providers": {
            "gate": {
                "baseUrl": base_url,
                "api": api,
                "apiKey": "$RELEASE_GATE_TOKEN",
                "models": [
                    {
                        "id": model_id,
                        "reasoning": False,
                        "input": ["text"],
                        "contextWindow": window,
                        "maxTokens": max_tokens,
                        "cost": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
                    }
                ],
            }
        }
    }
    (agent / "models.json").write_text(json.dumps(models), encoding="utf-8")


def run_pi(workdir, prompt, tools, token, model_id, timeout=240):
    args = [
        shutil.which("pi") or "/opt/nvm/current/bin/pi",
        "--provider", "gate",
        "--model", model_id,
        "--mode", "json",
        "--tools", tools,
        "--no-extensions", "--no-mcp", "--no-context-files", "--no-skills",
        "--no-prompt-templates", "--no-themes", "--no-session", "--no-approve",
        "--offline", "--thinking", "off",
        prompt,
    ]
    keep = {k: os.environ[k] for k in ("PATH", "LD_LIBRARY_PATH", "LANG") if k in os.environ}
    keep.update(
        HOME=str(workdir),
        PI_CODING_AGENT_DIR=str(workdir / "agent"),
        RELEASE_GATE_TOKEN=token,
        DO_NOT_TRACK="1",
    )
    started = time.monotonic()
    try:
        done = subprocess.run(args, cwd=workdir, env=keep, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        return {"result": "timeout", "seconds": round(time.monotonic() - started, 1)}, []
    events = []
    for line in done.stdout.splitlines():
        try:
            events.append(json.loads(line))
        except ValueError:
            continue
    (workdir / "pi.json").write_text(json.dumps({"stdout": done.stdout, "stderr": done.stderr, "exit": done.returncode}, indent=2))
    return {"exit": done.returncode, "seconds": round(time.monotonic() - started, 1)}, events


def tool_calls(events, name):
    for event in events:
        if event.get("type") == "tool_execution_start" and name in json.dumps(event):
            return True
    return False


def final_text(events):
    text = ""
    for event in events:
        if event.get("type") == "message_end" and event.get("message", {}).get("role") == "assistant":
            for part in event["message"].get("content", []):
                if part.get("type") == "text":
                    text += part.get("text", "")
    return text


def task_prompt(task, workdir):
    if task == "file":
        return "Create result.txt containing exactly HELLO-GATE and a trailing newline, then read it back and report done."
    if task == "edit":
        (workdir / "calc.py").write_text("def add(a, b):\n    return a - b\n")
        (workdir / "test_calc.py").write_text(
            "import unittest\nfrom calc import add\n\nclass T(unittest.TestCase):\n    def test_add(self):\n        self.assertEqual(add(2, 3), 5)\n"
        )
        return "Fix calc.py so add adds. Run python3 -m unittest test_calc.py and confirm it passes."
    (workdir / "quote.txt").write_text(MARKER + "\n")

    return "Read quote.txt with the read tool, then quote its exact contents in your final answer."


def verify(task, workdir, events):
    if task == "file":
        target = workdir / "result.txt"
        ok = target.is_file() and target.read_bytes() == b"HELLO-GATE\n"
        return ok and tool_calls(events, "write") and tool_calls(events, "read"), None
    if task == "edit":
        check = subprocess.run([sys.executable, "-m", "unittest", "test_calc.py"], cwd=workdir, capture_output=True, timeout=60)
        return check.returncode == 0 and tool_calls(events, "edit") and tool_calls(events, "bash"), check.returncode
    return MARKER in final_text(events) and tool_calls(events, "read"), None


def stage_pi(outdir, port, db, cap_state):
    rows = []
    base_routed = (f"http://127.0.0.1:{port}/v1", f"http://127.0.0.1:{port}")
    direct_base = {
        "openai-responses": ("https://api.openai.com/v1", "gpt"),
        "anthropic-messages": ("https://api.anthropic.com", "claude"),
    }
    lanes = [("routed", api, group) for api, group in LANES] + [("direct", api, group) for api, group in DIRECT]
    for mode, api, group in lanes:
        model = group
        for task in TASKS:
            label = f"pi-{mode}-{api}-{group}-{task}"
            workdir = outdir / label
            workdir.mkdir(parents=True, exist_ok=True)
            if mode == "routed":
                base = base_routed[0] if api == "openai-responses" else base_routed[1]
                token = env("ROUTER_CALLER_TOKEN")
            else:
                base, real_group = direct_base[api]
                model = os.environ.get("RELEASE_GATE_DIRECT_" + real_group.upper(), "gpt-6-luna" if real_group == "gpt" else "claude-haiku-4-5-20251001")
                token = env("OPENAI_API_KEY") if api == "openai-responses" else env("ANTHROPIC_API_KEY")
            write_models(workdir / "agent", API[api], base, model)
            prompt = task_prompt(task, workdir)
            tools = {"file": "write,read", "edit": "read,edit,bash", "quote": "read"}[task]
            before = last_rowid(db)
            result, events = run_pi(workdir, prompt, tools, token, model)
            cost = 0.0
            if mode == "routed":
                cost_rows = usage_rows(db, before)
                cost = sum(r[5] or 0 for r in cost_rows)
            ok, check = verify(task, workdir, events) if events else (False, None)
            rows.append(
                {
                    "stage": "A",
                    "lane": f"{mode}-{api}-{group}",
                    "task": task,
                    "result": "pass" if ok and result.get("exit") == 0 and result.get("result") != "timeout" else "fail",
                    "exit": result.get("exit"),
                    "independent_test_exit": check,
                    "cost_usd": round(cost, 8),
                    "seconds": result.get("seconds"),
                }
            )
            log(f"  {rows[-1]['lane']} {task}: {rows[-1]['result']} (${cost:.6f})")
            cap_state["spent"] += cost
            if cap_state["spent"] > cap_state["cap"]:
                raise GateError(f"spend cap hit after {cap_state['spent']:.4f}")
    return rows


HARBOR_TASK_DEFAULT = "aider/polyglot_python_two-bucket"
HARBOR_AGENTS_DEFAULT = "codex,claude-code"
HARBOR_GROUPS_DEFAULT = "default,fast"


def stage_harbor(outdir, host=None):
    """Stage B: the bounded Harbor promotion matrix (<=4 cells by construction).

    Runs on a Docker host, either this machine or the SSH host in
    RELEASE_GATE_HARBOR_HOST. Missing Docker or missing Harbor inputs report
    `skipped` with the reason; a matrix that runs reports its own verdict and is
    never upgraded to a pass by this gate.
    """
    target = outdir / "harbor"
    target.mkdir(parents=True, exist_ok=True)
    promotion = ROOT / "scripts" / "harbor_promotion.py"
    task = env("HARBOR_PROMOTION_TASK") or HARBOR_TASK_DEFAULT
    common = ["--task", task,
              "--agents", env("HARBOR_PROMOTION_AGENTS") or HARBOR_AGENTS_DEFAULT,
              "--groups", env("HARBOR_PROMOTION_GROUPS") or HARBOR_GROUPS_DEFAULT,
              "--confirm", task,
              "--out", str(target)]
    if host:
        remote_out = env("RELEASE_GATE_HARBOR_REMOTE_OUT") or "~/harbor-out"
        quoted = " ".join(shlex.quote(part) for part in common[:-2] + ["--out", remote_out])
        command = f"cd ~/router && python3 scripts/harbor_promotion.py {quoted}"
        log_path = target / "harbor-remote.log"
        ssh = ["ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"]
        identity = env("RELEASE_GATE_HARBOR_KEY")
        if identity:
            ssh += ["-i", identity]
        ssh += [host, command]
        with log_path.open("w") as logfile:
            code = subprocess.run(ssh, stdout=logfile, stderr=subprocess.STDOUT, text=True).returncode
        return [{
            "stage": "B",
            "lane": f"harbor-direct/routed (both styles) on {host}",
            "task": f"{HARBOR_TASK_DEFAULT}",
            "result": "pass" if code == 0 else "blocked",
            "note": f"remote log: {log_path.name}; a Harbor matrix is bounded evidence, not a certification",
        }]
    if not shutil.which("docker"):
        return [{
            "stage": "B",
            "lane": "harbor-direct/routed (both styles)",
            "task": "5 tasks",
            "result": "skipped",
            "note": "no container runtime and no RELEASE_GATE_HARBOR_HOST; Harbor needs a Docker host",
        }]
    log_path = target / "harbor.log"
    with log_path.open("w") as logfile:
        code = subprocess.run([sys.executable, str(promotion), *common], cwd=ROOT, stdout=logfile, stderr=subprocess.STDOUT, text=True).returncode
    return [{
        "stage": "B",
        "lane": "harbor-direct/routed (both styles)",
        "task": HARBOR_TASK_DEFAULT,
        "result": "pass" if code == 0 else "blocked",
        "note": f"log: {log_path.name}; bounded Harbor matrix, not a certification",
    }]


def stage_compat_live(outdir, config):
    if not env("OPENAI_API_KEY") or not env("ANTHROPIC_API_KEY"):
        return [{"stage": "C", "lane": "live SDK suites", "task": "pinned testers", "result": "skipped", "note": "provider keys missing"}]
    target = outdir / "compat-live"
    # compat_live.py refuses to write anywhere inside the repository, so it stages
    # outside the worktree and the gate copies its sanitized report back in.
    with tempfile.TemporaryDirectory(prefix="release-gate-compat-") as staging:
        envs = dict(os.environ)
        envs.update(COMPAT_LIVE_CONFIG=str(config), COMPAT_LIVE_OUT=str(Path(staging) / "compat-live"), COMPAT_LIVE_ROUTER_SOURCE=str(ROOT))
        log_path = outdir / "compat-live.log"
        with log_path.open("w") as logfile:
            code = subprocess.run([sys.executable, str(ROOT / "scripts" / "compat_live.py")], cwd=ROOT / "scripts", env=envs, stdout=logfile, stderr=subprocess.STDOUT).returncode
        staged = Path(staging) / "compat-live"
        if staged.is_dir():
            target.mkdir(parents=True, exist_ok=True)
            shutil.copytree(staged, target, dirs_exist_ok=True)
    cost = 0.0
    for line in log_path.read_text(errors="replace").splitlines():
        match = re.search(r"after shutdown and audit drain: \$([0-9.]+)", line)
        if match:
            cost = float(match.group(1))
    return [
        {
            "stage": "C",
            "lane": "live SDK suites (openai/anthropic testers x gpt/claude)",
            "task": "pinned core subset",
            "result": "pass" if code == 0 else "fail",
            "exit": code,
            "cost_usd": round(cost, 6),
        }
    ]


def stage_parallel(outdir, cap):
    output = outdir / "parallel-tool-live.json"
    # The repo Makefile runs the parallel gate through uv because it needs
    # PyYAML; mirror that instead of relying on the ambient interpreter.
    command = ["uv", "run", "--with", "pyyaml", "python3", str(ROOT / "scripts" / "parallel_tool_live.py"), "--out", str(output), "--router-bin", str(outdir / "router"), "--cap", str(cap)]
    if not shutil.which("uv"):
        return [{"stage": "D", "lane": "parallel tool calls direct vs routed", "task": "live gate", "result": "skipped", "note": "uv is required to supply PyYAML"}]
    code = subprocess.run(command, cwd=ROOT).returncode
    cost = 0.0
    if output.is_file():
        report = json.loads(output.read_text())
        cost = float(report.get("list_rate_usage_usd") or 0)
    return [
        {
            "stage": "D",
            "lane": "parallel tool calls direct vs routed",
            "task": "live gate",
            "result": "pass" if code == 0 else "fail",
            "exit": code,
            "cost_usd": round(cost, 6),
        }
    ]


def render(rows, sha, cap, spent, config_source):
    lines = [
        f"Release gate for `{sha}` (cap ${cap:.2f}, list-price cost not invoice)",
        "",
        "| Stage | Lane | Task | Result | Cost USD | Notes |",
        "|---|---|---|---|---|---|",
    ]
    for row in rows:
        notes = row.get("note") or ""
        if row.get("independent_test_exit") is not None:
            notes = f"test exit {row['independent_test_exit']}"
        cost = row.get("cost_usd")
        lines.append(
            f"| {row['stage']} | {row['lane']} | {row['task']} | {row['result']} | "
            f"{'' if cost is None else format(cost, '.6f')} | {notes} |"
        )
    lines += ["", f"Total metered: ${spent:.6f} (list price, not invoice). Config: `{Path(config_source).name}`."]
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--config", default=env("RELEASE_GATE_CONFIG"), help="full-capability router config (required)")
    parser.add_argument("--out", default=str(ROOT / ".out" / "release-gate"))
    parser.add_argument("--cap", type=float, default=15.0)
    parser.add_argument("--stages", default="a,b,c,d")
    parser.add_argument("--allow-dirty", action="store_true")
    args = parser.parse_args()

    if not args.config or not Path(args.config).is_file():
        raise SystemExit("pass --config (or RELEASE_GATE_CONFIG) with a full-capability router config")
    ensure_gpt_responses_pinned(args.config)
    sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    dirty = subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT, text=True).strip()
    if dirty and not args.allow_dirty:
        raise SystemExit("worktree is dirty; the gate must run on an exact release-candidate commit")
    for key in ("ROUTER_CALLER_TOKEN", "OPENAI_API_KEY"):
        if not env(key):
            raise SystemExit(f"{key} must be set in the environment")

    stages = {part.strip() for part in args.stages.lower().split(",") if part.strip()}
    outdir = Path(args.out) / sha[:12]
    outdir.mkdir(parents=True, exist_ok=True)
    rows, spent = [], 0.0
    cap_state = {"cap": args.cap, "spent": 0.0}

    log(f"release gate {sha} -> {outdir}")
    if "b" in stages:
        rows += stage_harbor(outdir, env("RELEASE_GATE_HARBOR_HOST"))

    router = outdir / "router"
    config = None
    process = None
    db = None
    if "a" in stages or "c" in stages:
        subprocess.run(["go", "build", "-o", str(router), "./cmd/metrum-ai-router"], cwd=ROOT, check=True)
        if "a" in stages:
            port = free_port()
            db = outdir / "usage.sqlite"
            config = prepare_config(args.config, outdir, port, db)
            logf = (outdir / "router.log").open("w")
            process = subprocess.Popen([str(router), "--config", str(config)], cwd=outdir, stdout=logf, stderr=subprocess.STDOUT)
            try:
                wait_ready(process, port)
                log("stage A: pi agent end to end")
                rows += stage_pi(outdir, port, db, cap_state)
                metered, input_tokens, output_tokens = meter(db)
                log(f"stage A metered ${metered:.6f} ({input_tokens} in / {output_tokens} out)")
                spent += metered
            finally:
                if process and process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=30)
                    except subprocess.TimeoutExpired:
                        process.kill()
                logf.close()

    if "c" in stages:
        log("stage C: live SDK compatibility suites")
        # The config guard refuses a usage database or quota state that already
        # exists, so stage C always gets its own fresh paths.
        stage_c = outdir / "stage-c"
        stage_c.mkdir(parents=True, exist_ok=True)
        for stale in ("usage.sqlite", "router-state.json", "requests.jsonl"):
            (stage_c / stale).unlink(missing_ok=True)
        config = prepare_config(args.config, stage_c, free_port(), stage_c / "usage.sqlite")
        rows += stage_compat_live(outdir, config)
        spent += sum(row.get("cost_usd") or 0 for row in rows if row["stage"] == "C")

    if "d" in stages:
        log("stage D: live parallel tool gate")
        # scripts/parallel_tool_live.py enforces its own hard ceiling of $5.
        remaining = min(5.0, max(0.0, args.cap - spent))
        rows += stage_parallel(outdir, round(remaining, 2))
        spent += sum(row.get("cost_usd") or 0 for row in rows if row["stage"] == "D")

    table = render(rows, sha, args.cap, spent, args.config)
    (outdir / "RESULT.md").write_text(table, encoding="utf-8")
    (outdir / "results.json").write_text(json.dumps({"sha": sha, "cap": args.cap, "spent_usd": spent, "rows": rows}, indent=2), encoding="utf-8")
    print()
    print(table)
    failed = [row for row in rows if row["result"] in ("fail", "timeout")]
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
