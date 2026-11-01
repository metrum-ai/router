// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPiResponsesPlainAnthropicSystem(t *testing.T) {
	var hits atomic.Int32
	var mu sync.Mutex
	var seen, lastSeen map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits.Add(1)
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer up.Close()
	svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{anthropicBridgeTarget()})
	post := func(input, extra string) *httptest.ResponseRecorder {
		body := `{"model":"native-stream","stream":true,"instructions":"base instruction","input":` + input + extra + `}`
		r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		mu.Lock()
		lastSeen = cloneMap(seen)
		mu.Unlock()
		return w
	}
	w := post(`[{"role":"system","content":[{"type":"input_text","text":"allow"},{"type":"input_text","text":"listed"}]},{"role":"developer","content":[{"type":"input_text","text":"developer"}]},{"role":"user","content":"hello"}]`, "")
	if w.Code != 200 || hits.Load() != 1 || lastSeen["system"] != "base instruction\n\nallowlisted\n\ndeveloper" {
		t.Fatalf("leading system lost: %d %s upstream=%#v hits=%d", w.Code, w.Body.String(), lastSeen, hits.Load())
	}
	messages := valueAsSlice(lastSeen["messages"])
	if len(messages) != 1 || messages[0].(map[string]any)["role"] != "user" {
		t.Fatalf("bad messages %#v", messages)
	}
	for _, input := range []string{`[{"role":"user","content":"hello"},{"role":"system","content":"too late"}]`, `[{"role":"system","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]},{"role":"user","content":"hello"}]`, `[{"role":"user","content":"hello"},{"role":"developer","content":"too late"}]`, `[{"type":"reasoning","id":"rs_foreign","summary":[]},{"role":"developer","content":"too late"},{"role":"user","content":"hello"}]`, `[{"role":"developer","content":"first"},{"type":"reasoning","id":"rs_foreign","summary":[]},{"role":"system","content":"too late"},{"role":"user","content":"hello"}]`, `[{"role":"developer","content":"no conversation"}]`, `[{"role":"developer","content":[{"type":"input_text","text":"sec"},{"type":"input_text","text":"ret"}]},{"role":"user","content":"hello"}]`, `[{"role":"developer","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]},{"role":"user","content":"hello"}]`, `{"input":[{"role":"system","content":"must not disappear"},{"role":"user","content":"hello"}]}`} {
		w = post(input, "")
		if w.Code != 400 || hits.Load() != 1 {
			t.Fatalf("unsupported system not rejected: %d %s hits=%d", w.Code, w.Body.String(), hits.Load())
		}
	}
	// An item reference must never be silently dropped, even in JSON mode.
	for _, extra := range []string{"", `,"text":{"format":{"type":"json_object"}}`} {
		w = post(`[{"type":"item_reference","id":"msg_saved"},{"role":"user","content":"continue"}]`, extra)
		if w.Code != 400 || hits.Load() != 1 {
			t.Fatalf("unsupported item was not rejected: %d %s hits=%d", w.Code, w.Body.String(), hits.Load())
		}
	}
}
