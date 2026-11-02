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
	"time"
)

func TestPIICacheInstructionBoundaries645(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"resp_%d","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer-%d"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`, n, n)
	}))
	defer upstream.Close()
	svc := nativeStreamTestService(t, upstream.URL, "openai-responses", []Target{{Provider: "native", Model: "model"}}, func(cfg *Config) {
		cfg.Server.Cache = CacheConfig{Enabled: true, MaxBytes: 1 << 20, DefaultTTL: time.Minute}
		group := cfg.Models["native-stream"]
		group.PIIFilter = testPIIFilterConfig("redact_only")
		cfg.Models["native-stream"] = group
	})
	for i, parts := range []string{
		`[{"type":"input_text","text":"a"},{"type":"input_text","text":"b\nc"}]`,
		`[{"type":"input_text","text":"a\nb"},{"type":"input_text","text":"c"}]`,
		`[{"type":"input_text","text":"a\nb"},{"type":"input_text","text":"c"}]`,
	} {
		body := `{"model":"native-stream","temperature":0,"input":[{"role":"system","content":` + parts + `},{"role":"user","content":"hi"}]}`
		r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		if w.Code != 200 || hits.Load() != int32(i+1) || !strings.Contains(w.Body.String(), fmt.Sprintf("answer-%d", i+1)) {
			t.Fatalf("distinct target-bound parts collided: status=%d hits=%d body=%s", w.Code, hits.Load(), w.Body.String())
		}
	}
	if stats := svc.cache.Stats(); !stats.Enabled || stats.Items != 0 {
		t.Fatalf("PII-enabled requests populated response cache: %+v", stats)
	}
}
