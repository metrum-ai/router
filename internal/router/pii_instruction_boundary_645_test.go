// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The assembled instruction a translated target receives is built from several
// caller messages and parts, so a PII match can exist only after that join. The
// router filters the transmitted text itself, not just the raw values (#645).

var (
	spanningPartsRule    = PIIFilterRule{Name: "secret", Expression: `secret`, PlaceholderPrefix: "SECRET"}
	spanningMessagesRule = PIIFilterRule{Name: "ssn", Expression: `\d{3}-\d{2}-\s*\d{4}`, PlaceholderPrefix: "US_SSN"}
)

func piiInstructionGroup(cfg *Config, mode string, rules ...PIIFilterRule) {
	group := cfg.Models["native-stream"]
	group.PIIFilter = testPIIFilterConfig(mode)
	if len(rules) > 0 {
		group.PIIFilter.Rules = rules
	}
	cfg.Models["native-stream"] = group
}

// chatToResponsesTarget is a target that receives a Chat caller's instruction
// history through the Chat-to-Responses bridge.
func chatToResponsesTarget() Target {
	return Target{Provider: "native", Model: "bridge",
		RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"openai-chat"}, ValidationStatus: "passed"},
		Bridges:             BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true, Text: boolPtr(true), Tools: true, ToolChoice: true}}}
}

// TestChatInstructionsSpanningMessagesPII645 is the unguarded leak #645 fixes:
// a Chat caller's leading system messages are joined into one target-bound
// instruction, and a match that only exists across them must be redacted at
// that boundary.
func TestChatInstructionsSpanningMessagesPII645(t *testing.T) {
	var raw atomic.Bool
	var hits atomic.Int32
	var upstream map[string]any
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &upstream)
		if strings.Contains(string(data), "123-45-") || strings.Contains(string(data), "6789") {
			raw.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_645","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer upstreamServer.Close()
	svc := nativeStreamTestService(t, upstreamServer.URL, "openai-responses", []Target{chatToResponsesTarget()}, func(cfg *Config) {
		piiInstructionGroup(cfg, "redact_only", spanningMessagesRule)
	})
	body := `{"model":"native-stream","messages":[{"role":"system","content":"account 123-45-"},{"role":"system","content":"6789 is frozen"},{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
	}
	if raw.Load() {
		t.Fatalf("a match spanning instruction messages reached upstream: %#v", upstream)
	}
	instructions, _ := upstream["instructions"].(string)
	if !strings.Contains(instructions, "[US_SSN_1]") {
		t.Fatalf("upstream instruction was not filtered: %q", instructions)
	}
	if stats := svc.cache.Stats(); stats.Items != 0 {
		t.Fatalf("PII-enabled request populated the response cache: %+v", stats)
	}
}

// TestChatToolContinuationPII645 is the localhost tool-call, tool-result and
// continuation control for a PII-enabled Chat caller on an Anthropic target,
// whose system instruction is assembled by the function-history bridge. The
// tool pairing and answer must survive while every matched value stays a
// placeholder upstream.
func TestChatToolContinuationPII645(t *testing.T) {
	for _, mode := range []string{"redact_only", "fail_on_match"} {
		t.Run(mode, func(t *testing.T) {
			var raw atomic.Bool
			var hits atomic.Int32
			var upstream map[string]any
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				data, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(data, &upstream)
				if strings.Contains(string(data), "jane.doe@example.com") {
					raw.Store(true)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_645\",\"usage\":{\"input_tokens\":4,\"output_tokens\":0}}}\n\n")
				fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
				fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"E2E-TOOL-RESULT-7842\"}}\n\n")
				fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
				fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
				fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer upstreamServer.Close()
			svc := nativeStreamTestService(t, upstreamServer.URL, "anthropic", []Target{anthropicToolTarget()}, func(cfg *Config) {
				piiInstructionGroup(cfg, mode)
			})
			body := `{"model":"native-stream","stream":true,"max_tokens":64,` +
				`"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}],` +
				`"messages":[{"role":"system","content":"Operator: contact jane.doe@example.com for escalation."},{"role":"user","content":"read quote.txt"},` +
				`{"role":"assistant","content":null,"tool_calls":[{"id":"call_645","type":"function","function":{"name":"read","arguments":"{\"path\":\"quote.txt\"}"}}]},` +
				`{"role":"tool","tool_call_id":"call_645","content":"E2E-TOOL-RESULT-7842 owner jane.doe@example.com"}]}`
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if raw.Load() {
				t.Fatalf("cleartext reached upstream: %#v", upstream)
			}
			if mode == "fail_on_match" {
				if w.Code != http.StatusBadRequest || hits.Load() != 0 || !strings.Contains(w.Body.String(), "pii-filter-blocked") {
					t.Fatalf("fail_on_match must block before dispatch: status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
				}
				return
			}
			if w.Code != http.StatusOK || hits.Load() != 1 {
				t.Fatalf("status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
			}
			system, _ := upstream["system"].(string)
			if !strings.Contains(system, "[EMAIL_1]") || strings.Contains(system, "jane.doe@example.com") {
				t.Fatalf("assembled instruction not filtered upstream: %q", system)
			}
			var toolUse, toolResult map[string]any
			for _, value := range valueAsSlice(upstream["messages"]) {
				message, _ := value.(map[string]any)
				for _, value := range valueAsSlice(message["content"]) {
					block, _ := value.(map[string]any)
					switch block["type"] {
					case "tool_use":
						toolUse = block
					case "tool_result":
						toolResult = block
					}
				}
			}
			if toolUse == nil || toolResult == nil || toolUse["id"] != toolResult["tool_use_id"] || toolUse["id"] != "call_645" {
				t.Fatalf("tool pairing lost: %#v %#v", toolUse, toolResult)
			}
			blocks := valueAsSlice(toolResult["content"])
			if len(blocks) != 1 || blocks[0].(map[string]any)["text"] != "E2E-TOOL-RESULT-7842 owner [EMAIL_1]" {
				t.Fatalf("tool result not filtered: %#v", toolResult["content"])
			}
			if !strings.Contains(w.Body.String(), "E2E-TOOL-RESULT-7842") || !strings.Contains(w.Body.String(), "[DONE]") {
				t.Fatalf("downstream stream lost the answer: %s", w.Body.String())
			}
		})
	}
}

// TestInstructionBoundarySelectiveScope645 pins the operator scope rules: the
// assembled instruction is filtered when either source scope is enabled, and an
// operator who disables both keeps the documented passthrough semantics.
func TestInstructionBoundarySelectiveScope645(t *testing.T) {
	cases := []struct {
		name         string
		system       *bool
		messages     *bool
		wantFiltered bool
	}{
		{"default", nil, nil, true},
		{"system-only", boolPtr(true), boolPtr(false), true},
		{"messages-only", boolPtr(false), boolPtr(true), true},
		{"both-disabled", boolPtr(false), boolPtr(false), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testPIIFilterConfig("redact_only")
			cfg.Rules = []PIIFilterRule{spanningMessagesRule}
			cfg.ApplyTo = PIIFilterApplyTo{System: c.system, Messages: c.messages}
			req, err := decodeRequest("openai-chat", []byte(`{"model":"m","messages":[{"role":"system","content":"account 123-45-"},{"role":"system","content":"6789 is frozen"},{"role":"user","content":"hi"}]}`), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := applyPIIFilter(req, cfg, "openai-chat"); err != nil {
				t.Fatal(err)
			}
			filtered := strings.Contains(req.Instructions["openai-responses"], "[US_SSN_1]")
			if filtered != c.wantFiltered {
				t.Fatalf("filtered=%t want %t (instructions %q)", filtered, c.wantFiltered, req.Instructions["openai-responses"])
			}
		})
	}
}

// TestInstructionBoundaryCacheIdentity645 proves two requests that normalize to
// the same IR but transmit different instructions cannot share a cache entry,
// including a PII-redacted instruction that differs from the normalized text.
func TestInstructionBoundaryCacheIdentity645(t *testing.T) {
	for _, pair := range [][2]string{
		{`[{"type":"input_text","text":"a"},{"type":"input_text","text":"b\nc"}]`, `[{"type":"input_text","text":"a\nb"},{"type":"input_text","text":"c"}]`},
		{`"alpha"`, `"alpha\nbeta"`},
	} {
		first := mustDecodeResponses(t, `{"model":"m","input":[{"role":"system","content":`+pair[0]+`},{"role":"user","content":"hi"}]}`)
		second := mustDecodeResponses(t, `{"model":"m","input":[{"role":"system","content":`+pair[1]+`},{"role":"user","content":"hi"}]}`)
		if first.System != second.System && first.Input == second.Input {
			// The IR differs, so the normalized key already separates them; the
			// canonical instruction is still part of the identity below.
		}
		firstKey := instructionBoundaryKey(first, "openai-responses", "anthropic")
		secondKey := instructionBoundaryKey(second, "openai-responses", "anthropic")
		if firstKey == secondKey {
			t.Fatalf("distinct transmitted instructions share instruction identity: %q", pair[1])
		}
		firstCache := targetCacheKey(first, ProviderConfig{}, Target{Provider: "p", Model: "m"}, "c", "p", "openai-responses")
		secondCache := targetCacheKey(second, ProviderConfig{}, Target{Provider: "p", Model: "m"}, "c", "p", "openai-responses")
		if firstCache == secondCache {
			t.Fatalf("distinct transmitted instructions share a cache key: %q", pair[1])
		}
	}

	// PII-enabled: the transmitted instruction is the redacted assembled text.
	cfg := testPIIFilterConfig("redact_only")
	cfg.Rules = []PIIFilterRule{spanningMessagesRule}
	first := mustDecodeResponses(t, `{"model":"m","input":[{"role":"system","content":"account 123-45-"},{"type":"message","role":"system","content":"6789 is frozen"},{"role":"user","content":"hi"}]}`)
	second := mustDecodeResponses(t, `{"model":"m","input":[{"role":"system","content":"account 123-45-"},{"type":"message","role":"system","content":"no account here"},{"role":"user","content":"hi"}]}`)
	for _, req := range []*IRRequest{first, second} {
		if _, err := applyPIIFilter(req, cfg, "openai-responses"); err != nil {
			t.Fatal(err)
		}
	}
	if first.Instructions["anthropic"] != "account [US_SSN_1] is frozen" {
		t.Fatalf("assembled instruction was not filtered: %q", first.Instructions["anthropic"])
	}
	if instructionBoundaryKey(first, "openai-responses", "anthropic") == instructionBoundaryKey(second, "openai-responses", "anthropic") {
		t.Fatal("filtered instructions collided in cache identity")
	}
}

// TestResponsesSystemHistoryStillFailsClosed645 records the remaining limit:
// Responses system-message histories stay excluded from Anthropic and Bedrock
// Messages targets on PII-enabled groups until the fail-closed limitation is
// removed under independent security review.
func TestResponsesSystemHistoryStillFailsClosed645(t *testing.T) {
	var hits atomic.Int32
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
	}))
	defer upstreamServer.Close()
	svc := nativeStreamTestService(t, upstreamServer.URL, "anthropic", []Target{anthropicToolTarget()}, func(cfg *Config) {
		cfg.Server.Cache = CacheConfig{Enabled: true, MaxBytes: 1 << 20, DefaultTTL: time.Minute}
		piiInstructionGroup(cfg, "redact_only", spanningPartsRule)
	})
	body := `{"model":"native-stream","temperature":0,"input":[{"role":"system","content":[{"type":"input_text","text":"sec"},{"type":"input_text","text":"ret"}]},{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if hits.Load() != 0 || w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"system_pii_unsupported"`) {
		t.Fatalf("system history must stay fail-closed: status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
	}
}

func mustDecodeResponses(t *testing.T, body string) *IRRequest {
	t.Helper()
	req, err := decodeRequest("openai-responses", []byte(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// TestInstructionAssemblerMatchesBridges645 keeps the canonical assembler and the
// bridges from drifting: without filtering, the instruction text the bridge
// transmits must equal the assembled instruction the filter and cache identity
// use, for both caller dialects.
func TestInstructionAssemblerMatchesBridges645(t *testing.T) {
	t.Run("chat-tools", func(t *testing.T) {
		req, err := decodeRequest("openai-chat", []byte(`{"model":"m","max_tokens":32,"messages":[{"role":"system","content":"one"},{"role":"system","content":"two"},{"role":"user","content":"read"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"done"}]}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := encodeOpenAIToolsToAnthropic("m", "openai-chat", req, anthropicToolTarget())
		if err != nil {
			t.Fatal(err)
		}
		var sent map[string]any
		if err := json.Unmarshal(raw, &sent); err != nil {
			t.Fatal(err)
		}
		if got, want := stringValue(sent["system"]), assembleTargetInstruction(req, "openai-chat", "anthropic-tools"); got != want {
			t.Fatalf("chat tools system drifted: bridge=%q assembler=%q", got, want)
		}
	})
	t.Run("responses-tools", func(t *testing.T) {
		req, err := decodeRequest("openai-responses", []byte(`{"model":"m","max_output_tokens":32,"input":[{"role":"system","content":"one"},{"role":"system","content":"two"},{"role":"developer","content":"three"},{"role":"user","content":"read"},{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"done"}]}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := encodeOpenAIToolsToAnthropic("m", "openai-responses", req, anthropicToolTarget())
		if err != nil {
			t.Fatal(err)
		}
		var sent map[string]any
		if err := json.Unmarshal(raw, &sent); err != nil {
			t.Fatal(err)
		}
		if got, want := stringValue(sent["system"]), assembleTargetInstruction(req, "openai-responses", "anthropic-tools"); got != want {
			t.Fatalf("responses tools system drifted: bridge=%q assembler=%q", got, want)
		}
	})
	t.Run("chat-responses", func(t *testing.T) {
		req, err := decodeRequest("openai-chat", []byte(`{"model":"m","messages":[{"role":"system","content":"one"},{"role":"system","content":"two"},{"role":"user","content":"hi"}]}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		target := chatToResponsesTarget()
		raw, err := encodeChatToResponsesBridge("m", req, target, "")
		if err != nil {
			t.Fatal(err)
		}
		var sent map[string]any
		if err := json.Unmarshal(raw, &sent); err != nil {
			t.Fatal(err)
		}
		if got, want := stringValue(sent["instructions"]), assembleTargetInstruction(req, "openai-chat", "openai-responses"); got != want {
			t.Fatalf("chat responses instruction drifted: bridge=%q assembler=%q", got, want)
		}
	})
}

// TestChatAnthropicPlainInstruction645 covers review finding 1: a Chat caller
// without function history reaches an Anthropic target through the plain
// translate path, which used to transmit req.System directly. With only the
// messages scope enabled the folded instruction must still be filtered, because
// it is transmitted as the system prompt.
func TestChatAnthropicPlainInstruction645(t *testing.T) {
	var raw atomic.Bool
	var upstream map[string]any
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &upstream)
		if strings.Contains(string(data), "jane.doe@example.com") {
			raw.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
	}))
	defer upstreamServer.Close()
	svc := nativeStreamTestService(t, upstreamServer.URL, "anthropic", []Target{anthropicToolTarget()}, func(cfg *Config) {
		group := cfg.Models["native-stream"]
		group.PIIFilter = testPIIFilterConfig("redact_only")
		no, yes := false, true
		group.PIIFilter.ApplyTo = PIIFilterApplyTo{System: &no, Messages: &yes}
		cfg.Models["native-stream"] = group
	})
	body := `{"model":"native-stream","messages":[{"role":"system","content":"Operator: contact jane.doe@example.com"},{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if raw.Load() {
		t.Fatalf("folded Chat instruction reached upstream unfiltered: %#v", upstream)
	}
	if system, _ := upstream["system"].(string); !strings.Contains(system, "[EMAIL_1]") {
		t.Fatalf("transmitted system prompt not filtered: %q", system)
	}
}

// TestResponsesInstructionItemScope645 covers review finding 2: a system-role
// item inside a Responses input array is instruction text, so the system scope
// governs it even when messages is disabled, on the native passthrough path that
// re-sends the raw item.
func TestResponsesInstructionItemScope645(t *testing.T) {
	cases := []struct {
		name    string
		applyTo PIIFilterApplyTo
	}{
		{"system-only", PIIFilterApplyTo{System: boolPtr(true), Messages: boolPtr(false)}},
		{"input-only", PIIFilterApplyTo{System: boolPtr(false), Messages: boolPtr(false), ResponsesInput: boolPtr(true)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var raw atomic.Bool
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				if strings.Contains(string(data), "jane.doe@example.com") {
					raw.Store(true)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"resp","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
			}))
			defer upstreamServer.Close()
			svc := nativeStreamTestService(t, upstreamServer.URL, "openai-responses", []Target{{Provider: "native", Model: "model"}}, func(cfg *Config) {
				group := cfg.Models["native-stream"]
				group.PIIFilter = testPIIFilterConfig("redact_only")
				group.PIIFilter.ApplyTo = c.applyTo
				cfg.Models["native-stream"] = group
			})
			body := `{"model":"native-stream","input":[{"role":"system","content":"Operator: contact jane.doe@example.com"},{"role":"user","content":"hi"}]}`
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if raw.Load() {
				t.Fatalf("instruction item reached the native upstream unfiltered")
			}
		})
	}
}

// TestResponsesJSONObjectInstructionPII645 covers review finding 3: the
// router-added JSON-object directive must survive the canonical instruction
// substitution on a PII-enabled Responses-to-Anthropic request.
func TestResponsesJSONObjectInstructionPII645(t *testing.T) {
	var upstream map[string]any
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &upstream)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"{}"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
	}))
	defer upstreamServer.Close()
	target := anthropicToolTarget()
	target.StructuredOutput = true
	svc := nativeStreamTestService(t, upstreamServer.URL, "anthropic", []Target{target}, func(cfg *Config) {
		piiInstructionGroup(cfg, "redact_only")
	})
	body := `{"model":"native-stream","response_format":{"type":"json_object"},"input":[{"role":"user","content":"contact jane.doe@example.com"}]}`
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	system, _ := upstream["system"].(string)
	if !strings.Contains(system, "Return only a valid JSON object") {
		t.Fatalf("JSON-object directive lost: %q", system)
	}
	if strings.Contains(system, "jane.doe@example.com") {
		t.Fatalf("cleartext reached upstream: %q", system)
	}
}

// TestInstructionBoundaryKeyFraming645 covers review finding 5: caller text with
// NUL bytes cannot borrow another surface's framing.
func TestInstructionBoundaryKeyFraming645(t *testing.T) {
	first := &IRRequest{Instructions: map[string]string{"openai-responses": "a"}, System: "b"}
	second := &IRRequest{Instructions: map[string]string{"openai-responses": "a\x00\x00openai-responses\x001\x00b"}, System: "b"}
	if instructionBoundaryKey(first, "openai-chat", "openai-responses") == instructionBoundaryKey(second, "openai-chat", "openai-responses") {
		t.Fatal("NUL bytes in caller text collide in instruction identity")
	}
}

// TestAnthropicArraySystemScope645 covers the review's second follow-up: an
// Anthropic request may carry the system prompt as an array of text blocks, and
// the native passthrough re-sends those raw blocks. The system scope must
// filter them even when the messages scope is disabled.
func TestAnthropicArraySystemScope645(t *testing.T) {
	var raw atomic.Bool
	var upstream map[string]any
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &upstream)
		if strings.Contains(string(data), "jane.doe@example.com") {
			raw.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
	}))
	defer upstreamServer.Close()
	svc := nativeStreamTestService(t, upstreamServer.URL, "anthropic", []Target{anthropicToolTarget()}, func(cfg *Config) {
		group := cfg.Models["native-stream"]
		group.PIIFilter = testPIIFilterConfig("redact_only")
		no, yes := false, true
		group.PIIFilter.ApplyTo = PIIFilterApplyTo{System: &yes, Messages: &no}
		cfg.Models["native-stream"] = group
	})
	body := `{"model":"native-stream","max_tokens":32,"system":[{"type":"text","text":"Operator: contact jane.doe@example.com"}],"messages":[{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(body))
	r.Header.Set("x-api-key", testToken)
	r.Header.Set("anthropic-version", "2023-06-01")
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if raw.Load() {
		t.Fatalf("array-form system prompt reached upstream unfiltered: %#v", upstream)
	}
	if system := upstream["system"]; system == nil || !strings.Contains(fmt.Sprint(system), "[EMAIL_1]") {
		t.Fatalf("upstream system prompt not filtered: %#v", upstream["system"])
	}
}
