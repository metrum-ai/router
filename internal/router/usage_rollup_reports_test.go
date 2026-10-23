// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokenBreakdownRollupOnlyReadsCompletedWholeDays(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	day := today.Add(-24 * time.Hour)
	for _, tc := range []struct {
		name     string
		from, to time.Time
	}{
		{"complete", day, today}, {"premature-finalization", day.Add(-24 * time.Hour), today}, {"fractional-start", day.Add(time.Nanosecond), today}, {"partial-end", day, today.Add(-time.Nanosecond)}, {"current-day", today, today.Add(24 * time.Hour)}, {"future-day", today.Add(24 * time.Hour), today.Add(48 * time.Hour)},
	} {
		ts := tc.from.Add(time.Hour)
		if tc.name == "premature-finalization" {
			ts = day.Add(time.Hour)
		}
		rec := pricedTokenBreakdownRecord(tc.name, "miss", ts)
		rec.CallerID = tc.name
		rows := dailyRollupRecords([]usageRow{rowFromRecord(rec)}, "finalized", formatUsageTime(tc.from), formatUsageTime(tc.to), usageRollupBaselineFromOptions(UsageRollupOptions{}))
		run := usageRollupRunRecord{RollupType: "daily", Status: "finalized", WindowStart: formatUsageTime(tc.from), WindowEnd: formatUsageTime(tc.to), FinalizedAt: formatUsageTime(time.Now())}
		if tc.name == "premature-finalization" {
			run.FinalizedAt = formatUsageTime(day.Add(time.Hour))
		}
		if err := store.db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
		rows[0].RunID = run.ID
		if err := store.db.Create(&rows[0]).Error; err != nil {
			t.Fatal(err)
		}
	}
	report, err := store.adminOverviewSQL(UsageReportOptions{From: day, To: today.Add(48 * time.Hour), RollupHistory: true}, adminSavingsBaselineDTO{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total.Calls != 1 || report.Total.InputTokens != 100 || report.Total.ReasoningTokens != 20 {
		t.Fatalf("partial/current/future day leaked into historical totals: %+v", report.Total)
	}
}

func TestTokenBreakdownRollupFilterValidation(t *testing.T) {
	day := time.Now().UTC().Truncate(24 * time.Hour)
	yes := true
	for _, tc := range []struct {
		name, path, query string
		opts              UsageReportOptions
		bad               bool
	}{
		{name: "raw", path: "summary", query: "history_source=raw", opts: UsageReportOptions{ReasoningPresent: &yes}},
		{name: "unknown-source", path: "summary", query: "history_source=maybe", bad: true},
		{name: "requests", path: "requests", query: "history_source=rollup", bad: true},
		{name: "reasoning-row-filter", path: "summary", query: "history_source=rollup", opts: UsageReportOptions{ReasoningPresent: &yes}, bad: true},
		{name: "cache-row-filter", path: "summary", query: "history_source=rollup", opts: UsageReportOptions{CachePresent: &yes}, bad: true},
		{name: "status-row-filter", path: "summary", query: "history_source=rollup", opts: UsageReportOptions{Status: 200}, bad: true},
		{name: "private-source-filter", path: "summary", query: "history_source=rollup", opts: UsageReportOptions{CallerIP: "127.0.0.1"}, bad: true},
		{name: "usage", path: "usage-by-caller", query: "history_source=rollup", opts: UsageReportOptions{CallerProject: "private", CallerEnvironment: "production"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.From = day.Add(-25 * time.Hour)
			opts.To = day.Add(time.Hour)
			q, _ := url.ParseQuery(tc.query)
			err := configureRollupHistory(&opts, q, "/admin/reports/api/"+tc.path, "/admin/reports")
			if (err != nil) != tc.bad {
				t.Fatalf("err=%v bad=%v", err, tc.bad)
			}
			if err == nil && q.Get("history_source") == "rollup" {
				if !opts.From.Equal(day.Add(-48*time.Hour)) || !opts.To.Equal(day) {
					t.Fatalf("not UTC day-aligned: %+v", opts)
				}
				if opts.CallerProject != tc.opts.CallerProject || opts.CallerEnvironment != tc.opts.CallerEnvironment {
					t.Fatal("authorization scope changed")
				}
			}
		})
	}
}
func TestTokenBreakdownMetricsUsePersistedReasoningCoverage(t *testing.T) {
	measured, final := 30, 10
	rec := logRecord{CallerID: "alice", UsageSource: "reported", ReasoningCoverageMeasured: true, ReasoningTokens: &measured, ReasoningAttemptCount: 2, ReasoningSuccessfulAttemptCount: 2, ReasoningReportedAttemptCount: 2, Usage: Usage{InputTokens: 100, OutputTokens: 40, TotalTokens: 140, ReasoningTokens: &final}}
	m := newMetricsStore()
	m.Observe(rec)
	text := m.Prometheus(nil, MigrationStatus{})
	var got string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "metrum_ai_router_reasoning_tokens_total{") {
			got = line
		}
	}
	if !strings.HasSuffix(got, " 30") {
		t.Fatalf("metrics did not use persisted attempt coverage: %s", got)
	}
	row := rowFromRecord(rec)
	if row.ReasoningTokens == nil || *row.ReasoningTokens != 30 {
		t.Fatal("fixture no longer represents stored reasoning coverage")
	}
}
