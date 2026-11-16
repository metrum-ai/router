// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func sequenceUpstream(t *testing.T, responses ...string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	index := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		position := index
		if index < len(responses)-1 {
			index++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[position]))
	}))
}

func chatToolCallBody(t *testing.T, arguments string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"id": "up",
		"choices": []map[string]any{{
			"message": map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]any{{
					"id":       "call_1",
					"type":     "function",
					"function": map[string]any{"name": "read_file", "arguments": arguments},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func enforceConfig(t *testing.T, upstreamURL, dir string, retries, escalations int, targets int) *Config {
	t.Helper()
	cfg := toolRoutingConfig(t, upstreamURL, dir, ToolRoutingConfig{
		Mode:     "enforce",
		Recovery: ToolRoutingRecoveryConfig{MaxRetriesSameTarget: retries, MaxEscalations: escalations},
	})
	group := cfg.Models["agent-tools"]
	if targets > 1 {
		group.Targets = append(group.Targets, Target{
			Provider: "mock", Model: "tool-model-b", Weight: 5,
			ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
		})
	}
	cfg.Models["agent-tools"] = group
	return cfg
}

func TestEnforceRetriesSameTargetAndDeliversTheValidCall(t *testing.T) {
	upstream := sequenceUpstream(t, chatToolCallBody(t, `{"path":`), chatToolCallBody(t, `{"path":"notes.txt"}`))
	defer upstream.Close()
	dir := t.TempDir()
	svc, err := New(enforceConfig(t, upstream.URL, dir, 1, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	response := serveToolRouting(t, svc, "agent-tools")
	svc.Close()
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `{\"path\":\"notes.txt\"}`) {
		t.Fatalf("delivered response is not the valid retry: %s", response.Body.String())
	}
	record := readToolRoutingLog(t, filepath.Join(dir, "requests.jsonl"))
	attempts, _ := record["attempts_detail"].([]any)
	if len(attempts) != 2 {
		t.Fatalf("attempts=%d, want 2: %#v", len(attempts), attempts)
	}
	first, _ := attempts[0].(map[string]any)
	second, _ := attempts[1].(map[string]any)
	if first["selected"] == true {
		t.Fatalf("the invalid attempt must not be selected: %#v", first)
	}
	if second["selected"] != true {
		t.Fatalf("the retried attempt must be selected: %#v", second)
	}
	validation, _ := second["tool_validation"].(map[string]any)
	if validation["action"] != "retried" {
		t.Fatalf("retry action: %#v", validation)
	}
	if first["input_tokens"] == nil || first["output_tokens"] == nil {
		t.Fatalf("discarded attempt usage must be recorded: %#v", first)
	}
	if record["attempts"].(float64) != 2 {
		t.Fatalf("request attempts=%v, want 2", record["attempts"])
	}
}

func TestEnforceEscalatesToTheNextTarget(t *testing.T) {
	upstream := sequenceUpstream(t, chatToolCallBody(t, `{`), chatToolCallBody(t, `{"path":"notes.txt"}`))
	defer upstream.Close()
	dir := t.TempDir()
	svc, err := New(enforceConfig(t, upstream.URL, dir, 0, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	response := serveToolRouting(t, svc, "agent-tools")
	svc.Close()
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	record := readToolRoutingLog(t, filepath.Join(dir, "requests.jsonl"))
	attempts, _ := record["attempts_detail"].([]any)
	if len(attempts) != 2 {
		t.Fatalf("attempts=%d, want 2: %#v", len(attempts), attempts)
	}
	second, _ := attempts[1].(map[string]any)
	if second["model"] != "tool-model-b" {
		t.Fatalf("escalated to %v, want tool-model-b", second["model"])
	}
	validation, _ := second["tool_validation"].(map[string]any)
	if validation["action"] != "escalated" {
		t.Fatalf("escalation action: %#v", validation)
	}
	if record["fallback_used"] != true {
		t.Fatalf("escalation must mark fallback_used: %#v", record["fallback_used"])
	}
}

func TestEnforceExhaustedDeliversLastResponse(t *testing.T) {
	upstream := sequenceUpstream(t, chatToolCallBody(t, `{`), chatToolCallBody(t, `{"path":`))
	defer upstream.Close()
	dir := t.TempDir()
	svc, err := New(enforceConfig(t, upstream.URL, dir, 0, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	response := serveToolRouting(t, svc, "agent-tools")
	svc.Close()
	if response.Code != http.StatusOK {
		t.Fatalf("exhausted recovery must still deliver a response, got %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `{\"path\":`) {
		t.Fatalf("delivered the wrong response: %s", response.Body.String())
	}
	record := readToolRoutingLog(t, filepath.Join(dir, "requests.jsonl"))
	attempts, _ := record["attempts_detail"].([]any)
	if len(attempts) != 2 {
		t.Fatalf("attempts=%d, want 2 (one retry-free escalation)", len(attempts))
	}
}

func TestEnforceStreamingIsObservedNotRetried(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		frames := []string{
			`{"id":"up","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
			`{"id":"up","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		}
		for _, frame := range frames {
			_, _ = w.Write([]byte("data: " + frame + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	dir := t.TempDir()
	svc, err := New(enforceConfig(t, upstream.URL, dir, 1, 0, 1))
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
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("streaming must not be retried, upstream calls=%d", calls)
	}
}

func TestToolMetricsExposeValidationCounters(t *testing.T) {
	store := newMetricsStore()
	store.Observe(logRecord{
		CallerID:      "alice",
		Status:        200,
		ResolvedGroup: "agent-tools",
		AttemptsDetail: []attemptLogRecord{
			{Index: 1, ToolValidation: &toolValidationLogRecord{Valid: 0, Invalid: 1, Action: "retried"}},
			{Index: 2, ToolValidation: &toolValidationLogRecord{Valid: 1, Action: "observed"}},
			{Index: 3, ToolValidation: &toolValidationLogRecord{Valid: 0, Invalid: 1, Action: "escalated"}},
		},
	})
	body := store.Prometheus(nil, MigrationStatus{})
	for _, name := range []string{
		"metrum_ai_router_tool_call_valid_total",
		"metrum_ai_router_tool_call_invalid_total",
		"metrum_ai_router_tool_call_retries_total",
		"metrum_ai_router_tool_call_escalations_total",
	} {
		if !strings.Contains(body, name) {
			t.Fatalf("missing metric %s", name)
		}
	}
}

func TestRecoveryRetriesTheFallbackThatServedTheResponse(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		position := calls
		mu.Unlock()
		switch position {
		case 1:
			// The primary fails at the transport layer, so the delivered response
			// comes from the fallback target.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		case 2:
			_, _ = w.Write([]byte(chatToolCallBody(t, `{`)))
		default:
			_, _ = w.Write([]byte(chatToolCallBody(t, `{"path":"notes.txt"}`)))
		}
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := enforceConfig(t, upstream.URL, dir, 1, 0, 2)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	response := serveToolRouting(t, svc, "agent-tools")
	svc.Close()
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `{\"path\":\"notes.txt\"}`) {
		t.Fatalf("recovery did not deliver the valid call: %s", response.Body.String())
	}
	record := readToolRoutingLog(t, filepath.Join(dir, "requests.jsonl"))
	attempts, _ := record["attempts_detail"].([]any)
	if len(attempts) != 3 {
		t.Fatalf("attempts=%d, want 3: %#v", len(attempts), attempts)
	}
	first, _ := attempts[0].(map[string]any)
	second, _ := attempts[1].(map[string]any)
	third, _ := attempts[2].(map[string]any)
	if first["model"] != "tool-model" || second["model"] != "tool-model-b" || third["model"] != "tool-model-b" {
		t.Fatalf("retry must stay on the fallback that served the response: %v %v %v",
			first["model"], second["model"], third["model"])
	}
	if third["selected"] != true {
		t.Fatalf("retried attempt must be selected: %#v", third)
	}
}

func TestRecoveryStopsAtTheSpendCeiling(t *testing.T) {
	upstream := sequenceUpstream(t, chatToolCallBody(t, `{`), chatToolCallBody(t, `{"path":"notes.txt"}`))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := enforceConfig(t, upstream.URL, dir, 0, 1, 2)
	group := cfg.Models["agent-tools"]
	group.SpendCeiling = SpendCeilingConfig{MaxTier: "cheap"}
	group.Targets[0].Tier = "cheap"
	group.Targets[1].Tier = "heavy"
	cfg.Models["agent-tools"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	response := serveToolRouting(t, svc, "agent-tools")
	svc.Close()
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	record := readToolRoutingLog(t, filepath.Join(dir, "requests.jsonl"))
	attempts, _ := record["attempts_detail"].([]any)
	if len(attempts) != 1 {
		t.Fatalf("ceiling must block escalation, attempts=%d: %#v", len(attempts), attempts)
	}
	traces, _ := record["trace_events"].([]any)
	blocked := false
	for _, entry := range traces {
		trace, _ := entry.(map[string]any)
		if trace["event"] == "tool_recovery_ceiling_blocked" {
			blocked = true
		}
	}
	if !blocked {
		t.Fatalf("expected a ceiling trace reason: %#v", traces)
	}
}

func TestRecoveryStopsAtTheRequestDeadline(t *testing.T) {
	upstream := toolRoutingUpstream(t, `{"path":"notes.txt"}`)
	defer upstream.Close()
	dir := t.TempDir()
	svc, err := New(enforceConfig(t, upstream.URL, dir, 1, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	rc := &requestContext{rec: logRecord{RequestID: "req_deadline"}}
	req := &IRRequest{
		Model: "agent-tools",
		Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}},
	}
	dec := decision{GroupName: "agent-tools", Target: Target{Provider: "mock", Model: "tool-model"}}
	invalid := &IRResponse{ToolCalls: []IRToolCall{{Name: "read_file", Arguments: `{`}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recovered, attempts, _ := svc.recoverToolCall(ctx, rc, nil, "openai-chat", req, dec, invalid, 1, false)
	if recovered != invalid {
		t.Fatal("a cancelled deadline must keep the original response")
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d, want 1 (no retry after the deadline)", attempts)
	}
	if len(rc.rec.AttemptsDetail) != 0 {
		t.Fatalf("no attempt should be recorded after the deadline: %#v", rc.rec.AttemptsDetail)
	}
}

func TestRecoveryUsageCountsAgainstQuota(t *testing.T) {
	upstream := sequenceUpstream(t, chatToolCallBody(t, `{`), chatToolCallBody(t, `{"path":"notes.txt"}`))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := enforceConfig(t, upstream.URL, dir, 1, 0, 1)
	cfg.Callers[0].Quota = QuotaConfig{
		Day:     BudgetConfig{Requests: 100, Tokens: 120},
		Month:   BudgetConfig{Tokens: 1000},
		SoftPct: 80,
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first := serveToolRouting(t, svc, "agent-tools")
	if first.Code != http.StatusOK {
		t.Fatalf("first request status=%d body=%s", first.Code, first.Body.String())
	}
	record := readToolRoutingLog(t, filepath.Join(dir, "requests.jsonl"))
	discarded := 0
	for _, entry := range record["attempts_detail"].([]any) {
		attempt, _ := entry.(map[string]any)
		if attempt["selected"] != true {
			discarded += int(attempt["input_tokens"].(float64) + attempt["output_tokens"].(float64))
		}
	}
	delivered := int(record["usage"].(map[string]any)["total_tokens"].(float64))
	if discarded == 0 {
		t.Fatal("retry attempt did not record discarded tokens")
	}
	if delivered < discarded {
		t.Fatalf("request usage (%d) does not include the discarded attempt (%d)", delivered, discarded)
	}
	// The settled quota must include both attempts, so the next request is
	// admitted against a smaller remainder and is refused.
	second := serveToolRouting(t, svc, "agent-tools")
	svc.Close()
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status=%d body=%s, want 429 after the quota was settled with retry usage",
			second.Code, second.Body.String())
	}
}
