// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestPIICacheBypassTelemetry670(t *testing.T) {
	cases := []struct{ name, extra, header, reason string }{
		{"ordinary", "", "", "cache-pii-filter"},
		{"client-no-cache", "", "no-cache", "cache-request-no-cache"},
		{"tools", `,"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]`, "", "cache-tool-request"},
		{"stream", `,"stream":true`, "", "cache-streaming"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.name == "stream" {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			}))
			defer up.Close()
			dir := t.TempDir()
			cfg := testConfig(t, up.URL, "provider-key", dir)
			cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
			cfg.Server.DecisionTelemetry.Enabled = true
			group := cfg.Models["default"]
			group.PIIFilter = testPIIFilterConfig("redact_only")
			group.Targets[0].ToolSupport.OpenAIChat = []string{"tools"}
			cfg.Models["default"] = group
			svc, err := newServiceWithVerifiedFixture(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			body := `{"model":"default","temperature":0,"messages":[{"role":"user","content":"hello"}]` + tc.extra + `}`
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+testToken)
			if tc.header != "" {
				r.Header.Set("Cache-Control", tc.header)
			}
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			flushUsageForTest(t, svc)
			var reason decisionCacheReasonRecord
			if err := svc.usage.db.First(&reason).Error; err != nil {
				t.Fatal(err)
			}
			if reason.Status != "bypass" || reason.Reason != tc.reason {
				t.Fatalf("reason=%+v want=%s", reason, tc.reason)
			}
		})
	}
}
