// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHoneytokenAndCanaryNeverReachUpstream(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	generated, err := GenerateCallerToken(TokenGenerateOptions{
		OwnerUser: "security", Project: "decoys", Environment: "test", KeySlug: "honey",
		Allow: []string{"canary"}, HoneytokenLabel: "docs config example", Now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg.Models["canary"] = ModelGroup{Strategy: "static", Canary: &CanaryGroupConfig{Callers: []string{generated.Caller.ID}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "canary")
	cfg.Callers = append(cfg.Callers, generated.Caller)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	call := func(path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w
	}
	decoy := call("/v1/chat/completions", generated.Token, `{"model":"default","stream":true,"messages":[{"role":"user","content":"secret prompt"}]}`)
	if decoy.Code != http.StatusOK || decoy.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(decoy.Body.String(), honeytokenCompletionText) {
		t.Fatalf("honeytoken status=%d body=%s", decoy.Code, decoy.Body.String())
	}
	canary := call("/v1/chat/completions", generated.Token, `{"model":"canary","messages":[{"role":"user","content":"probe"}]}`)
	if canary.Code != http.StatusOK || !strings.Contains(canary.Body.String(), honeytokenCompletionText) {
		t.Fatalf("canary status=%d body=%s", canary.Code, canary.Body.String())
	}
	notSelected := call("/v1/chat/completions", testToken, `{"model":"canary","messages":[{"role":"user","content":"probe"}]}`)
	if notSelected.Code == http.StatusOK {
		t.Fatalf("non-selected caller used canary: %s", notSelected.Body.String())
	}
	if got := upstreamCalls.Load(); got != 0 {
		t.Fatalf("upstream calls=%d, want 0", got)
	}
	svc.Close()
	store, err := OpenUsageStore(cfg.Server.UsageDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var events []securityAccessEventRecord
	if err := store.db.Where("event_type IN ?", []string{"security_honeytoken_used", "security_canary_group_used"}).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Outcome != "critical" || events[1].Outcome != "critical" {
		t.Fatalf("security events=%+v, want two critical events", events)
	}

	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelsReq.Header.Set("Authorization", "Bearer "+generated.Token)
	modelsResp := httptest.NewRecorder()
	svc.Handler().ServeHTTP(modelsResp, modelsReq)
	if modelsResp.Code != http.StatusOK || !strings.Contains(modelsResp.Body.String(), `"id":"canary"`) {
		t.Fatalf("canary model visibility status=%d body=%s", modelsResp.Code, modelsResp.Body.String())
	}

	rows, err := HoneytokenReport(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Label != "docs config example" || rows[0].CallerID != generated.Caller.ID || rows[0].PlantedAt == "" || rows[0].LastSeen == "" {
		t.Fatalf("honeytoken report=%+v", rows)
	}
	for _, row := range rows {
		if strings.Contains(strings.ToLower(row.Label+row.CallerID+row.LastSeen), "secret") {
			t.Fatalf("report leaked secret-like material: %+v", row)
		}
	}
}
