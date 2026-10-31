// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeResponsesAffinityWrite(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := fmt.Sprintf("resp_upstream_%d", calls.Add(1))
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":%q,\"status\":\"in_progress\",\"model\":\"mock-model\",\"output\":[]}}\n\n", id)
			fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"status\":\"completed\",\"model\":\"mock-model\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n", id)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%q,"object":"response","status":"completed","model":"mock-model","metadata":{"id":"keep"},"output":[{"id":"msg_1","type":"message","content":[]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, id)
	}))
	defer up.Close()
	dir := t.TempDir()
	cfg := testConfig(t, up.URL, "provider-key", dir)
	cfg.Server.Identifiers = identifierTestConfig()
	p := cfg.Provider["mock"]
	p.Dialect = "openai-responses"
	p.KeyID = "account"
	cfg.Provider["mock"] = p
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("post: %d %s", w.Code, w.Body.String())
		}
		return w
	}
	for i := 1; i <= 2; i++ {
		w := post(`{"model":"default","input":"hello","temperature":0}`)
		id := svc.identifiers.Encode(fmt.Sprintf("resp_upstream_%d", i))
		if !strings.Contains(w.Body.String(), id) || strings.Contains(w.Body.String(), fmt.Sprintf("resp_upstream_%d", i)) || !strings.Contains(w.Body.String(), `"metadata":{"id":"keep"}`) {
			t.Fatalf("unary rewrite: %s", w.Body.String())
		}
		if err := svc.usage.writer.flush(context.Background()); err != nil {
			t.Fatal(err)
		}
		row, err := lookupOwnedResponseAffinity(svc.usage.db, id, "alice", time.Now())
		if err != nil || row == nil || row.UpstreamResponseID != fmt.Sprintf("resp_upstream_%d", i) || !row.UsageSettled {
			t.Fatalf("unary affinity: %+v %v", row, err)
		}
	}
	w := post(`{"model":"default","input":"hello","stream":true}`)
	streamID := svc.identifiers.Encode("resp_upstream_3")
	if !strings.Contains(w.Body.String(), streamID) {
		t.Fatalf("stream ID: %s", w.Body.String())
	}
	if err := svc.usage.writer.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	row, err := lookupOwnedResponseAffinity(svc.usage.db, streamID, "alice", time.Now())
	if err != nil || row == nil || row.Status != "in_progress" {
		t.Fatalf("stream affinity: %+v %v", row, err)
	}
	post(`{"model":"default","input":"hello","store":false}`)
	if err := svc.usage.writer.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	row, err = lookupOwnedResponseAffinity(svc.usage.db, svc.identifiers.Encode("resp_upstream_4"), "alice", time.Now())
	if err != nil || row != nil {
		t.Fatalf("store=false recorded: %+v %v", row, err)
	}
}
