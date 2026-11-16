// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metrum-ai/router/internal/stream"
)

func TestStreamToolCallHoldPassesTextAndReleasesValidatedCall(t *testing.T) {
	budget := newLineBufferBudget(1<<20, nil)
	hold := newStreamToolCallHold(
		ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 1000, HoldMaxBytes: 1 << 20},
		[]map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}},
		ToolRoutingConfig{}.validationOptions(),
		budget,
	)
	text := stream.Event{Data: []byte(`{"choices":[{"delta":{"content":"working"},"finish_reason":null}]}`)}
	got, err := hold.apply(text)
	if err != nil || len(got) != 1 || !strings.Contains(string(got[0].Data), "working") {
		t.Fatalf("text event was not passed through: events=%v err=%v", got, err)
	}
	first := stream.Event{Data: []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]},"finish_reason":null}]}`)}
	got, err = hold.apply(first)
	if err != nil || len(got) != 0 {
		t.Fatalf("incomplete call escaped hold: events=%v err=%v", got, err)
	}
	if used := budget.Used(); used <= 0 {
		t.Fatalf("tool-call hold did not reserve aggregate bytes: %d", used)
	}
	last := stream.Event{Data: []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"notes.txt\"}"}}]},"finish_reason":null}]}`)}
	got, err = hold.apply(last)
	if err != nil || len(got) != 0 {
		t.Fatalf("incomplete call escaped hold: events=%v err=%v", got, err)
	}
	terminal := stream.Event{Data: []byte(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)}
	got, err = hold.apply(terminal)
	if err != nil || len(got) != 3 {
		t.Fatalf("validated call was not released with terminal event: count=%d err=%v", len(got), err)
	}
	if !strings.Contains(string(got[0].Data), `"arguments":"{\"path\":"`) || !strings.Contains(string(got[2].Data), "tool_calls") {
		t.Fatalf("released tool frames changed: first=%q terminal=%q", got[0].Data, got[2].Data)
	}
	if used := budget.Used(); used <= 0 {
		t.Fatalf("hold released aggregate reservation before output emission: %d", used)
	}
	hold.releaseEmitted()
	if used := budget.Used(); used != 0 {
		t.Fatalf("aggregate budget after emission=%d, want 0", used)
	}
}

func TestStreamToolCallHoldProcessBudgetExhaustionReleasesReservation(t *testing.T) {
	budget := newLineBufferBudget(64, nil)
	hold := newStreamToolCallHold(
		ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 1000, HoldMaxBytes: 1 << 20},
		nil,
		ToolRoutingConfig{}.validationOptions(),
		budget,
	)
	event := stream.Event{Data: []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":null}]}`)}
	if got, err := hold.apply(event); err == nil || len(got) != 0 {
		t.Fatalf("over-budget tool frame was accepted: events=%v err=%v", got, err)
	} else {
		var upstreamErr upstreamError
		if !errors.As(err, &upstreamErr) || upstreamErr.Class != "upstream_stream_buffer_limit" {
			t.Fatalf("unexpected budget error: %v", err)
		}
	}
	if used := budget.Used(); used != 0 {
		t.Fatalf("budget after process-limit rejection=%d, want 0", used)
	}
}

func TestStreamToolCallHoldRecognizesResponsesAndAnthropicEvents(t *testing.T) {
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
	cases := []struct {
		name   string
		events []stream.Event
	}{
		{
			name: "responses",
			events: []stream.Event{
				{Type: "response.output_item.added", Data: []byte(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":""}}`)},
				{Type: "response.function_call_arguments.delta", Data: []byte(`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"path\":"}`)},
				{Type: "response.function_call_arguments.delta", Data: []byte(`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"\"notes.txt\"}"}`)},
				{Type: "response.function_call_arguments.done", Data: []byte(`{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"path\":\"notes.txt\"}"}`)},
				{Type: "response.output_item.done", Data: []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"notes.txt\"}"}}`)},
				{Type: "response.completed", Data: []byte(`{"type":"response.completed","response":{"output":[{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"notes.txt\"}"}]}}`)},
			},
		},
		{
			name: "anthropic",
			events: []stream.Event{
				{Type: "content_block_start", Data: []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"read_file","input":{}}}`)},
				{Type: "content_block_delta", Data: []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"notes.txt\"}"}}`)},
				{Type: "content_block_stop", Data: []byte(`{"type":"content_block_stop","index":0}`)},
				{Type: "message_delta", Data: []byte(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}`)},
				{Type: "message_stop", Data: []byte(`{"type":"message_stop"}`)},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			budget := newLineBufferBudget(1<<20, nil)
			hold := newStreamToolCallHold(ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 1000, HoldMaxBytes: 1 << 20}, tools, ToolRoutingConfig{}.validationOptions(), budget)
			var inspected []IRToolCall
			if tc.name == "responses" {
				hold.inspectors = append(hold.inspectors, streamToolCallInspectorFunc(func(calls []IRToolCall) (toolCallInspectionResult, error) {
					inspected = append([]IRToolCall(nil), calls...)
					return toolCallInspectionResult{Action: toolCallInspectionAllow}, nil
				}))
			}
			var emitted []stream.Event
			for i, event := range tc.events {
				got, err := hold.apply(event)
				if err != nil {
					t.Fatalf("event %d: %v", i, err)
				}
				if tc.name == "responses" && i < len(tc.events)-1 && len(got) != 0 {
					t.Fatalf("Responses tool event %s escaped before response.completed: %v", event.Type, got)
				}
				emitted = append(emitted, got...)
				if tc.name == "anthropic" && event.Type == "message_delta" {
					if len(got) < 4 || got[len(got)-1].Type != "message_delta" {
						t.Fatalf("message_delta did not release held tool frames first: emitted=%v", got)
					}
					if got[0].Type != "content_block_start" || got[1].Type != "content_block_delta" || got[2].Type != "content_block_stop" {
						t.Fatalf("Anthropic held event order changed: %v", got)
					}
				}
			}
			if tc.name == "responses" && (len(inspected) != 1 || inspected[0].Arguments != `{"path":"notes.txt"}`) {
				t.Fatalf("Responses `.delta`/`.done` arguments were not assembled exactly: %+v", inspected)
			}
			if len(emitted) < 2 || budget.Used() == 0 {
				t.Fatalf("validated %s call not held for emission: events=%d budget=%d", tc.name, len(emitted), budget.Used())
			}
			hold.releaseEmitted()
			if used := budget.Used(); used != 0 {
				t.Fatalf("%s budget after emission=%d, want 0", tc.name, used)
			}
		})
	}
}

type streamToolCallInspectorFunc func([]IRToolCall) (toolCallInspectionResult, error)

func (f streamToolCallInspectorFunc) inspectStreamToolCalls(calls []IRToolCall, _ bool) (toolCallInspectionResult, error) {
	return f(calls)
}

func TestStreamToolCallHoldInspectorCanBlockAndRedact(t *testing.T) {
	newHold := func() (*streamToolCallHold, *lineBufferBudget) {
		budget := newLineBufferBudget(1<<20, nil)
		hold := newStreamToolCallHold(ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 1000, HoldMaxBytes: 1 << 20}, nil, toolValidationOptions{}, budget)
		return hold, budget
	}
	tool := stream.Event{Data: []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"secret\":\"private-value\"}"}}]},"finish_reason":null}]}`)}
	terminal := stream.Event{Data: []byte(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)}
	t.Run("block", func(t *testing.T) {
		hold, budget := newHold()
		hold.inspectors = append(hold.inspectors, streamToolCallInspectorFunc(func([]IRToolCall) (toolCallInspectionResult, error) {
			return toolCallInspectionResult{Action: toolCallInspectionBlock}, nil
		}))
		if got, err := hold.apply(tool); err != nil || len(got) != 0 {
			t.Fatalf("tool arguments escaped before inspection: events=%v err=%v", got, err)
		}
		if got, err := hold.apply(terminal); err == nil || len(got) != 0 {
			t.Fatalf("blocking inspector released tool arguments: events=%v err=%v", got, err)
		}
		if used := budget.Used(); used != 0 {
			t.Fatalf("budget after inspector block=%d, want 0", used)
		}
	})
	t.Run("redact and continue chain", func(t *testing.T) {
		hold, budget := newHold()
		var sawRedacted bool
		hold.inspectors = append(hold.inspectors,
			streamToolCallInspectorFunc(func(calls []IRToolCall) (toolCallInspectionResult, error) {
				redacted := append([]IRToolCall(nil), calls...)
				redacted[0].Arguments = `{"secret":"[REDACTED]"}`
				return toolCallInspectionResult{Action: toolCallInspectionRedact, Calls: redacted}, nil
			}),
			streamToolCallInspectorFunc(func(calls []IRToolCall) (toolCallInspectionResult, error) {
				sawRedacted = calls[0].Arguments == `{"secret":"[REDACTED]"}`
				return toolCallInspectionResult{Action: toolCallInspectionAllow}, nil
			}),
		)
		if _, err := hold.apply(tool); err != nil {
			t.Fatal(err)
		}
		got, err := hold.apply(terminal)
		if err != nil || !sawRedacted || len(got) != 2 {
			t.Fatalf("redaction chain did not complete: events=%v sawRedacted=%v err=%v", got, sawRedacted, err)
		}
		if strings.Contains(string(got[0].Data), "private-value") || !strings.Contains(string(got[0].Data), "[REDACTED]") {
			t.Fatalf("redacted arguments not applied to held event: %s", got[0].Data)
		}
		hold.releaseEmitted()
		if used := budget.Used(); used != 0 {
			t.Fatalf("budget after redacted emission=%d, want 0", used)
		}
	})
}

func TestStreamToolCallResultEventsReplaceCallsForEveryDialect(t *testing.T) {
	for _, tc := range []struct {
		dialect  string
		terminal string
		wantText string
	}{
		{"openai-chat", `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`, "Tool call blocked by router policy."},
		{"openai-responses", `{"type":"response.completed","response":{"id":"resp_test","status":"completed","output":[]}}`, `"status":"approval_pending"`},
		{"anthropic", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`, `"status":"approval_pending"`},
	} {
		t.Run(tc.dialect, func(t *testing.T) {
			text := tc.wantText
			if tc.dialect != "openai-chat" {
				text = `{"status":"approval_pending","approval_id":"approval_0123456789abcdef0123456789abcdef"}`
			}
			calls := []IRToolCall{{ID: "call_secret", Name: "read", Arguments: `{"value":"secret_arg"}`, Dialect: tc.dialect}}
			data := toolCallResultEvents(calls, stream.Event{Data: []byte(tc.terminal)}, text)
			if len(data) == 0 {
				t.Fatal("no replacement events emitted")
			}
			var combined strings.Builder
			for _, event := range data {
				combined.Write(event.Frame())
			}
			got := combined.String()
			if !strings.Contains(got, "approval_pending") && !strings.Contains(got, "Tool call blocked") {
				t.Fatalf("replacement result missing: %s", got)
			}
			if strings.Contains(got, "call_secret") || strings.Contains(got, "secret_arg") || strings.Contains(got, "function_call") || strings.Contains(got, "tool_use") {
				t.Fatalf("held call leaked in replacement result: %s", got)
			}
		})
	}
}

type cancelableToolStreamReader struct {
	first []byte
	ctx   context.Context
}

func (r *cancelableToolStreamReader) Read(p []byte) (int, error) {
	if len(r.first) > 0 {
		n := copy(p, r.first)
		r.first = r.first[n:]
		return n, nil
	}
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

type failingToolStreamWriter struct {
	header http.Header
	writes int
}

func (w *failingToolStreamWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *failingToolStreamWriter) WriteHeader(int) {}
func (w *failingToolStreamWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("client disconnected")
}
func (w *failingToolStreamWriter) Flush() {}

func TestStreamToolCallHoldDisconnectAndUpstreamErrorReleaseBudget(t *testing.T) {
	toolFrame := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n"
	terminalFrame := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"
	newRequestContext := func(budget *lineBufferBudget) *requestContext {
		return &requestContext{start: time.Now(), maxSSELineBytes: 1 << 20, lineBufferBudget: budget}
	}
	newHold := func(budget *lineBufferBudget) *streamToolCallHold {
		return newStreamToolCallHold(ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 1000, HoldMaxBytes: 1 << 20}, nil, toolValidationOptions{}, budget)
	}
	t.Run("client disconnect during hold", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		budget := newLineBufferBudget(1<<20, nil)
		rc := newRequestContext(budget)
		rc.streamToolCallHold = newHold(budget)
		reader := &cancelableToolStreamReader{first: []byte(toolFrame), ctx: ctx}
		done := make(chan error, 1)
		go func() {
			_, err := proxyNativeSSE(ctx, httptest.NewRecorder(), reader, "openai-chat", "m", 0, rc)
			done <- err
		}()
		deadline := time.Now().Add(time.Second)
		for budget.Used() == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if budget.Used() == 0 {
			t.Fatal("tool frame did not enter hold")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("stream did not stop after client cancellation")
		}
		if used := budget.Used(); used != 0 {
			t.Fatalf("budget after disconnect during hold=%d, want 0", used)
		}
	})
	t.Run("client disconnect during release", func(t *testing.T) {
		budget := newLineBufferBudget(1<<20, nil)
		rc := newRequestContext(budget)
		rc.streamToolCallHold = newHold(budget)
		writer := &failingToolStreamWriter{}
		_, err := proxyNativeSSE(context.Background(), writer, strings.NewReader(toolFrame+terminalFrame), "openai-chat", "m", 0, rc)
		if err == nil || writer.writes == 0 {
			t.Fatalf("expected downstream disconnect while releasing held frames: writes=%d err=%v", writer.writes, err)
		}
		if used := budget.Used(); used != 0 {
			t.Fatalf("budget after disconnect during release=%d, want 0", used)
		}
	})
	t.Run("upstream error mid-held tool call", func(t *testing.T) {
		budget := newLineBufferBudget(1<<20, nil)
		rc := newRequestContext(budget)
		rc.streamToolCallHold = newHold(budget)
		reader := io.MultiReader(strings.NewReader(toolFrame), errorReader{err: io.ErrUnexpectedEOF})
		writer := httptest.NewRecorder()
		_, err := proxyNativeSSE(context.Background(), writer, reader, "openai-chat", "m", 0, rc)
		if err == nil || strings.Contains(writer.Body.String(), `"arguments"`) {
			t.Fatalf("mid-call upstream failure released held arguments: body=%s err=%v", writer.Body.String(), err)
		}
		if used := budget.Used(); used != 0 {
			t.Fatalf("budget after mid-call upstream failure=%d, want 0", used)
		}
	})
}

func TestStreamToolCallHoldClientDisconnectDuringHoldE2E(t *testing.T) {
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	cfg := toolRoutingConfig(t, upstream.URL, t.TempDir(), ToolRoutingConfig{
		Mode:      "observe",
		Streaming: ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 5000, HoldMaxBytes: 1 << 20},
	})
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	router := httptest.NewServer(svc.Handler())
	defer router.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := strings.Replace(toolRoutingRequestBody, "%s", "agent-tools", 1)
	body = strings.Replace(body, `"stream":false`, `"stream":true`, 1)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, router.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	done := make(chan struct{})
	go func() {
		resp, requestErr := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if requestErr == nil {
			t.Errorf("disconnected request unexpectedly returned a response")
		}
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not send the held tool frame")
	}
	deadline := time.Now().Add(2 * time.Second)
	for svc.lineBufferBudget.Used() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if used := svc.lineBufferBudget.Used(); used == 0 {
		t.Fatal("tool frame did not reserve the process budget")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("client request did not stop after disconnect")
	}
	deadline = time.Now().Add(time.Second)
	for svc.lineBufferBudget.Used() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if used := svc.lineBufferBudget.Used(); used != 0 {
		t.Fatalf("budget after client disconnect during hold=%d, want 0", used)
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func TestStreamToolCallHoldAbsoluteDeadlineBeatsByteTrickle(t *testing.T) {
	const holdTimeout = 120 * time.Millisecond
	pipeReader, pipeWriter := io.Pipe()
	idle := newStreamIdleReader(pipeReader, time.Second)
	defer idle.Close()
	hold := newStreamToolCallHold(ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: int(holdTimeout / time.Millisecond), HoldMaxBytes: 1 << 20}, nil, toolValidationOptions{})
	hold.bindIdleReader(idle, time.Second)
	tool := stream.Event{Data: []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":null}]}`)}
	if got, err := hold.apply(tool); err != nil || len(got) != 0 {
		t.Fatalf("tool frame did not enter hold: events=%v err=%v", got, err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, idle)
		readDone <- err
	}()
	writeDone := make(chan int, 1)
	go func() {
		written := 0
		for i := 0; i < 1000; i++ {
			if _, err := pipeWriter.Write([]byte{'x'}); err != nil {
				break
			}
			written++
			time.Sleep(5 * time.Millisecond)
		}
		writeDone <- written
	}()
	started := time.Now()
	select {
	case err := <-readDone:
		if !errors.Is(err, errStreamToolCallHoldTimeout) {
			t.Fatalf("trickled stream error=%v, want hold timeout", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("byte trickle extended the hold beyond its absolute deadline")
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("absolute deadline fired too late: %s", elapsed)
	}
	select {
	case count := <-writeDone:
		if count == 1000 {
			t.Fatal("trickle writer completed despite hold deadline")
		}
	case <-time.After(time.Second):
		t.Fatal("trickle writer did not unblock")
	}
	_ = pipeWriter.Close()
}

func TestStreamToolCallHoldRejectsInvalidAndOversizedCalls(t *testing.T) {
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
	newHold := func(maxBytes int) *streamToolCallHold {
		return newStreamToolCallHold(ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 1000, HoldMaxBytes: maxBytes}, tools, ToolRoutingConfig{}.validationOptions())
	}
	invalid := newHold(1 << 20)
	call := stream.Event{Data: []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{"}}]},"finish_reason":null}]}`)}
	if got, err := invalid.apply(call); err != nil || len(got) != 0 {
		t.Fatalf("invalid call frame escaped before validation: events=%v err=%v", got, err)
	}
	terminal := stream.Event{Data: []byte(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)}
	if got, err := invalid.apply(terminal); err == nil || len(got) != 0 {
		t.Fatalf("invalid call was released: events=%v err=%v", got, err)
	}
	oversized := newHold(64)
	large := stream.Event{Data: []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"` + strings.Repeat("x", 128) + `"}}]},"finish_reason":null}]}`)}
	got, err := oversized.apply(large)
	var upstreamErr upstreamError
	if !errors.As(err, &upstreamErr) || upstreamErr.Class != "tool_call_hold_too_large" || len(got) != 0 {
		t.Fatalf("oversized call was accepted: events=%v err=%v", got, err)
	}
}

func TestStreamToolCallHoldBoundPerStreamUnderLoad(t *testing.T) {
	const streams, maxBytes = 100, 1024
	const processLimit = 8 << 10
	budget := newLineBufferBudget(processLimit, nil)
	tool := stream.Event{Data: []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"` + strings.Repeat("x", 120) + `"}}]},"finish_reason":null}]}`)}
	var wg sync.WaitGroup
	var rejected atomic.Int32
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hold := newStreamToolCallHold(ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 1000, HoldMaxBytes: maxBytes}, nil, ToolRoutingConfig{}.validationOptions(), budget)
			defer hold.reset()
			for j := 0; j < 100; j++ {
				_, err := hold.apply(tool)
				if err != nil {
					var upstreamErr upstreamError
					if !errors.As(err, &upstreamErr) || (upstreamErr.Class != "tool_call_hold_too_large" && upstreamErr.Class != "upstream_stream_buffer_limit") {
						t.Errorf("stream returned unexpected error: %v", err)
					}
					rejected.Add(1)
					if hold.usedBytes != 0 {
						t.Errorf("oversized stream retained %d held bytes", hold.usedBytes)
					}
					return
				}
				if hold.usedBytes > maxBytes {
					t.Errorf("stream held %d bytes above %d-byte cap", hold.usedBytes, maxBytes)
					return
				}
				if budget.Used() <= 0 || budget.Used() > processLimit {
					t.Errorf("aggregate budget usage after accepted hold=%d, want (0,%d]", budget.Used(), processLimit)
					return
				}
			}
			t.Error("stream exceeded its cap without rejection")
		}()
	}
	wg.Wait()
	if rejected.Load() != streams {
		t.Fatalf("rejected streams=%d, want %d", rejected.Load(), streams)
	}
	if used := budget.Used(); used != 0 {
		t.Fatalf("aggregate budget after concurrent holds=%d, want 0", used)
	}
}

func TestStreamToolCallHoldTimeout(t *testing.T) {
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\"}}]},\"finish_reason\":null}]}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-time.After(250 * time.Millisecond)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := toolRoutingConfig(t, upstream.URL, dir, ToolRoutingConfig{
		Mode:      "observe",
		Streaming: ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 40, HoldMaxBytes: 1 << 20},
	})
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	body := strings.Replace(toolRoutingRequestBody, "%s", "agent-tools", 1)
	body = strings.Replace(body, `"stream":false`, `"stream":true`, 1)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	svc.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusGatewayTimeout || !strings.Contains(recorder.Body.String(), "tool-call-hold-timeout") {
		t.Fatalf("hold timeout status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if used := svc.lineBufferBudget.Used(); used != 0 {
		t.Fatalf("aggregate budget after hold timeout=%d, want 0", used)
	}
	if strings.Contains(recorder.Body.String(), `"arguments":"{\\"path\\"`) || upstreamRequests.Load() != 1 {
		t.Fatal("timed-out tool arguments escaped or were retried")
	}
}

func TestStreamToolCallHoldValidationFailureFailsClosedE2E(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_bad\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\"}}]},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
	}))
	defer upstream.Close()
	cfg := toolRoutingConfig(t, upstream.URL, t.TempDir(), ToolRoutingConfig{
		Mode:      "observe",
		Streaming: ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 1000, HoldMaxBytes: 1 << 20},
	})
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	body := strings.Replace(toolRoutingRequestBody, "%s", "agent-tools", 1)
	body = strings.Replace(body, `"stream":false`, `"stream":true`, 1)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	svc.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "tool-call-validation-failed") {
		t.Fatalf("invalid tool call status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "call_bad") || strings.Contains(recorder.Body.String(), `"arguments"`) {
		t.Fatalf("invalid held arguments escaped in response: %s", recorder.Body.String())
	}
	if used := svc.lineBufferBudget.Used(); used != 0 {
		t.Fatalf("aggregate budget after validation failure=%d, want 0", used)
	}
}

func TestStreamToolCallHoldLocalhostContinuationE2E(t *testing.T) {
	var upstreamRequests atomic.Int32
	heldToolFrames := make(chan struct{})
	releaseToolFrames := make(chan struct{})
	var releaseOnce sync.Once
	releaseHeldFrames := func() { releaseOnce.Do(func() { close(releaseToolFrames) }) }
	defer releaseHeldFrames()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if upstreamRequests.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = w.Write([]byte("data: {\"id\":\"hold_1\",\"model\":\"tool-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"checking\"},\"finish_reason\":null}]}\n\n"))
			flusher.Flush()
			_, _ = w.Write([]byte("data: {\"id\":\"hold_1\",\"model\":\"tool-model\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\"}}]},\"finish_reason\":null}]}\n\n"))
			flusher.Flush()
			close(heldToolFrames)
			<-releaseToolFrames
			_, _ = w.Write([]byte("data: {\"id\":\"hold_1\",\"model\":\"tool-model\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"notes.txt\\\"}\"}}]},\"finish_reason\":null}]}\n\n"))
			flusher.Flush()
			_, _ = w.Write([]byte("data: {\"id\":\"hold_1\",\"model\":\"tool-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		messages := valueAsSlice(body["messages"])
		if len(messages) < 3 {
			t.Error("continuation request omitted tool call history")
		} else {
			last, _ := messages[len(messages)-1].(map[string]any)
			if last["role"] != "tool" || last["tool_call_id"] != "call_1" {
				t.Error("continuation request omitted matching tool result")
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "done_1", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "continued"}, "finish_reason": "stop"}}})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := toolRoutingConfig(t, upstream.URL, dir, ToolRoutingConfig{
		Mode:      "observe",
		Streaming: ToolRoutingStreamingConfig{HoldUntilValidated: true, HoldTimeoutMS: 1000, HoldMaxBytes: 1 << 20},
	})
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	router := httptest.NewServer(svc.Handler())
	defer router.Close()
	streamBody := strings.Replace(toolRoutingRequestBody, "%s", "agent-tools", 1)
	streamBody = strings.Replace(streamBody, `"stream":false`, `"stream":true`, 1)
	streamReq, _ := http.NewRequest(http.MethodPost, router.URL+"/v1/chat/completions", strings.NewReader(streamBody))
	streamReq.Header.Set("Authorization", "Bearer "+testToken)
	streamResp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-heldToolFrames:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not reach the partial tool-call hold")
	}
	deadline := time.Now().Add(2 * time.Second)
	for svc.lineBufferBudget.Used() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if used := svc.lineBufferBudget.Used(); used <= 0 || used > int64(cfg.Server.Streaming.MaxInflightLineBytes) {
		releaseHeldFrames()
		t.Fatalf("aggregate budget during tool-call hold=%d, want (0,%d]", used, cfg.Server.Streaming.MaxInflightLineBytes)
	}
	releaseHeldFrames()
	streamOutput := make([]byte, 1<<20)
	n, err := streamResp.Body.Read(streamOutput)
	if err != nil {
		t.Fatal(err)
	}
	for err == nil {
		var read int
		read, err = streamResp.Body.Read(streamOutput[n:])
		n += read
		if n == len(streamOutput) {
			break
		}
	}
	streamResp.Body.Close()
	streamText := string(streamOutput[:n])
	if streamResp.StatusCode != http.StatusOK || !strings.Contains(streamText, "checking") || !strings.Contains(streamText, "call_1") || !strings.Contains(streamText, "notes.txt") {
		t.Fatalf("held stream status=%d body=%s", streamResp.StatusCode, streamText)
	}
	if used := svc.lineBufferBudget.Used(); used != 0 {
		t.Fatalf("aggregate budget after validated frames emitted=%d, want 0", used)
	}
	continuation := `{"model":"agent-tools","stream":false,"messages":[{"role":"user","content":"read the file"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"notes.txt\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"file contents"}]}`
	continueReq, _ := http.NewRequest(http.MethodPost, router.URL+"/v1/chat/completions", strings.NewReader(continuation))
	continueReq.Header.Set("Authorization", "Bearer "+testToken)
	continueResp, err := http.DefaultClient.Do(continueReq)
	if err != nil {
		t.Fatal(err)
	}
	defer continueResp.Body.Close()
	continuationOutput, err := io.ReadAll(continueResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if continueResp.StatusCode != http.StatusOK || !strings.Contains(string(continuationOutput), "continued") {
		t.Fatalf("tool continuation status=%d", continueResp.StatusCode)
	}
}
