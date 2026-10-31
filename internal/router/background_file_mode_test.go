// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBackgroundResponsesFileModeFailsClosed(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer up.Close()
	cfg := testConfig(t, up.URL, "provider-key", t.TempDir())
	p := cfg.Provider["mock"]
	p.Dialect = "openai-responses"
	p.KeyID = "account"
	cfg.Provider["mock"] = p
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"default","input":"test","background":true,"store":true}`))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if w.Code != 501 || calls.Load() != 0 {
		t.Fatalf("file mode: %d %s, upstream calls %d", w.Code, w.Body.String(), calls.Load())
	}
}
