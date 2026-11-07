// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMountAdminV1WiresEveryOperation proves the production wiring mounts all
// registered operations with the seams the listener supplies, and refuses to mount
// without them.
func TestMountAdminV1WiresEveryOperation(t *testing.T) {
	service := &Service{cfg: &Config{}}
	mux := http.NewServeMux()
	authenticate := &fakeAuthenticator{principal: Principal{Subject: "adminkey:audit", Kind: "adminkey"}}
	authorize := func(context.Context, Principal, string, string, string) error { return nil }
	if err := service.MountAdminV1(mux, authenticate, authorize); err != nil {
		t.Fatalf("mount: %v", err)
	}
	registry, err := AdminV1DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range registry.Operations() {
		probe := op.Path
		probe = strings.ReplaceAll(probe, "{group}", "g")
		probe = strings.ReplaceAll(probe, "{set_id}", "set-1")
		probe = strings.ReplaceAll(probe, "{request_id}", "req-1")
		probe = strings.ReplaceAll(probe, "{id}", "caller-1")
		probe = strings.ReplaceAll(probe, "{target_id...}", "prov/model")
		handler, pattern := mux.Handler(httptest.NewRequest(op.Method, probe, nil))
		if handler == nil || pattern != op.Pattern() {
			t.Fatalf("operation %s is not mounted (pattern %q)", op.ID, pattern)
		}
	}

	// Without the seams the mount must refuse rather than expose an ungoverned surface.
	if err := (&Service{cfg: &Config{}}).MountAdminV1(http.NewServeMux(), nil, authorize); err == nil {
		t.Fatal("mount accepted a nil authenticator")
	}
	if err := (&Service{cfg: &Config{}}).MountAdminV1(http.NewServeMux(), authenticate, nil); err == nil {
		t.Fatal("mount accepted a nil authorizer")
	}
}
