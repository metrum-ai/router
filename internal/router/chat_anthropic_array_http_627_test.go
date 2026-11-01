// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatAnthropic627ArrayResultHTTP(t *testing.T) {
	var body map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
	}))
	defer up.Close()
	svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{anthropicToolTarget()})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","messages":[{"role":"user","content":"read"},{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"read","arguments":"{\"path\":\"quote.txt\"}"}}]},{"role":"tool","tool_call_id":"call_a","content":[{"type":"text","text":"E2E-TOOL-"},{"type":"text","text":"RESULT-7842\n"}]}]}`))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("HTTP status=%d body=%s", w.Code, w.Body.String())
	}
	messages := valueAsSlice(body["messages"])
	if len(messages) != 3 {
		t.Fatalf("messages missing: %#v", body)
	}
	result := valueAsSlice(messages[2].(map[string]any)["content"])[0].(map[string]any)
	blocks := valueAsSlice(result["content"])
	if result["tool_use_id"] != "call_a" || len(blocks) != 2 || blocks[0].(map[string]any)["text"] != "E2E-TOOL-" || blocks[1].(map[string]any)["text"] != "RESULT-7842\n" {
		t.Fatalf("tool text or id lost: %#v", result)
	}
}
