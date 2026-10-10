// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Service-level tests for server.responses.model_identity (Metrum AI Router
// issues #254, #255 and #257): unary JSON, tool passthrough, dialect bridges,
// synthesized SSE, response-cache hits, fallback and caller-facing errors.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const (
	miGroup         = "big-coder"
	miLunaProvider  = "luna-provider"
	miLunaModel     = "gpt-6-luna"
	miFireProvider  = "fireworks"
	miFireModel     = "accounts/fireworks/models/deepseek-v4p1-flash"
	miUpstreamError = `{"error":{"message":"model gpt-6-luna on luna-provider rejected accounts/fireworks/models/deepseek-v4p1-flash","type":"server_error"}}`
)

// miUpstream is a three-dialect mock provider. It echoes the requested
// upstream model, returns tool calls when the request has tools, and fails
// any model listed in fail.
type miUpstream struct {
	mu     sync.Mutex
	bodies []map[string]any
	fail   map[string]int
}

func (u *miUpstream) requests() []map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]map[string]any(nil), u.bodies...)
}

func (u *miUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	u.mu.Lock()
	u.bodies = append(u.bodies, body)
	u.mu.Unlock()
	model := stringValue(body["model"])
	if status := u.fail[model]; status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, miUpstreamError)
		return
	}
	_, tools := body["tools"]
	switch {
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		message := map[string]any{"role": "assistant", "content": "hello from " + model}
		finish := "stop"
		if tools {
			message = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
				"id": "call_keep", "type": "function", "function": map[string]any{"name": "write", "arguments": `{"path":"proof.txt"}`},
			}}}
			finish = "tool_calls"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl_mi", "object": "chat.completion", "created": 1700000000, "model": model,
			"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
		})
	case strings.HasSuffix(r.URL.Path, "/responses"):
		output := []any{map[string]any{"type": "message", "id": "msg_mi", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "hello from " + model}}}}
		if tools {
			output = []any{map[string]any{"type": "function_call", "id": "fc_keep", "call_id": "call_keep", "name": "write", "arguments": `{"path":"proof.txt"}`}}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "resp_mi", "object": "response", "created_at": 1700000000, "status": "completed", "model": model,
			"output": output, "usage": map[string]any{"input_tokens": 3, "output_tokens": 2, "total_tokens": 5},
		})
	case strings.HasSuffix(r.URL.Path, "/messages"):
		content := []any{map[string]any{"type": "text", "text": "hello from " + model}}
		stop := "end_turn"
		if tools {
			content = []any{map[string]any{"type": "tool_use", "id": "toolu_keep", "name": "write", "input": map[string]any{"path": "proof.txt"}}}
			stop = "tool_use"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "msg_mi", "type": "message", "role": "assistant", "model": model, "content": content,
			"stop_reason": stop, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 3, "output_tokens": 2},
		})
	default:
		http.NotFound(w, r)
	}
}

var miToolSupport = ToolSupport{
	OpenAIChat:        []string{"tools", "tool_choice"},
	OpenAIResponses:   []string{"function"},
	AnthropicMessages: []string{"client_tools"},
}

func miTargets() []Target {
	return []Target{
		{Provider: miLunaProvider, Model: miLunaModel, ToolSupport: miToolSupport},
		{Provider: miFireProvider, Model: miFireModel, ToolSupport: miToolSupport},
	}
}

// modelIdentityTestService builds a router whose "big-coder" group falls back
// from luna-provider/gpt-6-luna to fireworks/deepseek, both speaking dialect.
func modelIdentityTestService(t *testing.T, identity, dialect string, mutate ...func(*Config)) (*Service, *miUpstream) {
	t.Helper()
	up := &miUpstream{fail: map[string]int{}}
	server := httptest.NewServer(up)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	cfg := testConfig(t, server.URL, "provider-key", dir)
	cfg.Server.Responses.ModelIdentity = identity
	cfg.Provider = map[string]ProviderConfig{
		miLunaProvider: {BaseURL: server.URL + "/v1", Dialect: dialect, APIKey: "provider-key"},
		miFireProvider: {BaseURL: server.URL + "/v1", Dialect: dialect, APIKey: "provider-key"},
	}
	cfg.Models = map[string]ModelGroup{miGroup: {Strategy: "static", Targets: miTargets()}}
	cfg.Server.DefaultModelGroup = miGroup
	cfg.Callers[0].Allow = []string{miGroup}
	for _, fn := range mutate {
		fn(cfg)
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, up
}

// miUsageDB enables the SQLite usage store for tests that read request rows.
func miUsageDB(t *testing.T) func(*Config) {
	return func(cfg *Config) {
		cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	}
}

func miPerform(t *testing.T, svc *Service, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	return rr
}

type miDialectCase struct {
	name, dialect, path, text, tools string
	toolID                           string
}

var miDialectCases = []miDialectCase{
	{
		name: "chat", dialect: "openai-chat", path: "/v1/chat/completions",
		text:   `{"model":"big-coder","messages":[{"role":"user","content":"hi %s"}]}`,
		tools:  `{"model":"big-coder","messages":[{"role":"user","content":"write %s"}],"tools":[{"type":"function","function":{"name":"write","parameters":{"type":"object"}}}]}`,
		toolID: "call_keep",
	},
	{
		name: "responses", dialect: "openai-responses", path: "/v1/responses",
		text:   `{"model":"big-coder","input":"hi %s"}`,
		tools:  `{"model":"big-coder","input":"write %s","tools":[{"type":"function","name":"write","parameters":{"type":"object"}}]}`,
		toolID: "call_keep",
	},
	{
		name: "anthropic", dialect: "anthropic", path: "/v1/messages",
		text:   `{"model":"big-coder","max_tokens":32,"messages":[{"role":"user","content":"hi %s"}]}`,
		tools:  `{"model":"big-coder","max_tokens":32,"messages":[{"role":"user","content":"write %s"}],"tools":[{"name":"write","input_schema":{"type":"object"}}]}`,
		toolID: "toolu_keep",
	},
}

func miWant(identity, upstream string) string {
	if identity == ModelIdentityRequestedGroup {
		return miGroup
	}
	return upstream
}

var miIdentities = []string{ModelIdentityUpstream, ModelIdentityRequestedGroup}

// #255: unary Chat, Responses and Anthropic, for synthesized text and raw
// tool passthrough. The upstream request still names the provider model.
func TestModelIdentityUnary(t *testing.T) {
	for _, tc := range miDialectCases {
		for _, shape := range []string{"text", "tools"} {
			for _, identity := range miIdentities {
				t.Run(tc.name+"/"+shape+"/"+identity, func(t *testing.T) {
					svc, up := modelIdentityTestService(t, identity, tc.dialect)
					body := tc.text
					if shape == "tools" {
						body = tc.tools
					}
					rr := miPerform(t, svc, tc.path, fmt.Sprintf(body, shape))
					if rr.Code != http.StatusOK {
						t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
					}
					reqs := up.requests()
					if len(reqs) != 1 || reqs[0]["model"] != miLunaModel {
						t.Fatalf("upstream requests=%v, want one for %s", reqs, miLunaModel)
					}
					got := mustJSONMap(t, rr.Body.String())
					if got["model"] != miWant(identity, miLunaModel) {
						t.Fatalf("model=%v body=%s", got["model"], rr.Body.String())
					}
					if shape == "tools" {
						if !strings.Contains(rr.Body.String(), `"`+tc.toolID+`"`) {
							t.Fatalf("tool-call id changed: %s", rr.Body.String())
						}
						// Raw passthrough: only the top-level model differs from
						// what a direct upstream call returns.
						direct := httptest.NewRecorder()
						directReq := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"model":"`+miLunaModel+`","tools":[{}]}`))
						up.ServeHTTP(direct, directReq)
						want := mustJSONMap(t, direct.Body.String())
						want["model"] = got["model"]
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("passthrough body changed beyond model:\n got %v\nwant %v", got, want)
						}
					}
					if identity == ModelIdentityRequestedGroup && strings.Contains(rr.Body.String(), `"model":"`+miLunaModel+`"`) {
						t.Fatalf("upstream model leaked: %s", rr.Body.String())
					}
				})
			}
		}
	}
}

// #255: both unary dialect bridges keep the provider model upstream and
// report the group to the caller.
func TestModelIdentityBridgeUnary(t *testing.T) {
	for _, tc := range []struct {
		name, upstreamDialect, path, body string
		target                            func(Target) Target
	}{
		{"chat-to-responses", "openai-responses", "/v1/chat/completions", `{"model":"big-coder","messages":[{"role":"user","content":"hi"}]}`, func(t Target) Target {
			t.Bridges = BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true}}
			return t
		}},
		{"responses-to-chat", "openai-chat", "/v1/responses", `{"model":"big-coder","input":"hi"}`, func(t Target) Target {
			t.ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true}
			return t
		}},
	} {
		for _, identity := range miIdentities {
			t.Run(tc.name+"/"+identity, func(t *testing.T) {
				svc, up := modelIdentityTestService(t, identity, tc.upstreamDialect, func(cfg *Config) {
					group := cfg.Models[miGroup]
					for i := range group.Targets {
						group.Targets[i] = tc.target(group.Targets[i])
					}
					cfg.Models[miGroup] = group
				})
				rr := miPerform(t, svc, tc.path, tc.body)
				if rr.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
				}
				if reqs := up.requests(); len(reqs) != 1 || reqs[0]["model"] != miLunaModel {
					t.Fatalf("upstream requests=%v", reqs)
				}
				if got := mustJSONMap(t, rr.Body.String())["model"]; got != miWant(identity, miLunaModel) {
					t.Fatalf("model=%v body=%s", got, rr.Body.String())
				}
			})
		}
	}
}

// #255: synthesized SSE for all three dialects, including the raw tool
// variants written by writeRawChatSSE, writeRawResponsesSSE and
// writeRawAnthropicSSE.
func TestModelIdentitySynthesizedSSE(t *testing.T) {
	for _, tc := range miDialectCases {
		for _, shape := range []string{"text", "tools"} {
			for _, identity := range miIdentities {
				t.Run(tc.name+"/"+shape+"/"+identity, func(t *testing.T) {
					svc, up := modelIdentityTestService(t, identity, tc.dialect, func(cfg *Config) {
						cfg.Server.Streaming.Translator = "synthesized"
					})
					body := tc.text
					if shape == "tools" {
						body = tc.tools
					}
					body = strings.Replace(fmt.Sprintf(body, shape), `"model":"big-coder",`, `"model":"big-coder","stream":true,`, 1)
					rr := miPerform(t, svc, tc.path, body)
					if rr.Code != http.StatusOK {
						t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
					}
					if reqs := up.requests(); len(reqs) != 1 || reqs[0]["model"] != miLunaModel {
						t.Fatalf("upstream requests=%v", reqs)
					}
					assertSSEModels(t, rr.Body.String(), miWant(identity, miLunaModel))
					if shape == "tools" && !strings.Contains(rr.Body.String(), `"`+tc.toolID+`"`) {
						t.Fatalf("tool-call id changed: %s", rr.Body.String())
					}
				})
			}
		}
	}
}

// writeRawResponsesSSE and the raw encoders must not mutate resp.Raw, which
// the response cache shares.
func TestModelIdentityRawResponseNotMutated(t *testing.T) {
	raw := map[string]any{"model": miLunaModel, "output": []any{}}
	resp := &IRResponse{ID: "resp_1", Model: miLunaModel, Raw: raw, RawResponse: true}
	writeRawResponsesSSE(func(string, any) {}, resp, miGroup)
	if len(raw) != 2 || raw["model"] != miLunaModel {
		t.Fatalf("raw mutated: %v", raw)
	}
	for _, encoded := range []map[string]any{encodeChatResponse(resp, miGroup), encodeResponsesResponse(resp, miGroup), encodeAnthropicResponse(resp, miGroup)} {
		if encoded["model"] != miGroup {
			t.Fatalf("encoded model=%v", encoded["model"])
		}
	}
	if raw["model"] != miLunaModel {
		t.Fatalf("raw mutated: %v", raw)
	}
	// Upstream mode returns the raw object untouched.
	if got := encodeChatResponse(resp, ""); reflect.ValueOf(got).Pointer() != reflect.ValueOf(raw).Pointer() {
		t.Fatal("upstream mode copied raw response")
	}
}

// #255: a cache entry stored from target A is served under the current
// request's group, and A and B never share a cache key. Streams are never
// cacheable, so a cache hit is always written as unary JSON.
func TestModelIdentityCacheHit(t *testing.T) {
	for _, identity := range miIdentities {
		t.Run(identity, func(t *testing.T) {
			svc, up := modelIdentityTestService(t, identity, "openai-chat", miUsageDB(t), func(cfg *Config) {
				cfg.Models = map[string]ModelGroup{
					miGroup:     {Strategy: "static", Targets: []Target{miTargets()[0]}},
					"alt-coder": {Strategy: "static", Targets: []Target{miTargets()[1]}},
				}
				cfg.Callers[0].Allow = []string{miGroup, "alt-coder"}
			})
			const prompt = `{"model":"%s","temperature":0,"messages":[{"role":"user","content":"cache me"}]}`
			first := miPerform(t, svc, "/v1/chat/completions", fmt.Sprintf(prompt, miGroup))
			if first.Code != http.StatusOK {
				t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
			}
			second := miPerform(t, svc, "/v1/chat/completions", fmt.Sprintf(prompt, miGroup))
			if second.Code != http.StatusOK {
				t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
			}
			if n := len(up.requests()); n != 1 {
				t.Fatalf("upstream requests=%d, want cache hit", n)
			}
			if got := mustJSONMap(t, second.Body.String())["model"]; got != miWant(identity, miLunaModel) {
				t.Fatalf("cache-hit model=%v", got)
			}
			rows := miRequestRows(t, svc, second.Header().Get("X-Request-Id"))
			if rows.usage.Cache != "hit" || rows.usage.TargetModel != miLunaModel {
				t.Fatalf("cache-hit usage cache=%s target=%s", rows.usage.Cache, rows.usage.TargetModel)
			}
			// Same prompt to a group served by B misses the cache.
			before := len(up.requests())
			third := miPerform(t, svc, "/v1/chat/completions", fmt.Sprintf(prompt, "alt-coder"))
			if third.Code != http.StatusOK || len(up.requests()) != before+1 {
				t.Fatalf("alt-coder status=%d upstream=%d, want cache miss", third.Code, len(up.requests())-before)
			}
			if got := mustJSONMap(t, third.Body.String())["model"]; got != map[string]string{ModelIdentityUpstream: miFireModel, ModelIdentityRequestedGroup: "alt-coder"}[identity] {
				t.Fatalf("alt-coder model=%v", got)
			}
		})
	}
	req := &IRRequest{Model: miGroup, Messages: []IRMessage{{Role: "user", Content: "cache me"}}}
	if cacheKey(req, miTargets()[0], "alice", "p") == cacheKey(req, miTargets()[1], "alice", "p") {
		t.Fatal("cache key collapsed two upstream targets")
	}
}

type miRows struct {
	attempts []requestAttemptRecord
	usage    usageRecord
}

func miRequestRows(t *testing.T, svc *Service, requestID string) miRows {
	t.Helper()
	var rows miRows
	if err := svc.usage.db.Where("request_id = ?", requestID).Order("attempt_index asc").Find(&rows.attempts).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.usage.db.Where("request_id = ?", requestID).First(&rows.usage).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// miIdentityTerms are strings that must never reach a requested_group caller.
var miIdentityTerms = []string{miLunaProvider, miLunaModel, miFireProvider, miFireModel, "deepseek"}

func assertNoUpstreamIdentity(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	body := strings.ToLower(rr.Body.String())
	headers := strings.ToLower(fmt.Sprint(rr.Header()))
	for _, term := range miIdentityTerms {
		term = strings.ToLower(term)
		if strings.Contains(body, term) || strings.Contains(headers, term) {
			t.Fatalf("caller response names %q:\nheaders=%v\nbody=%s", term, rr.Header(), rr.Body.String())
		}
	}
}

// #255/#257: after fallback the success body shows the group and omits the
// failed attempt, while attempt and usage rows keep both upstreams.
func TestModelIdentityFallback(t *testing.T) {
	for _, identity := range miIdentities {
		t.Run(identity, func(t *testing.T) {
			svc, up := modelIdentityTestService(t, identity, "openai-chat", miUsageDB(t))
			up.fail[miLunaModel] = http.StatusInternalServerError
			rr := miPerform(t, svc, "/v1/chat/completions", `{"model":"big-coder","messages":[{"role":"user","content":"fallback"}]}`)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if got := mustJSONMap(t, rr.Body.String())["model"]; got != miWant(identity, miFireModel) {
				t.Fatalf("model=%v", got)
			}
			if identity == ModelIdentityRequestedGroup {
				// The text echoes the upstream model; check only protocol fields.
				clean := httptest.NewRecorder()
				payload := mustJSONMap(t, rr.Body.String())
				delete(payload, "choices")
				raw, _ := json.Marshal(payload)
				_, _ = clean.Write(raw)
				for k, v := range rr.Header() {
					clean.Header()[k] = v
				}
				assertNoUpstreamIdentity(t, clean)
			}
			rows := miRequestRows(t, svc, rr.Header().Get("X-Request-Id"))
			if len(rows.attempts) != 2 || rows.attempts[0].Model != miLunaModel || rows.attempts[0].Provider != miLunaProvider || rows.attempts[1].Model != miFireModel {
				t.Fatalf("attempt rows=%+v", rows.attempts)
			}
			if rows.usage.TargetModel != miFireModel || rows.usage.TargetProvider != miFireProvider || rows.usage.RequestedModel != miGroup {
				t.Fatalf("usage target=%s/%s requested=%s", rows.usage.TargetProvider, rows.usage.TargetModel, rows.usage.RequestedModel)
			}
		})
	}
}
