// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
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

func TestStoredResponseOwnerOperations(t *testing.T) {
	var created atomic.Int64
	var forwarded atomic.Int64
	previous := make(chan string, 4)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		if r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Error("missing upstream auth")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" && (r.URL.Path == "/v1/responses" || r.URL.Path == "/v1/responses/input_tokens" || r.URL.Path == "/v1/responses/compact") {
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			if id, ok := request["previous_response_id"].(string); ok {
				if r.URL.Path == "/v1/responses" && request["store"] != false {
					t.Error("store=false not forwarded")
				}
				previous <- id
			} else if r.URL.Path == "/v1/responses" && request["store"] != true {
				t.Error("store=true not forwarded")
			}
			if r.URL.Path == "/v1/responses/input_tokens" {
				fmt.Fprint(w, `{"object":"response.input_tokens","input_tokens":4}`)
				return
			}
			id := fmt.Sprintf("resp_%d", created.Add(1))
			fmt.Fprintf(w, `{"id":%q,"object":"response","status":"completed","model":"mock-model","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, id)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/responses/"), "/")
		id := parts[0]
		if len(parts) > 1 && parts[1] == "input_items" {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"item_1","type":"message"}],"has_more":false}`)
			return
		}
		if len(parts) > 1 && parts[1] == "cancel" {
			fmt.Fprintf(w, `{"id":%q,"object":"response","status":"cancelled","output":[]}`, id)
			return
		}
		if r.Method == "DELETE" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Query().Get("stream") == "true" {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":%q,\"status\":\"in_progress\",\"output\":[]}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"status\":\"completed\",\"output\":[]}}\n\n", id, id)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"object":"response","status":"completed","model":"mock-model","output":[{"id":"item_1","type":"message","content":[]}]}`, id)
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
	defer svc.Close()
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w
	}
	create := func() string {
		t.Helper()
		w := request("POST", "/v1/responses", testToken, `{"model":"default","input":"hi","store":true}`)
		if w.Code != 200 {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		var object map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &object); err != nil {
			t.Fatal(err)
		}
		return stringValue(object["id"])
	}
	id := create()
	before := forwarded.Load()
	other := request("GET", "/v1/responses/"+id, "bob-token", "")
	missing := request("GET", "/v1/responses/missing", testToken, "")
	if other.Code != 404 || missing.Code != 404 || other.Body.String() != missing.Body.String() || forwarded.Load() != before {
		t.Fatalf("owner isolation: %d %s / %d %s", other.Code, other.Body.String(), missing.Code, missing.Body.String())
	}
	for _, suffix := range []string{"", "?stream=true", "/input_items?limit=5&order=asc"} {
		w := request("GET", "/v1/responses/"+id+suffix, testToken, "")
		if w.Code != 200 || (suffix != "/input_items?limit=5&order=asc" && !strings.Contains(w.Body.String(), id)) || strings.Contains(w.Body.String(), "resp_1") {
			t.Fatalf("get %q: %d %s", suffix, w.Code, w.Body.String())
		}
	}
	wSDK := request("GET", "/v1/responses/"+id, testToken, `{"stream":true}`)
	if wSDK.Code != 200 || !strings.HasPrefix(wSDK.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(wSDK.Body.String(), id) {
		t.Fatalf("SDK GET stream body: %d %s", wSDK.Code, wSDK.Body.String())
	}
	oldIDs := svc.identifiers
	rotatedCfg := identifierTestConfig()
	rotatedCfg.Transform.Current.KeyID = "idk-test-B"
	rotatedCfg.Transform.Current.Key = strings.Repeat("cd", 64)
	rotated, err := newIdentifierTransform(rotatedCfg)
	if err != nil {
		t.Fatal(err)
	}
	svc.identifiers = rotated
	w := request("GET", "/v1/responses/"+id+"?stream=true", testToken, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("stream changed stored ID after rotation: %d %s", w.Code, w.Body.String())
	}
	svc.identifiers = oldIDs
	p = svc.cfg.Provider["mock"]
	p.KeyID = "changed-account"
	svc.cfg.Provider["mock"] = p
	w = request("GET", "/v1/responses/"+id, testToken, "")
	if w.Code != 409 {
		t.Fatalf("removed target: %d %s", w.Code, w.Body.String())
	}
	p.KeyID = "account"
	svc.cfg.Provider["mock"] = p
	otherUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("continuation rerouted to another target")
		http.Error(w, "wrong target", 500)
	}))
	defer otherUp.Close()
	svc.cfg.Provider["wrong"] = ProviderConfig{BaseURL: otherUp.URL + "/v1", Dialect: "openai-responses", APIKey: "wrong-key", KeyID: "wrong-account"}
	group := svc.cfg.Models["default"]
	group.Targets = append([]Target{{Provider: "wrong", Model: "other-model"}}, group.Targets...)
	group.Strategy = "static"
	svc.cfg.Models["default"] = group
	before = forwarded.Load()
	w = request("POST", "/v1/responses", "bob-token", fmt.Sprintf(`{"model":"default","input":"next","previous_response_id":%q}`, id))
	if w.Code != 404 || forwarded.Load() != before {
		t.Fatalf("cross-owner continuation: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/v1/responses", "/v1/responses/input_tokens", "/v1/responses/compact"} {
		w = request("POST", path, testToken, fmt.Sprintf(`{"model":"default","input":"next","store":false,"previous_response_id":%q}`, id))
		if w.Code != 200 {
			t.Fatalf("pinned %s: %d %s", path, w.Code, w.Body.String())
		}
		if got := <-previous; got != "resp_1" {
			t.Fatalf("incorrect upstream ID: %q", got)
		}
	}
	// A child router's ID may itself start with mr_. The parent must forward
	// that exact opaque ID rather than trying to decode it a second time.
	childID := "mr_child_opaque"
	visibleID := svc.identifiers.Encode(childID)
	key, err := responseAffinityTargetKey("mock", p, group.Targets[1])
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := insertResponseAffinity(svc.usage.db, responseAffinityRecord{ResponseID: visibleID, UpstreamResponseID: childID, TargetKey: key, ModelGroup: "default", CallerID: "alice", Status: "completed", CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	w = request("POST", "/v1/responses", testToken, fmt.Sprintf(`{"model":"default","input":"next","store":false,"previous_response_id":%q}`, visibleID))
	if w.Code != 200 {
		t.Fatalf("hierarchy continuation: %d %s", w.Code, w.Body.String())
	}
	if got := <-previous; got != childID {
		t.Fatalf("child ID decoded twice: %q", got)
	}
	group.Targets = group.Targets[1:]
	svc.cfg.Models["default"] = group
	w = request("DELETE", "/v1/responses/"+id, testToken, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	w = request("GET", "/v1/responses/"+id, testToken, "")
	if w.Code != 404 {
		t.Fatalf("deleted response visible: %d %s", w.Code, w.Body.String())
	}
	id2 := create()
	w = request("POST", "/v1/responses/"+id2+"/cancel", testToken, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), id2) {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
}

func TestStoredResponseFileModeAndPaths(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for _, tc := range []struct{ method, path string }{{"GET", "/v1/responses/resp_1"}, {"DELETE", "/v1/responses/resp_1"}, {"GET", "/v1/responses/resp_1/input_items"}, {"POST", "/v1/responses/resp_1/cancel"}} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		if w.Code != 501 {
			t.Fatalf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
	for _, bad := range []string{"", "..", "a/b", "x%2fy", "x?y", strings.Repeat("a", 2049)} {
		if safeResponsePathID(bad) {
			t.Fatalf("unsafe ID accepted: %q", bad)
		}
	}
}
