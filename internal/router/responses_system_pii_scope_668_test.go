// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSystemPIIScopeAndDisabledControl668(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("enabled=%t/stream=%t", enabled, stream), func(t *testing.T) {
				var hits atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
				}))
				defer upstream.Close()
				svc := nativeStreamTestService(t, upstream.URL, "anthropic", []Target{anthropicToolTarget()}, func(cfg *Config) {
					if enabled {
						group := cfg.Models["native-stream"]
						group.PIIFilter = testPIIFilterConfig("redact_only")
						yes, no := true, false
						group.PIIFilter.ApplyTo.System = &no
						group.PIIFilter.ApplyTo.Messages = &no
						group.PIIFilter.ApplyTo.ResponsesInput = &yes
						cfg.Models["native-stream"] = group
					}
					if stream {
						cfg.Server.Streaming.Translator = "synthesized"
					}
				})
				body := fmt.Sprintf(`{"model":"native-stream","stream":%t,"input":[{"role":"system","content":[{"type":"input_text","text":"sec"},{"type":"input_text","text":"ret"}]},{"role":"user","content":"hi"}]}`, stream)
				r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				if enabled && (w.Code != 400 || hits.Load() != 0 || !strings.Contains(w.Body.String(), `"code":"system_pii_unsupported"`)) {
					t.Fatalf("selective filter must fail closed: status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
				}
				if !enabled && (w.Code != 200 || hits.Load() != 1) {
					t.Fatalf("disabled filter must preserve behavior: status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
				}
			})
		}
	}
}
