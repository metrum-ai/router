// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectBudgetManagerThresholdTripAndWindowReset(t *testing.T) {
	m := newProjectBudgetManager()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	cfg := ProjectBudgetConfig{DailyUSD: 1, MonthlyUSD: 2, AlertThresholds: []int{50, 80}}
	m.add("p1", cfg, .6)
	if !m.allow("p1", cfg) {
		t.Fatal("budget tripped below ceiling")
	}
	m.add("p1", cfg, .5)
	if m.allow("p1", cfg) {
		t.Fatal("daily ceiling did not block the next request")
	}
	now = now.Add(24 * time.Hour)
	if !m.allow("p1", cfg) {
		t.Fatal("daily window did not reset")
	}
}

func TestProjectBudgetHTTPEnforcementE2E(t *testing.T) {
	var logOutput bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logOutput)
	t.Cleanup(func() { log.SetOutput(oldOutput) })
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "budget", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1000, "completion_tokens": 0, "total_tokens": 1000}})
	}))
	defer provider.Close()
	dir := t.TempDir()
	cfg := testConfig(t, provider.URL, "budget-test-key", dir)
	cfg.Callers[0].Project = "local"
	cfg.Users = []UserConfig{{ID: "alice", Status: "active", Type: "human"}}
	cfg.Projects = []ProjectConfig{{ID: "local", Status: "active", Budget: ProjectBudgetConfig{DailyUSD: .5, AlertThresholds: []int{50}}}}
	cfg.ProjectMemberships = []ProjectMembershipConfig{{UserID: "alice", Project: "local", Role: "member", Status: "active"}}
	group := cfg.Models["default"]
	group.Targets[0].InputPricePerMillionUSD = 1000
	cfg.Models["default"] = group
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	router := httptest.NewServer(svc.Handler())
	defer router.Close()
	post := func() int {
		req, _ := http.NewRequest(http.MethodPost, router.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if status := post(); status != http.StatusOK {
		t.Fatalf("first request status=%d", status)
	}
	if status := post(); status != http.StatusServiceUnavailable {
		t.Fatalf("request after ceiling status=%d, want 503", status)
	}
	if !strings.Contains(logOutput.String(), "event=project_budget action=threshold project=\"local\"") || !strings.Contains(logOutput.String(), "action=trip project=\"local\"") {
		t.Fatalf("missing budget threshold/trip events: %s", logOutput.String())
	}
}

func TestProjectBudgetValidation(t *testing.T) {
	for _, cfg := range []ProjectBudgetConfig{{DailyUSD: -1}, {MonthlyUSD: 1, AlertThresholds: []int{101}}, {DailyUSD: 1, AlertThresholds: []int{50, 50}}} {
		base := minimalConfig(t)
		base.Projects = []ProjectConfig{{ID: "p1", Budget: cfg}}
		if err := base.validate(false); err == nil {
			t.Fatalf("accepted invalid budget: %+v", cfg)
		}
	}
}
