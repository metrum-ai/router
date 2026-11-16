// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	bt "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

func TestBedrockMapping(t *testing.T) {
	in, err := bedrockInput([]byte(`{"model":"us.meta.llama3-3-70b-instruct-v1:0","messages":[{"role":"system","content":"system"},{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"call_1","function":{"name":"write","arguments":"{\"path\":\"a\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"done"}],"tools":[{"type":"function","function":{"name":"write","description":"write a file","parameters":{"type":"object"}}}],"tool_choice":"required","max_tokens":32,"temperature":0.2,"stop":["END"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Messages) != 3 || len(in.System) != 1 || aws.ToInt32(in.InferenceConfig.MaxTokens) != 32 || in.ToolConfig == nil {
		t.Fatalf("bad mapping: %+v", in)
	}
	tool := in.Messages[1].Content[0].(*bt.ContentBlockMemberToolUse)
	result := in.Messages[2].Content[0].(*bt.ContentBlockMemberToolResult)
	if aws.ToString(tool.Value.ToolUseId) != aws.ToString(result.Value.ToolUseId) {
		t.Fatal("tool ID lost")
	}
	if _, ok := in.ToolConfig.ToolChoice.(*bt.ToolChoiceMemberAny); !ok {
		t.Fatal("choice lost")
	}
}
func TestBedrockResponsesToolContinuation(t *testing.T) {
	raw := []byte(`{"model":"m","max_output_tokens":32,"instructions":"system","input":[{"role":"user","content":[{"type":"input_text","text":"write a file"}]},{"type":"function_call","call_id":"call_1","name":"write","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"done"}]}`)
	req, err := decodeRequest("openai-responses", raw, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true}, Bridges: BridgeSupport{ResponsesToChat: DialectBridgeSupport{Tools: true}}}
	b, err := encodeResponsesToChatBridge("m", req, target)
	if err != nil {
		t.Fatal(err)
	}
	in, err := bedrockInput(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Messages) != 3 || aws.ToInt32(in.InferenceConfig.MaxTokens) != 32 {
		t.Fatalf("%s: %+v", b, in)
	}
	if aws.ToString(in.Messages[2].Content[0].(*bt.ContentBlockMemberToolResult).Value.ToolUseId) != "call_1" {
		t.Fatal("ID lost")
	}
	target.Bridges.ResponsesToChat.Tools = false
	if _, err := encodeResponsesToChatBridge("m", req, target); err == nil {
		t.Fatal("continuation must remain opt-in")
	}
}

func TestBedrockErrors(t *testing.T) {
	for code, status := range map[string]int{"ThrottlingException": 429, "ValidationException": 400, "ModelNotReadyException": 503, "AccessDeniedException": 403} {
		t.Run(code, func(t *testing.T) {
			r := bedrockError(&smithy.GenericAPIError{Code: code, Message: "fixture"})
			defer r.Body.Close()
			if r.StatusCode != status {
				t.Fatal(r.StatusCode)
			}
			if status == 429 || status == 503 {
				if !classifyUpstreamStatus(status, nil).Retryable {
					t.Fatal("must fail over")
				}
			}
		})
	}
}
func TestBedrockOptionalToolDescription(t *testing.T) {
	in, err := bedrockInput([]byte(`{"model":"unknown.model","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"ping"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	spec := in.ToolConfig.Tools[0].(*bt.ToolMemberToolSpec).Value
	if spec.Description != nil || spec.InputSchema == nil {
		t.Fatal("optional tool fields were not normalized")
	}
}

func TestBedrockMaxTokensBounds(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int32
	}{
		{"plain", `{"model":"m","messages":[],"max_tokens":16}`, 16},
		{"string number", `{"model":"m","messages":[],"max_tokens":"64"}`, 64},
		{"negative clamps to zero", `{"model":"m","messages":[],"max_tokens":-5}`, 0},
		{"int32 max passes through", `{"model":"m","messages":[],"max_tokens":2147483647}`, math.MaxInt32},
		{"int32 overflow clamps high", `{"model":"m","messages":[],"max_tokens":8589934592}`, math.MaxInt32},
		{"large string clamps high", `{"model":"m","messages":[],"max_tokens":"17179869184"}`, math.MaxInt32},
		{"completion tokens also clamped", `{"model":"m","messages":[],"max_completion_tokens":17179869184}`, math.MaxInt32},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, err := bedrockInput([]byte(c.body))
			if err != nil {
				t.Fatal(err)
			}
			if in.InferenceConfig.MaxTokens == nil || *in.InferenceConfig.MaxTokens != c.want {
				t.Fatalf("MaxTokens = %v, want %d", in.InferenceConfig.MaxTokens, c.want)
			}
		})
	}
	if got := *bedrockMaxTokens(int(math.MaxInt32) + 1); got != math.MaxInt32 {
		t.Fatalf("bedrockMaxTokens overflow: got %d, want %d", got, int32(math.MaxInt32))
	}
}

func TestBedrockUsage(t *testing.T) {
	u := bedrockUsage(&bt.TokenUsage{InputTokens: aws.Int32(12), OutputTokens: aws.Int32(3), TotalTokens: aws.Int32(15)})
	if u["total_tokens"] != int32(15) {
		t.Fatal(u)
	}
	if bedrockFinish(bt.StopReasonToolUse) != "tool_calls" {
		t.Fatal("finish")
	}
}

type bedrockFixtureTransport struct {
	t      *testing.T
	stream bool
}

func (f bedrockFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Helper()
	if !strings.Contains(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
		f.t.Error("request was not signed")
	}
	if f.stream {
		raw, err := os.ReadFile("../../testdata/bedrock/converse-stream.json")
		if err != nil {
			f.t.Fatal(err)
		}
		var events []struct {
			Event   string          `json:"event"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(raw, &events); err != nil {
			f.t.Fatal(err)
		}
		var buf bytes.Buffer
		enc := eventstream.NewEncoder()
		for _, e := range events {
			var h eventstream.Headers
			h.Set(":message-type", eventstream.StringValue("event"))
			h.Set(":event-type", eventstream.StringValue(e.Event))
			h.Set(":content-type", eventstream.StringValue("application/json"))
			if err := enc.Encode(&buf, eventstream.Message{Headers: h, Payload: e.Payload}); err != nil {
				f.t.Fatal(err)
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}}, Body: io.NopCloser(bytes.NewReader(buf.Bytes()))}, nil
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"output":{"message":{"role":"assistant","content":[{"text":"OK"}]}},"stopReason":"end_turn","usage":{"inputTokens":5,"outputTokens":2,"totalTokens":7}}`))}, nil
}
func TestBedrockRecordedStream(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	for _, stream := range []bool{false, true} {
		body := `{"model":"mistral.mistral-large-3-675b-instruct","messages":[{"role":"user","content":"Say OK"}],"max_tokens":16,"stream":` + fmt.Sprint(stream) + `}`
		r, err := bedrockDo(context.Background(), "us-east-1", []byte(body), &http.Client{Transport: bedrockFixtureTransport{t, stream}})
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if stream {
			for _, s := range []string{"[DONE]", "prompt_tokens", "42", "call_fixture", "write_file", "tool_calls"} {
				if !strings.Contains(string(b), s) {
					t.Fatalf("missing %s: %s", s, b)
				}
			}
		} else if !strings.Contains(string(b), "OK") {
			t.Fatal(string(b))
		}
	}
}

type bedrockReasoningTransport struct {
	t      *testing.T
	stream bool
	calls  *int
}

func (f bedrockReasoningTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Helper()
	if !strings.Contains(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
		f.t.Error("request was not signed")
	}
	requestBody, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	if f.calls != nil {
		*f.calls++
		if *f.calls > 1 && (!bytes.Contains(requestBody, []byte(`"reasoningContent"`)) || !bytes.Contains(requestBody, []byte(`"signature":"signature"`))) {
			f.t.Errorf("Converse request lost opaque reasoning: %s", requestBody)
		}
	} else if !bytes.Contains(requestBody, []byte(`"reasoningContent"`)) || !bytes.Contains(requestBody, []byte(`"signature":"signature"`)) {
		f.t.Errorf("Converse request lost opaque reasoning: %s", requestBody)
	}
	if strings.HasSuffix(r.URL.Path, "/converse") {
		body := `{"output":{"message":{"role":"assistant","content":[{"reasoningContent":{"reasoningText":{"text":"thought","signature":"signature"}}},{"text":"hello"},{"reasoningContent":{"redactedContent":"Y2lwaGVy"}},{"toolUse":{"toolUseId":"call_1","name":"write","input":{"path":"a"}}}]}} ,"stopReason":"tool_use","usage":{"inputTokens":5,"outputTokens":2,"totalTokens":7}}`
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	if f.stream && strings.HasSuffix(r.URL.Path, "/converse-stream") {
		fixtures := []struct {
			event   string
			payload string
		}{
			{"messageStart", `{"role":"assistant"}`},
			{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"reasoningContent":{"text":"thought"}}}`},
			{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"reasoningContent":{"signature":"signature"}}}`},
			{"contentBlockStop", `{"contentBlockIndex":0}`},
			{"contentBlockDelta", `{"contentBlockIndex":1,"delta":{"reasoningContent":{"redactedContent":"Y2lwaGVy"}}}`},
			{"contentBlockStop", `{"contentBlockIndex":1}`},
			{"contentBlockStart", `{"contentBlockIndex":2,"start":{"toolUse":{"toolUseId":"call_1","name":"write"}}}`},
			{"contentBlockDelta", `{"contentBlockIndex":2,"delta":{"toolUse":{"input":"{\"path\":\"a\"}"}}}`},
			{"contentBlockStop", `{"contentBlockIndex":2}`},
			{"messageStop", `{"stopReason":"tool_use"}`},
			{"metadata", `{"usage":{"inputTokens":5,"outputTokens":2,"totalTokens":7}}`},
		}
		var buf bytes.Buffer
		encoder := eventstream.NewEncoder()
		for _, fixture := range fixtures {
			var headers eventstream.Headers
			headers.Set(":message-type", eventstream.StringValue("event"))
			headers.Set(":event-type", eventstream.StringValue(fixture.event))
			headers.Set(":content-type", eventstream.StringValue("application/json"))
			if err := encoder.Encode(&buf, eventstream.Message{Headers: headers, Payload: []byte(fixture.payload)}); err != nil {
				f.t.Fatal(err)
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}}, Body: io.NopCloser(bytes.NewReader(buf.Bytes()))}, nil
	}
	return nil, fmt.Errorf("unexpected Bedrock request path %q", r.URL.Path)
}

func TestBedrockInputPassesClaudeThinkingConfiguration(t *testing.T) {
	input, err := bedrockInput([]byte(`{"model":"us.anthropic.claude-haiku-4-5-20251001-v1:0","max_tokens":1152,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if input.AdditionalModelRequestFields == nil {
		t.Fatal("Claude thinking configuration was dropped")
	}
	got, err := input.AdditionalModelRequestFields.MarshalSmithyDocument()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(got, &fields); err != nil {
		t.Fatal(err)
	}
	thinking, _ := fields["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(1024) {
		t.Fatalf("thinking config = %#v", thinking)
	}
}

func TestBedrockAnthropicBridgePassesThinkingConfiguration(t *testing.T) {
	model := "us.anthropic.claude-haiku-4-5-20251001-v1:0"
	req, err := decodeRequest("anthropic", []byte(`{"model":"m","max_tokens":1152,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"record","input_schema":{"type":"object"}}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	target := anthropicToolTarget()
	target.Provider = "bedrock"
	target.Reasoning = ReasoningSupport{Supported: true, Mode: "opt_in", Control: "token_budget", MinBudgetTokens: 1024, MaxBudgetTokens: 1024, BudgetMustBeLessThanMaxTokens: true}
	body, err := encodeAnthropicToolsBridge(model, req, target, "openai-chat", true)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	thinking, _ := got["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(1024) {
		t.Fatalf("thinking config = %#v", thinking)
	}
}

func TestBedrockReasoningConverseRoundTrip(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	body := `{"model":"anthropic.claude-fixture","max_tokens":64,"bedrock_reasoning":true,"messages":[{"role":"assistant","bedrock_content":[{"type":"thinking","thinking":"thought","signature":"signature"},{"type":"text","text":"hello"},{"type":"redacted_thinking","data":"Y2lwaGVy"},{"type":"tool_use","id":"call_1","name":"write","input":{"path":"a"}}]}]}`
	response, err := bedrockDo(context.Background(), "us-east-1", []byte(body), &http.Client{Transport: bedrockReasoningTransport{t: t}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d body=%s", response.StatusCode, data)
	}
	var chat map[string]any
	if err := json.NewDecoder(response.Body).Decode(&chat); err != nil {
		t.Fatal(err)
	}
	if choices := valueAsSlice(chat["choices"]); len(choices) != 1 {
		t.Fatalf("choices=%#v", choices)
	} else if choice, ok := choices[0].(map[string]any); !ok || len(valueAsSlice(choice["message"].(map[string]any)["bedrock_content"])) != 4 {
		t.Fatalf("ordered response=%#v", choices[0])
	}
}

func TestBedrockReasoningConverseStreamRoundTrip(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	body := `{"model":"anthropic.claude-fixture","max_tokens":64,"stream":true,"bedrock_reasoning":true,"messages":[{"role":"assistant","bedrock_content":[{"type":"thinking","thinking":"thought","signature":"signature"}]}]}`
	response, err := bedrockDo(context.Background(), "us-east-1", []byte(body), &http.Client{Transport: bedrockReasoningTransport{t: t, stream: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"bedrock_events", "thinking_delta", "signature_delta", "[DONE]"} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("stream missing %q: %s", expected, data)
		}
	}
}

func TestBedrockAnthropicHandlerToolContinuation(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	calls := 0
	target := anthropicToolTarget()
	target.Model = "anthropic.claude-fixture"
	svc := nativeStreamTestService(t, "https://bedrock-runtime.us-east-1.amazonaws.com", "openai-chat", []Target{target})
	provider := svc.cfg.Provider[target.Provider]
	provider.AuthScheme = "bedrock"
	svc.cfg.Provider[target.Provider] = provider
	svc.httpClient = &http.Client{Transport: bedrockReasoningTransport{t: t, stream: true, calls: &calls}}
	first := `{"model":"native-stream","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"write a file"}],"tools":[{"name":"write","input_schema":{"type":"object"}}]}`
	request := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(first))
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "event: content_block_start") || !strings.Contains(response.Body.String(), `"type":"thinking"`) || !strings.Contains(response.Body.String(), `"type":"signature_delta"`) || !strings.Contains(response.Body.String(), `"type":"redacted_thinking"`) || !strings.Contains(response.Body.String(), `"type":"tool_use"`) {
		t.Fatalf("first response lost reasoning/tool call: %s", response.Body.String())
	}
	second := `{"model":"native-stream","max_tokens":64,"messages":[{"role":"user","content":"write a file"},{"role":"assistant","content":[{"type":"thinking","thinking":"thought","signature":"signature"},{"type":"redacted_thinking","data":"Y2lwaGVy"},{"type":"tool_use","id":"call_1","name":"write","input":{"path":"a"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"done"}]}],"tools":[{"name":"write","input_schema":{"type":"object"}}]}`
	request = httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(second))
	request.Header.Set("Authorization", "Bearer "+testToken)
	response = httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || calls != 2 {
		t.Fatalf("continuation status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
}

type bedrockTimeoutTransport struct{}

func (bedrockTimeoutTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	<-r.Context().Done()
	return nil, r.Context().Err()
}

func TestBedrockReasoningStreamTimeoutDoesNotComplete(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	body := `{"model":"anthropic.claude-fixture","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"continue"}]}`
	response, err := bedrockDo(ctx, "us-east-1", []byte(body), &http.Client{Transport: bedrockTimeoutTransport{}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusBadGateway || strings.Contains(string(data), "[DONE]") {
		t.Fatalf("timeout status=%d body=%s", response.StatusCode, data)
	}
}

func TestBedrockThrottleFailover(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/converse") {
			w.Header().Set("x-amzn-errortype", "ThrottlingException")
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"message":"fixture throttle"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"fallback","choices":[{"message":{"role":"assistant","content":"fallback OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", upstream.URL)
	cfg := testConfig(t, upstream.URL, "", t.TempDir())
	cfg.Provider["bedrock"] = ProviderConfig{Dialect: "openai-chat", AuthScheme: "bedrock", BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com"}
	cfg.Models["bedrock-failover"] = ModelGroup{Strategy: "failover", Targets: []Target{{Provider: "bedrock", Model: "mistral.mistral-large-3-675b-instruct"}, {Provider: "mock", Model: "fallback"}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bedrock-failover")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"bedrock-failover","messages":[{"role":"user","content":"hello"}],"max_tokens":16}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "fallback OK") {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
}

func TestBedrockLive(t *testing.T) {
	if os.Getenv("ROUTER_BEDROCK_LIVE") != "1" {
		t.Skip("opt-in live test")
	}
	r, err := bedrockDo(context.Background(), "us-east-1", []byte(`{"model":"us.anthropic.claude-haiku-4-5-20251001-v1:0","messages":[{"role":"user","content":"Call record with value ok"}],"thinking":{"type":"enabled","budget_tokens":1024},"tools":[{"type":"function","function":{"name":"record","parameters":{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}}}],"tool_choice":"required","max_tokens":1152,"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 200 || !strings.Contains(string(b), "tool_calls") || !strings.Contains(string(b), `"type":"thinking"`) || !strings.Contains(string(b), "prompt_tokens") {
		t.Fatalf("status=%d; expected Claude tool call, thinking blocks, and usage", r.StatusCode)
	}
}

func TestBedrockLiveRouted(t *testing.T) {
	if os.Getenv("ROUTER_BEDROCK_LIVE") != "1" {
		t.Skip("opt-in live test")
	}
	dir := t.TempDir()
	usageEnabled := true
	model := "us.anthropic.claude-haiku-4-5-20251001-v1:0"
	target := anthropicToolTarget()
	target.Model = model
	target.Region = "us-east-1"
	target.InputPricePerMillionUSD = 1.10
	target.OutputPricePerMillionUSD = 5.50
	target.Reasoning = ReasoningSupport{Supported: true, Mode: "opt_in", Control: "token_budget", MinBudgetTokens: 1024, MaxBudgetTokens: 1024, BudgetMustBeLessThanMaxTokens: true}
	path := filepath.Join(dir, "usage.sqlite")
	svc := nativeStreamTestService(t, "https://bedrock-runtime.us-east-1.amazonaws.com", "openai-chat", []Target{target}, func(cfg *Config) {
		provider := cfg.Provider["native"]
		provider.AuthScheme = "bedrock"
		cfg.Provider["native"] = provider
		cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(path, &usageEnabled)
	})
	body := `{"model":"native-stream","max_tokens":1152,"thinking":{"type":"enabled","budget_tokens":1024},"tool_choice":{"type":"any"},"tools":[{"name":"record","input_schema":{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}}],"messages":[{"role":"user","content":"Call record with value ok"}]}`
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	var rows []usageRecord
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := svc.usage.db.Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(rows) != 1 {
		t.Fatalf("status=%d usage rows=%d", rr.Code, len(rows))
	}
	t.Logf("routed status=%d model=%s input_tokens=%d output_tokens=%d cost_billed_usd=%.9f", rr.Code, rows[0].TargetModel, rows[0].InputTokens, rows[0].OutputTokens, rows[0].CostBilledUSD)
	if rr.Code == http.StatusOK && (!strings.Contains(rr.Body.String(), `"type":"thinking"`) || !strings.Contains(rr.Body.String(), `"type":"tool_use"`)) {
		t.Fatal("successful routed response omitted thinking or tool-use blocks")
	}
}

// Text-only multipart tool results must keep the caller's exact bytes; the
// previous conversion inserted a newline between parts. Non-text tool result
// content is unchanged here (see the limit noted in the issue).
func TestBedrockToolResultText678(t *testing.T) {
	t.Run("responses-caller", func(t *testing.T) {
		raw := []byte(`{"model":"m","max_output_tokens":32,"input":[{"role":"user","content":[{"type":"input_text","text":"read"}]},{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"marker-a-"},{"type":"input_text","text":""},{"type":"input_text","text":"multipart"}]}]}`)
		req, err := decodeRequest("openai-responses", raw, http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		target := Target{ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true}, Bridges: BridgeSupport{ResponsesToChat: DialectBridgeSupport{Tools: true}}}
		b, err := encodeResponsesToChatBridge("m", req, target)
		if err != nil {
			t.Fatal(err)
		}
		in, err := bedrockInput(b)
		if err != nil {
			t.Fatal(err)
		}
		block := in.Messages[2].Content[0].(*bt.ContentBlockMemberToolResult)
		text := block.Value.Content[0].(*bt.ToolResultContentBlockMemberText).Value
		if text != "marker-a-multipart" {
			t.Fatalf("text=%q body=%s", text, b)
		}
		if aws.ToString(block.Value.ToolUseId) != "call_1" {
			t.Fatalf("tool id lost: %s", b)
		}
	})
	t.Run("chat-caller", func(t *testing.T) {
		body := []byte(`{"model":"m","max_tokens":32,"messages":[{"role":"user","content":"read"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"α"},{"type":"text","text":"β\n"},{"type":"text","text":""}]}]}`)
		in, err := bedrockInput(body)
		if err != nil {
			t.Fatal(err)
		}
		block := in.Messages[2].Content[0].(*bt.ContentBlockMemberToolResult)
		if got := block.Value.Content[0].(*bt.ToolResultContentBlockMemberText).Value; got != "αβ\n" {
			t.Fatalf("text=%q", got)
		}
	})
	t.Run("string-unchanged", func(t *testing.T) {
		in, err := bedrockInput([]byte(`{"model":"m","messages":[{"role":"tool","tool_call_id":"call_1","content":"done\n"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		block := in.Messages[0].Content[0].(*bt.ContentBlockMemberToolResult)
		if got := block.Value.Content[0].(*bt.ToolResultContentBlockMemberText).Value; got != "done\n" {
			t.Fatalf("text=%q", got)
		}
	})
}

// Translated Messages callers reach bedrockInput after the Anthropic-to-Chat
// bridge, which builds typed part slices before JSON encoding.
func TestBedrockToolResultText678MessagesBridge(t *testing.T) {
	req, err := decodeRequest("anthropic", []byte(`{"model":"m","max_tokens":32,"messages":[{"role":"user","content":"read"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"marker-a-"},{"type":"text","text":"multipart"}]}]}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := encodeAnthropicToolsBridge("m", req, anthropicToolTarget(), "openai-chat")
	if err != nil {
		t.Fatal(err)
	}
	in, err := bedrockInput(b)
	if err != nil {
		t.Fatal(err)
	}
	block := in.Messages[2].Content[0].(*bt.ContentBlockMemberToolResult)
	if got := block.Value.Content[0].(*bt.ToolResultContentBlockMemberText).Value; got != "marker-a-multipart" {
		t.Fatalf("text=%q body=%s", got, b)
	}
	if aws.ToString(block.Value.ToolUseId) != "call_1" {
		t.Fatalf("tool id lost: %s", b)
	}
}

// Media inside a tool result must never be reduced to text silently (issue #694):
// the adapter rejects non-text content it cannot represent instead.
func TestBedrockToolResultMediaRejected694(t *testing.T) {
	const png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="
	cases := []struct{ name, body, part string }{
		{"chat-image-part", `{"model":"m","max_tokens":32,"messages":[{"role":"user","content":"shot"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"shot","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"chart"},{"type":"image_url","image_url":{"url":"` + png + `"}}]}]}`, "image_url"},
		{"chat-audio-part", `{"model":"m","max_tokens":32,"messages":[{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"clip"},{"type":"input_audio","input_audio":{"data":"AA==","format":"wav"}}]}]}`, "input_audio"},
		{"chat-media-object", `{"model":"m","max_tokens":32,"messages":[{"role":"tool","tool_call_id":"call_1","content":{"type":"image_url","image_url":{"url":"` + png + `"}}}]}`, "image_url"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, err := bedrockInput([]byte(c.body))
			if err == nil {
				t.Fatalf("tool-result media was silently dropped: %+v", in.Messages)
			}
			if !strings.Contains(err.Error(), c.part) {
				t.Fatalf("error must name the unsupported part %q: %v", c.part, err)
			}
		})
	}

	// The Anthropic bridge carries a tool_result image into Chat as a typed part,
	// so the same rejection applies to a translated Messages caller.
	t.Run("messages-tool-result-image", func(t *testing.T) {
		req, err := decodeRequest("anthropic", []byte(`{"model":"m","max_tokens":32,"messages":[{"role":"user","content":"shot"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"shot","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"chart"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}]}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		b, err := encodeAnthropicToolsBridge("m", req, anthropicToolTarget(), "openai-chat")
		if err != nil {
			t.Fatal(err)
		}
		if in, err := bedrockInput(b); err == nil {
			t.Fatalf("translated tool-result image was silently dropped: %+v body=%s", in.Messages, b)
		} else if !strings.Contains(err.Error(), "image_url") {
			t.Fatalf("error must name the unsupported part: %v body=%s", err, b)
		}
	})

	// The rejection happens before dispatch and surfaces as a client error.
	t.Run("client-error", func(t *testing.T) {
		r, err := bedrockDo(context.Background(), "us-east-1", []byte(cases[0].body))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d", r.StatusCode)
		}
	})
}
