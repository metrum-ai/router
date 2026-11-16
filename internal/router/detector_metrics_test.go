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

	detect "github.com/metrum-ai/router/internal/security/detect"
	"github.com/metrum-ai/router/pkg/detector"
)

type metricsBlockingDetector struct {
	started chan struct{}
	gate    chan struct{}
}

func (d metricsBlockingDetector) Detect(context.Context, detector.Event) ([]detector.Signal, error) {
	select {
	case d.started <- struct{}{}:
	default:
	}
	<-d.gate
	return nil, nil
}

func TestMetricsExportsDetectorDropCounter(t *testing.T) {
	started, gate := make(chan struct{}, 1), make(chan struct{})
	service := &Service{metrics: newMetricsStore(), detectorDispatcher: detect.NewDispatcher(1, 1, nil)}
	defer func() { close(gate); service.detectorDispatcher.Close() }()
	implementation := metricsBlockingDetector{started: started, gate: gate}
	event := detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "request"}
	policy := detect.Policy{Mode: "report", Timeout: time.Second, OnError: "fallback", SampleRate: 1}
	service.detectorDispatcher.Submit("test", "caller", implementation, event, policy)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("detector worker did not start")
	}
	service.detectorDispatcher.Submit("test", "caller", implementation, event, policy)
	service.detectorDispatcher.Submit("test", "caller", implementation, event, policy)
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	service.writeMetricsPayload(response, request, nil)
	if !strings.Contains(response.Body.String(), "metrum_ai_router_detector_dropped_total 1") {
		t.Fatalf("detector drop counter missing from metrics:\n%s", response.Body.String())
	}
}

func TestMetricsExportsInvalidDetectorEventDropCounter(t *testing.T) {
	service := &Service{metrics: newMetricsStore()}
	config := []DetectorConfig{{ID: "invalid-event", URL: "http://127.0.0.1:9000/v1/detect", AllowHosts: []string{"127.0.0.1"}, AllowHTTP: true, Mode: "report", TimeoutMS: 500, OnError: "fallback", SampleRate: 1}}
	if err := service.replaceDetectorConfig(config); err != nil {
		t.Fatal(err)
	}
	defer service.detectorDispatcher.Close()
	service.submitDetectorEvent(detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "model_denied", DeniedGroup: "org/model"}, nil, false)
	if service.detectorInvalidDropped.Load() != 1 {
		t.Fatalf("invalid event drop count=%d, want 1", service.detectorInvalidDropped.Load())
	}
	response := httptest.NewRecorder()
	service.writeMetricsPayload(response, httptest.NewRequest(http.MethodGet, "/metrics", nil), nil)
	if !strings.Contains(response.Body.String(), "metrum_ai_router_detector_invalid_dropped_total 1") {
		t.Fatalf("invalid detector event drop metric missing:\n%s", response.Body.String())
	}
}
