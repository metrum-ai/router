// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package smartrouterctl

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCurrentConfigETag(t *testing.T) {
	for _, test := range []struct {
		name, etag string
		status     int
		wantError  bool
	}{
		{"current", `"rev-12"`, 200, false},
		{"missing", "", 200, true},
		{"weak", `W/"rev-12"`, 200, true},
		{"multiple", `"rev-12", "rev-13"`, 200, true},
		{"denied", "", 403, true},
		{"unavailable", "", 503, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/admin/v1/config/status" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("unexpected discovery request")
				}
				w.Header().Set("ETag", test.etag)
				w.WriteHeader(test.status)
				w.Write([]byte("{}"))
			}))
			defer server.Close()
			client := &AdminAPIClient{BaseURL: server.URL, Token: "fixture", HTTP: server.Client()}
			etag, err := client.CurrentConfigETag()
			if (err != nil) != test.wantError || (!test.wantError && etag != test.etag) {
				t.Fatalf("ETag=%q err=%v", etag, err)
			}
		})
	}
}

func TestCurrentConfigETagTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client := &AdminAPIClient{BaseURL: server.URL, Token: "fixture", HTTP: &http.Client{Timeout: 20 * time.Millisecond}}
	if _, err := client.CurrentConfigETag(); err == nil {
		t.Fatal("timed-out discovery succeeded")
	}
}
