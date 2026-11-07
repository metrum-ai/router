// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeMachineGenerationRootRemoval(t *testing.T) {
	caA, keyA, _ := certificateBindingCA(t)
	caB, keyB, _ := certificateBindingCA(t)
	leafA, leafB := runtimeMTLSLeaf(t, caA, keyA), runtimeMTLSLeaf(t, caB, keyB)
	build := func(ca *x509.Certificate) *adminMachineGeneration {
		t.Helper()
		path := filepath.Join(t.TempDir(), "ca.pem")
		trustStoreWriteCA(t, path, ca)
		trust, err := NewAdminMTLSTrustStore(AdminMTLSTrustConfig{Issuer: "configured", CAFiles: []string{path}, Subjects: []AdminMTLSSubjectConfig{{SAN: "dns:robot.example", Subject: "robot"}}})
		if err != nil {
			t.Fatal(err)
		}
		auth, err := newAdminMTLSTrustAuthenticator(trust)
		if err != nil {
			t.Fatal(err)
		}
		grants, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true}, nil, nil, AdminAuthConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := grants.enforcer.AddPolicy("robot", "default", "admin:auth", "read"); err != nil {
			t.Fatal(err)
		}
		return &adminMachineGeneration{auth: NewAuthenticatorChain(auth), authorizer: grants, trust: trust}
	}
	svc := &Service{}
	old, newer := build(caA), build(caB)
	if err := svc.publishAdminMachineGeneration(old); err != nil {
		t.Fatal(err)
	}
	// Native old connection state persists; ONLY the captured complete bundle
	// may decide whether its root is trusted for a newly started request.
	handler := svc.adminMachineGenerationMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFrom(r.Context())
		if err := svc.Authorize(svc.PinAuthorization(r.Context()), p, "default", "admin:auth", "read"); err != nil {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(204)
	}))
	request := func(leaf, ca *x509.Certificate) int {
		r := httptest.NewRequest("GET", "https://admin.example/admin/v1/auth", nil)
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca}}}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if status := request(leafA, caA); status != 204 {
		t.Fatalf("initial root control: %d", status)
	}
	if err := svc.publishAdminMachineGeneration(newer); err != nil {
		t.Fatal(err)
	}
	if status := request(leafA, caA); status != 401 {
		t.Fatalf("removed root on old native connection accepted: %d", status)
	}
	if status := request(leafB, caB); status != 204 {
		t.Fatalf("new root generation failed: %d", status)
	}
}

func TestRuntimeMachineGenerationFiniteContext(t *testing.T) {
	grants, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true}, nil, nil, AdminAuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{}
	auth := testMachineAuthenticator(func(context.Context, *http.Request) (Principal, error) {
		return Principal{Subject: "robot", Kind: "mtls"}, nil
	})
	if err := svc.publishAdminMachineGeneration(&adminMachineGeneration{auth: auth, authorizer: grants}); err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	handler := svc.adminMachineGenerationMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline, ok := r.Context().Deadline()
		if !ok || deadline.After(start.Add(60*time.Second+time.Second)) {
			t.Error("machine context missing finite60s bound")
		}
		cancel()
		p, _ := PrincipalFrom(r.Context())
		if err := svc.Authorize(svc.PinAuthorization(r.Context()), p, "default", "admin:auth", "read"); !errors.Is(err, ErrAuthorizationUnavailable) {
			t.Error("canceled request retained authorization authority")
		}
		w.WriteHeader(204)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "https://admin.example/admin/v1/auth", nil).WithContext(parent))
	if w.Code != 204 {
		t.Fatal("context test did not reach handler")
	}
}
