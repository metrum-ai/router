// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

// Concurrent immutable publications may produce a valid identity from one
// complete observed publication or a terminal retry/denial, never a mixed map.
// This is not joint authn/authorization/dispatch generation fencing.
func TestRuntimeMTLSTrustConcurrentPublication(t *testing.T) {
	caA, keyA, _ := certificateBindingCA(t)
	caB, _, _ := certificateBindingCA(t)
	leaf := runtimeMTLSLeaf(t, caA, keyA)
	paths := []string{filepath.Join(t.TempDir(), "a.pem"), filepath.Join(t.TempDir(), "b.pem")}
	trustStoreWriteCA(t, paths[0], caA)
	trustStoreWriteCA(t, paths[1], caB)
	cfgA := AdminMTLSTrustConfig{Issuer: "authority-a", CAFiles: []string{paths[0]}, Subjects: []AdminMTLSSubjectConfig{{SAN: "dns:robot.example", Subject: "subject-a"}}}
	cfgB := AdminMTLSTrustConfig{Issuer: "authority-b", CAFiles: []string{paths[1]}, Subjects: []AdminMTLSSubjectConfig{{SAN: "dns:robot.example", Subject: "subject-b"}}}
	store, err := NewAdminMTLSTrustStore(cfgA)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := newAdminMTLSTrustAuthenticator(store)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 40; i++ {
			cfg := cfgA
			if i%2 == 0 {
				cfg = cfgB
			}
			if err := store.ReloadConfig(cfg); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 80; i++ {
				r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
				r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, caA}}}
				p, err := identity.Authenticate(r.Context(), r)
				if err == nil {
					if p.Subject != "subject-a" || p.Issuer != "authority-a" || p.Kind != "mtls" {
						t.Error("mixed trust/map publication produced identity")
					}
				} else if !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
					t.Error("unexpected publication result")
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if err := store.ReloadConfig(cfgB); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, caA}}}
	if p, err := identity.Authenticate(r.Context(), r); !errors.Is(err, ErrInvalidCredential) || p != (Principal{}) {
		t.Fatal("removed authority accepted old native connection")
	}
}
