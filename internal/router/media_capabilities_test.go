// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDocumentInputCapabilityValidation(t *testing.T) {
	if err := validateInputModalities([]string{"text", "document"}); err != nil {
		t.Fatal(err)
	}
	if err := validateModalities([]string{"document"}); err == nil {
		t.Fatal("document should not be accepted as an output modality")
	}
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	group := cfg.Models["default"]
	group.Targets[0].InputModalities = []string{"text", "document"}
	cfg.Models["default"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
}

func TestMediaCapabilitiesAdmissionAndForwarding(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, supported := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "unary", true: "stream"}[stream], map[bool]string{false: "reject", true: "forward"}[supported]}, "/"), func(t *testing.T) {
				attempts := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempts++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					encoded, _ := json.Marshal(body)
					if !strings.Contains(string(encoded), "file-canary") {
						t.Errorf("document lost: %s", encoded)
					}
					writeJSON(w, 200, map[string]any{"id": "chatcmpl_test", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}})
				}))
				defer upstream.Close()
				cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
				cfg.Provider["media"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
				target := Target{Provider: "media", Model: "m", RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"anthropic"}, ValidationStatus: "passed"}}
				if supported {
					target.Modalities = []string{"text", "document"}
				}
				cfg.Models["media"] = ModelGroup{Strategy: "static", Targets: []Target{target}}
				cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "media")
				svc, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer svc.Close()
				body := map[string]any{"model": "media", "stream": stream, "max_tokens": 32, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "document", "source": map[string]any{"type": "file", "file_id": "file-canary"}}}}}}
				raw, _ := json.Marshal(body)
				r := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(string(raw)))
				r.Header.Set("Authorization", "Bearer "+testToken)
				rr := httptest.NewRecorder()
				svc.Handler().ServeHTTP(rr, r)
				if !supported {
					if rr.Code != 400 || attempts != 0 || !strings.Contains(rr.Body.String(), "unsupported_modality") {
						t.Fatalf("status=%d attempts=%d %s", rr.Code, attempts, rr.Body)
					}
				} else if attempts != 1 {
					t.Fatalf("not forwarded: %d %s", rr.Code, rr.Body)
				}
			})
		}
	}
}

func TestMediaCodecsPreserveDocuments(t *testing.T) {
	for _, in := range []string{"anthropic", "openai-chat", "openai-responses"} {
		raw := map[string]string{"anthropic": `{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file-canary"}}]}]}`, "openai-chat": `{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file-canary"}}]}]}`, "openai-responses": `{"input":[{"role":"user","content":[{"type":"input_file","file_id":"file-canary"}]}]}`}[in]
		req, err := decodeRequest(in, []byte(raw), http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		for _, out := range []string{"anthropic", "openai-chat", "openai-responses"} {
			b, err := encodeUpstream(out, "m", req)
			if err != nil || !strings.Contains(string(b), "file-canary") {
				t.Fatalf("%s -> %s: %s %v", in, out, b, err)
			}
		}
	}
}

func TestOpenAISchemaMapsToAnthropic(t *testing.T) {
	for _, tc := range []struct{ dialect, body string }{
		{"openai-chat", `{"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"x","strict":true,"schema":{"type":"object","additionalProperties":false}}}}`},
		{"openai-responses", `{"input":"hi","text":{"format":{"type":"json_schema","name":"x","strict":true,"schema":{"type":"object","additionalProperties":false}}}}`},
	} {
		req, err := decodeRequest(tc.dialect, []byte(tc.body), http.Header{})
		if err != nil {
			t.Fatal(err)
		}
		b, err := encodeUpstreamForTarget("anthropic", "m", req, Target{StructuredOutput: true})
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		config, _ := body["output_config"].(map[string]any)
		format, _ := config["format"].(map[string]any)
		schema, _ := format["schema"].(map[string]any)
		if format["type"] != "json_schema" || schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatalf("%s schema lost: %s", tc.dialect, b)
		}
	}
}

func TestAnthropicStructuredFormatMapping(t *testing.T) {
	req, err := decodeRequest("anthropic", []byte(`{"messages":[{"role":"user","content":"hi"}],"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}}}}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := encodeUpstreamForTarget("openai-chat", "m", req, Target{StructuredOutput: true})
	if err != nil {
		t.Fatal(err)
	}
	if !requestHasStructuredOutput(req) || !strings.Contains(string(b), `"response_format":{"json_schema"`) {
		t.Fatalf("format lost: %s", b)
	}
	if !unknownAnthropicBlocks([]any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "future_block", "text": "never flatten"}}}}) {
		t.Fatal("unknown accepted")
	}
}
