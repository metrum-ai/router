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

func TestContextManagementNull459(t *testing.T) {
	const beta = "context-management-2025-06-27"
	cases := []struct {
		name, field, callerBeta string
		allowed                 []string
		wantStatus              int
		wantField               bool
		wantBeta                string
	}{
		{"absent", "", "", nil, 200, false, ""},
		{"null-no-beta", `,"context_management":null`, "", nil, 200, false, ""},
		{"null-with-unpermitted-beta", `,"context_management":null`, beta, nil, 200, false, ""},
		{"non-null-no-beta", `,"context_management":{}`, "", nil, 400, false, ""},
		{"non-null-unpermitted-beta", `,"context_management":{}`, beta, nil, 400, false, ""},
		{"non-null-allowed-no-header", `,"context_management":{}`, "", []string{beta}, 400, false, ""},
		{"non-null-permitted", `,"context_management":{}`, beta, []string{beta}, 200, true, beta},
		{"nonnull-false", `,"context_management":false`, "", nil, 400, false, ""},
		{"other-container-null", `,"container":null`, "", nil, 400, false, ""},
		{"other-mcp-null", `,"mcp_servers":null`, "", nil, 400, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			var gotBody map[string]any
			gotBeta := ""
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				gotBeta = r.Header.Get("Anthropic-Beta")
				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{{Provider: "native", Model: "private-upstream", AllowedBetas: tc.allowed}})
			body := `{"model":"native-stream","max_tokens":128,"messages":[{"role":"user","content":"hi"}]` + tc.field + `}`
			r := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(body))
			r.Header.Set("x-api-key", testToken)
			r.Header.Set("anthropic-version", "2023-06-01")
			if tc.callerBeta != "" {
				r.Header.Set("anthropic-beta", tc.callerBeta)
			}
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.wantStatus == 400 {
				if hits.Load() != 0 || !strings.Contains(w.Body.String(), "requires a permitted anthropic-beta") {
					t.Fatalf("expected fail-closed before upstream: hits=%d body=%s", hits.Load(), w.Body.String())
				}
				return
			}
			_, present := gotBody["context_management"]
			if hits.Load() != 1 || present != tc.wantField || gotBeta != tc.wantBeta {
				t.Fatalf("hits=%d field-present=%t beta=%q", hits.Load(), present, gotBeta)
			}
		})
	}
}

func TestContextManagementNull459DecodeScope(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"context_management":null}`)
	for _, dialect := range []string{"anthropic", "openai-chat"} {
		req, err := decodeRequest(dialect, body, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, present := req.Raw["context_management"]
		if present != (dialect == "openai-chat") {
			t.Errorf("dialect=%s field-present=%t", dialect, present)
		}
	}
}
