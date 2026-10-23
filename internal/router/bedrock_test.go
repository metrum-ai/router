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
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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

func TestBedrockThrottleFailover(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
	t.Setenv("AWS_SESSION_TOKEN", "")
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
	r, err := bedrockDo(context.Background(), "us-east-1", []byte(`{"model":"mistral.mistral-large-3-675b-instruct","messages":[{"role":"user","content":"Say OK"}],"max_tokens":16,"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 200 || !strings.Contains(string(b), "[DONE]") || !strings.Contains(string(b), "prompt_tokens") {
		t.Fatalf("status %d: %s", r.StatusCode, b)
	}
}
