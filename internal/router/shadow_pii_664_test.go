// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPIIGroupDoesNotShadowOriginalBody(t *testing.T) {
	for _, tc := range []struct {
		name, mode, body string
		enabled          bool
		wantStatus       int
		wantShadow       bool
	}{
		{"redact-only", "redact_only", `{"model":"default","messages":[{"role":"user","content":"alice@example.com"}]}`, true, 200, false},
		{"fail-on-match", "fail_on_match", `{"model":"default","messages":[{"role":"user","content":"alice@example.com"}]}`, true, 400, false},
		{"disabled-control", "", `{"model":"default","messages":[{"role":"user","content":"hello"}]}`, false, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shadowSeen := make(chan struct{}, 1)
			unblock := make(chan struct{})
			var release sync.Once
			var mainHits atomic.Int32
			var rawPII atomic.Bool
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				if strings.HasPrefix(r.UserAgent(), "metrum-ai-router-shadow/") {
					shadowSeen <- struct{}{}
					<-unblock
				} else {
					mainHits.Add(1)
					if strings.Contains(string(b), "alice@example.com") {
						rawPII.Store(true)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			}))
			defer up.Close()
			defer release.Do(func() { close(unblock) })
			cfg := testConfig(t, up.URL, "provider-key", t.TempDir())
			group := cfg.Models["default"]
			if tc.enabled {
				group.PIIFilter = testPIIFilterConfig(tc.mode)
			}
			cfg.Models["default"] = group
			svc, err := newServiceWithVerifiedFixture(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			runs := map[string]*shadowRunState{"default": {runID: "test", group: "default", provider: "mock", model: "mock-model", rate: 1}}
			svc.shadows.runs.Store(&runs)
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if rawPII.Load() {
				t.Fatal("main upstream received unredacted PII")
			}
			if tc.wantStatus == 200 && mainHits.Load() != 1 || tc.wantStatus == 400 && mainHits.Load() != 0 {
				t.Fatalf("main calls=%d", mainHits.Load())
			}
			if tc.wantShadow {
				select {
				case <-shadowSeen:
					release.Do(func() { close(unblock) })
				case <-time.After(3 * time.Second):
					t.Fatal("PII-disabled control did not shadow")
				}
			} else if len(svc.shadows.slots) != 0 {
				t.Fatal("PII-enabled group scheduled an unfiltered shadow")
			}
		})
	}
}

func TestPIIShadowGuardOtherDialects(t *testing.T) {
	cases := []struct{ path, dialect, request, response string }{
		{"/v1/responses", "openai-responses", `{"model":"default","input":"alice@example.com"}`, `{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`},
		{"/anthropic/v1/messages", "anthropic", `{"model":"default","max_tokens":10,"messages":[{"role":"user","content":"alice@example.com"}]}`, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			unblock := make(chan struct{})
			var raw atomic.Bool
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.UserAgent(), "metrum-ai-router-shadow/") {
					<-unblock
				}
				b, _ := io.ReadAll(r.Body)
				if strings.Contains(string(b), "alice@example.com") {
					raw.Store(true)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.response)
			}))
			defer up.Close()
			defer close(unblock)
			cfg := testConfig(t, up.URL, "provider-key", t.TempDir())
			p := cfg.Provider["mock"]
			p.Dialect = tc.dialect
			cfg.Provider["mock"] = p
			group := cfg.Models["default"]
			group.PIIFilter = testPIIFilterConfig("redact_only")
			cfg.Models["default"] = group
			svc, err := newServiceWithVerifiedFixture(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			runs := map[string]*shadowRunState{"default": {runID: "test", group: "default", provider: "mock", model: "mock-model", rate: 1}}
			svc.shadows.runs.Store(&runs)
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.request))
			req.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, req)
			if w.Code != 200 || raw.Load() || len(svc.shadows.slots) != 0 {
				t.Fatalf("status=%d raw=%t shadow_slots=%d body=%s", w.Code, raw.Load(), len(svc.shadows.slots), w.Body.String())
			}
		})
	}
}

func TestShadowExecutionRechecksPIIPolicy(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `{"id":"r","object":"response","status":"completed","output":[]}`)
	}))
	defer up.Close()
	cfg := testConfig(t, up.URL, "provider-key", t.TempDir())
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	run := &shadowRunState{group: "default", provider: "mock", model: "mock-model"}
	body := []byte(`{"model":"default","messages":[{"role":"user","content":"alice@example.com"}]}`)
	svc.executeShadowRequest(run, "openai-chat", body)
	if hits.Load() != 1 {
		t.Fatal("disabled-policy control did not dispatch")
	}
	current := svc.configSnapshot.Load()
	next := *current
	next.Models = make(map[string]ModelGroup, len(current.Models))
	for name, group := range current.Models {
		next.Models[name] = group
	}
	group := next.Models["default"]
	group.PIIFilter = testPIIFilterConfig("redact_only")
	next.Models["default"] = group
	svc.configSnapshot.Store(&next)
	svc.executeShadowRequest(run, "openai-chat", body)
	if hits.Load() != 1 {
		t.Fatal("shadow execution bypassed newly active PII policy")
	}
}
