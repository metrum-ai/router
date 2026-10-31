// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

func TestBackgroundResponsesSettlementLifecycleSQLite(t *testing.T) {
	exerciseBackgroundResponsesSettlementLifecycle(t, nil)
}

func TestBackgroundResponsesSettlementLifecyclePostgres(t *testing.T) {
	_, cleanup, schema := configAdminPostgresTestDB(t)
	defer cleanup()
	cfg := UsageDBConfig{Driver: "postgres", DSN: pgDSNWithSearchPath(os.Getenv("ROUTER_TEST_PG_DSN"), schema), MigrationPolicy: usageDBMigrationPolicyAutoSafe}
	runner, closeDB, err := UsageMigrationRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.ApplyPending("background-lifecycle-test"); err != nil {
		closeDB()
		t.Fatal(err)
	}
	status, err := runner.Verify()
	if err != nil {
		closeDB()
		t.Fatal(err)
	}
	for _, job := range status.Jobs {
		if job.State != migrationDataJobValidated {
			if _, err := runner.RunDataJob(context.Background(), job.Key, "background-lifecycle-test", DataJobCheckpoint{Ordinal: 0}); err != nil {
				closeDB()
				t.Fatal(err)
			}
		}
	}
	closeDB()
	exerciseBackgroundResponsesSettlementLifecycle(t, &cfg)
}

func exerciseBackgroundResponsesSettlementLifecycle(t *testing.T, database *UsageDBConfig) {
	var count atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" && r.URL.Path == "/v1/responses" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			if body["background"] != true {
				t.Errorf("background not forwarded: %v", body)
			}
			id := fmt.Sprintf("resp_background_%d", count.Add(1))
			if body["stream"] == true {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":%q,\"status\":\"in_progress\",\"output\":[]}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n", id, id)
				return
			}
			status := "queued"
			usage := "null"
			if body["input"] == "complete" {
				status = "completed"
				usage = `{"input_tokens":10,"output_tokens":5,"total_tokens":15}`
			}
			fmt.Fprintf(w, `{"id":%q,"object":"response","status":%q,"model":"mock-model","output":[],"usage":%s}`, id, status, usage)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/responses/")
		cancel := strings.HasSuffix(id, "/cancel")
		id = strings.TrimSuffix(id, "/cancel")
		if r.Method == "DELETE" {
			fmt.Fprintf(w, `{"id":%q,"object":"response.deleted","deleted":true}`, id)
			return
		}
		if cancel {
			if id == "resp_background_5" {
				fmt.Fprintf(w, `{"id":%q,"object":"response","status":"cancelled","output":[],"usage":null}`, id)
				return
			}
			fmt.Fprintf(w, `{"id":%q,"object":"response","status":"cancelled","output":[],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`, id)
			return
		}
		if r.URL.Query().Get("stream") == "true" {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n", id)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"object":"response","status":"completed","model":"mock-model","output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`, id)
	}))
	defer up.Close()
	dir := t.TempDir()
	cfg := testConfig(t, up.URL, "provider-key", dir)
	cfg.Callers[0].Rate.Concurrent = 32
	cfg.Server.Identifiers = identifierTestConfig()
	p := cfg.Provider["mock"]
	p.Dialect = "openai-responses"
	p.KeyID = "account"
	cfg.Provider["mock"] = p
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	if database != nil {
		cfg.Server.UsageDB = *database
	}
	sum := sha256.Sum256([]byte("bob-token"))
	bob := cfg.Callers[0]
	bob.ID = "bob"
	bob.TokenID = "bob"
	bob.TokenSHA256 = hex.EncodeToString(sum[:])
	cfg.Callers = append(cfg.Callers, bob)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if svc != nil {
			svc.Close()
		}
	}()
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w
	}
	create := func(input string) string {
		t.Helper()
		w := request("POST", "/v1/responses", testToken, fmt.Sprintf(`{"model":"default","input":%q,"store":true,"background":true,"max_output_tokens":16}`, input))
		if w.Code != 200 {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		var obj map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
			t.Fatal(err)
		}
		return stringValue(obj["id"])
	}
	flush := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.usage.writer.flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	total := func() int64 {
		t.Helper()
		var n int64
		if err := svc.usage.db.Raw("SELECT COALESCE(SUM(tokens),0) FROM response_background_quota_daily WHERE caller_id = ?", "alice").Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	if w := request("POST", "/v1/responses", testToken, `{"model":"default","input":"x","background":true,"store":false}`); w.Code != 400 || count.Load() != 0 {
		t.Fatalf("background without storage: %d %s", w.Code, w.Body.String())
	}
	complete := create("complete")
	flush()
	if total() != 15 {
		t.Fatalf("POST settlement = %d", total())
	}
	queued := create("queued")
	flush()
	if total() != 15 {
		t.Fatal("queued work billed before terminal usage")
	}
	if w := request("GET", "/v1/responses/"+queued, "bob-token", ""); w.Code != 404 {
		t.Fatalf("cross caller: %d %s", w.Code, w.Body.String())
	}
	if w := request("DELETE", "/v1/responses/"+queued, testToken, ""); w.Code != 409 {
		t.Fatalf("deleted unsettled response: %d %s", w.Code, w.Body.String())
	}
	// Free stored retrieval remains available after the owner's key is exhausted.
	svc.quota.callers["alice"].cfg.Key.LifetimeTokens = 1
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := request("GET", "/v1/responses/"+queued, testToken, "")
			if w.Code != 200 && w.Code != 503 {
				t.Errorf("concurrent GET: %d %s", w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	if w := request("GET", "/v1/responses/"+queued, testToken, ""); w.Code != 200 {
		t.Fatalf("retry GET: %d %s", w.Code, w.Body.String())
	}
	if total() != 30 {
		t.Fatalf("GET double charged: %d", total())
	}
	svc.quota.callers["alice"].cfg.Key.LifetimeTokens = 0
	stream := create("queued")
	flush()
	if w := request("GET", "/v1/responses/"+stream+"?stream=true", testToken, ""); w.Code != 200 || !strings.Contains(w.Body.String(), stream) {
		t.Fatalf("stream GET: %d %s", w.Code, w.Body.String())
	}
	if total() != 45 {
		t.Fatalf("stream settlement = %d", total())
	}
	cancelID := create("queued")
	flush()
	if w := request("POST", "/v1/responses/"+cancelID+"/cancel", testToken, ""); w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if total() != 49 {
		t.Fatalf("cancel settlement = %d", total())
	}
	if w := request("POST", "/v1/responses/"+cancelID+"/cancel", testToken, ""); w.Code != 200 {
		t.Fatalf("repeat cancel: %d %s", w.Code, w.Body.String())
	}
	if total() != 49 {
		t.Fatalf("cancel double billed = %d", total())
	}
	if w := request("DELETE", "/v1/responses/"+complete, testToken, ""); w.Code != 200 {
		t.Fatalf("delete settled: %d %s", w.Code, w.Body.String())
	}
	unknown := create("queued")
	flush()
	if w := request("POST", "/v1/responses/"+unknown+"/cancel", testToken, ""); w.Code != 200 {
		t.Fatalf("cancel without usage: %d %s", w.Code, w.Body.String())
	}
	if total() != 49 {
		t.Fatal("unknown usage billed as zero or charged")
	}
	if w := request("DELETE", "/v1/responses/"+unknown, testToken, ""); w.Code != 409 {
		t.Fatalf("deleted unknown usage: %d", w.Code)
	}
	if w := request("GET", "/v1/responses/"+unknown, testToken, ""); w.Code != 200 {
		t.Fatalf("reconcile unknown usage: %d %s", w.Code, w.Body.String())
	}
	if total() != 64 {
		t.Fatalf("reconciled usage = %d", total())
	}
	if w := request("DELETE", "/v1/responses/"+unknown, testToken, ""); w.Code != 200 {
		t.Fatalf("delete reconciled: %d %s", w.Code, w.Body.String())
	}
	w := request("POST", "/v1/responses", testToken, `{"model":"default","input":"stream","background":true,"store":true,"stream":true,"max_output_tokens":16}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "response.completed") {
		t.Fatalf("stream POST: %d %s", w.Code, w.Body.String())
	}
	flush()
	if total() != 79 {
		t.Fatalf("stream POST settlement = %d", total())
	}
	var streamed responseAffinityRecord
	if err := svc.usage.db.Where("upstream_response_id = ?", "resp_background_6").Take(&streamed).Error; err != nil || !streamed.UsageSettled {
		t.Fatalf("stream affinity/settlement: %+v %v", streamed, err)
	}
	svc.Close()
	svc = nil
	svc, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "/v1/responses/"+queued, testToken, ""); w.Code != 200 {
		t.Fatalf("restart GET: %d %s", w.Code, w.Body.String())
	}
	if total() != 79 {
		t.Fatalf("restart double billed = %d", total())
	}
}
