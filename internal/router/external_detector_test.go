// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/metrum-ai/router/pkg/detector"
)

func TestExternalDetectorContract(t *testing.T) {
	server := httptest.NewServer(detector.Handler(contractDetector{}))
	defer server.Close()
	client, err := newExternalDetector(server.URL+"/v1/detect", []string{"127.0.0.1"}, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	signals, err := client.Detect(context.Background(), detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "match"})
	if err != nil || len(signals) != 1 || signals[0].DetectorID != "contract" {
		t.Fatalf("signals=%+v err=%v", signals, err)
	}
}

func TestExternalDetectorRejectsUnlistedHost(t *testing.T) {
	_, err := newExternalDetector("https://detector.example/v1/detect", nil, false, time.Second)
	if err == nil {
		t.Fatal("unlisted detector host accepted")
	}
}

func TestExternalDetectorHonorsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()
	client, err := newExternalDetector(server.URL+"/v1/detect", []string{"127.0.0.1"}, false, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Detect(context.Background(), detector.Event{SchemaVersion: detector.SchemaVersion}); err == nil {
		t.Fatal("timed out request succeeded")
	}
}

func TestExternalDetectorRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]` + strings.Repeat(" ", detector.MaxRequestBytes+1)))
	}))
	defer server.Close()
	client, err := newExternalDetector(server.URL+"/v1/detect", []string{"127.0.0.1"}, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Detect(context.Background(), detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "request"}); err == nil {
		t.Fatal("oversized response succeeded")
	}
}

type contractDetector struct{}

func (contractDetector) Detect(_ context.Context, event detector.Event) ([]detector.Signal, error) {
	if event.Kind != "match" {
		return nil, nil
	}
	return []detector.Signal{{SchemaVersion: detector.SchemaVersion, DetectorID: "contract", Severity: "low", Score: 0.5}}, nil
}
