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

// Regression expectation under investigation: native Responses should retain
// the caller's documented reasoning context control, not just effort/summary.
func TestResponses625NativeReasoningContextPreserved(t *testing.T) {
	var got map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer up.Close()
	target := anthropicBridgeTarget()
	target.Reasoning = ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, SupportsSummaries: true}
	target.ForceStoreFalse = true
	svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{target})
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","input":"hi","reasoning":{"effort":"low","summary":"auto","context":"all_turns"}}`))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	want := map[string]any{"effort": "low", "summary": "auto", "context": "all_turns"}
	if w.Code != 200 || !reflect.DeepEqual(got["reasoning"], want) {
		t.Fatalf("native reasoning context lost: status=%d got=%#v want=%#v response=%s", w.Code, got["reasoning"], want, w.Body.String())
	}
}
