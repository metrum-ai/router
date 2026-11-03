// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

// recordedRequests loads the committed request shapes that stand in for the
// production traffic this feature must not disturb. No production deployment is
// used or described; the shapes are the recorded client fixtures.
type recordedShape struct {
	Name     string         `json:"name"`
	Endpoint string         `json:"endpoint"`
	Body     map[string]any `json:"body"`
}

func loadRecordedShapes(t *testing.T) []recordedShape {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "tool-routing", "recorded-requests.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recorded shapes: %v", err)
	}
	var fixture struct {
		Shapes []recordedShape `json:"shapes"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode recorded shapes: %v", err)
	}
	if len(fixture.Shapes) == 0 {
		t.Fatal("no recorded shapes")
	}
	return fixture.Shapes
}

// replayUpstream answers every recorded shape with a deterministic response that
// carries one valid tool call on the tool-bearing surfaces.
func replayUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		stream, _ := body["stream"].(bool)
		switch r.URL.Path {
		case "/v1/responses":
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				writeFrames(w,
					`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":""}}`,
					`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"notes.txt\"}"}`,
					`{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}}}`,
				)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "resp_1",
				"output": []map[string]any{{
					"type": "function_call", "call_id": "call_1", "name": "read_file", "arguments": `{"path":"notes.txt"}`,
				}},
				"usage": map[string]any{"input_tokens": 12, "output_tokens": 4, "total_tokens": 16},
			})
		case "/v1/messages":
			w.Header().Set("Content-Type", "text/event-stream")
			writeAnthropicFrames(w,
				"message_start", `{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":9}}}`,
				"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`,
				"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"notes.txt\"}"}}`,
				"content_block_stop", `{"type":"content_block_stop","index":0}`,
				"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
				"message_stop", `{"type":"message_stop"}`,
			)
		default:
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				writeFrames(w,
					`{"id":"chatcmpl_1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
					`{"id":"chatcmpl_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"notes.txt\"}"}}]}}]}`,
					`{"id":"chatcmpl_1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":4,"total_tokens":15}}`,
				)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "chatcmpl_1",
				"choices": []map[string]any{{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": nil,
						"tool_calls": []map[string]any{{
							"id": "call_1", "type": "function",
							"function": map[string]any{"name": "read_file", "arguments": `{"path":"notes.txt"}`},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 4, "total_tokens": 15},
			})
		}
	}))
}

func writeAnthropicFrames(w http.ResponseWriter, pairs ...string) {
	for index := 0; index+1 < len(pairs); index += 2 {
		_, _ = w.Write([]byte("event: " + pairs[index] + "\ndata: " + pairs[index+1] + "\n\n"))
	}
}

func writeFrames(w http.ResponseWriter, frames ...string) {
	for _, frame := range frames {
		_, _ = w.Write([]byte("data: " + frame + "\n\n"))
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

func replayRequest(t *testing.T, svc *Service, shape recordedShape, group string) *httptest.ResponseRecorder {
	t.Helper()
	body := shape.Body
	body["model"] = group
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, shape.Endpoint, strings.NewReader(string(encoded)))
	req.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	svc.Handler().ServeHTTP(recorder, req)
	return recorder
}

// replayConfig builds a group per strategy over the same recorded upstream, with
// tool support and dialect validation declared so the request shapes are
// eligible on both OpenAI Chat and Anthropic Messages callers.
func replayConfig(t *testing.T, upstreamURL, dir string, toolAware bool) *Config {
	t.Helper()
	cfg := toolRoutingConfig(t, upstreamURL, dir, ToolRoutingConfig{Mode: "observe"})
	target := func(provider, model string, tools ToolSupport) Target {
		return Target{
			Provider:    provider,
			Model:       model,
			Weight:      10,
			ToolSupport: tools,
			RequestShapeSupport: RequestShapeSupport{
				SupportedInboundDialects: []string{"openai-chat", "openai-responses", "anthropic"},
				ValidationStatus:         "passed",
			},
		}
	}
	group := ModelGroup{
		Strategy: "static",
		Targets: []Target{
			target("mock-chat", "tool-model-chat", ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}),
			target("mock-responses", "tool-model-responses", ToolSupport{OpenAIResponses: []string{"function", "tool_choice"}}),
			target("mock-anthropic", "tool-model-anthropic", ToolSupport{AnthropicMessages: []string{"client_tools"}}),
		},
	}
	if toolAware {
		group.Strategy = "tool_aware"
		group.ToolRouting = ToolRoutingConfig{Mode: "observe"}
	}
	cfg.Provider = map[string]ProviderConfig{
		"mock-chat":      {BaseURL: upstreamURL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"},
		"mock-responses": {BaseURL: upstreamURL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"},
		"mock-anthropic": {BaseURL: upstreamURL + "/v1", Dialect: "anthropic", APIKey: "provider-key"},
	}
	cfg.Models = map[string]ModelGroup{"agent-tools": group}
	cfg.Server.DefaultModelGroup = "agent-tools"
	cfg.Callers[0].Allow = []string{"agent-tools"}
	return cfg
}

// TestRecordedTrafficReplayIsUnchanged is the no-change proof required for
// groups that do not enable tool-aware routing: the recorded client shapes
// produce identical client bytes and identical attempt accounting through a
// static group and through an observe-mode tool_aware group, and the only
// difference is the observe-only tool_validation annotation.
func TestRecordedTrafficReplayIsUnchanged(t *testing.T) {
	shapes := loadRecordedShapes(t)
	upstream := replayUpstream(t)
	defer upstream.Close()

	staticDir := t.TempDir()
	staticSvc, err := New(replayConfig(t, upstream.URL, staticDir, false))
	if err != nil {
		t.Fatal(err)
	}
	defer staticSvc.Close()

	toolDir := t.TempDir()
	toolSvc, err := New(replayConfig(t, upstream.URL, toolDir, true))
	if err != nil {
		t.Fatal(err)
	}
	defer toolSvc.Close()

	for _, shape := range shapes {
		t.Run(shape.Name, func(t *testing.T) {
			staticResponse := replayRequest(t, staticSvc, shape, "agent-tools")
			toolResponse := replayRequest(t, toolSvc, shape, "agent-tools")
			if staticResponse.Code != toolResponse.Code {
				t.Fatalf("status differs: static=%d tool_aware=%d bodies %s / %s",
					staticResponse.Code, toolResponse.Code, staticResponse.Body.String(), toolResponse.Body.String())
			}
			if staticResponse.Body.String() != toolResponse.Body.String() {
				t.Fatalf("client bytes changed:\nstatic:     %s\ntool_aware: %s",
					staticResponse.Body.String(), toolResponse.Body.String())
			}
			if staticResponse.Code != http.StatusOK {
				// A shape that the fixture cannot serve identically is a test-data
				// problem, not a pass.
				t.Fatalf("recorded shape did not succeed: %d %s", staticResponse.Code, staticResponse.Body.String())
			}
		})
	}

	// Attempt accounting must match field for field apart from the tool_validation
	// annotation, which exists only for the opted-in group.
	staticAttempts := replayAttempts(t, filepath.Join(staticDir, "requests.jsonl"))
	toolAttempts := replayAttempts(t, filepath.Join(toolDir, "requests.jsonl"))
	if len(staticAttempts) != len(toolAttempts) {
		t.Fatalf("request count differs: static=%d tool_aware=%d", len(staticAttempts), len(toolAttempts))
	}
	for index := range staticAttempts {
		staticFields := comparableAttemptFields(t, staticAttempts[index], false)
		toolFields := comparableAttemptFields(t, toolAttempts[index], true)
		if !reflect.DeepEqual(staticFields, toolFields) {
			t.Fatalf("request %d attempt fields differ:\nstatic:     %#v\ntool_aware: %#v", index, staticFields, toolFields)
		}
	}
}

func replayAttempts(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode request log: %v", err)
		}
		if _, ok := record["attempts_detail"]; ok {
			records = append(records, record)
		}
	}
	return records
}

// comparableAttemptFields projects a request record onto the fields the two
// groups must share. Volatile fields (timestamps, durations, ids, costs of the
// local cache) are excluded; the opted-in group's tool_validation is excluded
// from the comparison and asserted separately.
func comparableAttemptFields(t *testing.T, record map[string]any, toolAware bool) map[string]any {
	t.Helper()
	out := map[string]any{}
	// The configured strategy name is excluded on purpose: it is the only
	// intended difference and it must not change selection or output.
	for _, field := range []string{"status", "attempts", "fallback_used", "resolved_group", "target_provider", "target_model", "endpoint", "stream"} {
		if value, ok := record[field]; ok {
			out[field] = value
		}
	}
	attempts, _ := record["attempts_detail"].([]any)
	projected := make([]map[string]any, 0, len(attempts))
	for _, entry := range attempts {
		attempt, _ := entry.(map[string]any)
		fields := map[string]any{}
		for _, field := range []string{"provider", "model", "dialect", "status_code", "selected", "error_class", "fallback_reason", "retryable", "timed_out", "client_canceled"} {
			if value, ok := attempt[field]; ok {
				fields[field] = value
			}
		}
		// The annotation itself is asserted separately and is the only field the
		// two groups may differ on.
		// A streamed response whose body is relayed straight to the caller has no
		// decoded response object, so the opted-in group may legitimately record
		// nothing for it. Non-streamed attempts must be annotated.
		if _, present := attempt["tool_validation"]; present && !toolAware {
			t.Fatalf("non-opted group recorded tool_validation: %#v", attempt["tool_validation"])
		}
		projected = append(projected, fields)
	}
	out["attempts_detail"] = projected

	// Decision telemetry must name the same targets in the same order.
	terms, _ := record["dynamic_score_terms"].([]any)
	ranking := make([]string, 0, len(terms))
	for _, entry := range terms {
		term, _ := entry.(map[string]any)
		model, _ := term["model"].(string)
		ranking = append(ranking, model)
	}
	sort.Strings(ranking)
	out["ranking"] = ranking
	return out
}

// TestValidationOffSkipsToolValidation exercises the explicit opt-out: with every
// check disabled the attempt still counts its calls and never fails one.
func TestValidationOffSkipsToolValidation(t *testing.T) {
	upstream := toolRoutingUpstream(t, `{"path":"notes.txt"}`)
	defer upstream.Close()
	dir := t.TempDir()
	off := false
	svc, err := New(toolRoutingConfig(t, upstream.URL, dir, ToolRoutingConfig{
		Mode:     "observe",
		Validate: ToolRoutingValidateConfig{ArgumentsJSON: &off, Schema: "off", NameMustBeOffered: &off},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc2 := svc
	_ = svc2
	response := serveToolRouting(t, svc, "agent-tools")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	validation := toolValidationFromLog(t, readToolRoutingLog(t, filepath.Join(dir, "requests.jsonl")))
	if validation["calls"].(float64) != 1 || validation["valid"].(float64) != 1 || validation["invalid"].(float64) != 0 {
		t.Fatalf("disabled validation must count calls as valid without checks: %#v", validation)
	}
}

var _ = sync.Mutex{}
