// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"testing"
)

func TestUsageHardening(t *testing.T) {
	for _, tc := range []struct {
		name, body     string
		in, out, total int
		warning        bool
	}{
		{"explicit zeros", `{"input_tokens":0,"prompt_tokens":100,"output_tokens":0,"completion_tokens":50}`, 0, 0, 0, false},
		{"aliases", `{"prompt_tokens":100,"completion_tokens":50}`, 100, 50, 150, false},
		{"numeric strings", `{"input_tokens":"100","output_tokens":"50","total_tokens":"151"}`, 100, 50, 151, false},
		{"negative", `{"input_tokens":-2,"output_tokens":-3,"total_tokens":-5,"completion_tokens_details":{"reasoning_tokens":-1}}`, 0, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal([]byte(tc.body), &m); err != nil {
				t.Fatal(err)
			}
			u := usageFromMap(m)
			if u.InputTokens != tc.in || u.OutputTokens != tc.out || u.TotalTokens != tc.total || (len(u.Warnings) > 0) != tc.warning {
				t.Fatalf("usage=%+v", u)
			}
			if tc.warning && (u.ReasoningTokens == nil || *u.ReasoningTokens != 0) {
				t.Fatal("negative reasoning not clamped")
			}
		})
	}
	u := usageFromMap(map[string]any{"input_tokens": json.Number("100"), "output_tokens": json.Number("50"), "cost": json.Number("0.25")})
	if u.InputTokens != 100 || u.OutputTokens != 50 || u.UpstreamReportedTotalCostUSD != 0.25 {
		t.Fatalf("UseNumber usage=%+v", u)
	}
	for _, v := range []any{json.Number("0.25"), "0.25", int64(1)} {
		if n, ok := numberAsFloat(v); !ok || n <= 0 {
			t.Fatalf("numeric cost %v: %v %v", v, n, ok)
		}
	}
	for _, v := range []any{json.Number("bad"), "bad", nil} {
		if _, ok := numberAsInt(v); ok {
			t.Fatalf("accepted %v", v)
		}
	}
	dst := Usage{}
	mergeStreamUsage(&dst, Usage{InputTokens: 100, OutputTokens: 20, TotalTokens: 150})
	mergeStreamUsage(&dst, Usage{OutputTokens: 30})
	if dst.TotalTokens != 150 {
		t.Fatalf("provider total overwritten: %+v", dst)
	}
	dst = Usage{}
	mergeStreamUsage(&dst, Usage{InputTokens: 10, OutputTokens: 2})
	if dst.TotalTokens != 12 {
		t.Fatalf("missing total: %+v", dst)
	}
	failed, served := 100, 7
	row := rowFromRecord(logRecord{Usage: Usage{OutputTokens: 10}, AttemptsDetail: []attemptLogRecord{{ReasoningTokens: &failed}, {Selected: true, ReasoningTokens: &served}}})
	if row.ReasoningTokens == nil || *row.ReasoningTokens != 7 || row.ReasoningReportedAttemptCount != 2 {
		t.Fatalf("served usage=%+v", row)
	}
}
