// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUsageProvenanceStreamOptions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		raw      map[string]any
		disabled bool
		want     string
	}{
		{"inject", nil, false, `{"include_usage":true}`},
		{"preserve", map[string]any{"stream_options": map[string]any{"include_usage": false, "custom": true}}, false, `{"custom":true,"include_usage":false}`},
		{"opt-out", nil, true, "null"},
		{"explicit-with-opt-out", map[string]any{"stream_options": map[string]any{"include_usage": false}}, true, `{"include_usage":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := enableNativeUpstreamStream([]byte(`{"model":"test"}`), &IRRequest{Raw: tc.raw}, "openai-chat", tc.disabled)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(data, &body); err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(body["stream_options"])
			if string(got) != tc.want {
				t.Fatalf("options=%s want=%s", got, tc.want)
			}
		})
	}
}

func TestUsageProvenanceStreamEstimateAndRollups(t *testing.T) {
	req := &IRRequest{MaxTokens: 32000}
	usage := estimatedStreamUsage(req, strings.Repeat("a", 160))
	if usage.OutputTokens != 40 {
		t.Fatalf("estimate=%+v", usage)
	}
	row := usageRow{TS: time.Now(), UsageSource: "estimated", InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens}
	rec := recordFromRow(row)
	roundtrip, err := rowFromUsageRecord(*rec)
	if err != nil || rec.UsageSource != "estimated" || roundtrip.UsageSource != "estimated" {
		t.Fatal("lost provenance")
	}
	daily := periodRollupRecords([]usageRow{row}, "daily", "finalized", "2026-10-01", "2026-10-02", usageRollupBaseline{})
	if len(daily) != 1 || daily[0].EstimatedRequestCount != 1 || daily[0].EstimatedOutputTokens != 40 {
		t.Fatalf("daily=%+v", daily)
	}
	hourly := aggregateRollupRecordsFromDaily(daily, "hourly")
	if len(hourly) != 1 || hourly[0].EstimatedOutputTokens != 40 {
		t.Fatalf("hourly=%+v", hourly)
	}
	var a agg
	a.add(row)
	if adminSummaryFromAgg(&a).EstimatedTokenShare != 100 {
		t.Fatal("missing estimated KPI")
	}
	if adminRequestFromRow(row).UsageSource != "estimated" {
		t.Fatal("missing detail provenance")
	}
}

func TestUsageProvenanceMetrics(t *testing.T) {
	m := newMetricsStore()
	for _, source := range []string{"reported", "estimated"} {
		m.Observe(logRecord{CallerID: "alice", UsageSource: source, Usage: Usage{InputTokens: 10, OutputTokens: 40, TotalTokens: 50}})
	}
	text := m.Prometheus(nil, MigrationStatus{})
	for _, source := range []string{"reported", "estimated"} {
		if !strings.Contains(text, `usage_source="`+source+`"`) {
			t.Fatalf("missing source %s: %s", source, text)
		}
	}
}

func TestUsageProvenanceSchemaAndFilter(t *testing.T) {
	store, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := verifyUsageProvenanceMigration(store.db); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"", "reported", "estimated"} {
		if err := store.db.Create(&usageRecord{RequestID: source + "test", UsageSource: source}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := applyUsageProvenanceMigration(store.db); err != nil {
		t.Fatal(err)
	}
	var unknown int64
	store.db.Model(&usageRecord{}).Where("usage_source = ''").Count(&unknown)
	if unknown != 1 {
		t.Fatal("invented historic provenance")
	}
}
