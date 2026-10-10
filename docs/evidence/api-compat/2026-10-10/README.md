# API compatibility live evidence — 2026-10-10

Dated, operator-run evidence for [issue #94](https://github.com/metrum-ai/router/issues/94)
collected by Metrum AI against a **local, non-production** Metrum AI Router built
from this branch. Every file here is scalar and sanitized: no prompts beyond the
fixed synthetic smoke text, no keys, caller tokens, token hashes, IPs or hostnames.

This is bounded smoke and real-agent evidence, **not** statistical
certification (EVAL-03): each Harbor cell is 3 trials of one task.

## Environment

| Item | Value |
|---|---|
| Router SHA (final) | `c402232` (`fix(router): decode replayed Responses output item ids on ingress`) |
| Router SHA (pi / Claude Code Harbor cells) | `24215aa` (router code unchanged on the Chat and Anthropic paths since; pi continuation re-run on `c402232`) |
| Environment name | `local-dev` (loopback listener, sqlite usage DB, `identifiers.mode: rewrite`) |
| Config source | `scripts/local_pi_instance.py` output plus local overlay groups `live-smoke`, `live-resp-stored`, `live-anthropic`, `live-codex` |
| Routing config fingerprints | recorded per cell in each JSON file (`routing_config_fingerprints`); the config changed between cells as groups were added |
| Harbor | 0.13.2, Docker 29.0.1, task container built from `tests/harbor/tasks/HARBOR-01-read-edit-repair/environment/Dockerfile` |
| Clients | pi `@earendil-works/pi-coding-agent@1.1.0`, Claude Code `2.1.296`, Codex CLI `0.162.0`, live runner `scripts/api_compat_live.py` (stdlib urllib) |
| Upstreams | Fireworks `deepseek-v4p1-flash` (Chat), OpenAI `gpt-5.6-sol` (Responses via Chat→Responses bridge in `big-coder`), OpenAI `gpt-5.4-nano` (Responses), MiniMax `MiniMax-M3` (native Anthropic Messages) |
| Total provider spend (router-recorded) | **$0.72** across 455 router requests, all cells and failed attempts included (`usage-session-summary.json`) |

## Results

| Cell | Outcome | Evidence |
|---|---|---|
| `make api-compat-live` `smoke-nonprod` (Chat, Responses, Messages text) | **passed** twice on `c402232`; each case served natively by its dialect's target; $0.0012–$0.0013 per run | `live-smoke-native.json` |
| Anthropic tool-less SSE through native passthrough | **passed**: `message_start` … `message_stop`, text deltas | `live-anthropic-probes.json` |
| Anthropic tool turn two (forced `tool_use` → `tool_result` → final) | **passed**: second turn `end_turn`, echoes tool result | `live-anthropic-probes.json` |
| RESP-07 `previous_response_id` continuation (native Responses) | **passed**: upstream `resp_` id round-trips; turn 2 recalls turn-1 fact; bogus id → `502 upstream_bad_request`, 1 attempt | `live-resp07-continuation.json` |
| pi tool continuation (AGENTS.md definition of done) | **passed**: `write` → `read` → `DONE <token>`; usage rows `status=200` | `pi-continuation.json` |
| HARBOR-REAL-AGENT · pi × HARBOR-01 (`big-coder`) | **3/3 reward 1.0** | `harbor-real-agent.json` |
| HARBOR-REAL-AGENT · Claude Code × HARBOR-01 (`live-anthropic`) | **3/3 reward 1.0** | `harbor-real-agent.json` |
| HARBOR-REAL-AGENT · Codex × HARBOR-01 (`live-codex`) | **3/3 reward 1.0** on `c402232` | `harbor-real-agent.json` |

Router-mode isolation (AGENT-02): every client was configured only with the
router base URL and a router caller token; usage rows attribute each trial's
requests to the router group and the serving target listed in
`harbor-real-agent.json`.

## Failed attempts retained (EVAL-04)

No failed first attempt is dropped. All are in `harbor-real-agent.json` with a
classification:

1. pi, 1 trial — **infrastructure**: HARBOR-01 `test.sh` ran the verifier from a
   heredoc, so `import verify` failed (`RewardFileNotFoundError`). Fixed in
   `cb9f886`.
2. pi, 3 trials, reward 0 — **infrastructure**: `task.toml` `docker_image`
   override skipped the Dockerfile `COPY workspace/ /app/`; the agent found no
   task files. Fixed in `ec8cfa0`.
3. Codex, 3 trials — **local config**: the `live-codex` target lacked reasoning
   metadata; Codex requests reasoning, so the router returned
   `502 no-eligible-target` before any upstream call (correct router behavior).
4. Codex, 6 trials over two jobs — **router bug**: the native Responses stream
   encodes every output item id under identifier rewrite, but ingress did not
   decode replayed reasoning/message/function_call item ids, so OpenAI rejected
   `input[N].id` (`string_above_max_length`). Fixed in `c402232`.

Two earlier `smoke-nonprod` runs also passed but served `LIVE-RESP-TEXT-01`
through Responses→Anthropic translation on the MiniMax target (see
limitations); they are not counted as native Responses evidence.

## Limitations

- One task (HARBOR-01) per client, 3 trials. HARBOR-02..06 are offline
  verifier/protocol stubs without a Harbor `test.sh`; they were not run live.
- No direct-provider or fixed-model controls were run (EVAL-01); these cells do
  not support promotion.
- The `live-smoke` group mixes dialect-native targets. With no
  `supported_inbound_dialects` on the native Anthropic target, OpenAI Chat and
  Responses callers can be translated onto it by default; the native run
  restricted it to `[anthropic]`. This default is recorded as an open product
  question in the stream worklog.
- `big-coder` Harbor pi traffic mixes a native Chat target and a Chat→Responses
  bridge target; both appear in the usage breakdown.
