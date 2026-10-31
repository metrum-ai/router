// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOpenAIAnthropicToolBridgeRejectsMalformedBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer up.Close()
	svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{anthropicToolTarget()})
	body := `{"model":"native-stream","messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"not-json"}}]}]}`
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if w.Code != 400 || calls.Load() != 0 {
		t.Fatalf("malformed request: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
}
