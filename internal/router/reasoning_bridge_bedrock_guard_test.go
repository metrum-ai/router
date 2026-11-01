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

// Bedrock currently uses a Chat-to-Converse adapter, not native Anthropic.
// Until a native reasoning codec is approved, retain the existing fail-closed
// behavior instead of silently stripping provider-owned thinking history.
func TestReasoningBedrockOpaqueHistoryRejectedBeforeDispatch(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer up.Close()
	target := anthropicToolTarget()
	svc := nativeStreamTestService(t, up.URL, "openai-chat", []Target{target})
	provider := svc.cfg.Provider[target.Provider]
	provider.AuthScheme = "bedrock"
	svc.cfg.Provider[target.Provider] = provider
	plain, err := decodeRequest("anthropic", []byte(`{"messages":[{"role":"user","content":"hello"}],"max_tokens":1024}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	fit := svc.targetRequestShapeFit(target, plain, "anthropic", "openai-chat", requestTokenEstimateLogRecord{})
	if fit.FilterReason == "bedrock-opaque-reasoning-unsupported" {
		t.Fatal("plain Bedrock messages must not hit opaque-history guard")
	}
	for _, block := range []string{`{"type":"thinking","thinking":"thought","signature":"signature"}`, `{"type":"redacted_thinking","data":"cipher"}`} {
		for _, stream := range []string{"false", "true"} {
			body := `{"model":"native-stream","max_tokens":1024,"stream":` + stream + `,"messages":[{"role":"assistant","content":[` + block + `,{"type":"text","text":"hello"}]},{"role":"user","content":"continue"}]}`
			r := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if w.Code != 400 || hits.Load() != 0 {
				t.Fatalf("stream=%s code=%d hits=%d body=%s", stream, w.Code, hits.Load(), w.Body.String())
			}
		}
	}
}
