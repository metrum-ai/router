// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package tlsacme

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestARICertIDMatchesRFC9773Example(t *testing.T) {
	leaf := &x509.Certificate{
		AuthorityKeyId: rfcAKI,
		SerialNumber:   new(big.Int).SetBytes([]byte{0x87, 0x65, 0x43, 0x21}),
	}
	id, err := ariCertID(leaf)
	if err != nil {
		t.Fatalf("cert id: %v", err)
	}
	// The serial keeps its DER sign byte (00) because the high bit is set.
	if want := testARILeafID; id != want {
		t.Fatalf("cert id = %q, want %q", id, want)
	}
}

func TestARICertIDRequiresAKI(t *testing.T) {
	if _, err := ariCertID(&x509.Certificate{SerialNumber: big.NewInt(1)}); err == nil {
		t.Fatal("expected an error without an authority key identifier")
	}
}

func TestARIWindow(t *testing.T) {
	start := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	var requestedPath string
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/directory":
			_ = json.NewEncoder(w).Encode(map[string]string{"renewalInfo": baseURL + "/renewalInfo"})
		case "/renewalInfo/" + testARILeafID:
			requestedPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(map[string]any{
				"suggestedWindow": map[string]string{
					"start": start.Format(time.RFC3339),
					"end":   end.Format(time.RFC3339),
				},
			})
		default:
			t.Errorf("unexpected ARI path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	baseURL = server.URL

	client := &ariClient{httpClient: server.Client()}
	leaf := testARILeaf(t)
	gotStart, gotEnd, err := client.window(context.Background(), server.URL+"/directory", leaf)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if !gotStart.Equal(start) || !gotEnd.Equal(end) {
		t.Fatalf("window = %v..%v, want %v..%v", gotStart, gotEnd, start, end)
	}
	if requestedPath == "" {
		t.Fatal("the ARI endpoint was not called")
	}
}

// rfcAKI is the authority key identifier of the RFC 9773 example certificate.
var rfcAKI = []byte{0x69, 0x88, 0x5b, 0x6b, 0x87, 0x46, 0x40, 0x41, 0xe1, 0xb3, 0x7b, 0x84, 0x7b, 0xa0, 0xae, 0x2c, 0xde, 0x01, 0xc8, 0xd4}

const testARILeafID = "aYhba4dGQEHhs3uEe6CuLN4ByNQ.AIdlQyE"

func testARILeaf(t *testing.T) *x509.Certificate {
	t.Helper()
	return &x509.Certificate{
		AuthorityKeyId: rfcAKI,
		SerialNumber:   new(big.Int).SetBytes([]byte{0x87, 0x65, 0x43, 0x21}),
		NotAfter:       time.Now().Add(30 * 24 * time.Hour),
	}
}

func TestARIUnsupportedDirectory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{})
	}))
	defer server.Close()
	client := &ariClient{httpClient: server.Client()}
	_, _, err := client.window(context.Background(), server.URL, testARILeaf(t))
	if err == nil {
		t.Fatal("expected an error")
	}
	if err != errARIUnsupported {
		t.Fatalf("error = %v, want errARIUnsupported", err)
	}
}

func TestARIMalformedWindow(t *testing.T) {
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/directory" {
			_ = json.NewEncoder(w).Encode(map[string]string{"renewalInfo": baseURL + "/renewalInfo"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"suggestedWindow": map[string]string{
			"start": "2030-01-02T00:00:00Z",
			"end":   "2030-01-01T00:00:00Z",
		}})
	}))
	defer server.Close()
	baseURL = server.URL
	client := &ariClient{httpClient: server.Client()}
	if _, _, err := client.window(context.Background(), server.URL+"/directory", testARILeaf(t)); err == nil {
		t.Fatal("expected an error for a reversed window")
	}
}

func TestARIWindowClampsToCertificateExpiry(t *testing.T) {
	leaf := testARILeaf(t)
	start := leaf.NotAfter.Add(-time.Hour)
	end := leaf.NotAfter.Add(48 * time.Hour)
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/directory" {
			_ = json.NewEncoder(w).Encode(map[string]string{"renewalInfo": baseURL + "/renewalInfo"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"suggestedWindow": map[string]string{
			"start": start.Format(time.RFC3339),
			"end":   end.Format(time.RFC3339),
		}})
	}))
	defer server.Close()
	baseURL = server.URL
	client := &ariClient{httpClient: server.Client()}
	_, gotEnd, err := client.window(context.Background(), server.URL+"/directory", leaf)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if !gotEnd.Equal(leaf.NotAfter) {
		t.Fatalf("end = %v, want the certificate expiry %v", gotEnd, leaf.NotAfter)
	}
}
