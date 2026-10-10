// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUsageFromMapPreservesReasoningTokenPresence(t *testing.T) {
	reported := usageFromMap(map[string]any{"prompt_tokens": 4, "completion_tokens": 3, "completion_tokens_details": map[string]any{"reasoning_tokens": 0}})
	if reported.ReasoningTokens == nil || *reported.ReasoningTokens != 0 {
		t.Fatalf("reported zero lost: %#v", reported)
	}
	missing := usageFromMap(map[string]any{"prompt_tokens": 4, "completion_tokens": 3})
	if missing.ReasoningTokens != nil {
		t.Fatalf("missing field became a value: %#v", missing)
	}
}

func TestExportReasoningCoverageIsAggregateOnly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	store, err := OpenUsageStore(freshSQLiteUsageDBConfigForTest(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	ts := "2026-07-23T12:00:00.000Z"
	five := 5
	if err := store.db.Create(&usageRecord{RequestID: "must-not-export", TS: ts, ReasoningTokens: &five, ReasoningAttemptCount: 2, ReasoningSuccessfulAttemptCount: 1, ReasoningReportedAttemptCount: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&[]requestAttemptRecord{
		{RequestID: "must-not-export", AttemptIndex: 1, TS: ts, Provider: "provider-a", Model: "model-a", Dialect: "openai-chat", ReasoningTokens: &five},
		{RequestID: "must-not-export", AttemptIndex: 2, TS: ts, Provider: "provider-b", Model: "model-b", Dialect: "anthropic", ErrorMessage: "must-not-export"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	from, _ := time.Parse(time.RFC3339, "2026-07-23T11:00:00Z")
	to, _ := time.Parse(time.RFC3339, "2026-07-23T13:00:00Z")
	got, err := ExportReasoningCoverage(UsageReportOptions{DBPath: dbPath, MigrationPolicy: usageDBMigrationPolicyAutoSafe, From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if got.ReasoningTokens != 5 || got.ReasoningAttemptCount != 2 || got.ReasoningSuccessfulAttemptCount != 1 || got.ReasoningReportedAttemptCount != 1 || len(got.Coverage) != 2 {
		t.Fatalf("coverage=%#v", got)
	}
	if got.Coverage[0].Provider != "provider-a" || got.Coverage[0].ReportedAttempts != 1 || got.Coverage[0].ReasoningTokens != 5 {
		t.Fatalf("first coverage row=%#v", got.Coverage[0])
	}
}

func TestExportReasoningCoverageImportsJSONL(t *testing.T) {
	dir := t.TempDir()
	seven := 7
	record := logRecord{TS: "2026-07-23T12:00:00.000Z", RequestID: "jsonl-request", ResolvedGroup: "eval-group", AttemptsDetail: []attemptLogRecord{{Provider: "provider", Model: "model", Dialect: "openai-chat", Selected: true, ReasoningTokens: &seven}}}
	populateReasoningUsageCoverage(&record)
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "usage.jsonl")
	if err := os.WriteFile(logPath, append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	from, _ := time.Parse(time.RFC3339, "2026-07-23T11:00:00Z")
	to, _ := time.Parse(time.RFC3339, "2026-07-23T13:00:00Z")
	got, err := ExportReasoningCoverage(UsageReportOptions{DBPath: filepath.Join(dir, "usage.sqlite"), MigrationPolicy: usageDBMigrationPolicyAutoSafe, LogPath: logPath, From: from, To: to, ResolvedGroup: "eval-group"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ReasoningTokens != 7 || got.ReasoningAttemptCount != 1 || got.ReasoningReportedAttemptCount != 1 || len(got.Coverage) != 1 {
		t.Fatalf("JSONL coverage=%#v", got)
	}
}

func TestUsageTimePreservesSubsecondBounds(t *testing.T) {
	tm, err := time.Parse(time.RFC3339Nano, "2026-07-23T12:00:00.123456789Z")
	if err != nil {
		t.Fatal(err)
	}
	if got := formatUsageTime(tm); got != "2026-07-23T12:00:00.123456789Z" {
		t.Fatalf("formatted=%q", got)
	}
	parsed, err := parseUsageTime("2026-07-23T12:00:00.123456789Z")
	if err != nil || !parsed.Equal(tm) {
		t.Fatalf("parsed=%v err=%v", parsed, err)
	}
}

func TestReasoningUsageColumnsPersistAsNullableScalars(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, column := range []string{"reasoning_tokens", "reasoning_attempt_count", "reasoning_successful_attempt_count", "reasoning_reported_attempt_count"} {
		if !store.db.Migrator().HasColumn(&usageRecord{}, column) {
			t.Fatalf("request_usage missing %s", column)
		}
	}
	if !store.db.Migrator().HasColumn(&requestAttemptRecord{}, "reasoning_tokens") {
		t.Fatal("request_attempts missing reasoning_tokens")
	}
	zero := 0
	if err := store.db.Create(&requestAttemptRecord{RequestID: "reasoning-zero", AttemptIndex: 1, TS: "2026-07-23T00:00:00.000Z", Provider: "mock", Model: "mock", Dialect: "openai-chat", EndpointHost: "mock", ReasoningTokens: &zero}).Error; err != nil {
		t.Fatal(err)
	}
	var got requestAttemptRecord
	if err := store.db.First(&got, "request_id = ? AND attempt_index = ?", "reasoning-zero", 1).Error; err != nil {
		t.Fatal(err)
	}
	if got.ReasoningTokens == nil || *got.ReasoningTokens != 0 {
		t.Fatalf("nullable zero did not round trip: %#v", got)
	}
}

func TestReasoningUsageAggregateSurvivesDiagnosticChildSuppression(t *testing.T) {
	reasoning := 0
	rec := logRecord{AttemptsDetail: []attemptLogRecord{{StatusCode: 200, Selected: true, ReasoningTokens: &reasoning}}}
	populateReasoningUsageCoverage(&rec)
	rec.AttemptsDetail = nil // the diagnostics-disabled path removes child telemetry before usage persistence
	row := rowFromRecord(rec)
	if row.ReasoningTokens == nil || *row.ReasoningTokens != 0 || row.ReasoningAttemptCount != 1 || row.ReasoningSuccessfulAttemptCount != 1 || row.ReasoningReportedAttemptCount != 1 {
		t.Fatalf("suppressed diagnostics lost reasoning aggregate: %#v", row)
	}
}

func TestAdminReasoningCoverageDTOsPreserveNullability(t *testing.T) {
	zero := 0
	request := adminRequestFromRow(usageRow{ReasoningTokens: &zero, ReasoningAttemptCount: 2, ReasoningSuccessfulAttemptCount: 2, ReasoningReportedAttemptCount: 1})
	if request.ReasoningTokens == nil || *request.ReasoningTokens != 0 || request.ReasoningAttemptCount != 2 || request.ReasoningSuccessfulAttemptCount != 2 || request.ReasoningReportedAttemptCount != 1 {
		t.Fatalf("request DTO=%#v", request)
	}
	attempt := adminAttemptsFromRecords([]requestAttemptRecord{{ReasoningTokens: &zero}})[0]
	if attempt.ReasoningTokens == nil || *attempt.ReasoningTokens != 0 {
		t.Fatalf("attempt DTO=%#v", attempt)
	}
	if adminRequestFromRow(usageRow{}).ReasoningTokens != nil || adminAttemptsFromRecords([]requestAttemptRecord{{}})[0].ReasoningTokens != nil {
		t.Fatal("missing reasoning usage must remain null in admin DTOs")
	}
}

func TestReasoningUsageResponseEncodingAndAttemptCoverage(t *testing.T) {
	n := 7
	usage := Usage{InputTokens: 2, OutputTokens: 10, TotalTokens: 12, ReasoningTokens: &n}
	chat := encodeChatResponse(&IRResponse{Usage: usage}, "")["usage"].(map[string]any)
	if got := chat["completion_tokens_details"].(map[string]any)["reasoning_tokens"]; got != 7 {
		t.Fatalf("chat reasoning usage=%#v", chat)
	}
	responses := encodeResponsesResponse(&IRResponse{Usage: usage}, "")["usage"].(map[string]any)
	if got := responses["output_tokens_details"].(map[string]any)["reasoning_tokens"]; got != 7 {
		t.Fatalf("responses reasoning usage=%#v", responses)
	}
	zero := 0
	total, attempts, successful, reported := reasoningUsageCoverage([]attemptLogRecord{{StatusCode: 502}, {StatusCode: 200, ReasoningTokens: &zero, ErrorClass: "decode_error"}, {StatusCode: 200, ReasoningTokens: &n, Selected: true}})
	if total == nil || *total != 7 || attempts != 3 || successful != 1 || reported != 2 {
		t.Fatalf("coverage total=%v attempts=%d successful=%d reported=%d", total, attempts, successful, reported)
	}
	if absent, _, _, count := reasoningUsageCoverage([]attemptLogRecord{{StatusCode: 200}}); absent != nil || count != 0 {
		t.Fatalf("absence was not preserved: total=%v reported=%d", absent, count)
	}
	stream := httptest.NewRecorder()
	writeChatTextSSE(func(_ string, v any) { _ = json.NewEncoder(stream).Encode(v) }, stream, &IRResponse{ID: "id", Model: "model", Usage: usage}, "")
	if !bytes.Contains(stream.Body.Bytes(), []byte("reasoning_tokens")) {
		t.Fatalf("streaming usage lost reasoning tokens: %s", stream.Body.String())
	}
	bridged, err := decodeResponsesToChatBridge([]byte(`{"id":"chat","usage":{"prompt_tokens":2,"completion_tokens":10,"completion_tokens_details":{"reasoning_tokens":7}},"choices":[{"message":{"content":"ok"}}]}`), "model")
	if err != nil || bridged.Raw["usage"].(map[string]any)["output_tokens_details"].(map[string]any)["reasoning_tokens"] != 7 {
		t.Fatalf("Responses-to-Chat bridge lost reasoning usage: %#v, %v", bridged, err)
	}
}
