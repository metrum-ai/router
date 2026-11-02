// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The Messages bridge builds content part arrays as []map[string]any. The shared
// decoders must accept that shape exactly like decoded JSON ([]any), otherwise a
// mixed-media tool result is silently dispatched as an empty output.
func TestDecodeContentPartsTypedSlice679(t *testing.T) {
	typed := []map[string]any{
		{"type": "text", "text": "a"},
		{"type": "image", "source": map[string]any{"type": "url", "url": "https://example.invalid/img"}},
		{"type": "text", "text": "b"},
	}
	parts := decodeContentParts(typed)
	if len(parts) != 3 || parts[0].Text != "a" || parts[1].Type != "image" || parts[2].Text != "b" {
		t.Fatalf("typed slice lost content: %#v", parts)
	}
	// Nested Anthropic tool_result arrays built as typed slices must also surface.
	nested := []map[string]any{{"type": "tool_result", "tool_use_id": "c", "content": []map[string]any{{"type": "text", "text": "x"}, {"type": "image", "source": map[string]any{"type": "url", "url": "https://example.invalid/n"}}}}}
	if got := decodeContentParts(nested); len(got) != 2 || got[0].Text != "x" || got[1].Type != "image" {
		t.Fatalf("nested typed tool_result lost content: %#v", got)
	}
	// Pure text typed slices must stay exact, keeping the #672 no-invented-separator rule.
	if text, ok := toolResultText([]map[string]any{{"type": "text", "text": "marker-a-"}, {"type": "text", "text": "multipart"}}); !ok || text != "marker-a-multipart" {
		t.Fatalf("typed text result: %q %v", text, ok)
	}
	// Mixed media must not take the exact-text path.
	if _, ok := toolResultText(typed); ok {
		t.Fatal("mixed media treated as text")
	}
}

func TestMessagesResponsesMixedToolResult679(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts string
		want  []string
	}{
		{"mixed", `[{"type":"text","text":"a"},{"type":"image","source":{"type":"url","url":"https://example.invalid/img"}},{"type":"text","text":"b"}]`, []string{"text:a", "image", "text:b"}},
		{"image-first", `[{"type":"image","source":{"type":"url","url":"https://example.invalid/first"}},{"type":"text","text":"after"}]`, []string{"image", "text:after"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			var dispatched map[string]any
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if err := json.NewDecoder(r.Body).Decode(&dispatched); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"id":"r","object":"response","status":"completed","model":"m","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`))
			}))
			defer up.Close()
			target := anthropicToolTarget()
			target.InputModalities = []string{"text", "image"}
			target.Bridges.ChatToResponses = DialectBridgeSupport{Enabled: true, Tools: true, Images: true, ToolChoice: true, ParallelToolCalls: true}
			svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{target})
			body := `{"model":"native-stream","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"read"},{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":` + tc.parts + `}]}]}`
			r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			r.Header.Set("x-api-key", testToken)
			r.Header.Set("anthropic-version", "2023-06-01")
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if w.Code != 200 || hits.Load() != 1 {
				t.Fatalf("status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
			}
			var output any
			for _, raw := range valueAsSlice(dispatched["input"]) {
				item, _ := raw.(map[string]any)
				if stringValue(item["type"]) == "function_call_output" {
					if stringValue(item["call_id"]) != "c" {
						t.Fatalf("call_id lost: %#v", item)
					}
					output = item["output"]
				}
			}
			parts := valueAsSlice(output)
			if len(parts) != len(tc.want) {
				t.Fatalf("dispatched output %#v, want %v", output, tc.want)
			}
			for i, want := range tc.want {
				part, _ := parts[i].(map[string]any)
				switch {
				case want == "image":
					if stringValue(part["type"]) != "input_image" || stringValue(part["image_url"]) == "" {
						t.Fatalf("part %d not an image: %#v", i, part)
					}
				default:
					if stringValue(part["type"]) != "input_text" || "text:"+stringValue(part["text"]) != want {
						t.Fatalf("part %d: %#v want %s", i, part, want)
					}
				}
			}
		})
	}
}
