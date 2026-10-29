// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise bounded security CSV exports under completed requests, fallback,
// and deliberately cut-off streams. The security CSV is not usage billing.
func TestGovernanceLoadAuditExport(t *testing.T) {
	runGovernanceLoadAuditExport(t, nil)
}

// Use a disposable, schema-isolated PostgreSQL database. Never point this
// destructive schema test at production data.
func TestGovernanceLoadAuditExportPostgres(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("GOVERNANCE_LOAD_POSTGRES_DSN"))
	if base == "" {
		t.Skip("set GOVERNANCE_LOAD_POSTGRES_DSN to a disposable PostgreSQL database")
	}
	// The shared helper uses this variable; the dedicated opt-in above keeps
	// this load test from running against an unrelated PostgreSQL test DSN.
	t.Setenv("ROUTER_TEST_PG_DSN", base)
	_, cleanup, schema := configAdminPostgresTestDB(t)
	defer cleanup()
	cfg := UsageDBConfig{Driver: "postgres", DSN: pgDSNWithSearchPath(base, schema), MigrationPolicy: usageDBMigrationPolicyDeploymentJob}
	runner, closeUsageDB, err := UsageMigrationRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeUsageDB()
	if err := runner.ApplyPending("governance-load-test"); err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyMaintenancePendingWithEvidence("governance-load-test", "empty-disposable-test-schema"); err != nil {
		t.Fatal(err)
	}
	if err := runner.ApplyPending("governance-load-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunDataJob(context.Background(), "historical-usage-validation-v1", "governance-load-test", DataJobCheckpoint{Ordinal: 0}); err != nil {
		t.Fatal(err)
	}
	status, err := runner.Verify()
	if err != nil || !UsageMigrationServingCompatible(status) {
		t.Fatalf("usage schema not serving-compatible: state=%s err=%v", status.State, err)
	}
	runGovernanceLoadAuditExport(t, &cfg)
}

func runGovernanceLoadAuditExport(t *testing.T, usageCfg *UsageDBConfig) {
	t.Helper()
	const workers = 64
	const secondToken = "synthetic-audit-second-token"
	t.Setenv("GOVERNANCE_TEST_ADMIN_HASH", mustBcryptHash(t, "synthetic-password"))
	var primaryCalls, fallbackCalls atomic.Int64
	canceledUpstreams := make(chan struct{}, workers/8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		if body["model"] == "primary-model" {
			primaryCalls.Add(1)
			http.Error(w, "synthetic failure", 503)
			return
		}
		if body["model"] != "fallback-model" {
			http.Error(w, "unexpected target", 400)
			return
		}
		fallbackCalls.Add(1)
		if stream, _ := body["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"audit_mock\",\"object\":\"chat.completion.chunk\",\"model\":\"fallback-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
			raw, _ := json.Marshal(body)
			if bytes.Contains(raw, []byte("abort-stream")) {
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				select {
				case <-r.Context().Done():
					canceledUpstreams <- struct{}{}
				case <-time.After(15 * time.Second):
				}
				return
			}
			fmt.Fprint(w, "data: {\"id\":\"audit_mock\",\"object\":\"chat.completion.chunk\",\"model\":\"fallback-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "audit_mock", "object": "chat.completion", "model": "fallback-model", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.Cache.Enabled = false
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	if usageCfg != nil {
		cfg.Server.UsageDB = *usageCfg
	}
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, Realm: "Synthetic", AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: "GOVERNANCE_TEST_ADMIN_HASH", Subject: "basic:admin", Domain: "local/test"}}}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, basic:admin, *, admin:security_reports, read|export", "p, basic:admin, *, admin:reports, read|export|drilldown"}}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 256, Security: AdminSecurityReportsConfig{Enabled: true, RetentionDays: 30}}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "primary-model"}, {Provider: "mock", Model: "fallback-model"}}}
	cfg.Callers[0].Rate = RateConfig{RPM: 10000, TPM: 10000000, Concurrent: workers + 10}
	second := cfg.Callers[0]
	second.ID, second.TokenID = "audit-second", "rtr_audit_second"
	digest := sha256.Sum256([]byte(secondToken))
	second.TokenSHA256 = hex.EncodeToString(digest[:])
	cfg.Callers = append(cfg.Callers, second)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	router := httptest.NewServer(svc.Handler())
	defer router.Close()
	client := &http.Client{Timeout: 90 * time.Second}
	type result struct {
		key, id string
		err     error
	}
	results := make(chan result, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			token, key := testToken, cfg.Callers[0].TokenID
			if i%2 != 0 {
				token, key = secondToken, second.TokenID
			}
			content := "synthetic audit request"
			if i%8 == 0 {
				content = "abort-stream"
			}
			body, _ := json.Marshal(map[string]any{"model": "default", "stream": (i/2)%2 == 0, "max_tokens": 64, "messages": []any{map[string]any{"role": "user", "content": content}}})
			req, err := http.NewRequest("POST", router.URL+"/v1/chat/completions", bytes.NewReader(body))
			if err != nil {
				results <- result{key: key, err: err}
				return
			}
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := client.Do(req)
			if err != nil {
				results <- result{key: key, err: err}
				return
			}
			defer resp.Body.Close()
			if i%8 == 0 {
				var firstLine string
				firstLine, err = bufio.NewReader(resp.Body).ReadString('\n')
				if err == nil && !strings.HasPrefix(firstLine, "data:") {
					err = fmt.Errorf("worker %d did not receive an SSE frame before cutoff", i)
				}
				resp.Body.Close()
			} else {
				_, err = io.Copy(io.Discard, resp.Body)
			}
			if err == nil && resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("worker %d status %d", i, resp.StatusCode)
			}
			results <- result{key: key, id: resp.Header.Get("X-Request-Id"), err: err}
		}(i)
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(120 * time.Second):
		t.Fatal("audit load timeout")
	}
	close(results)
	for i := 0; i < workers/8; i++ {
		select {
		case <-canceledUpstreams:
		case <-time.After(15 * time.Second):
			t.Fatal("upstream did not observe client cancellation")
		}
	}
	want := map[string]map[string]bool{cfg.Callers[0].TokenID: {}, second.TokenID: {}}
	for result := range results {
		if result.err != nil {
			t.Error(result.err)
			continue
		}
		if result.id == "" || want[result.key][result.id] {
			t.Errorf("missing or duplicate request id for key %s", result.key)
			continue
		}
		want[result.key][result.id] = true
	}
	if primaryCalls.Load() != workers || fallbackCalls.Load() != workers {
		t.Fatalf("primary=%d fallback=%d, want %d each", primaryCalls.Load(), fallbackCalls.Load(), workers)
	}
	// A canceled client can observe EOF before the router has settled its
	// asynchronous security event. Wait for the bounded writer drain.
	deadline := time.Now().Add(15 * time.Second)
	for {
		flushUsageForTest(t, svc)
		var count int64
		if err := svc.usage.db.Model(&securityAccessEventRecord{}).Where("token_id IN ?", []string{cfg.Callers[0].TokenID, second.TokenID}).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count >= workers {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d security events persisted", count, workers)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for key, ids := range want {
		if len(ids) != workers/2 {
			t.Fatalf("key %s generated %d IDs", key, len(ids))
		}
		req := httptest.NewRequest("GET", "/admin/reports/security/export.csv?since=24h&limit=256&token_id="+key, nil)
		req.SetBasicAuth("admin", "synthetic-password")
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("CSV status=%d", rr.Code)
		}
		for _, forbidden := range []string{testToken, secondToken, "synthetic audit request", "abort-stream"} {
			if strings.Contains(rr.Body.String(), forbidden) {
				t.Fatal("CSV exposed synthetic credential or request content")
			}
		}
		rows, err := csv.NewReader(strings.NewReader(rr.Body.String())).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, row := range rows[1:] {
			if len(row) < 15 || row[14] != key || !ids[row[1]] || seen[row[1]] {
				t.Fatalf("unexpected, duplicate or cross-key CSV event")
			}
			seen[row[1]] = true
		}
		if len(seen) != len(ids) {
			t.Fatalf("key %s got %d events want %d", key, len(seen), len(ids))
		}
		t.Logf("token_id=%s requests=%d csv_events=%d duplicates=0", key, len(ids), len(seen))
	}
}
