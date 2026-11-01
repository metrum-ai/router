// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestResponsesExplicitNoStoreNativeHTTP(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/force=%t", stream, force), func(t *testing.T) {
				var got map[string]any
				var hits atomic.Int32
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					got = nil
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
					}
				}))
				defer up.Close()
				target := anthropicBridgeTarget()
				target.ForceStoreFalse = force
				svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{target})
				for _, tc := range []struct {
					name, field string
					want        any
					present     bool
				}{
					{"false", `,"store":false`, false, true}, {"true", `,"store":true`, false, force}, {"null", `,"store":null`, false, force}, {"absent", "", false, force},
				} {
					body := fmt.Sprintf(`{"model":"native-stream","input":"hello","stream":%t%s}`, stream, tc.field)
					r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
					r.Header.Set("Authorization", "Bearer "+testToken)
					w := httptest.NewRecorder()
					svc.Handler().ServeHTTP(w, r)
					value, ok := got["store"]
					if w.Code != 200 || ok != tc.present || (ok && value != tc.want) {
						t.Fatalf("%s status=%d store=(%#v,%t) hits=%d response=%s", tc.name, w.Code, value, ok, hits.Load(), w.Body.String())
					}
				}
			})
		}
	}
}

func TestResponsesNoStoreTranslationHTTP(t *testing.T) {
	for _, dialect := range []string{"openai-chat", "anthropic"} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%t", dialect, force), func(t *testing.T) {
				var hits atomic.Int32
				var got map[string]any
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"chat_1","object":"chat.completion","model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
				}))
				defer up.Close()
				target := anthropicBridgeTarget()
				target.ForceStoreFalse = force
				target.ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, ValidationStatus: "passed"}
				svc := nativeStreamTestService(t, up.URL, dialect, []Target{target})
				r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","input":"hello","store":false}`))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				if dialect == "openai-chat" && force {
					if w.Code != 200 || hits.Load() != 1 || got["store"] != false {
						t.Fatalf("Chat must forward false: status=%d hits=%d body=%#v response=%s", w.Code, hits.Load(), got, w.Body.String())
					}
				} else if w.Code != 400 || hits.Load() != 0 || !strings.Contains(w.Body.String(), `"param":"store"`) {
					t.Fatalf("must reject store before dispatch: status=%d hits=%d response=%s", w.Code, hits.Load(), w.Body.String())
				}
			})
		}
	}
}

func TestResponsesNoStoreUnsupportedTargetHTTP(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer up.Close()
	target := anthropicBridgeTarget()
	target.RequestShapeSupport.UnsupportedRequestFeatures = []string{"store"}
	svc := nativeStreamTestService(t, up.URL, "openai-responses", []Target{target})
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"native-stream","input":"hello","store":false}`))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if w.Code != 400 || hits.Load() != 0 {
		t.Fatalf("unsupported target dispatched: status=%d hits=%d response=%s", w.Code, hits.Load(), w.Body.String())
	}
}

func TestResponsesNoStoreShadowSkipped(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Header().Set("Content-Type","application/json"); fmt.Fprint(w, `{"id":"r","object":"response","status":"completed","output":[]}`) }))
	defer up.Close()
	svc := nativeStreamTestService(t,up.URL,"openai-responses",[]Target{anthropicBridgeTarget()})
	run := &shadowRunState{group:"native-stream",provider:"native",model:"up"}
	svc.executeShadowRequest(run,"openai-responses",[]byte(`{"model":"native-stream","input":"hi","store":false}`))
	if hits.Load()!=0 {t.Fatal("explicit no-store was shadow replayed")}
	svc.executeShadowRequest(run,"openai-responses",[]byte(`{"model":"native-stream","input":"hi"}`))
	if hits.Load()!=1 {t.Fatalf("omitted store should retain existing shadow behavior, hits=%d",hits.Load())}
}

func TestResponsesNoStoreStreamRejection(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }));defer up.Close()
	target := anthropicBridgeTarget()
	svc := nativeStreamTestService(t,up.URL,"anthropic",[]Target{target})
	r:=httptest.NewRequest(http.MethodPost,"/v1/responses",strings.NewReader(`{"model":"native-stream","input":"hi","store":false,"stream":true}`));r.Header.Set("Authorization","Bearer "+testToken)
	w:=httptest.NewRecorder();svc.Handler().ServeHTTP(w,r)
	if w.Code!=400 || hits.Load()!=0 || !strings.Contains(w.Body.String(),`"param":"store"`) {t.Fatalf("stream must reject before dispatch: status=%d hits=%d response=%s",w.Code,hits.Load(),w.Body.String())}
}

func TestResponsesNoStoreDiagnosticPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, body, wrongParam string
		target                 Target
	}{
		{"oversize", `{"model":"native-stream","input":"` + strings.Repeat("x", 300) + `","store":false}`, `"param":"store"`, Target{Provider: "native", Model: "up", RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"openai-responses"}, ValidationStatus: "passed", MaxRequestBytes: 128}}},
		{"missing-tools", `{"model":"native-stream","input":"hi","store":false,"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`, `"param":"store"`, Target{Provider: "native", Model: "up", RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"openai-responses"}, ValidationStatus: "passed"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
			defer up.Close()
			svc := nativeStreamTestService(t, up.URL, "anthropic", []Target{tc.target})
			r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if w.Code != 400 || hits.Load() != 0 || strings.Contains(w.Body.String(), tc.wrongParam) {
				t.Fatalf("misattributed: status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
			}
		})
	}
}

func TestResponsesNoStoreTranslationAdmission(t *testing.T) {
	req, err := decodeRequest("openai-responses", []byte(`{"model":"m","input":"hello","store":false}`), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	fit := targetRequestShapeFit{ContextFit: true, RequestBytesFit: true, ToolSchemaFit: true}
	for _, tc := range []struct {
		out   string
		force bool
		want  string
	}{{"openai-responses", false, ""}, {"openai-chat", false, "store-false-unsupported"}, {"openai-chat", true, ""}, {"anthropic", false, "store-false-unsupported"}, {"anthropic", true, "store-false-unsupported"}} {
		target := anthropicBridgeTarget()
		target.ForceStoreFalse = tc.force
		target.ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, ValidationStatus: "passed"}
		if got := requestShapeFilterReason(target, req, "openai-responses", tc.out, requestTokenEstimateLogRecord{}, fit); got != tc.want {
			t.Fatalf("out=%s force=%t got=%q want=%q", tc.out, tc.force, got, tc.want)
		}
	}
}
