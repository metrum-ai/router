// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package smartrouterctl

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPolicyClientNeverReplaysLostMutationResponse(t *testing.T) {
	t.Setenv("ROUTER_ADMIN_KEY", "test-only-no-replay-key")
	t.Setenv("ROUTER_ADMIN_KEY_FILE", "")
	var mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Write([]byte(`{}`))
			return
		}
		mutations.Add(1)
		if r.Header.Get("Idempotency-Key") != "explicit-replay-key" {
			t.Error("server-visible replay key lost")
		}
		io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer server.Close()
	client, err := NewPolicyAPIClient(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	// Populate a reusable connection before losing a mutation response. Default
	// Go transport treats Idempotency-Key as replayable and can transmit twice.
	if _, err := client.Call("get", "one", nil, PolicyCallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call("activate", "one", nil, PolicyCallOptions{IfMatch: `"rev-1"`, IdempotencyKey: "explicit-replay-key"}); err == nil {
		t.Fatal("lost response reported success")
	}
	if got := mutations.Load(); got != 1 {
		t.Fatalf("mutation automatically transmitted %d times, want1", got)
	}
}
