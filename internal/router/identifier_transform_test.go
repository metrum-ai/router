// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func identifierTestConfig() IdentifierConfig {
	return IdentifierConfig{Transform: IdentifierTransformConfig{Current: IdentifierKeyConfig{KeyID: "idk-test-A", Key: strings.Repeat("ab", 64)}}}
}
func identifierTestEpoch(t testing.TB, keyID string) string {
	t.Helper()
	epoch, err := epochFromKeyID(keyID)
	if err != nil {
		t.Fatal(err)
	}
	return epoch
}
func testIdentifierTransform(t testing.TB) IdentifierTransform {
	t.Helper()
	tr, e := newIdentifierTransform(identifierTestConfig())
	if e != nil {
		t.Fatal(e)
	}
	return tr
}
func TestID001RoundTrip(t *testing.T) {
	tr := testIdentifierTransform(t)
	epoch := identifierTestEpoch(t, "idk-test-A")
	for _, id := range []string{"", "call_abc", "msg_123", "mr_upstream", "日本語/\"\n", strings.Repeat("x", 4096)} {
		encoded := tr.Encode(id)
		if !strings.HasPrefix(encoded, "mr_"+epoch) || encoded != tr.Encode(id) {
			t.Fatal("wire format or determinism")
		}
		got, e := tr.Decode(encoded)
		if e != nil || got != id {
			t.Fatalf("roundtrip %q: %v", id, e)
		}
	}
	c := identifierTestConfig()
	c.Transform.Current.Key = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xab}, 64))
	other, e := newIdentifierTransform(c)
	if e != nil || other.Encode("id") != tr.Encode("id") {
		t.Fatal("base64 key parity", e)
	}
}
func TestID002Rotation(t *testing.T) {
	old := testIdentifierTransform(t)
	c := identifierTestConfig()
	previous := c.Transform.Current
	previous.ValidUntil = time.Now().Add(time.Hour)
	c.Transform.Previous = &previous
	c.Transform.Current = IdentifierKeyConfig{KeyID: "idk-test-B", Key: strings.Repeat("cd", 64)}
	tr, e := newIdentifierTransform(c)
	if e != nil {
		t.Fatal(e)
	}
	if got, e := tr.Decode(old.Encode("call")); e != nil || got != "call" {
		t.Fatal(got, e)
	}
	if !strings.HasPrefix(tr.Encode("call"), "mr_"+identifierTestEpoch(t, "idk-test-B")) {
		t.Fatal("not current")
	}
	a, b := tr.KeyIDs()
	if a != "idk-test-B" || b != "idk-test-A" {
		t.Fatal(a, b)
	}
	tr.(*identifierTransform).validUntil = time.Now().Add(-time.Second)
	if _, e = tr.Decode(old.Encode("call")); e == nil || e.Error() != "id-decode-unknown-epoch" {
		t.Fatal(e)
	}
}
func TestID003Validation(t *testing.T) {
	for name, edit := range map[string]func(*IdentifierConfig){
		"missing": func(c *IdentifierConfig) { c.Transform.Current.Key = "" },
		"length":  func(c *IdentifierConfig) { c.Transform.Current.Key = strings.Repeat("ab", 32) },
		"empty_key_id": func(c *IdentifierConfig) { c.Transform.Current.KeyID = "" },
		"whitespace_key_id": func(c *IdentifierConfig) { c.Transform.Current.KeyID = "bad id" },
		"collision": func(c *IdentifierConfig) {
			p := c.Transform.Current
			p.ValidUntil = time.Now().Add(time.Hour)
			c.Transform.Previous = &p
		},
		"missing_expiry": func(c *IdentifierConfig) {
			p := c.Transform.Current
			p.KeyID = "idk-test-B"
			c.Transform.Previous = &p
		},
		"expired": func(c *IdentifierConfig) {
			p := c.Transform.Current
			p.KeyID = "idk-test-B"
			p.ValidUntil = time.Now().Add(-time.Hour)
			c.Transform.Previous = &p
		},
		"over_30d": func(c *IdentifierConfig) {
			p := c.Transform.Current
			p.KeyID = "idk-test-B"
			p.ValidUntil = time.Now().Add(31 * 24 * time.Hour)
			c.Transform.Previous = &p
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := identifierTestConfig()
			edit(&c)
			if _, e := newIdentifierTransform(c); e == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
	var c IdentifierConfig
	if e := yaml.Unmarshal([]byte("transform:\n  current: {}\n  previous: {}\n  third: {}\n"), &c); e == nil {
		t.Fatal("third epoch accepted")
	}
	cfg := minimalConfig(t)
	cfg.Server.Identifiers = IdentifierConfig{}
	if e := cfg.Validate(); e == nil {
		t.Fatal("default must require key")
	}
}
func TestID004DecodeBuckets(t *testing.T) {
	tr := testIdentifierTransform(t)
	epoch := identifierTestEpoch(t, "idk-test-A")
	valid := tr.Encode("private-id")
	b, _ := base64.RawURLEncoding.DecodeString(valid[4:])
	b[len(b)-1] ^= 1
	for input, want := range map[string]string{
		"mr_": "id-decode-malformed",
		"mr_" + epoch + "!": "id-decode-malformed",
		"mr_Zabcd":          "id-decode-unknown-epoch",
		"mr_" + epoch + base64.RawURLEncoding.EncodeToString(b): "id-decode-auth-failed",
	} {
		if _, e := tr.Decode(input); e == nil || e.Error() != want {
			t.Fatalf("want %s, got %v", want, e)
		}
	}
}
func TestID005Ingress(t *testing.T) {
	tr := testIdentifierTransform(t)
	for _, field := range []string{"tool_call_id", "tool_use_id", "call_id", "previous_response_id"} {
		raw := []byte(`{"` + field + `":"` + tr.Encode("upstream") + `","content":"mr_untouched"}`)
		out, e := decodeIdentifierFields(raw, tr)
		if e != nil || !bytes.Contains(out, []byte(`"upstream"`)) || !bytes.Contains(out, []byte(`"mr_untouched"`)) {
			t.Fatal(string(out), e)
		}
	}

	// IDs inside tool input are application data, not protocol references.
	raw := []byte(`{"messages":[{"content":[{"type":"tool_use","input":{"call_id":"mr_A!"}}]}]}`)
	if out, err := decodeIdentifierFields(raw, tr); err != nil || !bytes.Equal(out, raw) {
		t.Fatalf("tool input changed: %s %v", out, err)
	}
	svc := nativeStreamTestService(t, "http://localhost", "openai-chat", []Target{{Provider: "native", Model: "test"}})
	svc.cfg.Server.Identifiers = identifierTestConfig()
	svc.identifiers = tr
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"tool","tool_call_id":"mr_A!","content":"ok"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "invalid-tool-call-id") {
		t.Fatal(rr.Code, rr.Body.String())
	}
}
func TestID006ReadinessAndPassthrough(t *testing.T) {
	svc := nativeStreamTestService(t, "http://localhost", "openai-chat", []Target{{Provider: "native", Model: "test"}})
	svc.cfg.Server.Identifiers = identifierTestConfig()
	svc.identifiers = nil
	for path, status := range map[string]int{"/healthz": 200, "/readyz": 503, "/v1/chat/completions": 503, "/v1/responses": 503, "/v1/messages": 503, "/v1/messages/count_tokens": 503} {
		method := "GET"
		if strings.HasPrefix(path, "/v1/") {
			method = "POST"
		}
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, httptest.NewRequest(method, path, nil))
		if rr.Code != status {
			t.Fatal(path, rr.Code)
		}
		if strings.Contains(rr.Body.String(), svc.cfg.Server.Identifiers.Transform.Current.Key) {
			t.Fatal("key leak")
		}
	}
	svc.cfg.Server.Identifiers = IdentifierConfig{Mode: "passthrough"}
	if !svc.identifiersAvailable() {
		t.Fatal("passthrough unavailable")
	}
	raw := []byte("data: {\"id\":\"original\"}\n\n")
	r := nativeIDRewriter{}
	if !bytes.Equal(r.rewrite(raw, "openai-chat"), raw) {
		t.Fatal("passthrough modified")
	}
}
func TestID007aChat(t *testing.T) {
	tr := testIdentifierTransform(t)
	r := nativeIDRewriter{transform: tr}
	raw := []byte("event: chunk\r\ndata: { \"id\" : \"chat_1\", \"choices\":[{\"index\":0,\"delta\":{\"content\":\"escaped \\u0061\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"arguments\":\"{\\\"id\\\":\\\"stay\\\"}\"}}]}}]}\r\n\r\n")
	want := bytes.Replace(raw, []byte(`"chat_1"`), []byte(`"`+tr.Encode("chat_1")+`"`), 1)
	want = bytes.Replace(want, []byte(`"call_1"`), []byte(`"`+tr.Encode("call_1")+`"`), 1)
	if got := r.rewrite(raw, "openai-chat"); !bytes.Equal(got, want) {
		t.Fatalf("%s\nwant %s", got, want)
	}
	// Continuations without IDs remain unchanged apart from the chunk ID.
	next := []byte(`data: {"id":"chat_1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"more"}}]}}]}` + "\n\n")
	if got := r.rewrite(next, "openai-chat"); !bytes.Equal(got, bytes.Replace(next, []byte(`"chat_1"`), []byte(`"`+tr.Encode("chat_1")+`"`), 1)) {
		t.Fatal(string(got))
	}
}
func TestID007bAnthropic(t *testing.T) {
	tr := testIdentifierTransform(t)
	r := nativeIDRewriter{transform: tr}
	for _, raw := range []string{`{"type":"message_start","message":{"id":"target"}}`, `{"type":"content_block_start","content_block":{"type":"tool_use","id":"target","input":{"id":"stay"}}}`} {
		frame := []byte("data: " + raw + "\n\n")
		want := strings.Replace(string(frame), `"target"`, `"`+tr.Encode("target")+`"`, 1)
		if got := r.rewrite(frame, "anthropic"); string(got) != want {
			t.Fatal(string(got))
		}
	}
	raw := []byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"partial_json\":\"id target\"}}\n\n")
	if !bytes.Equal(raw, r.rewrite(raw, "anthropic")) {
		t.Fatal("content changed")
	}
}
func TestID007cResponses(t *testing.T) {
	tr := testIdentifierTransform(t)
	r := nativeIDRewriter{transform: tr}
	for _, raw := range []string{`{"type":"response.created","response":{"id":"target"}}`, `{"type":"response.in_progress","response":{"id":"target"}}`, `{"type":"response.completed","response":{"id":"target"}}`, `{"type":"response.output_item.added","item":{"type":"function_call","id":"target"}}`, `{"type":"response.function_call_arguments.delta","item_id":"target","delta":"unchanged"}`} {
		frame := []byte("data: " + raw + "\n\n")
		want := strings.Replace(string(frame), `"target"`, `"`+tr.Encode("target")+`"`, 1)
		if got := r.rewrite(frame, "openai-responses"); string(got) != want {
			t.Fatal(string(got))
		}
	}
}
func TestID007dNativeProxyMultiline(t *testing.T) {
	tr := testIdentifierTransform(t)
	frame := "event: chunk\r\ndata: {\"id\":\"target\",\r\ndata: \"choices\":[{\"delta\":{\"content\":\"a\\u0062\"},\"finish_reason\":\"stop\"}]}\r\n\r\ndata: [DONE]\r\n\r\n"
	rr := httptest.NewRecorder()
	result, e := proxyNativeSSE(context.Background(), rr, strings.NewReader(frame), "openai-chat", "model", 1<<20, nil, tr)
	if e != nil || !result.Done || rr.Code != http.StatusOK {
		t.Fatal(result, e)
	}
	if want := strings.Replace(frame, `"target"`, `"`+tr.Encode("target")+`"`, 1); rr.Body.String() != want {
		t.Fatal(rr.Body.String())
	}
	var value any
	if e = json.Unmarshal(sseFrameData(bytes.Split(rr.Body.Bytes(), []byte("\r\n\r\n"))[0]), &value); e != nil {
		t.Fatal(e)
	}
}
func BenchmarkChatIDRewrite(b *testing.B) {
	tr := testIdentifierTransform(b)
	r := nativeIDRewriter{transform: tr}
	raw := []byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	for b.Loop() {
		r.rewrite(raw, "openai-chat")
	}
}

func TestIdentifierBootAndNativeService(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		messages := body["messages"].([]any)
		if id := messages[0].(map[string]any)["tool_call_id"]; id != "call_original" {
			t.Errorf("upstream ID = %v", id)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chat_original\",\"choices\":[{\"delta\":{\"content\":\"unchanged\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.Identifiers = identifierTestConfig()
	cfg.Provider["mock"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "mock-model", ToolSupport: ToolSupport{OpenAIChat: []string{"tools"}}}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if svc.identifiers == nil {
		t.Fatal("boot did not initialize transform")
	}
	body := `{"model":"default","stream":true,"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"messages":[{"role":"tool","tool_call_id":"` + svc.identifiers.Encode("call_original") + `","content":"result"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), svc.identifiers.Encode("chat_original")) || strings.Contains(rr.Body.String(), "chat_original") {
		t.Fatal(rr.Code, rr.Body.String())
	}
}

func TestIdentifierF011RestoreAlias(t *testing.T) {
	cfg := minimalConfig(t)
	for _, mode := range []string{"", "redact_only", "redact_and_restore"} {
		filter := testPIIFilterConfig("redact_only")
		filter.Mode = mode
		filter.RestoreResponse = boolPtr(true)
		if err := validatePIIFilter("default", filter); err == nil || !strings.Contains(err.Error(), "F-011") {
			t.Fatal(mode, err)
		}
	}
	cfg.Server.Identifiers = identifierTestConfig()
	cfg.setDefaults()
	if cfg.Server.Identifiers.Mode != "rewrite" {
		t.Fatal("rewrite not default")
	}
}
