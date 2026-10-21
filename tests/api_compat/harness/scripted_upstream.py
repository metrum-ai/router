# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Scripted multi-turn FakeUpstream with sequence validation (ARCH-03)."""

from __future__ import annotations

import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable


AttemptValidator = Callable[[dict[str, Any]], None]
ResponseFactory = Callable[[dict[str, Any]], dict[str, Any] | bytes | list[str] | None]


class ScriptedUpstreamError(AssertionError):
    """Raised when an upstream call violates the scripted scenario."""


class ScriptedScenario:
    """Ordered expected upstream calls. Unexpected calls fail the scenario."""

    def __init__(self) -> None:
        self._steps: list[dict[str, Any]] = []
        self._lock = threading.Lock()
        self._index = 0
        self.failures: list[str] = []

    def expect(
        self,
        *,
        path_suffix: str | None = None,
        path: str | None = None,
        validate: AttemptValidator | None = None,
        response: dict[str, Any] | bytes | list[str] | ResponseFactory | None = None,
        status: int = 200,
        content_type: str | None = None,
        split_sse_bytes: int | None = None,
        gate: threading.Event | None = None,
        gate_after_frames: int | None = None,
        close_socket: bool = False,
        delay_s: float = 0.0,
    ) -> ScriptedScenario:
        self._steps.append(
            {
                "path_suffix": path_suffix,
                "path": path,
                "validate": validate,
                "response": response,
                "status": status,
                "content_type": content_type,
                "split_sse_bytes": split_sse_bytes,
                "gate": gate,
                "gate_after_frames": gate_after_frames,
                "close_socket": close_socket,
                "delay_s": delay_s,
            }
        )
        return self

    def reset(self) -> None:
        with self._lock:
            self._index = 0
            self.failures.clear()

    def next_step(self, call: dict[str, Any]) -> dict[str, Any] | None:
        with self._lock:
            if self._index >= len(self._steps):
                msg = f"unexpected upstream call after script exhausted: {call['path']}"
                self.failures.append(msg)
                raise ScriptedUpstreamError(msg)
            step = self._steps[self._index]
            self._index += 1
        expected_path = step.get("path")
        suffix = step.get("path_suffix")
        if expected_path is not None and call["path"] != expected_path:
            msg = f"expected path {expected_path!r}, got {call['path']!r}"
            self.failures.append(msg)
            raise ScriptedUpstreamError(msg)
        if suffix is not None and not call["path"].endswith(suffix):
            msg = f"expected path suffix {suffix!r}, got {call['path']!r}"
            self.failures.append(msg)
            raise ScriptedUpstreamError(msg)
        validate = step.get("validate")
        if validate is not None:
            validate(call)
        return step

    def assert_complete(self) -> None:
        with self._lock:
            if self.failures:
                raise ScriptedUpstreamError("; ".join(self.failures))
            if self._index != len(self._steps):
                raise ScriptedUpstreamError(
                    f"script incomplete: consumed {self._index} of {len(self._steps)} steps"
                )


def default_chat_stream_frames(*, include_usage_empty_choices: bool = False) -> list[str]:
    """Default Chat SSE matching historical FakeUpstream, plus optional include_usage shape."""
    if include_usage_empty_choices:
        return [
            'data: {"id":"chatcmpl_synthetic","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"synthetic chat"},"finish_reason":null}]}\n\n',
            'data: {"id":"chatcmpl_synthetic","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}\n\n',
            'data: {"id":"chatcmpl_synthetic","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}\n\n',
            "data: [DONE]\n\n",
        ]
    # Legacy finish+usage-on-choices (existing smoke default).
    return [
        'data: {"id":"chatcmpl_synthetic","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"synthetic chat"},"finish_reason":null}]}\n\n',
        'data: {"id":"chatcmpl_synthetic","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}\n\n',
        "data: [DONE]\n\n",
    ]


def default_messages_stream_frames() -> list[str]:
    return [
        'event: message_start\ndata: {"type":"message_start","message":{"id":"msg_synthetic","type":"message","role":"assistant","model":"synthetic-messages","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}\n\n',
        'event: content_block_start\ndata: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}\n\n',
        'event: content_block_delta\ndata: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"synthetic messages"}}\n\n',
        'event: content_block_stop\ndata: {"type":"content_block_stop","index":0}\n\n',
        'event: message_delta\ndata: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}\n\n',
        'event: message_stop\ndata: {"type":"message_stop"}\n\n',
    ]


def default_responses_stream_frames(*, include_done: bool = False) -> list[str]:
    """Default Responses SSE ending at response.completed (no Chat [DONE] by default)."""
    frames = [
        'event: response.created\ndata: {"type":"response.created","sequence_number":1,"response":{"id":"resp_synthetic","object":"response","status":"in_progress","model":"synthetic-responses","output":[]}}\n\n',
        'event: response.in_progress\ndata: {"type":"response.in_progress","sequence_number":2,"response":{"id":"resp_synthetic","object":"response","status":"in_progress","model":"synthetic-responses","output":[]}}\n\n',
        'event: response.output_item.added\ndata: {"type":"response.output_item.added","sequence_number":3,"output_index":0,"item":{"id":"msg_synthetic","type":"message","status":"in_progress","role":"assistant","content":[]}}\n\n',
        'event: response.content_part.added\ndata: {"type":"response.content_part.added","sequence_number":4,"item_id":"msg_synthetic","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}\n\n',
        'event: response.output_text.delta\ndata: {"type":"response.output_text.delta","sequence_number":5,"item_id":"msg_synthetic","output_index":0,"content_index":0,"delta":"synthetic responses"}\n\n',
        'event: response.output_text.done\ndata: {"type":"response.output_text.done","sequence_number":6,"item_id":"msg_synthetic","output_index":0,"content_index":0,"text":"synthetic responses"}\n\n',
        'event: response.content_part.done\ndata: {"type":"response.content_part.done","sequence_number":7,"item_id":"msg_synthetic","output_index":0,"content_index":0,"part":{"type":"output_text","text":"synthetic responses"}}\n\n',
        'event: response.output_item.done\ndata: {"type":"response.output_item.done","sequence_number":8,"output_index":0,"item":{"id":"msg_synthetic","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"synthetic responses"}]}}\n\n',
        'event: response.completed\ndata: {"type":"response.completed","sequence_number":9,"response":{"id":"resp_synthetic","object":"response","status":"completed","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}\n\n',
    ]
    if include_done:
        frames.append("data: [DONE]\n\n")
    return frames


class FakeUpstream(BaseHTTPRequestHandler):
    """Backward-compatible synthetic upstream used by the session router fixture.

    Bodies stay in ephemeral class-level memory only (cleared between tests).
    Optional ``scenario`` enables scripted multi-turn validation.
    """

    calls: list[dict[str, Any]] = []
    scenario: ScriptedScenario | None = None
    chat_include_usage_empty_choices: bool = False
    _attempt_counter: int = 0
    _counter_lock = threading.Lock()

    def log_message(self, _format: str, *args: Any) -> None:
        pass

    @classmethod
    def reset_state(cls) -> None:
        cls.calls = []
        cls.scenario = None
        cls.chat_include_usage_empty_choices = False
        cls._attempt_counter = 0

    @classmethod
    def _next_attempt_id(cls) -> str:
        with cls._counter_lock:
            cls._attempt_counter += 1
            return f"attempt-{cls._attempt_counter}"

    def do_POST(self) -> None:
        length = int(self.headers.get("Content-Length", "0"))
        raw_body = self.rfile.read(length) if length else b"{}"
        body = json.loads(raw_body.decode() or "{}")
        # Keep only ephemeral in-memory records for the active test process.
        headers = {key: value for key, value in self.headers.items()}
        attempt_id = self._next_attempt_id()
        call = {
            "path": self.path,
            "body": body,
            "headers": headers,
            "attempt_id": attempt_id,
        }
        self.__class__.calls.append(call)

        scenario = self.__class__.scenario
        if scenario is not None:
            step = scenario.next_step(call)
            assert step is not None
            self._serve_scripted(step, body)
            return

        self._serve_default(body)

    def _serve_scripted(self, step: dict[str, Any], body: dict[str, Any]) -> None:
        if step.get("delay_s"):
            time.sleep(float(step["delay_s"]))
        gate = step.get("gate")
        gate_after = step.get("gate_after_frames")
        # Pre-response gate (existing): block before any bytes.
        # Mid-stream gate: emit gate_after_frames first, then wait.
        if gate is not None and gate_after is None:
            gate.wait(timeout=30)
        if step.get("close_socket"):
            self.close_connection = True
            try:
                self.connection.close()
            except OSError:
                pass
            return

        response = step.get("response")
        if callable(response):
            response = response(body)

        status = int(step.get("status") or 200)
        if response is None:
            # Fall back to default dialect handler for this path.
            self._serve_default(body, status=status)
            return

        if isinstance(response, list):
            if gate is not None and gate_after is not None:
                self._write_sse_gated(
                    response,
                    gate=gate,
                    after_frames=int(gate_after),
                    status=status,
                )
                return
            payload = "".join(response)
            self._write_sse(payload, split_bytes=step.get("split_sse_bytes"), status=status)
            return
        if isinstance(response, (bytes, bytearray)):
            content_type = step.get("content_type") or "application/json"
            self._write_raw(bytes(response), content_type=content_type, status=status)
            return
        if isinstance(response, dict):
            content_type = step.get("content_type") or "application/json"
            raw = json.dumps(response).encode()
            self._write_raw(raw, content_type=content_type, status=status)
            return
        raise ScriptedUpstreamError(f"unsupported scripted response type: {type(response)}")

    def _serve_default(self, body: dict[str, Any], status: int = 200) -> None:
        model = body.get("model", "")
        stream = bool(body.get("stream"))
        if self.path.endswith("/chat/completions"):
            if stream:
                frames = default_chat_stream_frames(
                    include_usage_empty_choices=self.__class__.chat_include_usage_empty_choices
                )
                self._write_sse("".join(frames), status=status)
                return
            message: dict[str, Any] = {"role": "assistant", "content": "synthetic chat"}
            if body.get("tools"):
                message["tool_calls"] = [
                    {
                        "id": "call_synthetic",
                        "type": "function",
                        "function": {"name": "lookup", "arguments": "{}"},
                    }
                ]
            response = {
                "id": "chatcmpl_synthetic",
                "object": "chat.completion",
                "model": body.get("model", "synthetic-chat"),
                "choices": [
                    {
                        "index": 0,
                        "message": message,
                        "finish_reason": "tool_calls" if body.get("tools") else "stop",
                    }
                ],
                "usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
            }
        elif self.path.endswith("/responses"):
            if stream:
                self._write_sse("".join(default_responses_stream_frames()), status=status)
                return
            output: list[dict[str, Any]] = [
                {
                    "type": "message",
                    "role": "assistant",
                    "content": [{"type": "output_text", "text": "synthetic responses"}],
                }
            ]
            if body.get("tools"):
                output = [
                    {
                        "type": "function_call",
                        "id": "fc_synthetic",
                        "call_id": "call_synthetic",
                        "name": "lookup",
                        "arguments": "{}",
                    }
                ]
            response = {
                "id": "resp_synthetic",
                "object": "response",
                "model": model,
                "status": "completed",
                "output": output,
                "output_text": "synthetic responses",
                "usage": {"input_tokens": 3, "output_tokens": 2, "total_tokens": 5},
            }
        elif self.path.endswith("/messages"):
            if stream:
                self._write_sse("".join(default_messages_stream_frames()), status=status)
                return
            content: list[dict[str, Any]] = [{"type": "text", "text": "synthetic messages"}]
            stop_reason = "end_turn"
            if body.get("tools"):
                content = [
                    {
                        "type": "tool_use",
                        "id": "toolu_synthetic",
                        "name": "lookup",
                        "input": {},
                    }
                ]
                stop_reason = "tool_use"
            response = {
                "id": "msg_synthetic",
                "type": "message",
                "role": "assistant",
                "model": model,
                "stop_reason": stop_reason,
                "content": content,
                "usage": {"input_tokens": 3, "output_tokens": 2},
            }
        else:
            self.send_error(404)
            return
        raw = json.dumps(response).encode()
        self._write_raw(raw, content_type="application/json", status=status)

    def _write_raw(self, raw: bytes, *, content_type: str, status: int = 200) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def _write_sse_gated(
        self,
        frames: list[str],
        *,
        gate: threading.Event,
        after_frames: int,
        status: int = 200,
    ) -> None:
        """Emit the first N SSE frames, wait on gate, then emit the remainder."""
        if after_frames < 0 or after_frames > len(frames):
            raise ScriptedUpstreamError(
                f"gate_after_frames={after_frames} out of range for {len(frames)} frames"
            )
        first = "".join(frames[:after_frames]).encode()
        rest = "".join(frames[after_frames:]).encode()
        self.send_response(status)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        # Omit Content-Length so the client can observe the first frame before gate release.
        self.end_headers()
        if first:
            self.wfile.write(first)
            self.wfile.flush()
        gate.wait(timeout=30)
        if rest:
            self.wfile.write(rest)
            self.wfile.flush()

    def _write_sse(
        self,
        payload: str,
        *,
        split_bytes: int | None = None,
        status: int = 200,
    ) -> None:
        raw = payload.encode()
        self.send_response(status)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        if split_bytes:
            # Omit Content-Length so chunked/incremental delivery is observable.
            self.end_headers()
            view = memoryview(raw)
            for start in range(0, len(raw), split_bytes):
                self.wfile.write(view[start : start + split_bytes])
                self.wfile.flush()
            return
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


def start_fake_upstream(
    handler: type[FakeUpstream] | None = None,
) -> tuple[ThreadingHTTPServer, threading.Thread, str]:
    cls = handler or FakeUpstream
    server = ThreadingHTTPServer(("127.0.0.1", 0), cls)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    url = f"http://127.0.0.1:{server.server_port}"
    return server, thread, url
