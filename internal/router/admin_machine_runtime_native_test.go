// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"testing"
	"time"
)

// Constructor-installed ordinary mTLS, native client-key TLS, actual listener,
// full CA factory replacement and sole authorization. Not emergency/outbox E2E.
func TestRuntimeMachineFactoryNativeTLSRootReload(t *testing.T) {
	dir := t.TempDir()
	serverCA, caA, caB := newAdminTestCA(t), newAdminTestCA(t), newAdminTestCA(t)
	serverCert, serverKey := serverCA.issue(t, "server", nil, nil, true)
	aCert, aKey := caA.issue(t, "client-a", nil, []string{"spiffe://ops/worker"}, false)
	bCert, bKey := caB.issue(t, "client-b", nil, []string{"spiffe://ops/worker"}, false)
	path := writeTestPEM(t, dir, "clients.pem", caA.certPEM)
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Admin.Enabled = true
	cfg.Admin.Listen = "127.0.0.1:0"
	cfg.Admin.TLS.Mode = adminTLSModeRouter
	cfg.Admin.TLS.CertFile = writeTestPEM(t, dir, "server.pem", serverCert)
	cfg.Admin.TLS.KeyFile = writeTestKey(t, dir, "server.key", serverKey)
	cfg.Admin.TLS.ClientAuth = adminClientAuthOptional
	cfg.Admin.Authentication.MTLS = AdminMTLSTrustConfig{Issuer: "authority", CAFiles: []string{path}, Subjects: []AdminMTLSSubjectConfig{{SAN: "uri:spiffe://ops/worker", Subject: "workload:worker"}}, ReloadInterval: "1h"}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, workload:worker, default, admin:callers, list"}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	var dispatches atomic.Int64
	svc.RegisterAdminRoutes(func(m *AdminMux) {
		m.HandleFunc("GET /admin/v1/native-factory", func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok || svc.Authorize(svc.PinAuthorization(r.Context()), p, "default", "admin:callers", "list") != nil {
				w.WriteHeader(403)
				return
			}
			dispatches.Add(1)
			w.WriteHeader(204)
		})
	})
	plane, err := NewAdminPlane(&cfg.Admin, svc.AdminHandler())
	if err != nil {
		t.Fatal(err)
	}
	plane.SetClientCASource(svc.AdminClientCASource())
	if err := plane.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = plane.Shutdown(ctx)
	}()
	serverRoots := x509.NewCertPool()
	serverRoots.AddCert(serverCA.cert)
	client := func(cert, key []byte) *http.Client {
		pair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			t.Fatal(err)
		}
		transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: serverRoots, Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}}
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Transport: transport, Timeout: 5 * time.Second}
	}
	a, b := client(aCert, aKey), client(bCert, bKey)
	request := func(c *http.Client, want int, reuse bool) {
		t.Helper()
		var reused atomic.Bool
		r, err := http.NewRequest("GET", "https://"+plane.Addr()+"/admin/v1/native-factory", nil)
		if err != nil {
			t.Fatal(err)
		}
		r = r.WithContext(httptrace.WithClientTrace(r.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }}))
		response, err := c.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("native factory status=%d want=%d", response.StatusCode, want)
		}
		if reuse && !reused.Load() {
			t.Fatal("root removal was not tested on retained TCP/TLS connection")
		}
	}
	request(a, 204, false)
	writeTestPEM(t, dir, "clients.pem", caB.certPEM)
	if err := svc.machineClientCASource().Reload(); err != nil {
		t.Fatal(err)
	}
	request(a, 401, true)
	request(b, 204, false)
	// Invalid preparation keeps both authentication and native handshake union.
	current := svc.machineGeneration.Load()
	writeTestPEM(t, dir, "clients.pem", []byte("invalid CA"))
	if err := svc.machineClientCASource().Reload(); err == nil {
		t.Fatal("malformed CA replacement accepted")
	}
	if svc.machineGeneration.Load() != current {
		t.Fatal("failed CA preparation changed machine generation")
	}
	request(b, 204, true)
	a.Transport.(*http.Transport).CloseIdleConnections()
	response, err := a.Get("https://" + plane.Addr() + "/admin/v1/native-factory")
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("fresh native TLS handshake accepted removed client root")
	}
	if dispatches.Load() != 3 {
		t.Fatalf("native factory dispatches=%d want3", dispatches.Load())
	}
}
