# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Summarize a local-router-pi-smoke run into scalar evidence (Metrum AI)."""
import json, re, sqlite3, sys
src, db, model, out = sys.argv[1:5]
t = open(src).read()
j = json.loads(t[t.index('{'):])
c = sqlite3.connect(db)
turns = []
for i, r in enumerate(dict.fromkeys(re.findall(r'req_[0-9a-f]{32}', t))):
    p = c.execute('select outcome,class_label,duration_ms from request_policy_executions where request_id=?', (r,)).fetchone()
    u = c.execute('select status,target_provider,target_model from request_usage where request_id=?', (r,)).fetchone()
    turns.append({"seq": i + 1, "policy_outcome": p[0], "class_label": p[1], "policy_ms": p[2],
                  "status": u[0], "served": f"{u[1]}/{u[2]}"})
res = {"schema": "metrum.d3-live.pi-continuation.v1", "d3_model": model,
       "command": "make local-router-pi-smoke LOCAL_ROUTER_OUT=tmp/test-d3 LOCAL_ROUTER_LISTEN=127.0.0.1:18301 LOCAL_ROUTER_GROUP=d3-auto < /dev/null",
       "ok": j["ok"], "failures": j["failures"], "pi_new_rows": j["pi"].get("new_rows_during_pi"), "pi_exit_code": j["pi"].get("exit_code"), "pi_tools": j["pi"].get("tools"), "pi_tool_errors": j["pi"].get("tool_errors"), "group_mode": sys.argv[5] if len(sys.argv) > 5 else "enforce, fail_closed",
       "turns": turns, "note": "turns 1..n are pi write/read tool continuation turns, then the smoke's unary and streaming chat"}
open(out, "w").write(json.dumps(res, indent=1) + "\n")
print(json.dumps({k: v for k, v in res.items() if k != "turns"}))
