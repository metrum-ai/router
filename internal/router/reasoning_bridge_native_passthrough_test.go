// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestReasoningNativeOpaqueHistoryHTTPPassthrough(t *testing.T) {
	for _, tc := range []struct{ dialect, path, field, raw, response string }{
		{"openai-responses", "/v1/responses", "input", `{"model":"native-stream","input":[{"type":"reasoning","id":"rs_1","encrypted_content":"opaque-cipher"},{"role":"user","content":"hi"}]}`, `{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`},
		{"anthropic", "/anthropic/v1/messages", "messages", `{"model":"native-stream","max_tokens":1024,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"visible-thought","signature":"opaque-signature"},{"type":"redacted_thinking","data":"opaque-redacted"}]},{"role":"user","content":"hi"}]}`, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`},
	} {
		t.Run(tc.dialect, func(t *testing.T) {
			var got map[string]any
			hits := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.response)
			}))
			defer up.Close()
			target := anthropicBridgeTarget()
			svc := nativeStreamTestService(t, up.URL, tc.dialect, []Target{target})
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.raw))
			r.Header.Set("Authorization", "Bearer "+testToken)
			r.Header.Set("x-api-key", testToken)
			r.Header.Set("anthropic-version", "2023-06-01")
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			var original map[string]any
			if err := json.Unmarshal([]byte(tc.raw), &original); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || hits != 1 || !reflect.DeepEqual(original[tc.field], got[tc.field]) {
				t.Fatalf("native history changed: status=%d hits=%d upstream=%#v response=%s", w.Code, hits, got, w.Body.String())
			}
		})
	}
}
