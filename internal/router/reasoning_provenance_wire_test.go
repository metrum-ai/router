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
	"time"
)

// provenanceWireEnv exposes the signing key through the process environment,
// exactly as an operator would.
func provenanceWireEnv(t *testing.T, mode string) {
	t.Helper()
	t.Setenv("RZ_TEST_KEY", "provenance-wiring-test-material-0123456789")
	t.Setenv("RZ_TEST_MODE", mode)
}

func provenanceWireService(t *testing.T, upstream *httptest.Server, mode string, mutate func(*Config)) *Service {
	t.Helper()
	provenanceWireEnv(t, mode)
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.ReasoningProvenance = ReasoningProvenanceConfig{Mode: mode, KeyEnv: "RZ_TEST_KEY", KeyID: "k1", TTL: "1h"}
	cfg.Provider["mock"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai", APIKey: "provider-key", KeyID: "acct-a"}
	cfg.Models["native"] = ModelGroup{Strategy: "failover", Targets: []Target{
		{Provider: "mock", Model: "mock-model", Dialect: "openai-responses"},
	}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "native")
	if mutate != nil {
		mutate(cfg)
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func provenanceTestBinding(t *testing.T, mode string) reasoningProvenanceBinding {
	t.Helper()
	provenanceWireEnv(t, mode)
	binding, err := newReasoningProvenanceBinding(ReasoningProvenanceConfig{Mode: mode, KeyEnv: "RZ_TEST_KEY", KeyID: "k1", TTL: "1h"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func provenanceOpenAIScope() reasoningScope {
	return reasoningScope{Dialect: "openai-responses", Provider: "mock", KeyID: "acct-a", Model: "mock-model"}
}

func TestReasoningProvenanceConfigValidation(t *testing.T) {
	if err := validateReasoningProvenanceConfig(ReasoningProvenanceConfig{}); err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
	if err := validateReasoningProvenanceConfig(ReasoningProvenanceConfig{Mode: "off"}); err != nil {
		t.Fatalf("explicit off rejected: %v", err)
	}
	for name, cfg := range map[string]ReasoningProvenanceConfig{
		"observe without key_env": {Mode: "observe"},
		"enforce without key_env": {Mode: "enforce"},
		"unknown mode":            {Mode: "audit", KeyEnv: "RZ_TEST_KEY"},
		"bad env name":            {Mode: "observe", KeyEnv: "not a name"},
		"bad key id":              {Mode: "observe", KeyEnv: "RZ_TEST_KEY", KeyID: "K1"},
		"ttl too long":            {Mode: "observe", KeyEnv: "RZ_TEST_KEY", TTL: "31d"},
		"ttl not a duration":      {Mode: "observe", KeyEnv: "RZ_TEST_KEY", TTL: "1 hour"},
		"key_env while off":       {Mode: "off", KeyEnv: "RZ_TEST_KEY"},
	} {
		if err := validateReasoningProvenanceConfig(cfg); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
	provenanceWireEnv(t, "observe")
	if _, err := newReasoningProvenanceBinding(ReasoningProvenanceConfig{Mode: "observe", KeyEnv: "MISSING_RZ_KEY"}, nil); err == nil {
		t.Fatal("missing key secret accepted")
	}
	binding, err := newReasoningProvenanceBinding(ReasoningProvenanceConfig{Mode: "observe", KeyEnv: "RZ_TEST_KEY"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if binding.ttl != 24*time.Hour || binding.key.id != "k1" || binding.off() {
		t.Fatalf("unexpected defaults: %+v", binding)
	}
	if off, err := newReasoningProvenanceBinding(ReasoningProvenanceConfig{}, nil); err != nil || !off.off() || off.wraps() {
		t.Fatalf("off mode must not wrap: %+v %v", off, err)
	}
}

func TestReasoningProvenanceWrapsAndUnwrapsSameOrigin(t *testing.T) {
	binding := provenanceTestBinding(t, "enforce")
	now := time.Now().UTC()
	scope := provenanceOpenAIScope()
	envelope, _ := binding.wrapOutboundValue(reasoningKindEncryptedContent, "provider-ciphertext", scope, now, &reasoningProvenanceStats{})
	if !strings.HasPrefix(envelope, "rtr_rz1.k1.") || len(envelope) <= len("provider-ciphertext") {
		t.Fatalf("value was not wrapped: %q", envelope)
	}
	request := map[string]any{"input": []any{map[string]any{"type": "reasoning", "encrypted_content": envelope}}}
	adjusted, stats := binding.applyInbound(request, "openai-responses", scope, now)
	if stats.unwrapped != 1 || len(stats.dropped) != 0 {
		t.Fatalf("unexpected stats: %s", stats.summary())
	}
	item := adjusted["input"].([]any)[0].(map[string]any)
	if item["encrypted_content"] != "provider-ciphertext" {
		t.Fatalf("value was not unwrapped: %v", item["encrypted_content"])
	}
}

func TestReasoningProvenanceDropsForeignOrigin(t *testing.T) {
	for _, mode := range []string{"observe", "enforce"} {
		binding := provenanceTestBinding(t, mode)
		now := time.Now().UTC()
		origin := provenanceOpenAIScope()
		foreign := origin
		foreign.KeyID = "acct-b"
		envelope, _ := binding.wrapOutboundValue(reasoningKindEncryptedContent, "ciphertext", foreign, now, &reasoningProvenanceStats{})
		request := map[string]any{"input": []any{
			map[string]any{"type": "reasoning", "encrypted_content": envelope},
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "keep me"}}},
		}}
		adjusted, stats := binding.applyInbound(request, "openai-responses", origin, now)
		if stats.dropped["scope-mismatch"] != 1 {
			t.Fatalf("%s: expected scope-mismatch, got %s", mode, stats.summary())
		}
		items := adjusted["input"].([]any)
		if mode == "enforce" {
			if len(items) != 1 {
				t.Fatalf("enforce must drop the foreign item: %v", items)
			}
		} else if len(items) != 2 {
			t.Fatalf("observe must keep the item: %v", items)
		}
	}
}

func TestReasoningProvenanceUnknownOriginBehaviour(t *testing.T) {
	now := time.Now().UTC()
	scope := provenanceOpenAIScope()
	request := map[string]any{"input": []any{map[string]any{"type": "reasoning", "encrypted_content": "raw-provider-ciphertext"}}}
	observe := provenanceTestBinding(t, "observe")
	adjusted, stats := observe.applyInbound(request, "openai-responses", scope, now)
	if stats.dropped["unknown-origin"] != 1 || len(adjusted["input"].([]any)) != 1 {
		t.Fatalf("observe must keep and trace unknown origin: %s", stats.summary())
	}
	enforce := provenanceTestBinding(t, "enforce")
	adjusted, stats = enforce.applyInbound(request, "openai-responses", scope, now)
	if stats.dropped["unknown-origin"] != 1 || len(adjusted["input"].([]any)) != 0 {
		t.Fatalf("enforce must drop unknown origin: %s", stats.summary())
	}
}

func TestReasoningProvenanceAnthropicBlocks(t *testing.T) {
	binding := provenanceTestBinding(t, "enforce")
	now := time.Now().UTC()
	scope := reasoningScope{Dialect: "anthropic", Provider: "mock", KeyID: "acct-a", Model: "claude-test"}
	signature, _ := binding.wrapOutboundValue(reasoningKindThinkingSig, "sig-1", scope, now, &reasoningProvenanceStats{})
	redacted, _ := binding.wrapOutboundValue(reasoningKindRedactedThinking, "opaque-redacted", scope, now, &reasoningProvenanceStats{})
	request := map[string]any{"messages": []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "reasoning text", "signature": signature},
			map[string]any{"type": "redacted_thinking", "data": redacted},
			map[string]any{"type": "tool_use", "id": "tu_1", "name": "lookup", "input": map[string]any{"q": "x"}},
		}},
	}}
	adjusted, stats := binding.applyInbound(request, "anthropic", scope, now)
	if stats.unwrapped != 2 || len(stats.dropped) != 0 {
		t.Fatalf("unexpected stats: %s", stats.summary())
	}
	blocks := adjusted["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if blocks[0].(map[string]any)["signature"] != "sig-1" || blocks[1].(map[string]any)["data"] != "opaque-redacted" {
		t.Fatalf("blocks were not unwrapped: %v", blocks)
	}

	foreign := scope
	foreign.KeyID = "acct-b"
	foreignSignature, _ := binding.wrapOutboundValue(reasoningKindThinkingSig, "sig-2", foreign, now, &reasoningProvenanceStats{})
	request["messages"].([]any)[0].(map[string]any)["content"] = []any{
		map[string]any{"type": "thinking", "thinking": "reasoning text", "signature": foreignSignature},
		map[string]any{"type": "tool_use", "id": "tu_1", "name": "lookup", "input": map[string]any{"q": "x"}},
	}
	adjusted, stats = binding.applyInbound(request, "anthropic", scope, now)
	if stats.dropped["scope-mismatch"] != 1 {
		t.Fatalf("expected scope mismatch: %s", stats.summary())
	}
	blocks = adjusted["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(blocks) != 1 || blocks[0].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("thinking block must be dropped while tool_use stays: %v", blocks)
	}
}

func TestReasoningProvenanceLeavesUserContentAlone(t *testing.T) {
	binding := provenanceTestBinding(t, "enforce")
	now := time.Now().UTC()
	request := map[string]any{"messages": []any{
		map[string]any{"role": "tool", "content": `{"type":"reasoning","encrypted_content":"user text, not provenance"}`},
		map[string]any{"role": "user", "content": "plain text"},
	}}
	adjusted, stats := binding.applyInbound(request, "openai-chat", provenanceOpenAIScope(), now)
	if !stats.empty() {
		t.Fatalf("user content was rewritten: %s", stats.summary())
	}
	if adjusted["messages"].([]any)[0].(map[string]any)["content"] != `{"type":"reasoning","encrypted_content":"user text, not provenance"}` {
		t.Fatal("tool result content changed")
	}
}

func TestReasoningProvenanceOutboundRawAndFrames(t *testing.T) {
	binding := provenanceTestBinding(t, "observe")
	now := time.Now().UTC()
	scope := provenanceOpenAIScope()
	raw := []byte(`{"id":"resp_1","output":[{"type":"reasoning","encrypted_content":"provider-ciphertext"}],"output_text":"done"}`)
	wrapped := binding.wrapOutboundRaw(raw, scope, now, &reasoningProvenanceStats{})
	if !strings.Contains(string(wrapped), "rtr_rz1.k1.") || strings.Contains(string(wrapped), `"encrypted_content":"provider-ciphertext"`) {
		t.Fatalf("unary response was not wrapped: %s", wrapped)
	}
	frame := []byte(`{"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"ciphertext-2"}}`)
	wrappedFrame := binding.wrapOutboundFrame(frame, scope, now, &reasoningProvenanceStats{})
	var parsed map[string]any
	if err := json.Unmarshal(wrappedFrame, &parsed); err != nil {
		t.Fatal(err)
	}
	item := parsed["item"].(map[string]any)
	if !strings.HasPrefix(item["encrypted_content"].(string), "rtr_rz1.") {
		t.Fatalf("streams frame was not wrapped: %s", wrappedFrame)
	}
	// Unrelated frames are returned untouched, byte for byte.
	other := []byte(`{"type":"response.output_text.delta","delta":"encrypted_content"}`)
	if got := binding.wrapOutboundFrame(other, scope, now, &reasoningProvenanceStats{}); string(got) != string(other) {
		t.Fatalf("unrelated frame changed: %s", got)
	}
	// Already-bound values are not double wrapped.
	envelope, _ := binding.wrapOutboundValue(reasoningKindEncryptedContent, "value", scope, now, &reasoningProvenanceStats{})
	if again, _ := binding.wrapOutboundValue(reasoningKindEncryptedContent, envelope, scope, now, &reasoningProvenanceStats{}); again != envelope {
		t.Fatal("value was wrapped twice")
	}
}

func TestReasoningProvenanceOffChangesNothing(t *testing.T) {
	binding := provenanceTestBinding(t, "off")
	now := time.Now().UTC()
	request := map[string]any{"input": []any{map[string]any{"type": "reasoning", "encrypted_content": "raw"}}}
	adjusted, stats := binding.applyInbound(request, "openai-responses", provenanceOpenAIScope(), now)
	if !stats.empty() || adjusted["input"].([]any)[0].(map[string]any)["encrypted_content"] != "raw" {
		t.Fatal("off mode modified the request")
	}
	raw := []byte(`{"output":[{"type":"reasoning","encrypted_content":"raw"}]}`)
	if got := binding.wrapOutboundRaw(raw, provenanceOpenAIScope(), now, &reasoningProvenanceStats{}); string(got) != string(raw) {
		t.Fatal("off mode modified the response")
	}
}

func TestReasoningProvenanceStatsAreBounded(t *testing.T) {
	var stats reasoningProvenanceStats
	for i := 0; i < 20; i++ {
		stats.drop("class-" + itoaSmall(i))
	}
	if len(stats.dropped) > 9 {
		t.Fatalf("too many classes tracked: %d", len(stats.dropped))
	}
	message := stats.summary()
	if strings.Contains(message, "raw") || strings.Contains(message, "rtr_rz1") {
		t.Fatalf("summary leaked a value: %s", message)
	}
	if !strings.Contains(message, "other=") {
		t.Fatalf("overflow class missing: %s", message)
	}
}

// End-to-end: a Responses request carrying history minted for this account is
// forwarded, and the unary provider response is returned wrapped.
func TestReasoningProvenanceEndToEndUnary(t *testing.T) {
	var gotBody atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		gotBody.Store(string(buf[:n]))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"mock-model","status":"completed","output":[{"type":"reasoning","encrypted_content":"fresh-provider-ciphertext","summary":[]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`))
	}))
	defer upstream.Close()
	svc := provenanceWireService(t, upstream, "enforce", nil)
	binding := svc.provenance
	scope := reasoningScope{Dialect: "openai-responses", Provider: "mock", KeyID: "acct-a", Model: "mock-model", Group: "native"}
	envelope, _ := binding.wrapOutboundValue(reasoningKindEncryptedContent, "prior-provider-ciphertext", scope, time.Now().UTC(), &reasoningProvenanceStats{})
	body := `{"model":"native","input":[{"type":"reasoning","encrypted_content":"` + envelope + `"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if sent, _ := gotBody.Load().(string); !strings.Contains(sent, "prior-provider-ciphertext") || strings.Contains(sent, "rtr_rz1.") {
		t.Fatalf("history was not unwrapped for its origin target: %s", sent)
	}
	if !strings.Contains(rr.Body.String(), "rtr_rz1.") || strings.Contains(rr.Body.String(), "fresh-provider-ciphertext") {
		t.Fatalf("response was not wrapped: %s", rr.Body.String())
	}
}

// End-to-end: in enforce mode history minted for another account is dropped
// before dispatch instead of being forwarded.
func TestReasoningProvenanceEndToEndCrossAccountDrop(t *testing.T) {
	var gotBody atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 8192)
		n, _ := r.Body.Read(buf)
		gotBody.Store(string(buf[:n]))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"mock-model","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`))
	}))
	defer upstream.Close()
	svc := provenanceWireService(t, upstream, "enforce", nil)
	foreignScope := reasoningScope{Dialect: "openai-responses", Provider: "mock", KeyID: "acct-b", Model: "mock-model", Group: "native"}
	envelope, _ := svc.provenance.wrapOutboundValue(reasoningKindEncryptedContent, "foreign-ciphertext", foreignScope, time.Now().UTC(), &reasoningProvenanceStats{})
	body := `{"model":"native","input":[{"type":"reasoning","encrypted_content":"` + envelope + `"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	sent, _ := gotBody.Load().(string)
	if strings.Contains(sent, "foreign-ciphertext") || strings.Contains(sent, "rtr_rz1") {
		t.Fatalf("foreign history reached the provider: %s", sent)
	}
	if !strings.Contains(sent, "hi") {
		t.Fatalf("request body lost user content: %s", sent)
	}
}

// End-to-end streaming: a reasoning item streamed to the caller is bound, and
// the same value is unwrapped when the caller replays it.
func TestReasoningProvenanceEndToEndStreaming(t *testing.T) {
	streamBody := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_s\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"encrypted_content\":\"streamed-provider-ciphertext\",\"summary\":[]}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_s\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n"
	var replayBody atomic.Value
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 8192)
		n, _ := r.Body.Read(buf)
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(streamBody))
			return
		}
		replayBody.Store(string(buf[:n]))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_s2\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}))
	defer upstream.Close()
	svc := provenanceWireService(t, upstream, "enforce", nil)
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		return rr
	}
	first := post(`{"model":"native","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`)
	if first.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", first.Code, first.Body.String())
	}
	if !strings.Contains(first.Body.String(), "rtr_rz1.") || strings.Contains(first.Body.String(), "streamed-provider-ciphertext") {
		t.Fatalf("streamed reasoning was not bound: %s", first.Body.String())
	}
	var envelope string
	for _, line := range strings.Split(first.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil {
			continue
		}
		if item, ok := event["item"].(map[string]any); ok && item["type"] == "reasoning" {
			envelope, _ = item["encrypted_content"].(string)
		}
	}
	if !strings.HasPrefix(envelope, "rtr_rz1.") {
		t.Fatalf("missing bound item in stream: %s", first.Body.String())
	}
	replay := `{"model":"native","input":[{"type":"reasoning","id":"rs_1","encrypted_content":"` + envelope + `","summary":[]},{"type":"message","role":"user","content":[{"type":"input_text","text":"again"}]}],"stream":true}`
	if second := post(replay); second.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", second.Code, second.Body.String())
	}
	sent, _ := replayBody.Load().(string)
	if !strings.Contains(sent, "streamed-provider-ciphertext") || strings.Contains(sent, "rtr_rz1.") {
		t.Fatalf("streamed history was not unwrapped for its origin: %s", sent)
	}
}

// A provider value above the binding limit is never emitted unbound in enforce
// mode; observe keeps it and traces the oversize class.
func TestReasoningProvenanceOversizeOutbound(t *testing.T) {
	now := time.Now().UTC()
	scope := provenanceOpenAIScope()
	atLimit := strings.Repeat("a", maxReasoningOpaqueBytes)
	overLimit := atLimit + "b"
	for _, mode := range []string{"observe", "enforce"} {
		binding := provenanceTestBinding(t, mode)
		var stats reasoningProvenanceStats
		got, dropped := binding.wrapOutboundValue(reasoningKindEncryptedContent, overLimit, scope, now, &stats)
		if stats.dropped["oversized"] != 1 {
			t.Fatalf("%s: expected oversize class, got %s", mode, stats.summary())
		}
		if mode == "enforce" {
			if !dropped || got != "" {
				t.Fatalf("enforce emitted an unbound oversize value")
			}
		} else {
			if dropped || got != overLimit {
				t.Fatalf("observe must keep the oversize value")
			}
		}
		// At the limit the value is bound, not dropped.
		var limitStats reasoningProvenanceStats
		bound, limitDrop := binding.wrapOutboundValue(reasoningKindEncryptedContent, atLimit, scope, now, &limitStats)
		if limitDrop || limitStats.wrapped != 1 || !strings.HasPrefix(bound, reasoningEnvelopePrefix+".") {
			t.Fatalf("%s: value at the limit was not bound: %s", mode, limitStats.summary())
		}
	}
}

// In enforce mode an oversize item is removed from the response document rather
// than handed to the caller unbound.
func TestReasoningProvenanceOversizeDropsResponseItem(t *testing.T) {
	now := time.Now().UTC()
	scope := provenanceOpenAIScope()
	overLimit := strings.Repeat("a", maxReasoningOpaqueBytes+1)
	raw := []byte(`{"output":[{"type":"reasoning","encrypted_content":"` + overLimit + `"},{"type":"message","content":[]}]}`)
	enforce := provenanceTestBinding(t, "enforce")
	var stats reasoningProvenanceStats
	wrapped := enforce.wrapOutboundRaw(raw, scope, now, &stats)
	if strings.Contains(string(wrapped), overLimit[:64]) || strings.Contains(string(wrapped), "reasoning") {
		t.Fatalf("enforce returned an unbound oversize value: %d bytes", len(wrapped))
	}
	if stats.dropped["oversized"] != 1 {
		t.Fatalf("expected oversize class: %s", stats.summary())
	}
	observe := provenanceTestBinding(t, "observe")
	wrapped = observe.wrapOutboundRaw(raw, scope, now, &reasoningProvenanceStats{})
	if !strings.Contains(string(wrapped), overLimit[:64]) {
		t.Fatal("observe must pass the oversize value through")
	}
}
