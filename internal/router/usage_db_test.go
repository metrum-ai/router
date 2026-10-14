// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteDriver "github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestUsageStoreRejectsLegacyRequestAttemptsMissingRetryAfter(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := gorm.Open(sqliteDriver.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&requestAttemptRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&requestAttemptRecord{}, "RetryAfterMS"); err != nil {
		t.Fatal(err)
	}
	for _, index := range []string{
		"idx_request_attempt_request",
		"idx_request_attempt_ts",
		"idx_request_attempt_provider_model",
		"idx_request_attempt_status",
		"idx_request_attempt_error",
		"idx_request_attempt_timeout",
		"idx_request_attempt_cancel",
	} {
		if err := db.Migrator().CreateIndex(&requestAttemptRecord{}, index); err != nil {
			t.Fatal(err)
		}
	}
	if db.Migrator().HasColumn(&requestAttemptRecord{}, "retry_after_ms") {
		t.Fatal("legacy request_attempts unexpectedly retained retry_after_ms")
	}
	if err := db.Exec(`
INSERT INTO request_attempts (
	request_id, attempt_index, ts, provider, model, dialect, endpoint_host,
	duration_ms, status_code, error_class, error_message, retryable, timed_out,
	client_canceled, selected, fallback_reason, request_bytes, response_bytes,
	attempt_timeout_ms
) VALUES (
	'req_legacy', 1, '2026-06-29T00:00:00.000Z', 'mock', 'mock-model',
	'openai-chat', 'example.test', 12, 200, '', '', false, false, false, true,
	'', 123, 456, 1000
)`).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenUsageStorePath(dbPath)
	if store != nil {
		_ = store.Close()
		t.Fatal("legacy store missing retry_after_ms unexpectedly opened")
	}
	if err == nil || !strings.Contains(err.Error(), "required usage column request_attempts.retry_after_ms is missing") {
		t.Fatalf("OpenUsageStorePath error = %v, want missing retry_after_ms rejection", err)
	}
}

func TestUsageStoreMigratesUpstreamErrorDetailsTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	store, err := OpenUsageStorePath(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !store.db.Migrator().HasTable(&requestUpstreamErrorDetailRecord{}) {
		t.Fatal("request_upstream_error_details table was not created")
	}
	for _, column := range []string{"request_id", "attempt_index", "seq", "status_code", "error_class", "field_name", "field_value", "source", "truncated"} {
		if !store.db.Migrator().HasColumn(&requestUpstreamErrorDetailRecord{}, column) {
			t.Fatalf("request_upstream_error_details missing column %s", column)
		}
	}
}

func TestUsageReportImportsJSONLAndRendersMarkdown(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	dbPath := filepath.Join(dir, "usage.sqlite")
	raw := strings.Join([]string{
		`{"ts":"2026-06-14T01:15:00.987654321Z","request_id":"req_1","caller_id":"sudarshan-prod","caller_user":"sudarshan","caller_project":"metrum-insights","caller_environment":"prod","caller_ip":"203.0.113.10","token_id":"rtr_metrum_sudarshan_metrum-insights_prod_k20260614","client":"codex","inbound_dialect":"openai-responses","requested_model":"big-coder","resolved_group":"big-coder","strategy":"weighted","target_provider":"openai","target_model":"gpt-5.4","target_dialect":"openai-responses","stream":false,"cache":"miss","status":200,"attempts":1,"fallback_used":false,"latency_ms":1200,"ttfb_ms":300,"usage":{"input_tokens":100,"output_tokens":40,"total_tokens":140},"input_price_per_million_usd":2,"output_price_per_million_usd":8,"input_cost_usd":0.0002,"output_cost_usd":0.00032,"total_cost_usd":0.00052,"pricing_source":"https://example.test/openai","pricing_updated_at":"2026-06-14","quota_state":"ok","key_state":"active","warnings":[]}`,
		`{"ts":"2026-06-14T02:05:00.000Z","request_id":"req_2","caller_id":"clay-prod","caller_user":"clay","caller_project":"metrum-insights","caller_environment":"prod","token_id":"rtr_metrum_clay_metrum-insights_prod_k20260614","client":"claude-code","inbound_dialect":"anthropic","requested_model":"small","resolved_group":"small","strategy":"weighted","target_provider":"openai","target_model":"gpt-5.4-nano","target_dialect":"openai-chat","stream":true,"cache":"hit","status":200,"attempts":0,"fallback_used":false,"latency_ms":30,"usage":{"input_tokens":12,"output_tokens":6,"total_tokens":18},"input_price_per_million_usd":0.1,"output_price_per_million_usd":0.625,"input_cost_usd":0.0000012,"output_cost_usd":0.00000375,"total_cost_usd":0.00000495,"pricing_source":"https://example.test/openai","pricing_updated_at":"2026-06-14","quota_state":"ok","key_state":"active","warnings":[]}`,
		`{"ts":"2026-06-14T02:45:00.000Z","request_id":"req_3","caller_id":"clay-prod","caller_user":"clay","caller_project":"metrum-insights","caller_environment":"prod","token_id":"rtr_metrum_clay_metrum-insights_prod_k20260614","client":"claude-code","inbound_dialect":"anthropic","requested_model":"small","resolved_group":"small","strategy":"weighted","target_provider":"minimax","target_model":"MiniMax-M3","target_dialect":"openai-chat","stream":false,"cache":"miss","status":502,"attempts":2,"fallback_used":true,"latency_ms":900,"usage":{"input_tokens":20,"output_tokens":0,"total_tokens":20},"input_price_per_million_usd":0.3,"output_price_per_million_usd":1.2,"input_cost_usd":0.000006,"output_cost_usd":0,"total_cost_usd":0.000006,"pricing_source":"https://example.test/minimax","pricing_updated_at":"2026-06-14","quota_state":"ok","key_state":"active","traffic_shape_applied":true,"traffic_shape_decision":"rejected","traffic_shape_scope":"caller","traffic_shape_bucket":"caller.total_reserved_tokens_per_sec","traffic_shape_retry_after_ms":750,"traffic_shape_queue_wait_ms":0,"traffic_shape_estimated_input_tokens":20,"traffic_shape_reserved_output_tokens":200,"traffic_shape_total_reserved_tokens":220,"traffic_shape_events":[{"seq":1,"scope":"caller","bucket":"total_reserved","decision":"rejected","cost":220,"retry_after_ms":750,"estimated_input_tokens":20,"reserved_output_tokens":200,"total_reserved_tokens":220}],"upstream_shape_events":[{"seq":1,"ts":"2026-06-14T02:45:00.000Z","scope":"provider","provider":"minimax","model":"MiniMax-M3","dialect":"openai-chat","bucket":"adaptive_backoff","decision":"cooldown_started","retry_after_ms":1000,"estimated_input_tokens":20,"reserved_output_tokens":200,"total_reserved_tokens":220,"backoff_reason":"adaptive-backoff-provider-429"}],"warnings":[],"error":"upstream-failed"}`,
		"",
	}, "\n")
	if err := os.WriteFile(logPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}

	md, err := GenerateUsageMarkdown(UsageReportOptions{
		DBPath:          dbPath,
		MigrationPolicy: usageDBMigrationPolicyAutoSafe,
		LogPath:         logPath,
		From:            time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC),
		To:              time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Metrum AI Router Usage Report",
		"Period UTC: `2026-06-14T00:00:00.000Z` to `2026-06-15T00:00:00.000Z`",
		"Requests: `3`",
		"Errors: `1`",
		"Total Tokens: `178`; Input Tokens: `132`; Output Tokens: `46`",
		"Cost: `$0.000531` total, `$0.000207` input, `$0.000000` image, `$0.000324` output",
		"rtr_metrum_clay_metrum-insights_prod_k20260614",
		"| Token ID | Owner User | Project | Env | Caller ID |",
		"| openai | gpt-5.4 | 1 | 0 | 140 | 100 | 40 | $0.000520 |",
		"| minimax | MiniMax-M3 | 1 | 1 | 20 | 20 | 0 | $0.000006 |",
		"| 2026-06-14 02:00 | 2 | 1 | 38 | 32 | 6 | $0.000011 |",
		"## Usage By Caller IP",
		"| 203.0.113.10 | 1 | 0 | 140 | 100 | 40 | $0.000520 |",
		"## Hourly Usage By Caller IP",
		"| 2026-06-14 01:00 | 203.0.113.10 | 1 | 0 | 140 | 100 | 40 | $0.000520 |",
		"## Cache Summary",
		"## Downstream User Performance",
		"## Upstream Endpoint Performance",
		"## Per-Request Throughput",
		"| 2026-06-14T01:15:00.987Z | `203.0.113.10` | `req_1` |",
		"## Traffic Shaping Summary",
		"P50 queue wait ms",
		"P95 queue wait ms",
		"## Provider Capacity Shaping",
		"adaptive-backoff-provider-429",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("report missing %q:\n%s", want, md)
		}
	}

	imported, err := ImportUsageJSONLTo(freshSQLiteUsageDBConfigForTest(dbPath), logPath)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 3 {
		t.Fatalf("imported=%d", imported)
	}
	store, err := OpenUsageStorePath(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted usageRecord
	if err := store.db.First(&persisted, "request_id = ?", "req_1").Error; err != nil {
		store.Close()
		t.Fatal(err)
	}
	if got, want := persisted.TS, "2026-06-14T01:15:00.987654321Z"; got != want {
		store.Close()
		t.Fatalf("persisted timestamp=%q, want lossless %q", got, want)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	md, err = GenerateUsageMarkdown(UsageReportOptions{
		DBPath:          dbPath,
		MigrationPolicy: usageDBMigrationPolicyAutoSafe,
		From:            time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC),
		To:              time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "Requests: `3`") {
		t.Fatalf("duplicate import changed request count:\n%s", md)
	}
}

func TestUsageCLIMarkdownTimestampsUseUTCMillisecondsWithoutChangingBrowserPrecision(t *testing.T) {
	from := time.Date(2026, 6, 14, 1, 15, 0, 987654321, time.UTC)
	to := time.Date(2026, 6, 14, 2, 15, 0, 123456789, time.UTC)
	row := usageRow{
		TS:             time.Date(2026, 6, 14, 1, 59, 59, 999999999, time.UTC),
		RequestID:      "req_subsecond",
		CallerIP:       "198.51.100.42",
		RequestedModel: "default",
		ResolvedGroup:  "default",
		TargetProvider: "mock",
		TargetModel:    "mock-model",
		Status:         200,
	}

	md := renderUsageCLIMarkdown(from, to, []usageRow{row}, decisionTelemetrySummary{}, nil)
	for _, want := range []string{
		"Period UTC: `2026-06-14T01:15:00.987Z` to `2026-06-14T02:15:00.123Z`",
		"| 2026-06-14T01:59:59.999Z | `198.51.100.42` | `req_subsecond` |",
		"| 2026-06-14 01:00 | 1 |",
		"| 2026-06-14 | 1 |",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("Markdown report missing %q:\n%s", want, md)
		}
	}
	for _, notWant := range []string{"2026-06-14T01:15:00.987654321Z", "2026-06-14T01:59:59.999999999Z"} {
		if strings.Contains(md, notWant) {
			t.Fatalf("Markdown report retained nanosecond precision %q:\n%s", notWant, md)
		}
	}
	if got, want := formatUsageTime(row.TS), "2026-06-14T01:59:59.999999999Z"; got != want {
		t.Fatalf("formatUsageTime=%q, want lossless %q", got, want)
	}

	browserMarkdown := renderUsageMarkdown(from, to, []usageRow{row}, decisionTelemetrySummary{}, nil)
	for _, want := range []string{
		"Period UTC: `2026-06-14T01:15:00.987654321Z` to `2026-06-14T02:15:00.123456789Z`",
		"| 2026-06-14T01:59:59.999999999Z | `198.51.100.42` | `req_subsecond` |",
	} {
		if !strings.Contains(browserMarkdown, want) {
			t.Fatalf("browser Markdown report missing existing-precision timestamp %q:\n%s", want, browserMarkdown)
		}
	}
}

func TestUsageJSONLImportSkipsOperationalEventRecords(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	dbPath := filepath.Join(dir, "usage.sqlite")
	raw := strings.Join([]string{
		`{"ts":"2026-06-14T01:00:00.000Z","event_type":"admin_report_query_failed","report":"requests","handler":"handleAdminReportRequests","admin_request_id":"admin-report-test","request_id":"req_real","error_class":"sqlite","error_message":"sanitized failure"}`,
		`{"ts":"2026-06-14T01:15:00.000Z","request_id":"req_real","caller_id":"alice","caller_user":"alice","caller_project":"metrum-insights","caller_environment":"test","token_id":"rtr_alice_test","client":"codex","inbound_dialect":"openai-chat","requested_model":"big-coder","resolved_group":"big-coder","strategy":"static","target_provider":"mock","target_model":"mock-model","target_dialect":"openai-chat","stream":false,"cache":"miss","status":200,"attempts":1,"fallback_used":false,"latency_ms":25,"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5},"quota_state":"ok","key_state":"active","warnings":[]}`,
		"",
	}, "\n")
	if err := os.WriteFile(logPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}

	imported, err := ImportUsageJSONLTo(freshSQLiteUsageDBConfigForTest(dbPath), logPath)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 1 {
		t.Fatalf("imported=%d, want 1 usage row", imported)
	}

	store, err := OpenUsageStorePath(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var rows []usageRecord
	if err := store.db.Order("request_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RequestID != "req_real" || rows[0].CallerID != "alice" {
		t.Fatalf("imported rows=%#v, want only the real usage record", rows)
	}
}

func TestSQLiteUsageDBUsesPrivateFileMode(t *testing.T) {
	clearUmaskForTest(t)
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	store, err := OpenUsageStorePath(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("usage db mode=%#o, want 0600", got)
	}
}

func TestUsageReportRendersThroughputAndCacheSnapshots(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	dbPath := filepath.Join(dir, "usage.sqlite")
	raw := strings.Join([]string{
		`{"ts":"2026-06-14T01:15:00.000Z","request_id":"req_tps","caller_id":"alice","caller_user":"alice","caller_project":"metrum-insights","caller_environment":"test","caller_ip":"198.51.100.22","token_id":"rtr_alice_test","client":"codex","inbound_dialect":"openai-responses","requested_model":"default","resolved_group":"default","strategy":"static","target_provider":"openai","target_model":"gpt-5.4-nano","target_dialect":"openai-responses","stream":false,"cache":"miss","status":200,"attempts":1,"fallback_used":false,"latency_ms":250,"upstream_duration_ms":200,"downstream_duration_ms":25,"upstream_output_tokens_per_sec":50,"upstream_total_tokens_per_sec":75,"downstream_output_tokens_per_sec":400,"downstream_total_tokens_per_sec":600,"usage":{"input_tokens":5,"output_tokens":10,"total_tokens":15},"input_price_per_million_usd":0.2,"output_price_per_million_usd":1.25,"input_cost_usd":0.000001,"output_cost_usd":0.0000125,"total_cost_usd":0.0000135,"cache_enabled":true,"cache_items":2,"cache_bytes":1024,"cache_max_bytes":4096,"cache_occupancy_pct":25,"quota_state":"ok","key_state":"active","warnings":[]}`,
		"",
	}, "\n")
	if err := os.WriteFile(logPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	md, err := GenerateUsageMarkdown(UsageReportOptions{
		DBPath:          dbPath,
		MigrationPolicy: usageDBMigrationPolicyAutoSafe,
		LogPath:         logPath,
		From:            time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC),
		To:              time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"upstream `50.00` output tok/s / `75.00` total tok/s",
		"downstream write `400.00` output tok/s / `600.00` total tok/s",
		"| 1 | 1 | 0 | 1 | 0 | 0.00% | 0.00% | 2 | 1024 | 4096 | 25.00% | 25.00% | 25.00% |",
		"| alice | metrum-insights | test | codex | 1 | 0 | 0 | 15 | 10 | 250 | 250 | 0 | 0 | 25 | 25 | 400.00 | 600.00 | 0 |",
		"| openai | gpt-5.4-nano | openai-responses | 1 | 0 | 1 | 0 | 0 | 15 | 10 | $0.000013 | 200 | 200 | 250 | 250 | 0 | 0 | 50.00 | 75.00 |",
		"| 2026-06-14T01:15:00.000Z | `198.51.100.22` | `req_tps` | `rtr_alice_test` | default | openai | gpt-5.4-nano | 200 | miss | 10 | 15 | $0.000013 | 200 | 25 | 50.00 | 75.00 | 400.00 | 600.00 |",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("report missing %q:\n%s", want, md)
		}
	}
}

func TestUsageReportRendersDecisionTelemetrySummary(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	dbPath := filepath.Join(dir, "usage.sqlite")
	raw := strings.Join([]string{
		`{"ts":"2026-06-14T01:15:00.000Z","request_id":"req_decision","caller_id":"alice","caller_user":"alice","caller_project":"metrum-insights","caller_environment":"test","token_id":"rtr_alice_test","client":"codex","inbound_dialect":"openai-chat","requested_model":"default","resolved_group":"default","strategy":"static","target_provider":"mock","target_model":"mock-model","target_dialect":"openai-chat","stream":false,"cache":"bypass","status":200,"attempts":1,"fallback_used":false,"latency_ms":25,"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"quota_state":"ok","key_state":"active","warnings":[],"decision_shape_features":[{"seq":1,"name":"has_tools","bool_value":true},{"seq":2,"name":"max_token_bucket","text_value":"tiny"},{"seq":3,"name":"input_token_bucket","text_value":"small"}],"decision_candidates":[{"candidate_index":0,"group_target_index":0,"provider":"mock","model":"mock-model","dialect":"openai-chat","context_tokens":8192,"tool_support":true,"structured_output":true,"honors_max_tokens":true,"eligible":true,"selected":true}],"decision_filter_reasons":[{"seq":1,"candidate_index":1,"stage":"request_shape","reason":"tool-support"},{"seq":2,"candidate_index":1,"stage":"request_shape","reason":"max-tokens-honored"},{"seq":3,"candidate_index":1,"stage":"dynamic_score","reason":"dynamic_score_thresholds"},{"seq":4,"candidate_index":1,"stage":"contract","reason":"contract-operational-threshold"}],"routing_decisions":[{"seq":1,"strategy":"static","selected_candidate_index":0,"provider":"mock","model":"mock-model","dialect":"openai-chat","fallback_count":0}],"routing_signals":[{"seq":1,"strategy":"dynamic_score","signal_name":"observed_performance","source":"dynamic_score","bool_value":true}],"dynamic_score_terms":[{"seq":1,"candidate_index":0,"rank":1,"provider":"mock","model":"mock-model","dialect":"openai-chat","term_name":"default","score_name":"latency_score","weight":0.5,"value":0.8,"contribution":0.4,"final_score":0.7,"observation_count":3,"selected":true}],"policy_executions":[{"seq":1,"strategy":"script","policy_kind":"typescript","outcome":"selected","duration_ms":12,"eligible_target_count":1,"selected_candidate_index":0}],"cache_reasons":[{"seq":1,"status":"bypass","reason":"cache-tool-request","candidate_index":0,"provider":"mock","model":"mock-model","dialect":"openai-chat"}]}`,
		"",
	}, "\n")
	if err := os.WriteFile(logPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	md, err := GenerateUsageMarkdown(UsageReportOptions{
		DBPath:          dbPath,
		MigrationPolicy: usageDBMigrationPolicyAutoSafe,
		LogPath:         logPath,
		From:            time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC),
		To:              time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Decision Telemetry Summary",
		"| 3 | 1 | 4 | 1 | 1 | 1 | 1 | 0 | 1 |",
		"### Routing Decisions By Strategy",
		"| static | 1 |",
		"### Target Filter Reasons",
		"| tool-support | 1 |",
		"### Cache Decision Reasons",
		"| cache-tool-request | 1 |",
		"### Dynamic Score Enabled Signals",
		"| observed_performance | 1 |",
		"### Dynamic Score Buckets",
		"| final_score:medium | 1 |",
		"| latency_score:high | 1 |",
		"### Dynamic Threshold Buckets",
		"| dynamic_score_thresholds | 1 |",
		"| max_token_cap_filtered | 1 |",
		"### Max Token Buckets",
		"| tiny | 1 |",
		"### Input Token Buckets",
		"| small | 1 |",
		"### Admission Reasons",
		"| admitted | 1 |",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("report missing %q:\n%s", want, md)
		}
	}
	thresholdSection := markdownSection(md, "### Dynamic Threshold Buckets", "### Max Token Buckets")
	if strings.Contains(thresholdSection, "| contract-operational-threshold |") {
		t.Fatalf("contract threshold leaked into dynamic threshold buckets:\n%s", md)
	}
}

func markdownSection(md, start, end string) string {
	startIdx := strings.Index(md, start)
	if startIdx < 0 {
		return ""
	}
	section := md[startIdx:]
	endIdx := strings.Index(section[len(start):], end)
	if endIdx < 0 {
		return section
	}
	return section[:len(start)+endIdx]
}

func TestDecisionTelemetryPersistsExplicitFalseAndZeroValues(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	store.Emit(logRecord{
		TS:             "2026-06-14T01:15:00.000Z",
		RequestID:      "req_zero_values",
		CallerID:       "alice",
		TokenID:        "rtr_alice_test",
		InboundDialect: "openai-chat",
		RequestedModel: "default",
		ResolvedGroup:  "default",
		Strategy:       "script",
		TargetProvider: "mock",
		TargetModel:    "mock-model",
		TargetDialect:  "openai-chat",
		Cache:          "bypass",
		Status:         200,
		Attempts:       1,
		DecisionCandidates: []decisionCandidateLogRecord{{
			CandidateIndex:  0,
			Provider:        "mock",
			Model:           "mock-model",
			Dialect:         "openai-chat",
			HonorsMaxTokens: false,
			Eligible:        true,
			Selected:        true,
		}},
		PolicyExecutions: []policyExecutionLogRecord{{
			Seq:                    1,
			Strategy:               "script",
			PolicyKind:             "typescript",
			Outcome:                "selected",
			EligibleTargetCount:    1,
			SelectedCandidateIndex: 0,
		}},
	})

	var candidate decisionTargetCandidateRecord
	if err := store.db.Where("request_id = ? AND candidate_index = ?", "req_zero_values", 0).First(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	if candidate.HonorsMaxTokens {
		t.Fatalf("honors_max_tokens should preserve explicit false: %#v", candidate)
	}

	var execution policyExecutionRecord
	if err := store.db.Where("request_id = ? AND seq = ?", "req_zero_values", 1).First(&execution).Error; err != nil {
		t.Fatal(err)
	}
	if execution.SelectedCandidateIndex != 0 {
		t.Fatalf("selected_candidate_index should preserve explicit zero: %#v", execution)
	}
}

func TestUsageReportFiltersRows(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	dbPath := filepath.Join(dir, "usage.sqlite")
	raw := strings.Join([]string{
		`{"ts":"2026-06-14T01:00:00.000Z","request_id":"req_codex_small","caller_id":"codex-small","caller_user":"codex-small","caller_project":"harbor-algotune-pca","caller_environment":"case-1","caller_ip":"203.0.113.10","token_id":"rtr_metrum_codex-small_harbor-algotune-pca_case-1_k20260614","client":"codex","inbound_dialect":"openai-responses","requested_model":"small","resolved_group":"small","strategy":"weighted","target_provider":"openrouter","target_model":"deepseek/deepseek-v4-flash:nitro","target_dialect":"openai-chat","stream":false,"cache":"miss","status":200,"attempts":1,"fallback_used":false,"latency_ms":1200,"usage":{"input_tokens":100,"output_tokens":40,"total_tokens":140},"quota_state":"ok","key_state":"active","warnings":[]}`,
		`{"ts":"2026-06-14T01:15:00.000Z","request_id":"req_claude_small","caller_id":"claude-small","caller_user":"claude-small","caller_project":"harbor-algotune-pca","caller_environment":"case-1","caller_ip":"203.0.113.11","token_id":"rtr_metrum_claude-small_harbor-algotune-pca_case-1_k20260614","client":"claude-code","inbound_dialect":"anthropic","requested_model":"small","resolved_group":"small","strategy":"weighted","target_provider":"minimax","target_model":"MiniMax-M3","target_dialect":"openai-chat","stream":false,"cache":"miss","status":200,"attempts":1,"fallback_used":false,"latency_ms":900,"usage":{"input_tokens":80,"output_tokens":20,"total_tokens":100},"quota_state":"ok","key_state":"active","warnings":[]}`,
		`{"ts":"2026-06-14T01:30:00.000Z","request_id":"req_other","caller_id":"other","caller_user":"other","caller_project":"metrum-insights","caller_environment":"prod","caller_ip":"203.0.113.12","token_id":"rtr_metrum_other_metrum-insights_prod_k20260614","client":"codex","inbound_dialect":"openai-responses","requested_model":"high","resolved_group":"high","strategy":"weighted","target_provider":"openai","target_model":"gpt-5.4-nano","target_dialect":"openai-responses","stream":false,"cache":"miss","status":200,"attempts":1,"fallback_used":false,"latency_ms":1500,"usage":{"input_tokens":300,"output_tokens":100,"total_tokens":400},"quota_state":"ok","key_state":"active","warnings":[]}`,
		"",
	}, "\n")
	if err := os.WriteFile(logPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	md, err := GenerateUsageMarkdown(UsageReportOptions{
		DBPath:            dbPath,
		MigrationPolicy:   usageDBMigrationPolicyAutoSafe,
		LogPath:           logPath,
		From:              time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC),
		To:                time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		CallerProject:     "harbor-algotune-pca",
		CallerEnvironment: "case-1",
		ResolvedGroup:     "small",
		Client:            "codex",
		TokenIDPrefix:     "rtr_metrum_codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Requests: `1`",
		"Total Tokens: `140`; Input Tokens: `100`; Output Tokens: `40`",
		"rtr_metrum_codex-small_harbor-algotune-pca_case-1_k20260614",
		"| openrouter | deepseek/deepseek-v4-flash:nitro | 1 | 0 | 140 | 100 | 40 | $0.000000 |",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("filtered report missing %q:\n%s", want, md)
		}
	}
	for _, notWant := range []string{
		"rtr_metrum_claude-small_harbor-algotune-pca_case-1_k20260614",
		"rtr_metrum_other_metrum-insights_prod_k20260614",
		"gpt-5.4-nano",
	} {
		if strings.Contains(md, notWant) {
			t.Fatalf("filtered report unexpectedly contains %q:\n%s", notWant, md)
		}
	}
}

func TestUsageReportMarkdownEscapesHTMLAndActiveMarkdownCells(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	dbPath := filepath.Join(dir, "usage.sqlite")
	raw := strings.Join([]string{
		`{"ts":"2026-06-14T01:00:00.000Z","request_id":"req_md","caller_id":"caller|pipe","caller_user":"<script>alert(1)</script>","caller_project":"[project](javascript:alert(1))","caller_environment":"prod","caller_ip":"203.0.113.10","token_id":"rtr_md","client":"<img src=x onerror=alert(1)>","inbound_dialect":"openai-chat","requested_model":"[model](javascript:alert(1))","resolved_group":"group|name","strategy":"weighted","target_provider":"<script>provider()</script>","target_model":"<img src=x onerror=alert(1)>","target_dialect":"openai-chat","stream":false,"cache":"miss","status":200,"attempts":1,"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"quota_state":"ok","key_state":"active","warnings":[]}`,
		"",
	}, "\n")
	if err := os.WriteFile(logPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	md, err := GenerateUsageMarkdown(UsageReportOptions{
		DBPath:          dbPath,
		MigrationPolicy: usageDBMigrationPolicyAutoSafe,
		LogPath:         logPath,
		From:            time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC),
		To:              time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, notWant := range []string{
		"<script>",
		"<img",
		"[project](javascript:alert(1))",
		"[model](javascript:alert(1))",
		"caller|pipe",
		"group|name",
	} {
		if strings.Contains(md, notWant) {
			t.Fatalf("markdown preserved unsafe value %q:\n%s", notWant, md)
		}
	}
	for _, want := range []string{
		"&lt;script&gt;alert\\(1\\)&lt;/script&gt;",
		"\\[project\\]\\(javascript:alert\\(1\\)\\)",
		"&lt;img src=x onerror=alert\\(1\\)&gt;",
		"caller\\|pipe",
		"group\\|name",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing escaped value %q:\n%s", want, md)
		}
	}
}

func TestSecurityAccessEventsFilterCallerEnvironment(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	store.EmitSecurityAccessEvent(securityAccessEvent{
		TS:                now,
		RequestID:         "req_test",
		EventType:         "api_allowed",
		Surface:           "v1_chat_completions",
		StatusCode:        200,
		Outcome:           "allowed",
		CallerProject:     "example",
		CallerEnvironment: "test",
	})
	store.EmitSecurityAccessEvent(securityAccessEvent{
		TS:                now,
		RequestID:         "req_prod",
		EventType:         "api_allowed",
		Surface:           "v1_chat_completions",
		StatusCode:        200,
		Outcome:           "allowed",
		CallerProject:     "example",
		CallerEnvironment: "prod",
	})
	rows, err := store.securityAccessEvents(SecurityReportOptions{
		From:              now.Add(-time.Hour),
		To:                now.Add(time.Hour),
		Limit:             10,
		CallerProject:     "example",
		CallerEnvironment: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RequestID != "req_test" {
		t.Fatalf("environment filter rows=%#v, want only req_test", rows)
	}
}

func TestUsageRollupDailyTotalsDraftRerunAndFinalize(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	from := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	ttfb := int64(25)
	upstream := int64(90)
	downstream := int64(10)
	upstreamTPS := 12.5
	downstreamTPS := 25.0
	rows := []usageRow{
		{
			TS:                            from.Add(time.Hour),
			RequestID:                     "req_rollup_1",
			CallerID:                      "caller-alice",
			CallerUser:                    "alice",
			CallerProject:                 "analytics",
			CallerEnvironment:             "prod",
			TokenID:                       "rtr_alice",
			Client:                        "codex",
			InboundDialect:                "openai-chat",
			RequestedModel:                "default",
			ResolvedGroup:                 "default",
			Strategy:                      "weighted",
			TargetProvider:                "mock",
			TargetModel:                   "model-a",
			TargetDialect:                 "openai-chat",
			Cache:                         "miss",
			Status:                        200,
			Attempts:                      1,
			LatencyMS:                     100,
			TTFBMS:                        &ttfb,
			UpstreamMS:                    &upstream,
			DownstreamMS:                  &downstream,
			UpstreamOutputTPS:             &upstreamTPS,
			DownstreamOutputTPS:           &downstreamTPS,
			InputTokens:                   10,
			OutputTokens:                  5,
			TotalTokens:                   15,
			InputCostUSD:                  0.10,
			ImageCostUSD:                  0.02,
			OutputCostUSD:                 0.20,
			TotalCostUSD:                  0.32,
			UpstreamReportedInputCostUSD:  0.11,
			UpstreamReportedOutputCostUSD: 0.21,
			UpstreamReportedTotalCostUSD:  0.33,
			InputHasImage:                 true,
			InputImageCount:               2,
			InputImageTokens:              321,
			PIIFilterApplied:              true,
			ContractBucket:                "ok",
			TargetValidationStatus:        "validated",
			CacheEnabled:                  true,
			CacheItems:                    3,
			CacheBytes:                    1000,
			CacheMaxBytes:                 4000,
			CacheOccupancyPct:             25,
		},
		{
			TS:                from.Add(2 * time.Hour),
			RequestID:         "req_rollup_2",
			CallerID:          "caller-bob",
			CallerUser:        "bob",
			CallerProject:     "platform",
			CallerEnvironment: "prod",
			TokenID:           "rtr_bob",
			Client:            "claude-code",
			InboundDialect:    "anthropic",
			RequestedModel:    "default",
			ResolvedGroup:     "default",
			Strategy:          "failover",
			TargetProvider:    "mock",
			TargetModel:       "model-b",
			TargetDialect:     "openai-chat",
			Stream:            true,
			Cache:             "hit",
			Status:            502,
			Attempts:          2,
			FallbackUsed:      true,
			LatencyMS:         250,
			InputTokens:       4,
			OutputTokens:      6,
			InputCostUSD:      0.04,
			OutputCostUSD:     0.06,
			TotalCostUSD:      0.10,
		},
		{
			TS:             to.Add(time.Hour),
			RequestID:      "req_rollup_outside",
			CallerUser:     "outside",
			TokenID:        "rtr_outside",
			RequestedModel: "default",
			ResolvedGroup:  "default",
			Cache:          "miss",
			Status:         200,
			Attempts:       1,
			LatencyMS:      999,
			InputTokens:    100,
			OutputTokens:   100,
			TotalTokens:    200,
		},
	}
	for _, row := range rows {
		if err := store.db.Create(recordFromRow(row)).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, feature := range []decisionShapeFeatureRecord{
		{RequestID: "req_rollup_1", Seq: 1, FeatureName: "max_token_bucket", TextValue: "tiny"},
		{RequestID: "req_rollup_1", Seq: 2, FeatureName: "input_token_bucket", TextValue: "large"},
		{RequestID: "req_rollup_2", Seq: 1, FeatureName: "max_token_bucket", TextValue: "standard"},
		{RequestID: "req_rollup_2", Seq: 2, FeatureName: "input_token_bucket", TextValue: "small"},
	} {
		if err := store.db.Create(&feature).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, signal := range []routingSignalRecord{
		{RequestID: "req_rollup_1", Seq: 1, Strategy: "dynamic_score", SignalName: "observed_performance", Source: "dynamic_score", BoolValue: true},
		{RequestID: "req_rollup_1", Seq: 2, Strategy: "dynamic_score", SignalName: "cost", Source: "dynamic_score", BoolValue: true},
	} {
		if err := store.db.Create(&signal).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, term := range []dynamicScoreTermRecord{
		{RequestID: "req_rollup_1", Seq: 1, CandidateIndex: 0, Rank: 1, Provider: "mock", Model: "model-a", Dialect: "openai-chat", TermName: "default", ScoreName: "latency_score", Value: 0.8, ValueBucket: "high", FinalScore: 0.7, FinalScoreBucket: "medium", Selected: true},
	} {
		if err := store.db.Create(&term).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, reason := range []decisionTargetFilterReasonRecord{
		{RequestID: "req_rollup_2", Seq: 1, CandidateIndex: 1, Stage: "request_shape", Reason: "max-tokens-honored"},
		{RequestID: "req_rollup_2", Seq: 2, CandidateIndex: 1, Stage: "dynamic_score", Reason: "dynamic_score_thresholds"},
	} {
		if err := store.db.Create(&reason).Error; err != nil {
			t.Fatal(err)
		}
	}

	result, err := store.generateUsageRollup(UsageRollupOptions{From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "draft" || result.SourceRequestCount != 2 || result.DailyRows != 2 || result.DecisionBucketRows == 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	var dailyRows []usageRollupDailyRecord
	if err := store.db.Where("run_id = ?", result.RunID).Order("token_id ASC").Find(&dailyRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(dailyRows) != 2 {
		t.Fatalf("got %d rollup rows, want 2: %#v", len(dailyRows), dailyRows)
	}
	alice := dailyRows[0]
	bob := dailyRows[1]
	if alice.TokenID != "rtr_alice" || alice.CallerUser != "alice" || alice.CallerProject != "analytics" || alice.Client != "codex" || alice.TargetModel != "model-a" || !alice.InputHasImage || !alice.PIIFilterApplied || alice.ContractBucket != "ok" || alice.TargetValidationStatus != "validated" {
		t.Fatalf("alice dimension row lost fields: %#v", alice)
	}
	if bob.TokenID != "rtr_bob" || bob.CallerUser != "bob" || bob.CallerProject != "platform" || bob.Client != "claude-code" || bob.TargetModel != "model-b" || bob.StatusClass != "5xx" || !bob.Stream || bob.Cache != "hit" {
		t.Fatalf("bob dimension row lost fields: %#v", bob)
	}
	if alice.DayUTC != "2026-06-14" || alice.SourceRequestCount != 1 || alice.ErrorCount != 0 || alice.StreamCount != 0 {
		t.Fatalf("unexpected alice counts: %#v", alice)
	}
	if bob.DayUTC != "2026-06-14" || bob.SourceRequestCount != 1 || bob.ErrorCount != 1 || bob.StreamCount != 1 {
		t.Fatalf("unexpected bob counts: %#v", bob)
	}
	if alice.CacheMissCount != 1 || bob.CacheHitCount != 1 || bob.FallbackCount != 1 || alice.AttemptCount+bob.AttemptCount != 3 {
		t.Fatalf("unexpected cache/fallback/attempt rollup: alice=%#v bob=%#v", alice, bob)
	}
	if alice.InputTokens+bob.InputTokens != 14 || alice.OutputTokens+bob.OutputTokens != 11 || alice.TotalTokens+bob.TotalTokens != 25 {
		t.Fatalf("unexpected token rollup: alice=%#v bob=%#v", alice, bob)
	}
	if alice.InputImageCount+bob.InputImageCount != 2 || alice.InputImageTokens+bob.InputImageTokens != 321 {
		t.Fatalf("unexpected image rollup: alice=%#v bob=%#v", alice, bob)
	}
	assertNear(t, alice.InputCostUSD+bob.InputCostUSD, 0.14, "input cost")
	assertNear(t, alice.ImageCostUSD+bob.ImageCostUSD, 0.02, "image cost")
	assertNear(t, alice.OutputCostUSD+bob.OutputCostUSD, 0.26, "output cost")
	assertNear(t, alice.TotalCostUSD+bob.TotalCostUSD, 0.42, "total cost")
	assertNear(t, alice.UpstreamReportedInputCostUSD+bob.UpstreamReportedInputCostUSD, 0.11, "upstream input cost")
	assertNear(t, alice.UpstreamReportedOutputCostUSD+bob.UpstreamReportedOutputCostUSD, 0.21, "upstream output cost")
	assertNear(t, alice.UpstreamReportedTotalCostUSD+bob.UpstreamReportedTotalCostUSD, 0.33, "upstream total cost")
	if alice.LatencyMSSum+bob.LatencyMSSum != 350 || maxInt64(alice.LatencyMSMax, bob.LatencyMSMax) != 250 || alice.TTFBMSSum+bob.TTFBMSSum != 25 || alice.TTFBMSCount+bob.TTFBMSCount != 1 || maxInt64(alice.TTFBMSMax, bob.TTFBMSMax) != 25 {
		t.Fatalf("unexpected latency rollup: alice=%#v bob=%#v", alice, bob)
	}
	if alice.UpstreamMSSum+bob.UpstreamMSSum != 90 || alice.UpstreamMSCount+bob.UpstreamMSCount != 1 || maxInt64(alice.UpstreamMSMax, bob.UpstreamMSMax) != 90 || alice.DownstreamMSSum+bob.DownstreamMSSum != 10 || alice.DownstreamMSCount+bob.DownstreamMSCount != 1 || maxInt64(alice.DownstreamMSMax, bob.DownstreamMSMax) != 10 {
		t.Fatalf("unexpected duration rollup: alice=%#v bob=%#v", alice, bob)
	}
	if alice.UpstreamOutputTokensPerSecSum+bob.UpstreamOutputTokensPerSecSum != 12.5 || alice.UpstreamOutputTokensPerSecCount+bob.UpstreamOutputTokensPerSecCount != 1 || alice.DownstreamOutputTokensPerSecSum+bob.DownstreamOutputTokensPerSecSum != 25 || alice.DownstreamOutputTokensPerSecCount+bob.DownstreamOutputTokensPerSecCount != 1 {
		t.Fatalf("unexpected throughput rollup: alice=%#v bob=%#v", alice, bob)
	}
	if alice.CacheSnapshotCount+bob.CacheSnapshotCount != 1 || maxInt64(alice.CacheItemsMax, bob.CacheItemsMax) != 3 || alice.CacheBytesSum+bob.CacheBytesSum != 1000 || maxInt64(alice.CacheBytesMax, bob.CacheBytesMax) != 1000 || alice.CacheMaxBytesLatest != 4000 || alice.CacheOccupancyPctSum+bob.CacheOccupancyPctSum != 25 || maxFloat64(alice.CacheOccupancyPctMax, bob.CacheOccupancyPctMax) != 25 {
		t.Fatalf("unexpected cache snapshot rollup: alice=%#v bob=%#v", alice, bob)
	}
	var bucketRows []usageRollupDecisionBucketRecord
	if err := store.db.Where("run_id = ?", result.RunID).Find(&bucketRows).Error; err != nil {
		t.Fatal(err)
	}
	rollupBucketCounts := map[string]int64{}
	for _, row := range bucketRows {
		rollupBucketCounts[row.BucketKind+"|"+row.BucketName+"|"+row.SecondaryBucket] += row.SourceRequestCount
	}
	for _, want := range []string{
		"max_token_bucket|tiny|",
		"input_token_bucket|large|",
		"enabled_signal|observed_performance|",
		"enabled_signal|cost|",
		"score_bucket|latency_score|high",
		"score_bucket|final_score|medium",
		"threshold_bucket|max_token_cap_filtered|",
		"threshold_bucket|dynamic_score_thresholds|",
		"admission_reason|admitted|",
	} {
		if rollupBucketCounts[want] == 0 {
			t.Fatalf("missing decision rollup bucket %q in %#v", want, bucketRows)
		}
	}

	third := rows[1]
	third.RequestID = "req_rollup_3"
	third.TS = from.Add(3 * time.Hour)
	if err := store.db.Create(recordFromRow(third)).Error; err != nil {
		t.Fatal(err)
	}
	rerun, err := store.generateUsageRollup(UsageRollupOptions{From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if rerun.RunID != result.RunID || rerun.SourceRequestCount != 3 {
		t.Fatalf("draft rerun did not replace same run: first=%#v rerun=%#v", result, rerun)
	}
	var dailyCount int64
	if err := store.db.Model(&usageRollupDailyRecord{}).Where("run_id = ?", result.RunID).Count(&dailyCount).Error; err != nil {
		t.Fatal(err)
	}
	if dailyCount != 2 {
		t.Fatalf("draft rerun left %d daily rows, want 2", dailyCount)
	}

	finalized, err := store.generateUsageRollup(UsageRollupOptions{From: from, To: to, Finalize: true})
	if err != nil {
		t.Fatal(err)
	}
	if finalized.RunID != result.RunID || finalized.Status != "finalized" || finalized.SourceRequestCount != 3 {
		t.Fatalf("unexpected finalized result: %#v", finalized)
	}
	var run usageRollupRunRecord
	if err := store.db.First(&run, finalized.RunID).Error; err != nil {
		t.Fatal(err)
	}
	if run.SourceChecksum == "" || run.SourceMinTS == "" || run.SourceMaxTS == "" || run.RollupRowCount != 2 || run.DecisionBucketRowCount == 0 || run.GeneratedAt == "" {
		t.Fatalf("finalized run missing provenance: %#v", run)
	}
	var auditEvents []usageRollupAuditEventRecord
	if err := store.db.Where("run_id = ?", finalized.RunID).Order("id ASC").Find(&auditEvents).Error; err != nil {
		t.Fatal(err)
	}
	if len(auditEvents) < 3 || auditEvents[len(auditEvents)-1].Action != "finalized" {
		t.Fatalf("unexpected audit events: %#v", auditEvents)
	}
	if _, err := store.generateUsageRollup(UsageRollupOptions{From: from, To: to}); err == nil || !strings.Contains(err.Error(), "overlaps a finalized window") {
		t.Fatalf("draft rerun after finalize err=%v, want finalized immutability", err)
	}
	if _, err := store.generateUsageRollup(UsageRollupOptions{From: from.Add(12 * time.Hour), To: to.Add(12 * time.Hour), Finalize: true}); err == nil || !strings.Contains(err.Error(), "overlaps a finalized window") {
		t.Fatalf("overlapping finalized window err=%v, want overlap rejection", err)
	}
}

func TestAdminScalarMultiBucketAggsSQLUsesNormalizedBuckets(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	from := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	rows := []usageRow{
		{
			TS:             from.Add(time.Hour),
			RequestID:      "req_bucket_1",
			CallerUser:     "alice",
			CallerProject:  "analytics",
			TokenID:        "rtr_alice",
			RequestedModel: "default",
			ResolvedGroup:  "default",
			TargetProvider: "mock",
			TargetModel:    "model-a",
			TargetDialect:  "openai-chat",
			Cache:          "miss",
			Status:         200,
			Attempts:       1,
			LatencyMS:      100,
			InputTokens:    10,
			OutputTokens:   5,
			TotalTokens:    15,
			TotalCostUSD:   0.15,
		},
		{
			TS:             from.Add(2 * time.Hour),
			RequestID:      "req_bucket_2",
			CallerUser:     "bob",
			CallerProject:  "platform",
			TokenID:        "rtr_bob",
			RequestedModel: "default",
			ResolvedGroup:  "default",
			TargetProvider: "mock",
			TargetModel:    "model-b",
			TargetDialect:  "openai-chat",
			Cache:          "hit",
			Status:         502,
			Attempts:       2,
			FallbackUsed:   true,
			LatencyMS:      250,
			InputTokens:    4,
			OutputTokens:   6,
			TotalTokens:    10,
			TotalCostUSD:   0.10,
		},
		{
			TS:             to.Add(time.Hour),
			RequestID:      "req_bucket_outside",
			CallerUser:     "alice",
			CallerProject:  "analytics",
			TokenID:        "rtr_alice",
			RequestedModel: "default",
			ResolvedGroup:  "default",
			Cache:          "miss",
			Status:         200,
			Attempts:       1,
			LatencyMS:      999,
			InputTokens:    100,
			OutputTokens:   100,
			TotalTokens:    200,
		},
	}
	for _, row := range rows {
		if err := store.db.Create(recordFromRow(row)).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, feature := range []decisionShapeFeatureRecord{
		{RequestID: "req_bucket_1", Seq: 1, FeatureName: "max_token_bucket", TextValue: "tiny"},
		{RequestID: "req_bucket_1", Seq: 2, FeatureName: "input_token_bucket", TextValue: "small"},
		{RequestID: "req_bucket_2", Seq: 1, FeatureName: "max_token_bucket", TextValue: "standard"},
		{RequestID: "req_bucket_2", Seq: 2, FeatureName: "input_token_bucket", TextValue: "small"},
		{RequestID: "req_bucket_outside", Seq: 1, FeatureName: "max_token_bucket", TextValue: "outside"},
	} {
		if err := store.db.Create(&feature).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, signal := range []routingSignalRecord{
		{RequestID: "req_bucket_1", Seq: 1, Strategy: "dynamic_score", SignalName: "observed_performance", Source: "dynamic_score", BoolValue: true},
		{RequestID: "req_bucket_2", Seq: 1, Strategy: "dynamic_score", SignalName: "cost", Source: "dynamic_score", BoolValue: true},
		{RequestID: "req_bucket_outside", Seq: 1, Strategy: "dynamic_score", SignalName: "outside", Source: "dynamic_score", BoolValue: true},
	} {
		if err := store.db.Create(&signal).Error; err != nil {
			t.Fatal(err)
		}
	}
	opts := UsageReportOptions{From: from, To: to, CallerProject: "analytics"}
	table, total, hasMore, err := store.adminScalarMultiBucketAggsSQL(opts, adminScalarEndpointSpec{Dimension: "max_token_bucket", Secondary: "model_group"}, "requests", 10)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore {
		t.Fatal("unexpected pagination overflow")
	}
	if total.Calls != 1 {
		t.Fatalf("total calls=%d, want filtered parent count 1", total.Calls)
	}
	if got := table[joinKey("tiny", "default")]; got == nil || got.Agg.Calls != 1 || got.Agg.TotalTokens != 15 {
		t.Fatalf("missing filtered max-token bucket aggregate: %#v", table)
	}
	if table[joinKey("standard", "default")] != nil || table[joinKey("outside", "default")] != nil {
		t.Fatalf("bucket aggregate ignored filters/window: %#v", table)
	}
	signals, _, _, err := store.adminScalarMultiBucketAggsSQL(UsageReportOptions{From: from, To: to}, adminScalarEndpointSpec{Dimension: "dynamic_signal", Secondary: "model_group"}, "requests", 10)
	if err != nil {
		t.Fatal(err)
	}
	if signals[joinKey("observed_performance", "default")] == nil || signals[joinKey("cost", "default")] == nil {
		t.Fatalf("missing dynamic signal aggregates: %#v", signals)
	}
	if signals[joinKey("outside", "default")] != nil {
		t.Fatalf("dynamic signal aggregate included outside-window row: %#v", signals)
	}
}

func TestAdminScalarAggsSQLUsesLatestCacheSnapshot(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	from := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	for _, row := range []usageRow{
		{
			TS:                from.Add(time.Hour),
			RequestID:         "req_cache_old",
			CallerUser:        "alice",
			RequestedModel:    "default",
			ResolvedGroup:     "default",
			Cache:             "miss",
			Status:            200,
			Attempts:          1,
			LatencyMS:         100,
			CacheEnabled:      true,
			CacheItems:        100,
			CacheBytes:        9000,
			CacheMaxBytes:     10000,
			CacheOccupancyPct: 90,
		},
		{
			TS:                from.Add(2 * time.Hour),
			RequestID:         "req_cache_latest",
			CallerUser:        "alice",
			RequestedModel:    "default",
			ResolvedGroup:     "default",
			Cache:             "hit",
			Status:            200,
			Attempts:          1,
			LatencyMS:         100,
			CacheEnabled:      true,
			CacheItems:        2,
			CacheBytes:        200,
			CacheMaxBytes:     10000,
			CacheOccupancyPct: 2,
		},
	} {
		if err := store.db.Create(recordFromRow(row)).Error; err != nil {
			t.Fatal(err)
		}
	}
	table, total, _, hasMore, err := store.adminScalarAggsSQL(UsageReportOptions{From: from, To: to}, adminScalarEndpointSpec{Dimension: "cache", Secondary: "model_group"}, adminSavingsBaselineDTO{}, "requests", 10)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore {
		t.Fatal("unexpected pagination overflow")
	}
	if total.CacheItemsLatest != 2 || total.CacheBytesLatest != 200 || total.CacheOccupancyLatest != 2 {
		t.Fatalf("total latest cache snapshot used max values: %#v", total)
	}
	if total.CacheItemsMax != 100 || total.CacheBytesMax != 9000 || total.CacheOccupancyMax != 90 {
		t.Fatalf("total max cache snapshot lost high-water values: %#v", total)
	}
	hit := table[joinKey("hit", "default")]
	if hit == nil || hit.Agg.CacheItemsLatest != 2 || hit.Agg.CacheBytesLatest != 200 || hit.Agg.CacheItemsMax != 2 {
		t.Fatalf("hit aggregate latest cache snapshot mismatch: %#v", hit)
	}
	miss := table[joinKey("miss", "default")]
	if miss == nil || miss.Agg.CacheItemsLatest != 100 || miss.Agg.CacheBytesLatest != 9000 {
		t.Fatalf("miss aggregate latest cache snapshot mismatch: %#v", miss)
	}
}

func TestAdminShapeAggsSQLAggregatesCallerShapingWithLimit(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	from := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	rows := []usageRow{
		{
			TS:                               from.Add(time.Hour),
			RequestID:                        "req_alice_1",
			CallerUser:                       "alice",
			CallerProject:                    "analytics",
			TokenID:                          "rtr_alice",
			Client:                           "codex",
			RequestedModel:                   "default",
			ResolvedGroup:                    "default",
			Status:                           429,
			Attempts:                         0,
			LatencyMS:                        20,
			TrafficShapeApplied:              true,
			TrafficShapeDecision:             trafficShapeDecisionQueued,
			TrafficShapeScope:                "caller",
			TrafficShapeBucket:               "caller.total_reserved_tokens_per_sec",
			TrafficShapeRetryAfterMS:         750,
			TrafficShapeQueueWaitMS:          100,
			TrafficShapeEstimatedInputTokens: 10,
			TrafficShapeReservedOutputTokens: 90,
			TrafficShapeTotalReservedTokens:  100,
		},
		{
			TS:                               from.Add(2 * time.Hour),
			RequestID:                        "req_alice_2",
			CallerUser:                       "alice",
			CallerProject:                    "analytics",
			TokenID:                          "rtr_alice",
			Client:                           "codex",
			RequestedModel:                   "default",
			ResolvedGroup:                    "default",
			Status:                           200,
			Attempts:                         1,
			LatencyMS:                        50,
			TrafficShapeApplied:              true,
			TrafficShapeDecision:             trafficShapeDecisionQueued,
			TrafficShapeScope:                "caller",
			TrafficShapeBucket:               "caller.total_reserved_tokens_per_sec",
			TrafficShapeRetryAfterMS:         250,
			TrafficShapeQueueWaitMS:          300,
			TrafficShapeEstimatedInputTokens: 20,
			TrafficShapeReservedOutputTokens: 80,
			TrafficShapeTotalReservedTokens:  100,
		},
		{
			TS:                               from.Add(3 * time.Hour),
			RequestID:                        "req_bob",
			CallerUser:                       "bob",
			CallerProject:                    "analytics",
			TokenID:                          "rtr_bob",
			Client:                           "claude-code",
			RequestedModel:                   "default",
			ResolvedGroup:                    "default",
			Status:                           429,
			Attempts:                         0,
			LatencyMS:                        25,
			TrafficShapeApplied:              true,
			TrafficShapeDecision:             trafficShapeDecisionRejected,
			TrafficShapeScope:                "caller",
			TrafficShapeBucket:               "caller.request_start_per_sec",
			TrafficShapeRetryAfterMS:         100,
			TrafficShapeEstimatedInputTokens: 1,
			TrafficShapeReservedOutputTokens: 1,
			TrafficShapeTotalReservedTokens:  2,
		},
		{
			TS:             from.Add(4 * time.Hour),
			RequestID:      "req_unshaped",
			CallerUser:     "carol",
			CallerProject:  "analytics",
			TokenID:        "rtr_carol",
			Client:         "codex",
			RequestedModel: "default",
			ResolvedGroup:  "default",
			Status:         200,
			Attempts:       1,
			LatencyMS:      10,
		},
	}
	for _, row := range rows {
		if err := store.db.Create(recordFromRow(row)).Error; err != nil {
			t.Fatal(err)
		}
	}
	table, total, hasMore, err := store.adminShapeAggsSQL(
		UsageReportOptions{From: from, To: to, CallerProject: "analytics"},
		adminScalarEndpointSpec{Report: "traffic-shaping-by-user", Dimension: "caller_user", Secondary: "shape_bucket", ShapeReport: "caller", Sort: "requests"},
		"requests",
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore {
		t.Fatal("expected top-N overflow")
	}
	if total.Calls != 4 {
		t.Fatalf("summary calls=%d, want all filtered usage rows", total.Calls)
	}
	if len(table) != 1 {
		t.Fatalf("table len=%d: %#v", len(table), table)
	}
	agg := table[joinKey("alice", "caller|caller.total_reserved_tokens_per_sec|"+trafficShapeDecisionQueued)]
	if agg == nil {
		t.Fatalf("missing alice aggregate: %#v", table)
	}
	got := agg.row()
	if got.Key != "alice" || got.Requests != 2 || got.Rejections != 0 || got.Queued != 2 || got.EstimatedInputTokens != 30 || got.TotalReservedTokens != 200 {
		t.Fatalf("caller aggregate mismatch: %#v", got)
	}
	if got.AvgRetryAfterMS != 500 || got.MaxRetryAfterMS != 750 || got.P50QueueWaitMS != 100 || got.P95QueueWaitMS != 300 {
		t.Fatalf("caller retry/queue aggregate mismatch: %#v", got)
	}
}

func TestAdminShapeAggsSQLAggregatesOverviewCallerAndProvider(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	from := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	for _, row := range []usageRow{
		{
			TS:                               from.Add(time.Hour),
			RequestID:                        "req_caller_1",
			CallerProject:                    "analytics",
			RequestedModel:                   "default",
			ResolvedGroup:                    "default",
			Status:                           200,
			Attempts:                         1,
			LatencyMS:                        20,
			TrafficShapeApplied:              true,
			TrafficShapeDecision:             trafficShapeDecisionQueued,
			TrafficShapeScope:                "caller",
			TrafficShapeBucket:               "caller.total_reserved_tokens_per_sec",
			TrafficShapeQueueWaitMS:          100,
			TrafficShapeEstimatedInputTokens: 10,
			TrafficShapeReservedOutputTokens: 90,
			TrafficShapeTotalReservedTokens:  100,
		},
		{
			TS:                               from.Add(2 * time.Hour),
			RequestID:                        "req_caller_2",
			CallerProject:                    "analytics",
			RequestedModel:                   "default",
			ResolvedGroup:                    "default",
			Status:                           200,
			Attempts:                         1,
			LatencyMS:                        25,
			TrafficShapeApplied:              true,
			TrafficShapeDecision:             trafficShapeDecisionQueued,
			TrafficShapeScope:                "caller",
			TrafficShapeBucket:               "caller.total_reserved_tokens_per_sec",
			TrafficShapeQueueWaitMS:          300,
			TrafficShapeEstimatedInputTokens: 20,
			TrafficShapeReservedOutputTokens: 80,
			TrafficShapeTotalReservedTokens:  100,
		},
		{
			TS:             from.Add(3 * time.Hour),
			RequestID:      "req_provider",
			CallerProject:  "analytics",
			RequestedModel: "default",
			ResolvedGroup:  "default",
			TargetProvider: "mock",
			TargetModel:    "mock-model",
			TargetDialect:  "openai-chat",
			Status:         429,
			Attempts:       1,
			LatencyMS:      30,
		},
	} {
		if err := store.db.Create(recordFromRow(row)).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := store.db.Create(&requestUpstreamShapeEventRecord{
		RequestID:            "req_provider",
		Seq:                  1,
		TS:                   formatUsageTime(from.Add(3 * time.Hour)),
		Scope:                "provider",
		Provider:             "mock",
		Model:                "mock-model",
		Dialect:              "openai-chat",
		Bucket:               shapeBucketRequestStart,
		Decision:             shapeDecisionRejected,
		RetryAfterMS:         750,
		QueueWaitMS:          250,
		EstimatedInputTokens: 5,
		ReservedOutputTokens: 15,
		TotalReservedTokens:  20,
		BackoffReason:        "provider-shape-throttled",
	}).Error; err != nil {
		t.Fatal(err)
	}
	spec := adminScalarEndpointSpec{Report: "traffic-shaping-overview", Dimension: "shape_surface", Secondary: "shape_bucket", ShapeReport: "overview", Sort: "requests"}
	table, total, hasMore, err := store.adminShapeAggsSQL(
		UsageReportOptions{From: from, To: to, CallerProject: "analytics"},
		spec,
		"requests",
		10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore {
		t.Fatal("unexpected overview pagination overflow")
	}
	if total.Calls != 3 {
		t.Fatalf("summary calls=%d, want 3", total.Calls)
	}
	callerKey := joinKey("caller", "caller|caller.total_reserved_tokens_per_sec|"+trafficShapeDecisionQueued)
	providerKey := joinKey("provider/model", "provider|"+shapeBucketRequestStart+"|"+shapeDecisionRejected)
	caller := table[callerKey].row()
	if caller.Key != "caller" || caller.SecondaryKey != "caller|caller.total_reserved_tokens_per_sec|"+trafficShapeDecisionQueued || caller.Requests != 2 || caller.Queued != 2 || caller.P50QueueWaitMS != 100 || caller.P95QueueWaitMS != 300 {
		t.Fatalf("caller overview aggregate mismatch: %#v", caller)
	}
	provider := table[providerKey].row()
	if provider.Key != "provider/model" || provider.SecondaryKey != "provider|"+shapeBucketRequestStart+"|"+shapeDecisionRejected || provider.Requests != 1 || provider.Rejections != 1 || provider.P50QueueWaitMS != 250 || provider.P95QueueWaitMS != 250 {
		t.Fatalf("provider overview aggregate mismatch: %#v", provider)
	}
	if got := adminShapeRowsFromAgg(table, "requests", 10); len(got) != 2 || got[0].Key != "caller" || got[1].Key != "provider/model" {
		t.Fatalf("requests sort mismatch: %#v", got)
	}
	if got := adminShapeRowsFromAgg(table, "errors", 10); len(got) != 2 || got[0].Key != "provider/model" || got[1].Key != "caller" {
		t.Fatalf("errors sort mismatch: %#v", got)
	}
	if got := adminShapeRowsFromAgg(table, "key", 10); len(got) != 2 || got[0].Key != "caller" || got[1].Key != "provider/model" {
		t.Fatalf("key sort mismatch: %#v", got)
	}
}

func TestAdminShapeSQLKeyExprOmitsSyntheticSurfaceFromGroupAndFilter(t *testing.T) {
	caller, err := adminCallerShapeSQLKeyExpr(adminScalarEndpointSpec{Dimension: "shape_surface", Secondary: "shape_bucket"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(caller.groupExpr(), "'caller'") || caller.keyFilterExpr != "" {
		t.Fatalf("caller synthetic key leaked into SQL group/filter: group=%q filter=%q", caller.groupExpr(), caller.keyFilterExpr)
	}
	if !strings.Contains(caller.groupExpr(), "traffic_shape_scope") || !strings.Contains(caller.groupExpr(), "traffic_shape_bucket") || !strings.Contains(caller.groupExpr(), "traffic_shape_decision") {
		t.Fatalf("caller synthetic group lost secondary bucket expression: %q", caller.groupExpr())
	}
	upstream, err := adminUpstreamShapeSQLKeyExpr(adminScalarEndpointSpec{Dimension: "shape_surface", Secondary: "shape_bucket"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(upstream.groupExpr(), "'provider/model'") || upstream.keyFilterExpr != "" {
		t.Fatalf("upstream synthetic key leaked into SQL group/filter: group=%q filter=%q", upstream.groupExpr(), upstream.keyFilterExpr)
	}
	if !strings.Contains(upstream.groupExpr(), "child.scope") || !strings.Contains(upstream.groupExpr(), "child.bucket") || !strings.Contains(upstream.groupExpr(), "child.decision") {
		t.Fatalf("upstream synthetic group lost secondary bucket expression: %q", upstream.groupExpr())
	}
	byProvider, err := adminUpstreamShapeSQLKeyExpr(adminScalarEndpointSpec{Dimension: "provider_model", Secondary: "shape_bucket"})
	if err != nil {
		t.Fatal(err)
	}
	if byProvider.keyFilterExpr == "" || !strings.Contains(byProvider.groupExpr(), byProvider.keyFilterExpr) {
		t.Fatalf("real upstream key missing from group/filter: group=%q filter=%q", byProvider.groupExpr(), byProvider.keyFilterExpr)
	}
}

func TestAdminShapeAggsSQLAggregatesProviderAndAdaptiveBackoff(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	from := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	for _, row := range []usageRow{
		{
			TS:             from.Add(time.Hour),
			RequestID:      "req_route_around",
			CallerProject:  "analytics",
			RequestedModel: "default",
			ResolvedGroup:  "default",
			TargetProvider: "mock",
			TargetModel:    "mock-model",
			TargetDialect:  "openai-chat",
			Status:         200,
			Attempts:       2,
			FallbackUsed:   true,
			LatencyMS:      100,
		},
		{
			TS:             from.Add(2 * time.Hour),
			RequestID:      "req_cooldown",
			CallerProject:  "analytics",
			RequestedModel: "default",
			ResolvedGroup:  "default",
			TargetProvider: "mock",
			TargetModel:    "mock-model",
			TargetDialect:  "openai-chat",
			Status:         503,
			Attempts:       1,
			LatencyMS:      80,
		},
	} {
		if err := store.db.Create(recordFromRow(row)).Error; err != nil {
			t.Fatal(err)
		}
	}
	events := []requestUpstreamShapeEventRecord{
		{
			RequestID:            "req_route_around",
			Seq:                  1,
			TS:                   formatUsageTime(from.Add(time.Hour)),
			Scope:                "provider",
			Provider:             "mock",
			Model:                "mock-model",
			Dialect:              "openai-chat",
			Bucket:               shapeBucketRequestStart,
			Decision:             shapeDecisionSkipped,
			RetryAfterMS:         100,
			EstimatedInputTokens: 10,
			ReservedOutputTokens: 20,
			TotalReservedTokens:  30,
			BackoffReason:        "provider-shape-throttled",
		},
		{
			RequestID:            "req_cooldown",
			Seq:                  1,
			TS:                   formatUsageTime(from.Add(2 * time.Hour)),
			Scope:                "provider",
			Provider:             "mock",
			Model:                "mock-model",
			Dialect:              "openai-chat",
			Bucket:               shapeBucketBackoff,
			Decision:             shapeDecisionCooldownStarted,
			RetryAfterMS:         1000,
			QueueWaitMS:          250,
			EstimatedInputTokens: 5,
			ReservedOutputTokens: 15,
			TotalReservedTokens:  20,
			BackoffReason:        "adaptive-backoff-provider-429",
		},
	}
	for _, event := range events {
		if err := store.db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
	}
	table, total, hasMore, err := store.adminShapeAggsSQL(
		UsageReportOptions{From: from, To: to, CallerProject: "analytics"},
		adminScalarEndpointSpec{Report: "provider-capacity-shaping", Dimension: "provider_model", Secondary: "shape_bucket", ShapeReport: "upstream", Sort: "requests"},
		"requests",
		10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore {
		t.Fatal("unexpected pagination overflow")
	}
	if total.Calls != 2 {
		t.Fatalf("summary calls=%d, want 2", total.Calls)
	}
	if got := table[joinKey("mock/mock-model", "provider|"+shapeBucketRequestStart+"|"+shapeDecisionSkipped)].row(); got.Requests != 1 || got.SkippedTargets != 1 || got.RouteAroundSuccesses != 1 || got.Fallbacks != 1 {
		t.Fatalf("provider skip aggregate mismatch: %#v", got)
	}
	if got := table[joinKey("mock/mock-model", "provider|"+shapeBucketBackoff+"|"+shapeDecisionCooldownStarted)].row(); got.Requests != 1 || got.CooldownsStarted != 1 || got.Upstream429Attempts != 1 || got.P50QueueWaitMS != 250 {
		t.Fatalf("provider cooldown aggregate mismatch: %#v", got)
	}
	adaptive, _, adaptiveMore, err := store.adminShapeAggsSQL(
		UsageReportOptions{From: from, To: to, CallerProject: "analytics"},
		adminScalarEndpointSpec{Report: "adaptive-upstream-backoff", Dimension: "backoff_reason", Secondary: "provider_model", ShapeReport: "adaptive", Sort: "requests"},
		"requests",
		10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if adaptiveMore {
		t.Fatal("unexpected adaptive pagination overflow")
	}
	if len(adaptive) != 1 {
		t.Fatalf("adaptive rows=%d: %#v", len(adaptive), adaptive)
	}
	if got := adaptive[joinKey("adaptive-backoff-provider-429", "mock|mock-model")].row(); got.Requests != 1 || got.SkippedTargets != 0 || got.CooldownsStarted != 1 {
		t.Fatalf("adaptive aggregate mismatch: %#v", got)
	}
}

func TestUsageRollupHourlyAndMonthlyWithBaseline(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	upstreamTPS := 10.0
	downstreamTPS := 20.0
	for _, row := range []usageRow{
		{
			TS:                  time.Date(2026, 6, 14, 1, 15, 0, 0, time.UTC),
			RequestID:           "req_hourly_monthly_1",
			CallerID:            "caller-acme",
			CallerUser:          "acme-owner",
			CallerProject:       "acme-project",
			CallerEnvironment:   "prod",
			TokenID:             "rtr_acme",
			Client:              "codex",
			InboundDialect:      "openai-chat",
			RequestedModel:      "default",
			ResolvedGroup:       "default",
			Strategy:            "weighted",
			TargetProvider:      "mock",
			TargetModel:         "model-a",
			TargetDialect:       "openai-chat",
			Cache:               "miss",
			Status:              200,
			Attempts:            1,
			LatencyMS:           100,
			UpstreamOutputTPS:   &upstreamTPS,
			DownstreamOutputTPS: &downstreamTPS,
			InputTokens:         1_000_000,
			OutputTokens:        500_000,
			TotalTokens:         1_500_000,
			InputCostUSD:        1.00,
			OutputCostUSD:       2.00,
			TotalCostUSD:        3.00,
		},
		{
			TS:                  time.Date(2026, 6, 14, 2, 15, 0, 0, time.UTC),
			RequestID:           "req_hourly_monthly_2",
			CallerID:            "caller-acme",
			CallerUser:          "acme-owner",
			CallerProject:       "acme-project",
			CallerEnvironment:   "prod",
			TokenID:             "rtr_acme",
			Client:              "codex",
			InboundDialect:      "openai-chat",
			RequestedModel:      "default",
			ResolvedGroup:       "default",
			Strategy:            "weighted",
			TargetProvider:      "mock",
			TargetModel:         "model-a",
			TargetDialect:       "openai-chat",
			Cache:               "miss",
			Status:              200,
			Attempts:            1,
			LatencyMS:           200,
			UpstreamOutputTPS:   &upstreamTPS,
			DownstreamOutputTPS: &downstreamTPS,
			InputTokens:         2_000_000,
			OutputTokens:        250_000,
			TotalTokens:         2_250_000,
			InputCostUSD:        2.00,
			OutputCostUSD:       1.00,
			TotalCostUSD:        3.00,
		},
	} {
		if err := store.db.Create(recordFromRow(row)).Error; err != nil {
			t.Fatal(err)
		}
	}
	baselineOpts := UsageRollupOptions{
		From:                             from,
		To:                               to,
		BaselineID:                       "baseline-premium",
		BaselineName:                     "Premium Baseline",
		BaselineVersion:                  "2026-06-25",
		BaselineInputPricePerMillionUSD:  5,
		BaselineOutputPricePerMillionUSD: 30,
	}
	hourly, err := store.generateUsageRollup(withRollupType(baselineOpts, "hourly"))
	if err != nil {
		t.Fatal(err)
	}
	if hourly.RollupType != "hourly" || hourly.RollupRows != 2 || hourly.SourceRequestCount != 2 || hourly.SourceChecksum == "" {
		t.Fatalf("unexpected hourly result: %#v", hourly)
	}
	var hourlyRows []usageRollupAggregateRecord
	if err := store.db.Table("usage_rollup_hourly").Where("run_id = ?", hourly.RunID).Order("bucket_utc ASC").Find(&hourlyRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(hourlyRows) != 2 || hourlyRows[0].BucketUTC != "2026-06-14T01:00:00Z" || hourlyRows[1].BucketUTC != "2026-06-14T02:00:00Z" {
		t.Fatalf("unexpected hourly rows: %#v", hourlyRows)
	}
	for _, row := range hourlyRows {
		if row.BaselineID != "baseline-premium" || row.SuccessCount != 1 || row.LatencyMSCount != 1 || row.UpstreamOutputTokensPerSecMin != 10 || row.UpstreamOutputTokensPerSecMax != 10 {
			t.Fatalf("hourly row missing baseline/count/perf fields: %#v", row)
		}
	}
	monthly, err := store.generateUsageRollup(withRollupType(baselineOpts, "monthly"))
	if err != nil {
		t.Fatal(err)
	}
	if monthly.RollupType != "monthly" || monthly.RollupRows != 1 || monthly.SourceRequestCount != 2 || monthly.SourceChecksum != hourly.SourceChecksum {
		t.Fatalf("unexpected monthly result: hourly=%#v monthly=%#v", hourly, monthly)
	}
	var monthlyRows []usageRollupAggregateRecord
	if err := store.db.Table("usage_rollup_monthly_billing").Where("run_id = ?", monthly.RunID).Find(&monthlyRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(monthlyRows) != 1 {
		t.Fatalf("monthly rows=%d want 1: %#v", len(monthlyRows), monthlyRows)
	}
	row := monthlyRows[0]
	if row.BucketUTC != "2026-06" || row.SourceRequestCount != 2 || row.InputTokens != 3_000_000 || row.OutputTokens != 750_000 || row.TotalTokens != 3_750_000 {
		t.Fatalf("unexpected monthly token row: %#v", row)
	}
	assertNear(t, row.BaselineInputCostUSD, 15.00, "baseline input")
	assertNear(t, row.BaselineOutputCostUSD, 22.50, "baseline output")
	assertNear(t, row.BaselineTotalCostUSD, 37.50, "baseline total")
	assertNear(t, row.TotalSavingsUSD, 31.50, "baseline savings")
	var run usageRollupRunRecord
	if err := store.db.First(&run, monthly.RunID).Error; err != nil {
		t.Fatal(err)
	}
	if run.SourceMinTS != formatUsageTime(time.Date(2026, 6, 14, 1, 15, 0, 0, time.UTC)) || run.SourceMaxTS != formatUsageTime(time.Date(2026, 6, 14, 2, 15, 0, 0, time.UTC)) || run.SourceChecksum == "" || run.RollupRowCount != 1 {
		t.Fatalf("monthly run missing provenance: %#v", run)
	}
}

func withRollupType(opts UsageRollupOptions, rollupType string) UsageRollupOptions {
	opts.RollupType = rollupType
	return opts
}

func assertNear(t *testing.T, got, want float64, label string) {
	t.Helper()
	if math.Abs(got-want) > 0.000000001 {
		t.Fatalf("%s=%f, want %f", label, got, want)
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func maxFloat64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func TestUsageDBSchemaIsRelationalOnly(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	type col struct {
		Name string
		Type string
	}
	for _, table := range []string{
		"request_usage",
		"request_attempts",
		"request_trace_events",
		"request_shapes",
		"request_translation_shapes",
		"request_translation_field_events",
		"request_decision_shape_features",
		"request_target_candidates",
		"request_target_filter_reasons",
		"request_routing_decisions",
		"request_routing_signals",
		"request_dynamic_score_terms",
		"request_policy_executions",
		"request_fallback_transitions",
		"request_cache_reasons",
		"request_errors",
		"request_content_captures",
		"request_content_headers",
		"request_content_audit_events",
		"security_access_events",
		"usage_rollup_runs",
		"usage_rollup_hourly",
		"usage_rollup_daily",
		"usage_rollup_monthly_billing",
		"usage_rollup_audit_events",
		"usage_rollup_decision_buckets",
		"retention_policy_versions",
		"retention_policy_rules",
		"retention_jobs",
		"retention_job_table_results",
		"legal_holds",
		"legal_hold_audit_events",
	} {
		var cols []col
		if err := store.db.Raw(`SELECT name, type FROM pragma_table_info(?)`, table).Scan(&cols).Error; err != nil {
			t.Fatal(err)
		}
		if len(cols) == 0 {
			t.Fatalf("%s schema not found", table)
		}
		for _, c := range cols {
			typ := strings.ToLower(c.Type)
			if strings.Contains(typ, "json") || strings.Contains(typ, "array") || strings.HasSuffix(typ, "[]") {
				t.Fatalf("non-relational column %s.%s type %s", table, c.Name, c.Type)
			}
		}
	}
}

func TestRetentionDryRunWritesJobRecordsAndHonorsLegalHold(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * 24 * time.Hour)
	fresh := now.Add(-24 * time.Hour)
	for _, row := range []contentCaptureRecord{
		{
			RequestID:      "req_retention_held",
			TS:             formatUsageTime(old),
			Scope:          contentCaptureScopeRequest,
			CallerID:       "alice",
			TokenID:        "rtr_alice_test",
			ResolvedGroup:  "default",
			InboundDialect: "openai-chat",
			ContentType:    "application/json",
			ContentText:    "held",
			ContentBytes:   len("held"),
			RetentionUntil: formatUsageTime(old.Add(24 * time.Hour)),
		},
		{
			RequestID:      "req_retention_free",
			TS:             formatUsageTime(old.Add(time.Hour)),
			Scope:          contentCaptureScopeResponse,
			CallerID:       "alice",
			TokenID:        "rtr_alice_test",
			ResolvedGroup:  "default",
			InboundDialect: "openai-chat",
			ContentType:    "application/json",
			ContentText:    "free",
			ContentBytes:   len("free"),
			RetentionUntil: formatUsageTime(old.Add(24 * time.Hour)),
		},
		{
			RequestID:      "req_retention_fresh",
			TS:             formatUsageTime(fresh),
			Scope:          contentCaptureScopeRequest,
			CallerID:       "alice",
			TokenID:        "rtr_alice_test",
			ResolvedGroup:  "default",
			InboundDialect: "openai-chat",
			ContentType:    "application/json",
			ContentText:    "fresh",
			ContentBytes:   len("fresh"),
			RetentionUntil: formatUsageTime(fresh.Add(24 * time.Hour)),
		},
	} {
		if err := store.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := store.db.Create(&legalHoldRecord{
		HoldID:     "hold-content-1",
		DataClass:  retentionDataClassContentCapture,
		Active:     true,
		StartTS:    formatUsageTime(old.Add(-time.Hour)),
		EndTS:      formatUsageTime(old.Add(30 * time.Minute)),
		ReasonCode: "litigation",
		Subject:    "matter-121",
		CreatedBy:  "unit-test",
		CreatedAt:  formatUsageTime(now),
	}).Error; err != nil {
		t.Fatal(err)
	}
	cfg := retentionTestConfig(retentionDataClassContentCapture, 7)
	result, err := store.runRetentionStatus(RetentionStatusOptions{Config: cfg, Now: now, RequestedBy: "unit-test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.JobID == 0 || result.PolicyVersionID == 0 || len(result.TableResults) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	table := result.TableResults[0]
	if table.TableName != "request_content_captures" || table.CandidateRows != 2 || table.HeldRows != 1 || table.EligibleRows != 1 || table.BlockedRows != 0 || table.Status != "dry_run" {
		t.Fatalf("unexpected table result: %#v", table)
	}
	var job retentionJobRecord
	if err := store.db.First(&job, result.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != "completed" || !job.DryRun || job.RequestedBy != "unit-test" {
		t.Fatalf("unexpected job: %#v", job)
	}
	var resultRow retentionJobTableResultRecord
	if err := store.db.Where("job_id = ? AND table_name = ?", result.JobID, "request_content_captures").First(&resultRow).Error; err != nil {
		t.Fatal(err)
	}
	if resultRow.CandidateRows != 2 || resultRow.HeldRows != 1 || resultRow.EligibleRows != 1 {
		t.Fatalf("unexpected stored result: %#v", resultRow)
	}
	var rules int64
	if err := store.db.Model(&retentionPolicyRuleRecord{}).Where("policy_version_id = ?", result.PolicyVersionID).Count(&rules).Error; err != nil {
		t.Fatal(err)
	}
	if rules != 1 {
		t.Fatalf("policy rules=%d, want 1", rules)
	}
	cfg.DryRun = boolPtr(false)
	purged, err := store.runRetentionJob(RetentionStatusOptions{Config: cfg, Now: now, RequestedBy: "unit-test"})
	if err != nil {
		t.Fatal(err)
	}
	if got := purged.TableResults[0]; got.DeletedRows != 1 || got.HeldRows != 1 || got.Status != "purged" {
		t.Fatalf("unexpected held content-capture purge result: %#v", got)
	}
	var remaining []contentCaptureRecord
	if err := store.db.Order("request_id").Find(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 2 || remaining[0].RequestID != "req_retention_fresh" || remaining[1].RequestID != "req_retention_held" {
		t.Fatalf("retention removed held/fresh rows: %#v", remaining)
	}
}

func TestRetentionUsageDetailRequiresFinalizedRollupBeforeFutureDelete(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * 24 * time.Hour)
	cutoff := now.Add(-7 * 24 * time.Hour)
	if err := store.db.Create(recordFromRow(usageRow{
		TS:             old,
		RequestID:      "req_usage_detail_old",
		CallerID:       "alice",
		CallerUser:     "alice",
		TokenID:        "rtr_alice",
		RequestedModel: "default",
		ResolvedGroup:  "default",
		Cache:          "miss",
		Status:         200,
		Attempts:       1,
	})).Error; err != nil {
		t.Fatal(err)
	}
	cfg := retentionTestConfig(retentionDataClassUsageDetail, 7)
	blocked, err := store.runRetentionStatus(RetentionStatusOptions{Config: cfg, Now: now, RequestedBy: "unit-test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked.TableResults) != 1 {
		t.Fatalf("blocked results: %#v", blocked)
	}
	if got := blocked.TableResults[0]; got.Status != "blocked_rollup_required" || got.CandidateRows != 1 || got.EligibleRows != 0 || got.BlockedRows != 1 {
		t.Fatalf("unexpected blocked usage_detail result: %#v", got)
	}
	mid := old.Add(24 * time.Hour)
	if _, err := store.generateUsageRollup(UsageRollupOptions{From: old.Add(-time.Hour), To: mid, Finalize: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.generateUsageRollup(UsageRollupOptions{From: mid, To: cutoff, Finalize: true}); err != nil {
		t.Fatal(err)
	}
	ready, err := store.runRetentionStatus(RetentionStatusOptions{Config: cfg, Now: now, RequestedBy: "unit-test"})
	if err != nil {
		t.Fatal(err)
	}
	if got := ready.TableResults[0]; got.Status != "dry_run" || got.CandidateRows != 1 || got.EligibleRows != 1 || got.BlockedRows != 0 {
		t.Fatalf("unexpected ready usage_detail result: %#v", got)
	}
	cfg.Classes[0].Enabled = boolPtr(true)
	cfg.DryRun = boolPtr(false)
	purged, err := store.runRetentionJob(RetentionStatusOptions{Config: cfg, Now: now, RequestedBy: "unit-test"})
	if err != nil {
		t.Fatal(err)
	}
	if got := purged.TableResults[0]; got.Status != "purged" || got.DeletedRows != 1 || got.CandidateRows != 1 || got.BlockedRows != 0 {
		t.Fatalf("unexpected purged usage_detail result: %#v", got)
	}
	var remaining int64
	if err := store.db.Model(&usageRecord{}).Where("request_id = ?", "req_usage_detail_old").Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("usage_detail row remaining=%d, want 0", remaining)
	}
	var finalized usageRollupRunRecord
	if err := store.db.Where("rollup_type = ? AND status = ?", "daily", "finalized").First(&finalized).Error; err != nil {
		t.Fatal(err)
	}
	if finalized.Status != "finalized" {
		t.Fatalf("rollup status mutated: %#v", finalized)
	}
}

func TestRetentionUsageDetailHonorsChildTelemetryLegalHold(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * 24 * time.Hour)
	cutoff := now.Add(-7 * 24 * time.Hour)
	requestID := "req_usage_detail_child_hold"
	if err := store.db.Create(recordFromRow(usageRow{
		TS:             old,
		RequestID:      requestID,
		CallerID:       "alice",
		CallerUser:     "alice",
		TokenID:        "rtr_alice",
		RequestedModel: "default",
		ResolvedGroup:  "default",
		Cache:          "miss",
		Status:         200,
		Attempts:       1,
	})).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&decisionShapeFeatureRecord{
		RequestID:   requestID,
		Seq:         1,
		FeatureName: "tools_present",
		BoolValue:   false,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.generateUsageRollup(UsageRollupOptions{From: old.Add(-time.Hour), To: cutoff, Finalize: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateLegalHold(LegalHoldCreateOptions{
		HoldID:     "hold-child-telemetry",
		DataClass:  retentionDataClassDecisionTelemetry,
		RequestID:  requestID,
		ReasonCode: "investigation",
		Subject:    "matter-121",
		Actor:      "unit-test",
		Now:        now,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := retentionTestConfig(retentionDataClassUsageDetail, 7)
	cfg.Classes[0].Enabled = boolPtr(true)
	cfg.DryRun = boolPtr(false)
	result, err := store.runRetentionJob(RetentionStatusOptions{Config: cfg, Now: now, RequestedBy: "unit-test"})
	if err != nil {
		t.Fatal(err)
	}
	got := tableResultFor(t, result, "request_usage")
	if got.Status != "no_eligible_rows" || got.CandidateRows != 1 || got.HeldRows != 1 || got.EligibleRows != 0 || got.DeletedRows != 0 {
		t.Fatalf("unexpected child-held usage_detail result: %#v", got)
	}
	assertTableCount(t, store, &usageRecord{}, 1)
	assertTableCount(t, store, &decisionShapeFeatureRecord{}, 1)
}

func TestRetentionDryRunAndPurgeDiagnosticsBatchHonorsRequestLegalHold(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * 24 * time.Hour)
	for i, requestID := range []string{"req_diag_delete_1", "req_diag_hold", "req_diag_delete_2"} {
		if err := store.db.Create(&requestAttemptRecord{
			RequestID:    requestID,
			AttemptIndex: 1,
			TS:           formatUsageTime(old.Add(time.Duration(i) * time.Minute)),
			Provider:     "test",
			Model:        "model",
			Dialect:      "openai-chat",
			StatusCode:   200,
			Selected:     true,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	hold, err := store.CreateLegalHold(LegalHoldCreateOptions{
		HoldID:     "hold-diag-request",
		DataClass:  retentionDataClassUsageDiagnostics,
		RequestID:  "req_diag_hold",
		ReasonCode: "litigation",
		Subject:    "matter-121",
		Actor:      "unit-test",
		Now:        now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hold.Active || hold.RequestID != "req_diag_hold" {
		t.Fatalf("unexpected hold: %#v", hold)
	}
	cfg := retentionTestConfig(retentionDataClassUsageDiagnostics, 7)
	cfg.Classes[0].BatchSize = 1
	dry, err := store.runRetentionStatus(RetentionStatusOptions{Config: cfg, Now: now, RequestedBy: "unit-test"})
	if err != nil {
		t.Fatal(err)
	}
	if got := tableResultFor(t, dry, "request_attempts"); got.Status != "dry_run" || got.CandidateRows != 3 || got.HeldRows != 1 || got.EligibleRows != 2 || got.DeletedRows != 0 {
		t.Fatalf("unexpected dry-run diagnostics result: %#v", got)
	}
	assertTableCount(t, store, &requestAttemptRecord{}, 3)

	cfg.DryRun = boolPtr(false)
	first, err := store.runRetentionJob(RetentionStatusOptions{Config: cfg, Now: now, RequestedBy: "unit-test"})
	if err != nil {
		t.Fatal(err)
	}
	if got := tableResultFor(t, first, "request_attempts"); got.Status != "purged" || got.CandidateRows != 3 || got.HeldRows != 1 || got.DeletedRows != 1 || got.EligibleRows != 1 {
		t.Fatalf("unexpected first purge result: %#v", got)
	}
	assertTableCount(t, store, &requestAttemptRecord{}, 2)

	second, err := store.runRetentionJob(RetentionStatusOptions{Config: cfg, Now: now, RequestedBy: "unit-test"})
	if err != nil {
		t.Fatal(err)
	}
	if got := tableResultFor(t, second, "request_attempts"); got.Status != "purged" || got.DeletedRows != 1 || got.HeldRows != 1 {
		t.Fatalf("unexpected second purge result: %#v", got)
	}
	assertTableCount(t, store, &requestAttemptRecord{}, 1)

	released, err := store.ReleaseLegalHold(LegalHoldReleaseOptions{
		HoldID:        "hold-diag-request",
		Actor:         "unit-test",
		ReleaseReason: "matter-closed",
		Now:           now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if released.Active {
		t.Fatalf("hold still active after release: %#v", released)
	}
	third, err := store.runRetentionJob(RetentionStatusOptions{Config: cfg, Now: now.Add(2 * time.Hour), RequestedBy: "unit-test"})
	if err != nil {
		t.Fatal(err)
	}
	if got := tableResultFor(t, third, "request_attempts"); got.Status != "purged" || got.DeletedRows != 1 || got.HeldRows != 0 {
		t.Fatalf("unexpected third purge result: %#v", got)
	}
	assertTableCount(t, store, &requestAttemptRecord{}, 0)

	var auditEvents int64
	if err := store.db.Model(&legalHoldAuditEventRecord{}).Where("hold_id = ?", "hold-diag-request").Count(&auditEvents).Error; err != nil {
		t.Fatal(err)
	}
	if auditEvents != 2 {
		t.Fatalf("legal hold audit events=%d, want 2", auditEvents)
	}
}

func TestRetentionRunPurgesContentCaptureAndHeaders(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * 24 * time.Hour)
	capture := &contentCaptureRecord{
		RequestID:      "req_retention_content",
		TS:             formatUsageTime(old),
		Scope:          contentCaptureScopeRequest,
		CallerID:       "alice",
		TokenID:        "rtr_alice_test",
		ResolvedGroup:  "default",
		InboundDialect: "openai-chat",
		ContentType:    "application/json",
		ContentText:    "content",
		ContentBytes:   len("content"),
		RetentionUntil: formatUsageTime(old.Add(24 * time.Hour)),
	}
	if err := store.db.Create(capture).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&contentCaptureHeaderRecord{CaptureID: capture.ID, RequestID: capture.RequestID, Scope: capture.Scope, Name: "X-Test", Value: "ciphertext"}).Error; err != nil {
		t.Fatal(err)
	}
	cfg := retentionTestConfig(retentionDataClassContentCapture, 7)
	cfg.DryRun = boolPtr(false)
	result, err := store.runRetentionJob(RetentionStatusOptions{Config: cfg, Now: now, RequestedBy: "unit-test"})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.TableResults[0]; got.Status != "purged" || got.BlockedRows != 0 || got.DeletedRows != 1 {
		t.Fatalf("unexpected content-capture delete result: %#v", got)
	}
	assertTableCount(t, store, &contentCaptureRecord{}, 0)
	assertTableCount(t, store, &contentCaptureHeaderRecord{}, 0)
	var job retentionJobRecord
	if err := store.db.First(&job, result.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != "completed" || job.ErrorMessage != "" {
		t.Fatalf("unexpected job: %#v", job)
	}
}

func retentionTestConfig(dataClass string, retentionDays int) RetentionConfig {
	cfg := RetentionConfig{
		Enabled:          true,
		DryRun:           boolPtr(true),
		DefaultBatchSize: 100,
		Classes: []RetentionClassConfig{{
			DataClass:     dataClass,
			RetentionDays: retentionDays,
			BatchSize:     50,
		}},
	}
	if dataClass == retentionDataClassUsageDetail {
		cfg.Classes[0].RequireFinalizedRollup = true
	}
	return cfg
}

func tableResultFor(t *testing.T, result RetentionStatusResult, table string) RetentionTableStatus {
	t.Helper()
	for _, item := range result.TableResults {
		if item.TableName == table {
			return item
		}
	}
	t.Fatalf("retention result for table %s not found: %#v", table, result.TableResults)
	return RetentionTableStatus{}
}

func assertTableCount(t *testing.T, store *usageStore, model any, want int64) {
	t.Helper()
	var got int64
	if err := store.db.Model(model).Count(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("table count=%d, want %d", got, want)
	}
}

func TestDecisionTelemetryTablesReferenceRequestUsage(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, table := range []string{
		"request_decision_shape_features",
		"request_target_candidates",
		"request_target_filter_reasons",
		"request_routing_decisions",
		"request_routing_signals",
		"request_dynamic_score_terms",
		"request_policy_executions",
		"request_fallback_transitions",
		"request_cache_reasons",
	} {
		assertRequestUsageForeignKey(t, store, table)
	}
}

func assertRequestUsageForeignKey(t *testing.T, store *usageStore, table string) {
	t.Helper()
	type fkRow struct {
		Table string
		From  string
		To    string
	}
	var rows []fkRow
	query := map[string]string{
		"request_decision_shape_features": "PRAGMA foreign_key_list(request_decision_shape_features)",
		"request_target_candidates":       "PRAGMA foreign_key_list(request_target_candidates)",
		"request_target_filter_reasons":   "PRAGMA foreign_key_list(request_target_filter_reasons)",
		"request_routing_decisions":       "PRAGMA foreign_key_list(request_routing_decisions)",
		"request_routing_signals":         "PRAGMA foreign_key_list(request_routing_signals)",
		"request_dynamic_score_terms":     "PRAGMA foreign_key_list(request_dynamic_score_terms)",
		"request_policy_executions":       "PRAGMA foreign_key_list(request_policy_executions)",
		"request_fallback_transitions":    "PRAGMA foreign_key_list(request_fallback_transitions)",
		"request_cache_reasons":           "PRAGMA foreign_key_list(request_cache_reasons)",
	}[table]
	if query == "" {
		t.Fatalf("unknown table %s", table)
	}
	if err := store.db.Raw(query).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Table == "request_usage" && row.From == "request_id" && row.To == "request_id" {
			return
		}
	}
	t.Fatalf("%s does not reference request_usage(request_id): %#v", table, rows)
}

func TestContentCaptureRetentionPurgeDeletesRowsAndAudits(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	expired := contentCaptureRecord{
		RequestID:      "req_expired",
		TS:             formatUsageTime(now.Add(-48 * time.Hour)),
		Scope:          contentCaptureScopeRequest,
		CallerID:       "alice",
		TokenID:        "rtr_alice_test",
		ResolvedGroup:  "default",
		InboundDialect: "openai-chat",
		ContentType:    "application/json",
		ContentText:    "expired",
		ContentBytes:   len("expired"),
		RetentionUntil: formatUsageTime(now.Add(-time.Hour)),
	}
	fresh := contentCaptureRecord{
		RequestID:      "req_fresh",
		TS:             formatUsageTime(now),
		Scope:          contentCaptureScopeRequest,
		CallerID:       "alice",
		TokenID:        "rtr_alice_test",
		ResolvedGroup:  "default",
		InboundDialect: "openai-chat",
		ContentType:    "application/json",
		ContentText:    "fresh",
		ContentBytes:   len("fresh"),
		RetentionUntil: formatUsageTime(now.Add(24 * time.Hour)),
	}
	if err := store.db.Create(&expired).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&contentCaptureHeaderRecord{CaptureID: expired.ID, RequestID: expired.RequestID, Scope: expired.Scope, Name: "User-Agent", Value: "test"}).Error; err != nil {
		t.Fatal(err)
	}

	deleted, err := store.PurgeExpiredContentCaptures(now, "content-admin", "rtr_content_admin", "unit-test")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d, want 1", deleted)
	}
	var captures []contentCaptureRecord
	if err := store.db.Order("request_id").Find(&captures).Error; err != nil {
		t.Fatal(err)
	}
	if len(captures) != 1 || captures[0].RequestID != "req_fresh" {
		t.Fatalf("remaining captures: %#v", captures)
	}
	var headerCount int64
	if err := store.db.Model(&contentCaptureHeaderRecord{}).Count(&headerCount).Error; err != nil {
		t.Fatal(err)
	}
	if headerCount != 0 {
		t.Fatalf("header rows=%d, want 0", headerCount)
	}
	var audit contentCaptureAuditRecord
	if err := store.db.Where("action = ?", "retention_purge").First(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if audit.RequestID != "req_expired" || audit.RowsAffected != 1 || audit.ActorCallerID != "content-admin" {
		t.Fatalf("unexpected audit row: %#v", audit)
	}
}

func TestRequestTranslationFieldEventRecordPreservesOtherBucket(t *testing.T) {
	rec := requestTranslationFieldEventRecordFromLog("req_other", translationFieldEventLogRecord{
		AttemptIndex: 2,
		Seq:          3,
		FieldName:    "other",
		Action:       "unsupported",
		Reason:       "unsupported_source_field",
	})
	if rec.FieldName != "other" {
		t.Fatalf("field name=%q, want safe other bucket", rec.FieldName)
	}
	if rec.Action != "unsupported" || rec.Reason != "unsupported_source_field" {
		t.Fatalf("unexpected action/reason: %#v", rec)
	}
}
