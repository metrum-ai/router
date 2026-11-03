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

// A Responses system history on a PII-enabled group is served by a Messages
// target now that the assembled instruction is filtered at its transmitted
// boundary (#645). Independently safe parts must not form an unfiltered match
// after concatenation, and fail_on_match must block before dispatch.
func TestResponsesSystemSplitPII645(t *testing.T) {
	for _, mode := range []string{"redact_only", "fail_on_match"} {
		for _, tool := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tool=%t", mode, tool), func(t *testing.T) {
				var cleartext atomic.Bool
				var hits atomic.Int32
				var upstream map[string]any
				upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					data, _ := io.ReadAll(r.Body)
					_ = json.Unmarshal(data, &upstream)
					if strings.Contains(string(data), "secret") {
						cleartext.Store(true)
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
				}))
				defer upstreamServer.Close()
				svc := nativeStreamTestService(t, upstreamServer.URL, "anthropic", []Target{anthropicToolTarget()}, func(cfg *Config) {
					cfg.Server.Cache = CacheConfig{Enabled: true, MaxBytes: 1 << 20, DefaultTTL: time.Minute}
					piiInstructionGroup(cfg, mode, spanningPartsRule)
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
				if cleartext.Load() {
					t.Fatalf("a match that only exists after assembly reached the upstream: %#v", upstream)
				}
				switch mode {
				case "redact_only":
					if w.Code != http.StatusOK || hits.Load() != 1 {
						t.Fatalf("filtered system history must be served: status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
					}
					if system, _ := upstream["system"].(string); !strings.Contains(system, "[SECRET_1]") {
						t.Fatalf("upstream instruction was not filtered: %q", system)
					}
					if stats := svc.cache.Stats(); stats.Items != 0 {
						t.Fatalf("PII-enabled request populated the response cache: %+v", stats)
					}
				case "fail_on_match":
					if w.Code != http.StatusBadRequest || hits.Load() != 0 || !strings.Contains(w.Body.String(), "pii-filter-blocked") {
						t.Fatalf("fail_on_match must block before dispatch: status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
					}
				}
			})
		}
	}
}
