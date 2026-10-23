// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrafficTuningAdvisorRoutesAroundUpstream400WhenShapingAdmitted(t *testing.T) {
	rows := []usageRow{
		advisorUsageRow("req_1", 502),
		advisorUsageRow("req_2", 502),
		advisorUsageRow("req_3", 200),
	}
	attempts := []trafficTuningAttempt{
		{RequestID: "req_1", StatusCode: 400, ErrorClass: "invalid_request"},
		{RequestID: "req_2", StatusCode: 400, ErrorClass: "invalid_request"},
	}
	got := BuildTrafficTuningAdvisor(rows, nil, attempts)
	if len(got) == 0 {
		t.Fatal("no advisor rows")
	}
	if got[0].Recommendation != "route_around_incompatible_target" {
		t.Fatalf("recommendation=%q, want route_around_incompatible_target: %#v", got[0].Recommendation, got[0])
	}
	if strings.Contains(got[0].Recommendation, "burst") || got[0].Rejected != 0 || got[0].Queued != 0 {
		t.Fatalf("upstream 400 admitted case should not recommend burst/queue: %#v", got[0])
	}
}

func TestTrafficTuningAdvisorEnablesQueueForRejectedBursts(t *testing.T) {
	rejected := advisorUsageRow("req_rejected", 429)
	rejected.TrafficShapeApplied = true
	rejected.TrafficShapeDecision = trafficShapeDecisionRejected
	rejected.TrafficShapeBucket = "caller.request_start_per_sec"
	rejected.TrafficShapeRetryAfterMS = 750
	got := BuildTrafficTuningAdvisor([]usageRow{rejected, advisorUsageRow("req_ok", 200)}, nil, nil)
	if got[0].Recommendation != "enable_queue" {
		t.Fatalf("recommendation=%q, want enable_queue: %#v", got[0].Recommendation, got[0])
	}
}

func TestTrafficTuningAdvisorIncreasesQueueDepthAfterQueuedRejections(t *testing.T) {
	rejected := advisorUsageRow("req_rejected", 429)
	rejected.TrafficShapeApplied = true
	rejected.TrafficShapeDecision = trafficShapeDecisionRejected
	rejected.TrafficShapeQueueWaitMS = 1200
	queued := advisorUsageRow("req_queued", 200)
	queued.TrafficShapeApplied = true
	queued.TrafficShapeDecision = trafficShapeDecisionQueued
	queued.TrafficShapeQueueWaitMS = 900
	got := BuildTrafficTuningAdvisor([]usageRow{rejected, queued}, nil, nil)
	if got[0].Recommendation != "increase_queue_depth" {
		t.Fatalf("recommendation=%q, want increase_queue_depth: %#v", got[0].Recommendation, got[0])
	}
}

func TestTrafficTuningAdvisorDisablesQueueForCanceledLatencySensitiveClient(t *testing.T) {
	row := advisorUsageRow("req_cancel", 499)
	row.TrafficShapeApplied = true
	row.TrafficShapeDecision = trafficShapeDecisionQueued
	row.TrafficShapeQueueWaitMS = 2500
	got := BuildTrafficTuningAdvisor([]usageRow{row}, nil, []trafficTuningAttempt{{RequestID: "req_cancel", ClientCanceled: true}})
	if got[0].Recommendation != "disable_queue_for_latency_sensitive_client" {
		t.Fatalf("recommendation=%q, want disable_queue_for_latency_sensitive_client: %#v", got[0].Recommendation, got[0])
	}
}

func TestTrafficTuningAdvisorProvider429AcrossUsers(t *testing.T) {
	alice := advisorUsageRow("req_alice", 502)
	alice.CallerUser = "alice"
	bob := advisorUsageRow("req_bob", 502)
	bob.CallerUser = "bob"
	events := []upstreamShapeJoinedEvent{
		{Row: alice, Event: requestUpstreamShapeEventRecord{Decision: shapeDecisionCooldownStarted, BackoffReason: "adaptive-backoff-provider-429"}},
		{Row: bob, Event: requestUpstreamShapeEventRecord{Decision: shapeDecisionCooldownStarted, BackoffReason: "adaptive-backoff-provider-429"}},
	}
	got := BuildTrafficTuningAdvisor([]usageRow{alice, bob}, events, []trafficTuningAttempt{
		{RequestID: "req_alice", StatusCode: 429},
		{RequestID: "req_bob", StatusCode: 429},
	})
	if got[0].Recommendation != "investigate_provider_429_capacity" {
		t.Fatalf("recommendation=%q, want investigate_provider_429_capacity: %#v", got[0].Recommendation, got[0])
	}
}

func TestTrafficTuningAdvisorHardCaller429UsesQuotaGuidance(t *testing.T) {
	row := advisorUsageRow("req_rpm", 429)
	row.Error = "rpm-exceeded"
	got := BuildTrafficTuningAdvisor([]usageRow{row}, nil, nil)
	if got[0].Recommendation != "adjust_caller_quota_or_rate_limit" {
		t.Fatalf("recommendation=%q, want adjust_caller_quota_or_rate_limit: %#v", got[0].Recommendation, got[0])
	}
	if strings.Contains(got[0].Recommendation, "burst") || got[0].Upstream429 != 0 {
		t.Fatalf("hard caller 429 should not be burst or upstream guidance: %#v", got[0])
	}
}

func TestTrafficTuningAdvisorCallerQuotaDoesNotLookLikeProviderCapacity(t *testing.T) {
	alice := advisorUsageRow("req_alice", 429)
	alice.CallerUser = "alice"
	alice.Error = "quota-exhausted"
	bob := advisorUsageRow("req_bob", 429)
	bob.CallerUser = "bob"
	bob.Error = "quota-exhausted"
	got := BuildTrafficTuningAdvisor([]usageRow{alice, bob}, nil, nil)
	if got[0].Recommendation == "investigate_provider_429_capacity" {
		t.Fatalf("caller quota should not be provider capacity guidance: %#v", got[0])
	}
	if got[0].Recommendation != "adjust_caller_quota_or_rate_limit" {
		t.Fatalf("recommendation=%q, want caller quota guidance: %#v", got[0].Recommendation, got[0])
	}
}

func TestTrafficTuningAdvisorNoSignificantErrors(t *testing.T) {
	got := BuildTrafficTuningAdvisor([]usageRow{advisorUsageRow("req_ok", 200)}, nil, nil)
	if got[0].Recommendation != "no_shaping_change_indicated" {
		t.Fatalf("recommendation=%q, want no_shaping_change_indicated: %#v", got[0].Recommendation, got[0])
	}
}

func TestTrafficTuningAdvisorSQLRoutesAroundUpstream400(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for i, spec := range []struct {
		requestID string
		status    int
		attempt   int
	}{
		{"req_sql_1", 502, 400},
		{"req_sql_2", 502, 400},
		{"req_sql_3", 200, 200},
	} {
		if err := store.db.Create(advisorUsageRecord(spec.requestID, spec.status, from.Add(time.Duration(i)*time.Minute))).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.db.Create(&requestAttemptRecord{
			RequestID:    spec.requestID,
			AttemptIndex: 1,
			TS:           formatUsageTime(from.Add(time.Duration(i) * time.Minute)),
			Provider:     "mock",
			Model:        "mock-model",
			Dialect:      "openai-chat",
			StatusCode:   spec.attempt,
			Selected:     true,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows, total, hasMore, err := store.trafficTuningAdvisorRowsSQL(UsageReportOptions{From: from, To: from.Add(time.Hour)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore {
		t.Fatal("hasMore=true, want false")
	}
	if total == nil || total.Calls != 3 {
		t.Fatalf("total=%#v, want 3 calls", total)
	}
	if len(rows) == 0 || rows[0].Recommendation != "route_around_incompatible_target" {
		t.Fatalf("top SQL recommendation=%#v, want route_around_incompatible_target", rows)
	}
	if rows[0].Upstream400 != 2 || rows[0].Rejected != 0 || rows[0].Queued != 0 {
		t.Fatalf("unexpected SQL evidence: %#v", rows[0])
	}
}

func TestTrafficTuningAdvisorSQLProvider429AcrossUsersAndLimit(t *testing.T) {
	store, err := OpenUsageStorePath(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for i, user := range []string{"alice", "bob"} {
		requestID := "req_provider_429_" + user
		record := advisorUsageRecord(requestID, 502, from.Add(time.Duration(i)*time.Minute))
		record.CallerUser = user
		if err := store.db.Create(record).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.db.Create(&requestAttemptRecord{
			RequestID:    requestID,
			AttemptIndex: 1,
			TS:           formatUsageTime(from.Add(time.Duration(i) * time.Minute)),
			Provider:     "mock",
			Model:        "mock-model",
			Dialect:      "openai-chat",
			StatusCode:   429,
			Selected:     true,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.db.Create(&requestUpstreamShapeEventRecord{
			RequestID:     requestID,
			Seq:           1,
			TS:            formatUsageTime(from.Add(time.Duration(i) * time.Minute)),
			Scope:         "provider",
			Provider:      "mock",
			Model:         "mock-model",
			Dialect:       "openai-chat",
			Bucket:        shapeBucketBackoff,
			Decision:      shapeDecisionCooldownStarted,
			BackoffReason: "adaptive-backoff-provider-429",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows, _, hasMore, err := store.trafficTuningAdvisorRowsSQL(UsageReportOptions{From: from, To: from.Add(time.Hour)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore {
		t.Fatal("hasMore=false, want true for limit 1")
	}
	if len(rows) != 1 || rows[0].Recommendation != "investigate_provider_429_capacity" {
		t.Fatalf("limited SQL recommendation=%#v, want provider capacity", rows)
	}
	if rows[0].AffectedUsers != 2 {
		t.Fatalf("affected_users=%d, want 2: %#v", rows[0].AffectedUsers, rows[0])
	}
}

func advisorUsageRow(requestID string, status int) usageRow {
	return usageRow{
		RequestID:          requestID,
		CallerID:           "caller-prod",
		CallerUser:         "alice",
		CallerProject:      "example",
		CallerEnvironment:  "prod",
		Client:             "codex",
		RequestedModel:     "default",
		ResolvedGroup:      "default",
		TargetProvider:     "mock",
		TargetModel:        "mock-model",
		TargetDialect:      "openai-chat",
		Status:             status,
		LatencyMS:          100,
		TrafficShapeBucket: "caller.request_start_per_sec",
	}
}

func advisorUsageRecord(requestID string, status int, ts time.Time) *usageRecord {
	row := advisorUsageRow(requestID, status)
	return &usageRecord{
		RequestID:               row.RequestID,
		TS:                      formatUsageTime(ts),
		CallerID:                row.CallerID,
		CallerUser:              row.CallerUser,
		CallerProject:           row.CallerProject,
		CallerEnvironment:       row.CallerEnvironment,
		TokenID:                 "tok_test",
		Client:                  row.Client,
		InboundDialect:          "openai-chat",
		RequestedModel:          row.RequestedModel,
		ResolvedGroup:           row.ResolvedGroup,
		Strategy:                "weighted",
		TargetProvider:          row.TargetProvider,
		TargetModel:             row.TargetModel,
		TargetDialect:           row.TargetDialect,
		Status:                  status,
		Attempts:                1,
		FallbackUsed:            status >= 500,
		LatencyMS:               100,
		QuotaState:              "ok",
		KeyState:                "active",
		TrafficShapeBucket:      row.TrafficShapeBucket,
		TrafficShapeDecision:    row.TrafficShapeDecision,
		TrafficShapeApplied:     row.TrafficShapeApplied,
		TrafficShapeQueueWaitMS: row.TrafficShapeQueueWaitMS,
	}
}
