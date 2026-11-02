// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A translated system prompt must be filtered at its actual target-bound text
// boundaries, including matches spanning adjacent content parts.
func TestResponsesSystemSplitPII645(t *testing.T) {
	for _, mode := range []string{"redact_only", "fail_on_match"} {
		for _, tool := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tool=%t", mode, tool), func(t *testing.T) {
				var raw atomic.Bool
				var hits atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					data, _ := io.ReadAll(r.Body)
					if strings.Contains(string(data), "secret") {
						raw.Store(true)
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
				}))
				defer upstream.Close()
				svc := nativeStreamTestService(t, upstream.URL, "anthropic", []Target{anthropicToolTarget()}, func(cfg *Config) {
					cfg.Server.Cache = CacheConfig{Enabled: true, MaxBytes: 1 << 20, DefaultTTL: time.Minute}
					group := cfg.Models["native-stream"]
					group.PIIFilter = testPIIFilterConfig(mode)
					group.PIIFilter.Rules = []PIIFilterRule{{Name: "secret", Expression: "secret", PlaceholderPrefix: "SECRET"}}
					cfg.Models["native-stream"] = group
				})
				extra := ""
				if tool {
					extra = `,"tools":[{"type":"function","name":"write","parameters":{"type":"object"}}]`
				}
				body := `{"model":"native-stream","temperature":0,"input":[{"role":"system","content":[{"type":"input_text","text":"sec"},{"type":"input_text","text":"ret"}]},{"role":"user","content":"hi"}]` + extra + `}`
				r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+testToken)
				w := httptest.NewRecorder()
				svc.Handler().ServeHTTP(w, r)
				if raw.Load() {
					t.Fatalf("translated upstream system contained matched cleartext, status=%d hits=%d", w.Code, hits.Load())
				}
				if w.Code != http.StatusBadRequest || hits.Load() != 0 || !strings.Contains(w.Body.String(), `"code":"system_pii_unsupported"`) {
					t.Fatalf("PII-enabled bridge must fail closed: status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
				}
			})
		}
	}
}
