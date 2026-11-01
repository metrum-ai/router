// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponses625ReasoningIdentifierRewrite(t *testing.T) {
	var calls []map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
		}
		calls = append(calls, b)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", reasoning625)
		fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[%s],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", reasoning625)
	}))
	defer up.Close()
	target := anthropicBridgeTarget()
	target.ForceStoreFalse = true
	svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{target}, func(cfg *Config) { cfg.Server.Identifiers = identifierTestConfig() })
	post := func(input any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"model": "native-stream", "input": input, "stream": true})
		r := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w
	}
	first := post("hello")
	if first.Code != 200 {
		t.Fatalf("status=%d body=%s", first.Code, first.Body.String())
	}
	var item map[string]any
	for _, line := range strings.Split(first.Body.String(), "\n") {
		if strings.HasPrefix(line, "data:") {
			var e map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &e) == nil {
				if i, ok := e["item"].(map[string]any); ok && i["type"] == "reasoning" {
					item = i
				}
			}
		}
	}
	if item == nil || item["id"] == "rs_synthetic_1" || !strings.HasPrefix(fmt.Sprint(item["id"]), "mr_") || item["encrypted_content"] != "opaque+/=synthetic\\nbytes" {
		t.Fatalf("identifier or ciphertext not preserved: %#v", item)
	}
	second := post([]any{item, map[string]any{"role": "user", "content": "continue"}})
	if second.Code != 200 || len(calls) != 2 {
		t.Fatalf("status=%d count=%d body=%s", second.Code, len(calls), second.Body.String())
	}
	history := calls[1]["input"].([]any)
	got := history[0].(map[string]any)
	if got["id"] != "rs_synthetic_1" || got["encrypted_content"] != "opaque+/=synthetic\\nbytes" {
		t.Fatalf("provider ID or ciphertext not restored: %#v", got)
	}
}
