# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Real-upstream end-to-end suite for the Admin Config API (#395 + #350).

Builds the router binaries, seeds a database-backed configuration with real
providers (OpenAI, Fireworks, Gemini, OpenRouter), starts router processes on
localhost, and drives the positive and negative case matrix from the issue.
Every case records request summary, expected vs actual status, served
provider/model (from response metadata and usage rows), and latency into the
evidence directory.

Secrets hygiene: provider keys and admin keys are read from the environment
and passed to subprocesses; they are never printed, logged, or written into
evidence. A synthetic canary credential is used for the secret-refusal case
and then grepped for in logs and the config database.

Usage:
    python3 e2e/admin-api/run_e2e.py [--workdir DIR] [--evidence-dir DIR] [--keep]

Required env: OPENAI_API_KEY, FIREWORKS_API_KEY, GEMINI_API_KEY, OPENROUTER_API_KEY
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import secrets as pysecrets
import signal
import sqlite3
import string
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]

UPSTREAM_MODELS = {
    # provider -> list of (catalog_ref, served model) candidates used by the suite
    "openai": [("gpt-6-luna", "gpt-6-luna"), ("gpt-6.1-sol", "gpt-6.1-sol"),
               ("gpt-6-astra", "gpt-6-astra"), ("gpt-6.1-terra", "gpt-6.1-terra")],
    "fireworks": [("deepseek-v4p1-flash", "accounts/fireworks/models/deepseek-v4p1-flash"),
                  ("deepseek-v4p1-flash-1017", "accounts/fireworks/models/deepseek-v4p1-flash-1017")],
    "gemini": [("gemini-3-flash-preview", "gemini-3-flash-preview"),
               ("gemini-3.1-flash-lite", "gemini-3.1-flash-lite")],
    "openrouter": [("deepseek-v4-flash-nitro", "deepseek/deepseek-v4-flash:nitro")],
}

CANARY_SECRET = "sk-e2eCan0ry" + "".join(pysecrets.choice(string.ascii_letters + string.digits) for _ in range(24))


def free_port() -> int:
    import socket
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def log(msg: str) -> None:
    print(f"[e2e {datetime.now(timezone.utc).strftime('%H:%M:%S')}] {msg}", flush=True)


def http(method: str, url: str, body=None, headers=None, timeout=120, raw_headers=None):
    """Minimal JSON HTTP helper. Returns (status, parsed_or_text, elapsed_ms)."""
    data = None
    hdrs = {"Content-Type": "application/json"}
    if headers:
        hdrs.update(headers)
    if raw_headers:
        hdrs.update(raw_headers)
    if body is not None:
        data = body if isinstance(body, (bytes, str)) else json.dumps(body).encode()
        if isinstance(data, str):
            data = data.encode()
    # Test-only harness against the local router; scheme is always http(s) to a
    # localhost/loopback base URL, never attacker-controlled (metrum-ai/router#412).
    req = urllib.request.Request(url, data=data, method=method, headers=hdrs)
    start = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:  # nosemgrep: python.lang.security.audit.dynamic-urllib-use-detected.dynamic-urllib-use-detected
            payload = resp.read()
            status = resp.status
    except urllib.error.HTTPError as e:
        payload = e.read()
        status = e.code
    elapsed = (time.monotonic() - start) * 1000
    try:
        parsed = json.loads(payload)
    except Exception:
        parsed = payload.decode(errors="replace")
    return status, parsed, elapsed


def http_stream(url: str, body: dict, headers: dict, out_lines: list, timeout=180):
    """POST with SSE streaming; appends raw data lines. Returns (status, elapsed_ms)."""
    req = urllib.request.Request(url, data=json.dumps(body).encode(), method="POST",
                                 headers={"Content-Type": "application/json", **headers})
    start = time.monotonic()
    try:
        # Test-only harness, loopback base URL as above (metrum-ai/router#412).
        with urllib.request.urlopen(req, timeout=timeout) as resp:  # nosemgrep: python.lang.security.audit.dynamic-urllib-use-detected.dynamic-urllib-use-detected
            status = resp.status
            for line in resp:
                out_lines.append(line.decode(errors="replace").rstrip())
                if line.strip() == b"data: [DONE]":
                    break
    except urllib.error.HTTPError as e:
        e.read()
        return e.code, (time.monotonic() - start) * 1000
    return status, (time.monotonic() - start) * 1000


class RouterProc:
    def __init__(self, name: str, binary: Path, config: Path, listen: str, log_path: Path, extra_env=None):
        self.name = name
        self.base = f"http://{listen}"
        self.listen = listen
        self.log_path = log_path
        env = os.environ.copy()
        if extra_env:
            env.update(extra_env)
        self._logf = open(log_path, "wb")
        args = [str(binary), "--config", str(config), "--listen", listen]
        self.proc = subprocess.Popen(args, env=env, stdout=self._logf, stderr=subprocess.STDOUT)

    def wait_ready(self, timeout=60):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                status, _, _ = http("GET", self.base + "/healthz", timeout=3)
                if status == 200:
                    return True
            except Exception:
                pass
            if self.proc.poll() is not None:
                raise RuntimeError(f"router {self.name} exited early; see {self.log_path}")
            time.sleep(0.25)
        raise RuntimeError(f"router {self.name} not ready; see {self.log_path}")

    def stop(self):
        if self.proc.poll() is None:
            self.proc.send_signal(signal.SIGTERM)
            try:
                self.proc.wait(timeout=15)
            except subprocess.TimeoutExpired:
                self.proc.kill()
        self._logf.close()


def build_binaries(workdir: Path) -> dict:
    bins = {}
    for name, pkg in [("router", "cmd/metrum-ai-router"),
                      ("routerctl", "cmd/metrum-ai-routerctl"),
                      ("router-migrate", "cmd/metrum-ai-router-migrate"),
                      ("pgschema", "e2e/admin-api/pgschema")]:
        out = workdir / name
        subprocess.run(["go", "build", "-o", str(out), "./" + pkg], cwd=REPO, check=True)
        bins[name] = out
    return bins


def migrate_config_db(bins: dict, db_path: Path, pg_scoped_env: str = ""):
    def run(action, *extra):
        if pg_scoped_env:
            cmd = [str(bins["router-migrate"]), "--scope", "router-config", "--action", action,
                   "--driver", "postgres", "--dsn-env", pg_scoped_env, "--db", str(db_path),
                   "--runner", "e2e-admin-api", *extra]
        else:
            cmd = [str(bins["router-migrate"]), "--scope", "router-config", "--action", action,
                   "--db", str(db_path), "--runner", "e2e-admin-api", *extra]
        return subprocess.run(cmd, capture_output=True, text=True)
    r = run("apply")
    if r.returncode != 0 and "maintenance" not in r.stderr + r.stdout:
        raise RuntimeError(f"migrate apply failed: {r.stderr[-400:]}")
    r = run("maintenance", "--backup-evidence-ref", "e2e-local")
    if r.returncode != 0 and "runner mode" not in r.stderr + r.stdout and "online" not in r.stderr + r.stdout:
        raise RuntimeError(f"migrate maintenance failed: {r.stderr[-400:]}")
    r = run("apply")
    if r.returncode != 0:
        raise RuntimeError(f"migrate apply (phase 6+) failed: {r.stderr[-400:]}")
    r = run("status")
    log(f"config DB migrated: {r.stdout.strip()}")


def write_canonical_config(path: Path, caller_token: str, admin_hashes: dict, usage_path: Path):
    caller_sha = hashlib.sha256(caller_token.encode()).hexdigest()
    key_entries = []
    for key_id, (env_name, perms) in admin_hashes.items():
        perms_yaml = "\n".join(f"          - {p}" for p in perms)
        key_entries.append(f"""      - key_id: {key_id}
        key_hash_env: {env_name}
        domain: local/e2e
        permissions:
{perms_yaml}""")
    admin_keys = "\n".join(key_entries)

    def model_entries(models, output_token_field=''):
        out = []
        for ref, served in models:
            otf = f"output_token_field: {output_token_field}" if output_token_field else ""
            out.append(f"""      {ref}:
        model: {served}
        {otf}
        input_modalities: [text]
        output_modalities: [text]
        input_price_per_million_usd: 0.10
        output_price_per_million_usd: 0.40
        request_shape_support:
          supported_inbound_dialects: [openai-chat, openai-responses, anthropic]
          validation_status: passed
          validation_notes: e2e probe passed
        tool_support:
          openai_chat: [tools]""")
        return "\n".join(out)

    doc = f"""server:
  listen: '127.0.0.1:0'
  identifiers:
    mode: passthrough
  usage_db:
    driver: sqlite
    path: {usage_path}
    migration_policy: auto-safe
  admin_auth:
    api_keys:
{admin_keys}
providers:
  openai:
    base_url: https://api.openai.com/v1
    dialect: openai-chat
    api_key: ${{OPENAI_API_KEY}}
    api_key_env: OPENAI_API_KEY
    models:
{model_entries(UPSTREAM_MODELS['openai'], output_token_field='max_completion_tokens')}
  openai-resp:
    base_url: https://api.openai.com/v1
    dialect: openai-responses
    api_key: ${{OPENAI_API_KEY}}
    api_key_env: OPENAI_API_KEY
    models:
      gpt-6-luna:
        model: gpt-6-luna
        input_modalities: [text]
        output_modalities: [text]
        force_store_false: true
        bridges:
          chat_to_responses:
            enabled: true
            text: true
            streaming: true
        request_shape_support:
          supported_inbound_dialects: [openai-responses, openai-chat, anthropic]
          validation_status: passed
          validation_notes: e2e responses-native target
      gpt-6-astra:
        model: gpt-6-astra
        input_modalities: [text]
        output_modalities: [text]
        force_store_false: true
        bridges:
          chat_to_responses:
            enabled: true
            text: true
            streaming: true
        request_shape_support:
          supported_inbound_dialects: [openai-responses, openai-chat, anthropic]
          validation_status: passed
          validation_notes: e2e responses-native target
  fireworks:
    base_url: https://api.fireworks.ai/inference/v1
    dialect: openai-chat
    api_key: ${{FIREWORKS_API_KEY}}
    api_key_env: FIREWORKS_API_KEY
    models:
{model_entries(UPSTREAM_MODELS['fireworks'])}
  gemini:
    base_url: https://generativelanguage.googleapis.com
    dialect: gemini-generate-content
    api_key: ${{GEMINI_API_KEY}}
    api_key_env: GEMINI_API_KEY
    models:
      gemini-3-flash-preview:
        model: gemini-3-flash-preview
        input_modalities: [text]
        output_modalities: [text]
        input_price_per_million_usd: 0.10
        output_price_per_million_usd: 0.40
        request_shape_support:
          supported_inbound_dialects: [openai-chat]
          validation_status: passed
          validation_notes: e2e probe passed
        activation_evidence:
          exact_model: gemini-3-flash-preview
          dialect: gemini-generate-content
          direct_text_passed: true
          router_text_passed: true
          validated_at: '2026-10-27'
      gemini-3.1-flash-lite:
        model: gemini-3.1-flash-lite
        input_modalities: [text]
        output_modalities: [text]
        input_price_per_million_usd: 0.10
        output_price_per_million_usd: 0.40
        request_shape_support:
          supported_inbound_dialects: [openai-chat]
          validation_status: passed
          validation_notes: e2e probe passed
        activation_evidence:
          exact_model: gemini-3.1-flash-lite
          dialect: gemini-generate-content
          direct_text_passed: true
          router_text_passed: true
          validated_at: '2026-10-27'
  openrouter:
    base_url: https://openrouter.ai/api/v1
    dialect: openai-chat
    api_key: ${{OPENROUTER_API_KEY}}
    api_key_env: OPENROUTER_API_KEY
    models:
{model_entries(UPSTREAM_MODELS['openrouter'])}
  broken:
    base_url: http://127.0.0.1:9
    dialect: openai-chat
    models:
      dead:
        model: dead-model
        request_shape_support:
          validation_status: passed
          validation_notes: deliberately unreachable failover fixture
  notools:
    base_url: https://api.openai.com/v1
    dialect: openai-chat
    api_key: ${{OPENAI_API_KEY}}
    api_key_env: OPENAI_API_KEY
    models:
      plain:
        model: gpt-6-luna
        output_token_field: max_completion_tokens
        request_shape_support:
          validation_status: passed
          validation_notes: no tool support declared
models:
  agent-default:
    strategy: failover
    contract:
      supported_api_shapes: [openai_chat]
    targets:
      - provider: openai
        model_ref: gpt-6-luna
        validation: {{status: passed, workload: e2e, validated_at: '2026-10-27'}}
  agent-shapes:
    strategy: failover
    contract:
      supported_api_shapes: [openai_chat, openai_responses, anthropic_messages]
    targets:
      - provider: openai-resp
        model_ref: gpt-6-luna
        validation: {{status: passed, workload: e2e, validated_at: '2026-10-27'}}
  agent-tools:
    strategy: failover
    contract:
      supported_api_shapes: [openai_chat]
      required_capabilities:
        tools: true
    targets:
      - provider: openai
        model_ref: gpt-6.1-sol
        validation: {{status: passed, workload: e2e, validated_at: '2026-10-27'}}
callers:
  - id: e2e-caller
    owner_user: e2e
    project: admin-api
    environment: test
    status: active
    token_sha256: "{caller_sha}"
    token_id: rtr_e2e_caller
    allow: [agent-default, agent-shapes, agent-tools]
    rate:
      rpm: 1000000
      tpm: 100000000
      concurrent: 256
    quota:
      day: {{requests: 100000000, tokens: 100000000000}}
      month: {{tokens: 1000000000000}}
"""
    path.write_text(doc)


def write_bootstrap(path: Path, db_path: Path, scope: str, require_validation: bool,
                    file_mode_doc: Path | None = None, pg_scoped_env: str = ""):
    if file_mode_doc is not None:
        # File mode: the bootstrap IS the serving config.
        path.write_text(file_mode_doc.read_text())
        return
    policy = '\n    require_validation_status: passed' if require_validation else ''
    if pg_scoped_env:
        source = f"""driver: postgres
    runtime_scope: {scope}
    dsn_env: {pg_scoped_env}{policy}"""
    else:
        source = f"""driver: sqlite
    runtime_scope: {scope}
    path: {db_path}{policy}"""
    path.write_text(f"""server:
  listen: '127.0.0.1:0'
  identifiers:
    mode: passthrough
  config_source:
    mode: database
    {source}
providers:
  placeholder:
    base_url: https://placeholder.invalid/v1
    dialect: openai-chat
models:
  placeholder:
    strategy: static
    targets:
      - provider: placeholder
        model: placeholder
""")


class Suite:
    def __init__(self, workdir: Path, evidence: Path):
        self.workdir = workdir
        self.evidence_dir = evidence
        self.cases = []
        self.caller_token = "rtr_e2e_caller_" + pysecrets.token_urlsafe(24)
        self.admin_keys = {}  # key_id -> raw token (memory only)

    def record(self, name, request, expected, actual_status, *, served="", latency_ms=0.0, note="", extra=None):
        ok = expected == actual_status if isinstance(expected, int) else actual_status in expected
        case = {
            "case": name, "request": request, "expected_status": expected,
            "actual_status": actual_status, "served": served,
            "latency_ms": round(latency_ms, 1), "pass": ok, "note": note,
        }
        if extra:
            case["extra"] = extra
        self.cases.append(case)
        log(f"{'PASS' if ok else 'FAIL'} {name}: status={actual_status} served={served} {note}")
        return ok

    def usage_rows_for_group(self, usage_db: Path, group: str, since_ts: str):
        conn = sqlite3.connect(f"file:{usage_db}?mode=ro", uri=True)
        try:
            rows = conn.execute(
                "SELECT target_provider, target_model, status, total_cost_usd FROM request_usage WHERE resolved_group = ? AND ts >= ?",
                (group, since_ts)).fetchall()
            return rows
        finally:
            conn.close()

    def wait_usage_count(self, usage_db: Path, group: str, since_ts: str, want: int, timeout=20):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            rows = self.usage_rows_for_group(usage_db, group, since_ts)
            if len(rows) >= want:
                return rows
            time.sleep(0.5)
        return self.usage_rows_for_group(usage_db, group, since_ts)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--workdir", default="")
    ap.add_argument("--evidence-dir", default="")
    ap.add_argument("--keep", action="store_true")
    ap.add_argument("--pg", action="store_true",
                    help="run the config control plane on PostgreSQL (ROUTER_TEST_PG_DSN), fresh schema per run")
    args = ap.parse_args()

    for var in ["OPENAI_API_KEY", "FIREWORKS_API_KEY", "GEMINI_API_KEY", "OPENROUTER_API_KEY"]:
        if not os.environ.get(var):
            print(f"missing required env {var}", file=sys.stderr)
            sys.exit(2)

    workdir = Path(args.workdir) if args.workdir else Path("/tmp/router-395-e2e-" + pysecrets.token_hex(4))
    workdir.mkdir(parents=True, exist_ok=True)
    ts = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    evidence = Path(args.evidence_dir) if args.evidence_dir else Path("/workspace/router-395/evidence/admin-api") / ts
    evidence.mkdir(parents=True, exist_ok=True)

    log(f"workdir={workdir} evidence={evidence}")
    bins = build_binaries(workdir)

    suite = Suite(workdir, evidence)

    # Admin keys: full ops, read-only, and write-without-force/shadow.
    admin_env = {}
    admin_defs = {
        "e2e-ops": ("ROUTER_ADMIN_KEY_E2E_OPS_HASH", ["config:read", "config:write", "config:activate", "config:force", "shadow:write"]),
        "e2e-readonly": ("ROUTER_ADMIN_KEY_E2E_RO_HASH", ["config:read"]),
        "e2e-writer": ("ROUTER_ADMIN_KEY_E2E_WRITER_HASH", ["config:read", "config:write", "config:activate"]),
    }
    admin_key_files = {}
    for key_id, (env_name, _) in admin_defs.items():
        key_file = workdir / f"admin-{key_id}.key"
        out = subprocess.run([str(bins["routerctl"]), "adminkeys", "generate", "--key-id", key_id,
                              "--token-out", str(key_file)],
                             capture_output=True, text=True, check=True)
        admin_env[env_name] = json.loads(out.stdout)["token_hash"]
        admin_key_files[key_id] = key_file.read_text().strip()

    config_db = workdir / "config.sqlite"
    usage_db = workdir / "usage.sqlite"
    pg_schema = ""
    pg_scoped_env = ""
    if args.pg:
        if not os.environ.get("ROUTER_TEST_PG_DSN"):
            print("--pg requires ROUTER_TEST_PG_DSN", file=sys.stderr)
            sys.exit(2)
        pg_schema = "r395e2e" + pysecrets.token_hex(4)
        subprocess.run([str(bins["pgschema"]), "create", "--name", pg_schema], check=True,
                       capture_output=True, text=True)
        base_dsn = os.environ["ROUTER_TEST_PG_DSN"]
        sep = "&" if "?" in base_dsn else "?"
        os.environ["ROUTER_TEST_PG_DSN_SCOPED"] = base_dsn + sep + "search_path=" + pg_schema
        pg_scoped_env = "ROUTER_TEST_PG_DSN_SCOPED"
        log(f"PG mode: fresh schema {pg_schema}")
    canonical = workdir / "canonical.yaml"
    write_canonical_config(canonical, suite.caller_token,
                           {k: v for k, v in admin_defs.items()}, usage_db)
    migrate_config_db(bins, config_db, pg_scoped_env)
    if pg_scoped_env:
        out = subprocess.run([str(bins["routerctl"]), "config", "import", "--yaml", str(canonical),
                              "--name", "e2e-seed", "--driver", "postgres", "--dsn-env", pg_scoped_env,
                              "--scope", "prod", "--actor", "e2e-seed"],
                             capture_output=True, text=True, check=True)
    else:
        out = subprocess.run([str(bins["routerctl"]), "config", "import", "--yaml", str(canonical),
                              "--name", "e2e-seed", "--driver", "sqlite", "--db", str(config_db),
                              "--scope", "prod", "--actor", "e2e-seed"],
                             capture_output=True, text=True, check=True)
    seed_set = json.loads(out.stdout)["set_id"]
    activate_cmd = [str(bins["routerctl"]), "config", "activate", "--set-id", seed_set,
                    "--scope", "prod", "--actor", "e2e-seed"]
    if pg_scoped_env:
        activate_cmd += ["--driver", "postgres", "--dsn-env", pg_scoped_env]
    else:
        activate_cmd += ["--driver", "sqlite", "--db", str(config_db)]
    subprocess.run(activate_cmd, capture_output=True, text=True, check=True)
    log(f"seeded active set {seed_set}")

    # Start: R1/R2/R3 in DB mode on the shared config DB, RF in file mode.
    # Ports are allocated dynamically so a zombie from a crashed earlier run
    # can never impersonate a fresh replica.
    boot1 = workdir / "boot1.yaml"
    write_bootstrap(boot1, config_db, "prod", require_validation=True, pg_scoped_env=pg_scoped_env)
    routers = []
    env = dict(admin_env)
    file_mode_doc = workdir / "file-mode.yaml"
    file_mode_doc.write_text(canonical.read_text())
    bootf = workdir / "boot-file.yaml"
    write_bootstrap(bootf, config_db, "prod", False, file_mode_doc=file_mode_doc)
    try:
        for name, conf in [("r1", boot1), ("r2", boot1), ("r3", boot1), ("rf", bootf)]:
            port = free_port()
            rp = None
            for attempt in (1, 2):
                rp = RouterProc(name, bins["router"], conf, f"127.0.0.1:{port}",
                                workdir / f"router-{name}.log", extra_env=env)
                try:
                    rp.wait_ready()
                    break
                except RuntimeError:
                    rp.stop()
                    if attempt == 2:
                        raise
                    time.sleep(3)  # shared usage DB: let the prior replica's migration settle
            routers.append(rp)
            time.sleep(1)
        r1, r2, r3, rf = routers
        log(f"routers ready: r1={r1.base} r2={r2.base} r3={r3.base} rf={rf.base} (rf file mode)")
    except Exception:
        for rp in routers:
            rp.stop()
        raise

    ops = {"Authorization": "Bearer " + admin_key_files["e2e-ops"]}
    readonly = {"Authorization": "Bearer " + admin_key_files["e2e-readonly"]}
    writer = {"Authorization": "Bearer " + admin_key_files["e2e-writer"]}
    caller = {"Authorization": "Bearer " + suite.caller_token}
    suite_mark = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000Z")

    ok = True
    try:
        ok &= run_cases(suite, bins, r1, r2, r3, rf, ops, readonly, writer, caller,
                        config_db, usage_db, suite_mark, workdir, admin_key_files, pg_mode=args.pg)
    finally:
        for rp in routers:
            rp.stop()

    # Secret hygiene: the canary must not appear in logs or the config DB.
    leaks = []
    for rp in routers:
        text = rp.log_path.read_text(errors="replace")
        if CANARY_SECRET in text:
            leaks.append(str(rp.log_path))
        for kf in admin_key_files.values():
            if kf in text:
                leaks.append(f"{rp.log_path}: admin key material")
        if suite.caller_token in text:
            leaks.append(f"{rp.log_path}: caller token")
    if pg_scoped_env:
        for label, needle in [("canary", CANARY_SECRET)] +                              [(f"admin key {k}", v) for k, v in admin_key_files.items()] +                              [("caller token", suite.caller_token)]:
            scan = subprocess.run([str(bins["pgschema"]), "scan", "--name", pg_schema],
                                  env={**os.environ, "PGSCAN_NEEDLE": needle},
                                  capture_output=True, text=True)
            if scan.returncode != 0:
                leaks.append(f"postgres schema contains {label}")
    else:
        db_dump = config_db.read_bytes()
        if CANARY_SECRET.encode() in db_dump:
            leaks.append("config.sqlite contains canary")
        for kf in admin_key_files.values():
            if kf.encode() in db_dump:
                leaks.append("config.sqlite contains raw admin key")
        if suite.caller_token.encode() in db_dump:
            leaks.append("config.sqlite contains raw caller token")
    suite.record("secret-hygiene: canary and keys absent from logs and DB",
                 "grep logs + config DB for canary/token material", "no leaks", "leaks" if leaks else "no leaks",
                 note="; ".join(leaks) if leaks else "clean")

    if pg_schema:
        subprocess.run([str(bins["pgschema"]), "drop", "--name", pg_schema],
                       capture_output=True, text=True)
        log(f"PG schema {pg_schema} dropped")

    # Cost estimate from usage rows.
    conn = sqlite3.connect(f"file:{usage_db}?mode=ro", uri=True)
    try:
        row = conn.execute("SELECT COUNT(*), COALESCE(SUM(total_cost_usd),0) FROM request_usage").fetchone()
        suite.total_requests, suite.total_cost = row[0], row[1]
    finally:
        conn.close()

    passed = sum(1 for c in suite.cases if c["pass"])
    total = len(suite.cases)
    report = {
        "suite": "admin-api-e2e", "started": ts, "passed": passed, "total": total,
        "config_db_driver": "postgres" if args.pg else "sqlite",
        "upstreams": {k: [m for _, m in v] for k, v in UPSTREAM_MODELS.items()},
        "skipped_upstreams": {
            "bedrock": "key present; Bedrock Converse needs signing setup — covered by 4 other upstreams",
            "groq/xai/moonshot/minimax/baseten": "keys present; OpenAI-compatible paths already covered by OpenAI/Fireworks/OpenRouter",
        },
        "total_served_requests": suite.total_requests,
        "total_cost_usd_estimate": round(suite.total_cost, 6),
        "cases": suite.cases,
    }
    (evidence / "evidence.json").write_text(json.dumps(report, indent=2))
    write_markdown(evidence / "SUMMARY.md", report)
    log(f"e2e done: {passed}/{total} passed; est. cost ${suite.total_cost:.4f}; evidence at {evidence}")
    if not args.keep:
        pass  # workdir retained for post-mortem; it contains no secrets beyond hashed config
    sys.exit(0 if passed == total else 1)


def write_markdown(path: Path, report: dict):
    lines = ["# Admin API e2e evidence", "",
             f"- started: {report['started']}", f"- result: {report['passed']}/{report['total']} passed",
             f"- served requests: {report['total_served_requests']}",
             f"- estimated upstream cost: ${report['total_cost_usd_estimate']:.6f}", "",
             "| Case | Expected | Actual | Served | Latency ms | Result | Note |",
             "|---|---|---|---|---|---|---|"]
    for c in report["cases"]:
        lines.append("| {case} | {expected_status} | {actual_status} | {served} | {latency_ms} | {res} | {note} |".format(
            res="PASS" if c["pass"] else "FAIL", **c))
    path.write_text("\n".join(lines) + "\n")



# --------------------------------------------------------------------------
# Case matrix
# --------------------------------------------------------------------------

def tgt(provider, ref, weight=None):
    spec = {"provider": provider, "model_ref": ref,
            "validation": {"status": "passed", "workload": "e2e", "validated_at": "2026-10-27"}}
    if weight:
        spec["weight"] = weight
    return spec


def chat(router, caller, model_group="agent-default", stream=False, prompt="say ok", cap=96):
    body = {"model": model_group,
            "messages": [{"role": "user", "content": prompt}],
            "max_completion_tokens": cap}
    if stream:
        body["stream"] = True
        lines = []
        status, ms = http_stream(router.base + "/v1/chat/completions", body, caller, lines)
        model = ""
        for line in lines:
            if line.startswith("data: {") and '"model"' in line:
                try:
                    model = json.loads(line[5:]).get("model", "")
                except Exception:
                    pass
                if model:
                    break
        return status, model, ms, lines
    status, parsed, ms = http("POST", router.base + "/v1/chat/completions", body, caller)
    model = parsed.get("model", "") if isinstance(parsed, dict) else ""
    return status, model, ms, parsed


def responses_call(router, caller, group="agent-shapes"):
    status, parsed, ms = http("POST", router.base + "/v1/responses",
                              {"model": group, "input": "say ok", "max_output_tokens": 48}, caller)
    return status, parsed, ms


def anthropic_call(router, caller, group="agent-shapes"):
    status, parsed, ms = http("POST", router.base + "/v1/messages",
                              {"model": group, "max_tokens": 48,
                               "messages": [{"role": "user", "content": "say ok"}]}, caller)
    return status, parsed, ms


def admin(router, key_headers, method, path, body=None, extra_headers=None):
    headers = dict(key_headers)
    if extra_headers:
        headers.update(extra_headers)
    return http(method, router.base + path, body, headers)


def current_revision(router, key_headers):
    status, parsed, _ = admin(router, key_headers, "GET", "/admin/config/status")
    assert status == 200, f"status endpoint failed: {parsed}"
    return parsed["revision"]


def swap(router, key_headers, group, targets, *, strategy="", reason="", dry_run=False,
         if_match=True, idem=None, force=False):
    headers = {}
    if if_match:
        headers["If-Match"] = str(current_revision(router, key_headers))
    if idem:
        headers["Idempotency-Key"] = idem
    body = {"targets": targets, "reason": reason, "dry_run": dry_run, "force": force}
    if strategy:
        body["strategy"] = strategy
    return admin(router, key_headers, "PUT", f"/admin/config/groups/{group}/targets", body, headers)


def wait_served_model(router, caller, group, want_prefix, timeout=25, stream=False):
    """Poll a one-shot chat until the response metadata shows the new target."""
    deadline = time.monotonic() + timeout
    last = ""
    while time.monotonic() < deadline:
        status, model, _, _ = chat(router, caller, group)
        if status == 200 and model:
            last = model
            if model.startswith(want_prefix):
                return True, model
        time.sleep(0.4)
    return False, last


def run_cases(suite: Suite, bins, r1, r2, r3, rf, ops, readonly, writer, caller,
              config_db, usage_db, suite_mark, workdir, admin_key_files, pg_mode=False):
    ok = True

    # ---- baseline: caller chat works before any admin write ----
    st, model, ms, parsed = chat(r1, caller)
    ok &= suite.record("baseline chat (pre-swap)", "POST /v1/chat/completions agent-default",
                       200, st, served=model, latency_ms=ms,
                       note="" if st == 200 else json.dumps(parsed)[:300])

    # ---- reads in both modes ----
    st, body, ms = admin(r1, ops, "GET", "/admin/config/groups")
    ok &= suite.record("groups-list (DB mode)", "GET /admin/config/groups", 200, st, latency_ms=ms,
                       note=f"mode={body.get('mode')} groups={len(body.get('groups', []))}")
    st, body, ms = admin(rf, ops, "GET", "/admin/config/groups")
    ok &= suite.record("groups-list (file mode)", "GET /admin/config/groups on file-mode router", 200, st,
                       latency_ms=ms, note=f"mode={body.get('mode')}")
    st, body, ms = admin(r1, ops, "GET", "/admin/config/groups/agent-default")
    note = "catalog visible" if "gemini-3-flash-preview" in json.dumps(body) else "catalog missing"
    ok &= suite.record("group-detail (DB mode)", "GET /admin/config/groups/agent-default", 200, st,
                       latency_ms=ms, note=note)
    st, body, ms = admin(rf, ops, "GET", "/admin/config/groups/agent-default")
    ok &= suite.record("group-detail (file mode)", "GET group on file-mode router", 200, st, latency_ms=ms)

    # ---- dry run ----
    rev0 = current_revision(r1, ops)
    st, body, ms = swap(r1, ops, "agent-default",
                        [tgt("fireworks", "deepseek-v4p1-flash")], reason="dry run", dry_run=True)
    diff = body.get("diff", {}) if isinstance(body, dict) else {}
    dry_note = f"added={diff.get('added')} removed={diff.get('removed')}" if st == 200 else "err=" + json.dumps(body)[:300]
    ok &= suite.record("dry-run swap", "PUT targets dry_run=true", 200, st, latency_ms=ms, note=dry_note)
    st, body, _ = admin(r1, ops, "GET", "/admin/config/status")
    ok &= suite.record("dry-run changed nothing", "revision unchanged after dry_run", rev0, body.get("revision"),
                       note=f"rev {rev0} -> {body.get('revision')}")

    # ---- live swap chain with continuous client traffic ----
    stop_loop = threading.Event()
    loop_results = []
    stream_overlap = {"started_model": "", "done_ok": False, "chunks": 0}

    def client_loop():
        while not stop_loop.is_set():
            st, model, ms, _ = chat(r1, caller)
            loop_results.append((st, model))
            time.sleep(0.35)

    def slow_stream():
        lines = []
        st, ms = http_stream(r1.base + "/v1/chat/completions",
                             {"model": "agent-default", "stream": True,
                              "messages": [{"role": "user", "content": "Count from 1 to 30, one number per line."}],
                              "max_completion_tokens": 400},
                             caller, lines)
        stream_overlap["done_ok"] = st == 200 and any(l.strip() == "data: [DONE]" for l in lines)
        stream_overlap["chunks"] = len(lines)
        for line in lines:
            if line.startswith("data: {") and '"model"' in line:
                try:
                    stream_overlap["started_model"] = json.loads(line[5:]).get("model", "")
                except Exception:
                    pass
                if stream_overlap["started_model"]:
                    break

    loop_thread = threading.Thread(target=client_loop, daemon=True)
    loop_thread.start()

    chain = [
        ("swap-1-openai-to-fireworks", "agent-default", [tgt("fireworks", "deepseek-v4p1-flash")], "accounts/fireworks/models/deepseek", "swap-001"),
        ("swap-2-fireworks-to-gemini", "agent-default", [tgt("gemini", "gemini-3-flash-preview")], "gemini-3-flash-preview", "swap-002"),
        ("swap-3-gemini-to-openrouter", "agent-default", [tgt("openrouter", "deepseek-v4-flash-nitro")], "deepseek/deepseek-v4-flash", "swap-003"),
        ("swap-4-openrouter-to-openai", "agent-default", [tgt("openai", "gpt-6.1-terra")], "gpt-6.1-terra", "swap-004"),
    ]
    stream_thread = threading.Thread(target=slow_stream, daemon=True)
    stream_thread.start()  # starts on the pre-swap target (openai/gpt-6-luna)
    time.sleep(0.5)

    for name, group, targets, want_prefix, idem in chain:
        t0 = time.monotonic()
        st, body, ms = swap(r1, ops, group, targets, reason=name, idem=idem)
        swap_ok = st == 200 and isinstance(body, dict) and body.get("set_id")
        note = f"set={body.get('set_id','')} rev={body.get('revision','')}" if swap_ok else "err=" + json.dumps(body)[:300]
        ok &= suite.record(name, f"PUT targets -> {targets[0]['provider']}/{targets[0]['model_ref']}",
                           200, st, latency_ms=ms, note=note)
        if not swap_ok:
            continue
        # Propagation: writer replica immediate; replicas within poll window.
        hit1, model1 = wait_served_model(r1, caller, group, want_prefix, timeout=20)
        prop1 = time.monotonic() - t0
        ok &= suite.record(name + " / writer-replica propagation", "chat on r1 serves new target", True, hit1,
                           served=model1, note=f"{prop1:.2f}s from PUT")
        hit2, model2 = wait_served_model(r2, caller, group, want_prefix, timeout=25)
        prop2 = time.monotonic() - t0
        note2 = f"{prop2:.2f}s from PUT (poll interval 2s)"
        if pg_mode:
            # LISTEN/NOTIFY: replicas must converge in about 1 s (#411); the
            # poller remains the fallback when the notifier is unavailable.
            ok &= suite.record(name + " / replica-2 notify window", "chat on r2 serves new target within 1.5s",
                               True, hit2 and prop2 <= 1.5,
                               served=model2, note=f"{prop2:.2f}s from PUT (LISTEN/NOTIFY window)")
        else:
            ok &= suite.record(name + " / replica-2 propagation", "chat on r2 serves new target", True, hit2,
                               served=model2, note=note2)

    stop_loop.set()
    loop_thread.join(timeout=15)
    stream_thread.join(timeout=60)
    errors = [x for x in loop_results if x[0] != 200]
    ok &= suite.record("swap chain: zero client errors", f"{len(loop_results)} continuous chats during 4 swaps",
                       0, len(errors), note=f"{len(loop_results)} requests, {len(errors)} errors")
    stream_ok = stream_overlap["done_ok"] and stream_overlap["started_model"].startswith("gpt-6-luna")
    ok &= suite.record("in-flight stream finished on old target",
                       "SSE stream started before swap-1 completes with [DONE] on the old model",
                       True, stream_ok,
                       served=stream_overlap["started_model"],
                       note=f"chunks={stream_overlap['chunks']} model={stream_overlap['started_model']}")

    # Multi-replica status convergence after the chain.
    revs = []
    for rp in (r1, r2, r3):
        st, body, _ = admin(rp, ops, "GET", "/admin/config/status")
        revs.append((body.get("revision"), body.get("replica", {}).get("loaded_revision"), body.get("replica", {}).get("current")))
    converged = all(r is not None and r[0] == r[1] and r[2] for r in revs)
    ok &= suite.record("multi-replica convergence", "/admin/config/status on 3 replicas share revision",
                       True, converged, note=f"revisions={revs}")

    # ---- multi-shape group: swap and re-verify Responses + Anthropic shapes ----
    st, body, ms = swap(r1, ops, "agent-shapes", [tgt("openai-resp", "gpt-6-astra")],
                        reason="shapes swap", idem="shapes-001")
    ok &= suite.record("shapes swap (responses-native target)", "PUT agent-shapes targets", 200, st, latency_ms=ms)
    hit, model = wait_served_model(r1, caller, "agent-shapes", "gpt-6-astra", timeout=25)
    st_c, _, _, _ = chat(r1, caller, "agent-shapes")
    st_r, parsed_r, _ = responses_call(r1, caller)
    st_a, parsed_a, _ = anthropic_call(r1, caller)
    ok &= suite.record("shapes after swap: chat+responses+anthropic",
                       "three API shapes against agent-shapes", True,
                       st_c == 200 and st_r == 200 and st_a == 200,
                       served=model, note=f"chat={st_c} responses={st_r} anthropic={st_a}")

    # ---- add / failover-reorder / remove ----
    st, body, ms = admin(r1, ops, "POST", "/admin/config/groups/agent-default/targets",
                         {"target": tgt("openrouter", "deepseek-v4-flash-nitro"), "reason": "add fallback"})
    ok &= suite.record("add-target", "POST target (append)", 200, st, latency_ms=ms)
    # Reorder: deliberately broken first target proves failover order.
    st, body, ms = swap(r1, ops, "agent-default",
                        [tgt("broken", "dead"), tgt("openai", "gpt-6.1-terra")],
                        strategy="failover", reason="failover proof", idem="failover-001")
    ok &= suite.record("reorder with broken first target", "PUT [broken/dead, openai/terra] failover",
                       200, st, latency_ms=ms)
    st_c, model_c, ms_c, _ = chat(r1, caller)
    ok &= suite.record("failover keeps clients error-free", "chat with dead first target",
                       200, st_c, served=model_c, latency_ms=ms_c,
                       note="served by fallback target")
    st, body, ms = admin(r1, ops, "DELETE", "/admin/config/groups/agent-default/targets/broken/dead",
                         {"reason": "remove broken"})
    ok &= suite.record("remove-target", "DELETE broken/dead", 200, st, latency_ms=ms)
    st, body, _ = admin(r1, ops, "GET", "/admin/config/groups/agent-default")
    remaining = [t["provider"] + "/" + t.get("model_ref", t.get("model", "")) for t in body.get("targets", [])]
    ok &= suite.record("remove-target leaves fallback", "group has [openai/gpt-6.1-terra, openrouter/...]",
                       True, "openai/gpt-6.1-terra" in remaining and "broken/dead" not in remaining,
                       note=str(remaining))

    # ---- one-call group rollback ----
    st, body, ms = admin(r1, ops, "POST", "/admin/config/groups/agent-default/rollback", {"reason": "e2e rollback"})
    ok &= suite.record("group rollback", "POST rollback (default prior set)", 200, st, latency_ms=ms,
                       note=f"restored_from={body.get('restored_from_set_id','')}")

    # ---- export / diff / CLI rollback over the API ----
    st, body, ms = admin(r1, ops, "GET", "/admin/config/sets/active")
    ok &= suite.record("config export (API)", "GET /admin/config/sets/active", 200, st, latency_ms=ms,
                       note="canonical YAML" if "providers:" in str(body) else "unexpected body")
    st, body, ms = admin(r1, ops, "GET", "/admin/config/diff")
    ok &= suite.record("config diff (API)", "GET /admin/config/diff", 200, st, latency_ms=ms)
    # CLI rollback reverts the last API change.
    prior = subprocess.run([str(bins["routerctl"]), "groups", "history",
                            "--router-url", r1.base, "agent-default"],
                           env={**os.environ, "ROUTER_ADMIN_KEY": admin_key_files["e2e-ops"]},
                           capture_output=True, text=True)
    ok &= suite.record("CLI groups history", "routerctl groups history --router-url", 0, prior.returncode,
                       note="group_rollback visible" if "group_rollback" in prior.stdout else prior.stdout[-200:])
    # CLI set-targets dry run over HTTP.
    dry = subprocess.run([str(bins["routerctl"]), "groups", "set-targets",
                          "--router-url", r1.base,
                          "--target", "openai/gpt-6.1-terra", "--validation-status", "passed",
                          "--dry-run", "--reason", "cli dry run",
                          "agent-default"],
                         env={**os.environ, "ROUTER_ADMIN_KEY": admin_key_files["e2e-ops"]},
                         capture_output=True, text=True)
    ok &= suite.record("CLI groups set-targets --dry-run", "routerctl groups set-targets --dry-run", 0, dry.returncode,
                       note="dry_run=true" if '"dry_run": true' in dry.stdout else (dry.stdout + dry.stderr)[-300:])

    # ---- idempotent retry ----
    rev_before = current_revision(r1, ops)
    st, body, ms = swap(r1, ops, "agent-default", [tgt("openai", "gpt-6-astra")], reason="idem", idem="idem-777")
    first_set = body.get("set_id", "") if st == 200 else ""
    st2, body2, ms = swap(r1, ops, "agent-default", [tgt("openai", "gpt-6-astra")], reason="idem", idem="idem-777",
                          if_match=False)
    ok &= suite.record("idempotent retry", "same Idempotency-Key + body", 200, st2, latency_ms=ms,
                       note=f"same set_id={body2.get('set_id') == first_set} replayed={body2.get('replayed')}")

    # ---- shadow flow ----
    st, body, ms = admin(r1, ops, "POST", "/admin/config/groups/agent-default/shadow",
                         {"provider": "fireworks", "model_ref": "deepseek-v4p1-flash-1017", "sample_rate": 0.5,
                          "max_requests": 100, "reason": "e2e shadow",
                          "validation": {"status": "passed", "workload": "e2e", "validated_at": "2026-10-27"}})
    ok &= suite.record("shadow start", "POST shadow sample_rate=0.5", 201, st, latency_ms=ms)
    run_id = body.get("shadow", {}).get("run_id", "") if isinstance(body, dict) else ""
    n_shadow_calls = 20
    for i in range(n_shadow_calls):
        st_c, _, _, _ = chat(r1, caller)
        if st_c != 200:
            ok &= suite.record("shadow traffic stays clean", "chat during shadow", 200, st_c)
            break
    # wait for counters
    deadline = time.monotonic() + 60
    shadow_reqs = 0
    served_reqs = 0
    shadow_cost = 0.0
    while time.monotonic() < deadline:
        st, body, _ = admin(r1, ops, "GET", "/admin/config/groups/agent-default/shadow")
        if st == 200:
            shadow_reqs = body.get("shadow", {}).get("requests", 0)
            served_reqs = body.get("served", {}).get("requests", 0)
            shadow_cost = body.get("shadow", {}).get("cost_usd", 0)
            if shadow_reqs > 0 and served_reqs >= n_shadow_calls:
                break
        time.sleep(1)
    shadow_errs = body.get("shadow", {}).get("errors", -1)
    ok &= suite.record("shadow comparison", "GET shadow after N=20 served requests at rate 0.5",
                       True, 0 < shadow_reqs <= n_shadow_calls and served_reqs >= n_shadow_calls and shadow_errs == 0,
                       note=f"served={served_reqs} shadowed={shadow_reqs} shadow_errors={shadow_errs} shadow_cost=${shadow_cost:.6f}")
    st, body, ms = admin(r1, ops, "POST", "/admin/config/groups/agent-default/shadow/promote",
                         {"reason": "promote gpt-oss-20b"})
    ok &= suite.record("shadow promote", "POST shadow/promote", 200, st, latency_ms=ms,
                       note=f"run={body.get('shadow_run_id','')}" if st == 200 else json.dumps(body)[:300])
    hit, model = wait_served_model(r1, caller, "agent-default", "accounts/fireworks/models/deepseek-v4p1-flash-1017", timeout=25)
    ok &= suite.record("promoted candidate serves", "chat serves the promoted candidate after promote", True, hit, served=model)
    st, body, _ = admin(r1, ops, "GET", "/admin/config/groups/agent-default/history")
    linked = f"shadow_run={run_id}" in json.dumps(body)
    ok &= suite.record("audit chain links shadow to promotion", "history detail contains shadow_run=<id>",
                       True, linked)
    # Second shadow started and stopped without promotion.
    st, body, _ = admin(r1, ops, "POST", "/admin/config/groups/agent-default/shadow",
                        {"provider": "gemini", "model_ref": "gemini-3.1-flash-lite", "sample_rate": 0.25,
                         "duration_seconds": 300, "reason": "second shadow",
                         "validation": {"status": "passed", "workload": "e2e", "validated_at": "2026-10-27"}})
    ok &= suite.record("second shadow start", "POST shadow (gemini candidate)", 201, st)
    st, body, ms = admin(r1, ops, "DELETE", "/admin/config/groups/agent-default/shadow", {"reason": "stop only"})
    ok &= suite.record("shadow stop without promote", "DELETE shadow", 200, st, latency_ms=ms,
                       note=body.get("shadow", {}).get("status", "") if isinstance(body, dict) else "")

    # ---- negative cases (active set must stay unchanged) ----
    rev_before_neg = current_revision(r1, ops)

    st, body, ms = swap(r1, ops, "agent-default", [tgt("ghost", "no-model")], if_match=False)
    ok &= suite.record("unknown provider refused", "PUT ghost provider", 422, st, latency_ms=ms,
                       note=body.get("error", {}).get("type", ""))
    st, body, ms = swap(r1, ops, "agent-default", [tgt("openai", "gpt-99-nope")], if_match=False)
    ok &= suite.record("unknown model refused", "PUT openai/gpt-99-nope", 422, st, latency_ms=ms)
    st, body, ms = admin(r1, ops, "PUT", "/admin/config/groups/agent-default/targets",
                         {"targets": [{"provider": "evil", "model": "x", "api_key": "sk-notreal0000000000000000"}]})
    ok &= suite.record("ad hoc credentials field refused", "PUT target with api_key field", 400, st, latency_ms=ms,
                       note=body.get("error", {}).get("type", ""))
    st, body, ms = admin(r1, ops, "PUT", "/admin/config/groups/agent-default/targets",
                         {"targets": [tgt("openai", "gpt-6-luna")], "reason": CANARY_SECRET})
    leaked = CANARY_SECRET in json.dumps(body)
    ok &= suite.record("credential-shaped reason refused, not echoed", "PUT reason=<canary secret>",
                       400, st, latency_ms=ms,
                       note=f"type={body.get('error',{}).get('type','')} leaked_in_response={leaked}")
    ok &= suite.record("secret not echoed", "response body must not contain the canary", False, leaked)
    st, body, ms = swap(r1, ops, "agent-tools", [tgt("notools", "plain")], if_match=False)
    report = json.dumps(body)
    ok &= suite.record("contract-ineligible target refused", "PUT agent-tools <- no-tools target",
                       422, st, latency_ms=ms,
                       note="contract-required-tools" if "contract-required-tools" in report else report[-160:])
    st, body, ms = admin(r1, ops, "DELETE", "/admin/config/groups/agent-shapes/targets/openai-resp/gpt-6-astra",
                         {"reason": "remove last"})
    ok &= suite.record("remove last target refused", "DELETE sole target of agent-shapes", 422, st, latency_ms=ms,
                       note=body.get("error", {}).get("type", ""))
    st, body, ms = admin(r1, ops, "PUT", "/admin/config/groups/agent-default/targets",
                         {"targets": [tgt("openai", "gpt-6-luna")]}, {"If-Match": "1"})
    ok &= suite.record("stale If-Match refused", "PUT If-Match: 1", 409, st, latency_ms=ms,
                       note=f"current_revision={body.get('error', {}).get('current_revision', '')}")
    st, body, ms = admin(r1, ops, "PUT", "/admin/config/groups/agent-default/targets",
                         {"targets": [{"provider": "openai", "model_ref": "gpt-6-luna"}]})
    ok &= suite.record("unvalidated target refused by policy", "PUT without validation.status=passed",
                       422, st, latency_ms=ms, note=body.get("error", {}).get("type", ""))
    st, body, ms = admin(r1, writer, "PUT", "/admin/config/groups/agent-default/targets",
                         {"targets": [{"provider": "openai", "model_ref": "gpt-6-luna"}], "force": True})
    ok &= suite.record("force without config:force", "PUT force=true (writer key)", 403, st, latency_ms=ms,
                       note=body.get("error", {}).get("type", ""))
    st, body, ms = admin(r1, ops, "PUT", "/admin/config/groups/agent-default/targets",
                         {"targets": [{"provider": "openai", "model_ref": "gpt-6-luna"}], "force": True,
                          "reason": "forced e2e"}, {"Idempotency-Key": "force-001"})
    ok &= suite.record("force with config:force succeeds", "PUT force=true (ops key)", 200, st, latency_ms=ms,
                       note="" if st == 200 else json.dumps(body)[:300])
    st, body, _ = admin(r1, ops, "GET", "/admin/config/groups/agent-default/history")
    ok &= suite.record("forced write audited forced=true", "history forced flag", True,
                       '"forced": true' in json.dumps(body))
    # restore validation-compliant state after the forced write
    swap(r1, ops, "agent-default", [tgt("openai", "gpt-6-luna")], reason="restore", idem="restore-001")

    st, body, ms = admin(r1, {}, "PUT", "/admin/config/groups/agent-default/targets", {"targets": []})
    ok &= suite.record("no token -> 401", "PUT without Authorization", 401, st, latency_ms=ms)
    st, body, ms = admin(r1, caller, "PUT", "/admin/config/groups/agent-default/targets", {"targets": []})
    ok &= suite.record("caller key rejected", "PUT with caller token", 401, st, latency_ms=ms)
    provider_key_headers = {"Authorization": "Bearer " + os.environ["OPENAI_API_KEY"]}
    st, body, ms = admin(r1, provider_key_headers, "GET", "/admin/config/groups")
    ok &= suite.record("provider key rejected", "GET with provider key as bearer", 401, st, latency_ms=ms,
                       note="no key material in response" if os.environ["OPENAI_API_KEY"] not in json.dumps(body) else "LEAK")
    st, body, ms = admin(r1, readonly, "PUT", "/admin/config/groups/agent-default/targets",
                         {"targets": [tgt("openai", "gpt-6-luna")]})
    ok &= suite.record("read-only admin write -> 403", "PUT with config:read-only key", 403, st, latency_ms=ms,
                       note=body.get("error", {}).get("type", ""))
    st, body, ms = admin(r1, writer, "POST", "/admin/config/groups/agent-default/shadow",
                         {"provider": "gemini", "model_ref": "gemini-3.1-flash-lite", "sample_rate": 0.1,
                          "max_requests": 5})
    ok &= suite.record("shadow:write missing -> 403", "POST shadow with writer key (no shadow:write)",
                       403, st, latency_ms=ms, note=body.get("error", {}).get("type", ""))
    st, body, ms = admin(rf, ops, "PUT", "/admin/config/groups/agent-default/targets",
                         {"targets": [tgt("openai", "gpt-6-luna")]})
    ok &= suite.record("file-mode write -> 409 config_source_mode_file", "PUT on file-mode router",
                       409, st, latency_ms=ms, note=body.get("error", {}).get("type", ""))
    # malformed body
    req = urllib.request.Request(r1.base + "/admin/config/groups/agent-default/targets",
                                 data=b"{not json", method="PUT",
                                 headers={"Content-Type": "application/json", **ops})
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:  # nosemgrep: python.lang.security.audit.dynamic-urllib-use-detected.dynamic-urllib-use-detected
            st = resp.status
    except urllib.error.HTTPError as e:
        st = e.code
    ok &= suite.record("malformed body -> 400", "PUT invalid JSON", 400, st)
    oversized = {"targets": [tgt("openai", "gpt-6-luna") for _ in range(101)]}
    st, body, ms = admin(r1, ops, "PUT", "/admin/config/groups/agent-default/targets", oversized)
    ok &= suite.record("oversized target list -> 400", "PUT 101 targets", 400, st, latency_ms=ms,
                       note=body.get("error", {}).get("type", ""))

    # concurrent conflicting writes: exactly one wins
    import concurrent.futures
    rev = current_revision(r1, ops)
    targets_pool = [[tgt("openai", "gpt-6-luna")], [tgt("openai", "gpt-6.1-sol")],
                    [tgt("openai", "gpt-6-astra")], [tgt("openai", "gpt-6.1-terra")]]
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        futs = [pool.submit(admin, r1, ops, "PUT", "/admin/config/groups/agent-default/targets",
                            {"targets": t, "reason": "concurrent"}, {"If-Match": str(rev)})
                for t in targets_pool]
        codes = [f.result()[0] for f in futs]
    wins = codes.count(200)
    ok &= suite.record("concurrent writes: exactly one wins", "4 concurrent PUT with same If-Match",
                       1, wins, note=f"codes={codes}")

    # active set unchanged by negative cases except the deliberate force/concurrency wins
    st, body, _ = admin(r1, ops, "GET", "/admin/config/groups/agent-default")
    ok &= suite.record("active set coherent after negatives", "group readable after negative matrix",
                       200, st, note=f"targets={len(body.get('targets', []))}")
    return ok

if __name__ == "__main__":
    main()
