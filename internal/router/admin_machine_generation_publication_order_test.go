// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeMachinePublicationHooksOrderedWithoutRequestGate(t *testing.T) {
	generations := make([]*adminMachineGeneration, 3)
	tokens := make([]string, 3)
	for i := range generations {
		generations[i], tokens[i] = generationKeyFixture(t, "admin:callers:list")
		ca, _, _ := certificateBindingCA(t)
		path := filepath.Join(t.TempDir(), "ca.pem")
		trustStoreWriteCA(t, path, ca)
		trust, err := NewAdminMTLSTrustStore(AdminMTLSTrustConfig{Issuer: "authority", CAFiles: []string{path}})
		if err != nil {
			t.Fatal(err)
		}
		generations[i].trust = trust
	}
	svc := &Service{}
	if err := svc.publishAdminMachineGeneration(generations[0]); err != nil {
		t.Fatal(err)
	}
	source := svc.machineClientCASource()
	entered, unblock := make(chan struct{}), make(chan struct{})
	var release sync.Once
	finish := func() { release.Do(func() { close(unblock) }) }
	defer finish()
	var last atomic.Pointer[x509.CertPool]
	source.OnReload(func(pool *x509.CertPool) {
		if pool == generations[1].trust.Snapshot() {
			close(entered)
			<-unblock
		}
		last.Store(pool)
	})
	doneB, doneC := make(chan error, 1), make(chan error, 1)
	go func() { doneB <- svc.publishAdminMachineGeneration(generations[1]) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("publication hook never entered")
	}
	go func() { doneC <- svc.publishAdminMachineGeneration(generations[2]) }()
	served := make(chan int, 1)
	go func() {
		request := httptest.NewRequest(http.MethodGet, "/admin/v1/example", nil)
		request.Header.Set("Authorization", "Bearer "+tokens[1])
		response := httptest.NewRecorder()
		svc.adminMachineGenerationMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok || svc.Authorize(r.Context(), p, "default", "admin:callers", "list") != nil {
				w.WriteHeader(403)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(response, request)
		served <- response.Code
	}()
	select {
	case code := <-served:
		if code != 204 {
			t.Fatalf("captured current generation request=%d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request waited for a producer hook/publication lock")
	}
	finish()
	for _, done := range []chan error{doneB, doneC} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("publisher did not finish")
		}
	}
	if last.Load() != generations[2].trust.Snapshot() || source.Snapshot() != last.Load() {
		t.Fatal("old notification overwrote newer handshake union")
	}
}
