// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type runtimeJWKSDeadlineTransport func(*http.Request) (*http.Response, error)

func (f runtimeJWKSDeadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Observe the real fetchJSON request deadline without wall-clock sleeps or
// network latency. The constructor/client timeout assertion alone missed this.
func TestRuntimeMachineJWKSFetchEffectiveDeadline(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		configured, parent, want time.Duration
	}{{"longer than old cap", 10 * time.Second, 0, 10 * time.Second}, {"short configured", 2 * time.Second, 0, 2 * time.Second}, {"earlier parent", 10 * time.Second, time.Second, time.Second}} {
		t.Run(tc.name, func(t *testing.T) {
			var remaining time.Duration
			transport := runtimeJWKSDeadlineTransport(func(r *http.Request) (*http.Response, error) {
				deadline, ok := r.Context().Deadline()
				if !ok {
					t.Error("fetch request has no deadline")
				}
				remaining = time.Until(deadline)
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
			})
			auth := &adminMachineJWTAuthenticator{cfg: adminMachineJWTConfig{Issuer: "https://issuer.example", JWKSRequestTimeout: tc.configured}, client: &http.Client{Transport: transport}}
			ctx := context.Background()
			if tc.parent != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.parent)
				defer cancel()
			}
			var body map[string]any
			if err := auth.fetchJSON(ctx, "https://issuer.example/keys", &body); err != nil {
				t.Fatal(err)
			}
			if remaining < tc.want-500*time.Millisecond || remaining > tc.want+100*time.Millisecond {
				t.Fatalf("effective fetch deadline=%v want approximately%v", remaining, tc.want)
			}
		})
	}
}
