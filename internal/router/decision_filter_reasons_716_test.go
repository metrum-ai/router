// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #716: per-target filter reasons must be stored for eligibility
// refusals, not only carried in the bounded error body. These tests pin the
// persistence for a pre-dispatch capability refusal, for the same refusal when
// candidate rows are switched off, for an upstream refusal after dispatch, and
// for deployments that leave decision telemetry disabled.

func usageTestDecisionRows(t *testing.T, path string) ([]decisionTargetCandidateRecord, []decisionTargetFilterReasonRecord) {
	t.Helper()
	store, err := OpenUsageStorePath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var candidates []decisionTargetCandidateRecord
	if err := store.db.Find(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	var reasons []decisionTargetFilterReasonRecord
	if err := store.db.Find(&reasons).Error; err != nil {
		t.Fatal(err)
	}
	return candidates, reasons
}

func TestFilterReasonsPersistForPreDispatchCapabilityRefusal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("refused request must not reach the upstream: %s", r.URL.Path)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"default","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"unsupported_capability"`) {
		t.Fatalf("status=%d body=%s, want typed 400 unsupported_capability", w.Code, w.Body.String())
	}
	flushUsageForTest(t, svc)

	candidates, reasons := usageTestDecisionRows(t, filepath.Join(dir, "usage.sqlite"))
	if len(candidates) != 1 || candidates[0].Eligible {
		t.Fatalf("candidates=%+v, want one ineligible candidate", candidates)
	}
	if len(reasons) == 0 || reasons[0].Reason != "tool-support" || reasons[0].Stage != "request_shape" {
		t.Fatalf("reasons=%+v, want the bounded tool-support reason", reasons)
	}
	if reasons[0].CandidateIndex != candidates[0].CandidateIndex {
		t.Fatalf("reason candidate index=%d, want %d", reasons[0].CandidateIndex, candidates[0].CandidateIndex)
	}
}

func TestFilterReasonsPersistWithoutCandidateRows(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("refused request must not reach the upstream: %s", r.URL.Path)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	recordCandidates := false
	cfg.Server.DecisionTelemetry.RecordCandidates = &recordCandidates
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"default","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want typed 400", w.Code, w.Body.String())
	}
	flushUsageForTest(t, svc)

	candidates, reasons := usageTestDecisionRows(t, filepath.Join(dir, "usage.sqlite"))
	if len(candidates) != 0 {
		t.Fatalf("candidates=%+v, want none when record_candidates is false", candidates)
	}
	if len(reasons) != 1 || reasons[0].Reason != "tool-support" {
		t.Fatalf("reasons=%+v, want the bounded tool-support reason", reasons)
	}
	if reasons[0].CandidateIndex != 0 {
		t.Fatalf("candidate index=%d, want the group target index 0", reasons[0].CandidateIndex)
	}
}

func TestFilterReasonsAbsentWhenDecisionTelemetryDisabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("refused request must not reach the upstream: %s", r.URL.Path)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"default","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	flushUsageForTest(t, svc)

	candidates, reasons := usageTestDecisionRows(t, filepath.Join(dir, "usage.sqlite"))
	if len(candidates) != 0 || len(reasons) != 0 {
		t.Fatalf("candidates=%+v reasons=%+v, want nothing stored while decision telemetry is disabled", candidates, reasons)
	}
}

func TestFilterReasonsPersistForPostDispatchUpstreamRefusal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"type":"invalid_request_error","code":"invalid_value","param":"messages"}}`)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	group := cfg.Models["default"]
	group.Targets[0].ToolSupport = ToolSupport{OpenAIChat: []string{"tools"}}
	cfg.Models["default"] = group
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"default","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("status=%d, want an upstream refusal", w.Code)
	}
	flushUsageForTest(t, svc)

	candidates, reasons := usageTestDecisionRows(t, filepath.Join(dir, "usage.sqlite"))
	if len(candidates) != 1 || !candidates[0].Eligible || !candidates[0].Selected {
		t.Fatalf("candidates=%+v, want the dispatched candidate recorded as eligible and selected", candidates)
	}
	if len(reasons) != 0 {
		t.Fatalf("reasons=%+v, want no filter reason for a dispatched candidate", reasons)
	}
}
