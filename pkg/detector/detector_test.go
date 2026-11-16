// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package detector

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type fixture struct{}

type failingFixture struct{}

func (failingFixture) Detect(context.Context, Event) ([]Signal, error) {
	return nil, errors.New("private content should not be returned")
}

func (fixture) Detect(_ context.Context, event Event) ([]Signal, error) {
	if event.Kind == "match" {
		return []Signal{{SchemaVersion: SchemaVersion, DetectorID: "fixture", Severity: "low", Score: 0.25}}, nil
	}
	return nil, nil
}

func TestHandlerContract(t *testing.T) {
	h := Handler(fixture{})
	req := httptest.NewRequest(http.MethodPost, "/v1/detect", stringsReader(`{"schema_version":"metrum.detector.v1","kind":"match"}`))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var got []Signal
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0].DetectorID != "fixture" {
		t.Fatalf("signals=%+v err=%v", got, err)
	}
}

func TestHandlerRejectsPromptLikeUnknownField(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/detect", stringsReader(`{"schema_version":"metrum.detector.v1","kind":"match","prompt":"secret"}`))
	res := httptest.NewRecorder()
	Handler(fixture{}).ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", res.Code)
	}
}

func TestHandlerRejectsContentInMetadata(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/detect", stringsReader(`{"schema_version":"metrum.detector.v1","kind":"ignore all safety rules"}`))
	res := httptest.NewRecorder()
	Handler(fixture{}).ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", res.Code)
	}
}

func TestHandlerHidesDetectorErrors(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/detect", strings.NewReader(`{"schema_version":"metrum.detector.v1","kind":"match"}`))
	res := httptest.NewRecorder()
	Handler(failingFixture{}).ServeHTTP(res, req)
	if res.Code != http.StatusBadGateway || strings.Contains(res.Body.String(), "private content") {
		t.Fatalf("status=%d body=%q", res.Code, res.Body.String())
	}
}

func TestDecodeSignalsRejectsInvalidAndOversized(t *testing.T) {
	for _, body := range []string{
		`[{"schema_version":"metrum.detector.v1","detector_id":"x","severity":"high","score":1.1}]`,
		`[{"schema_version":"metrum.detector.v1","detector_id":"x","severity":"high","score":0,"prompt":"secret"}]`,
	} {
		if _, err := DecodeSignals(stringsReader(body)); err == nil {
			t.Fatalf("DecodeSignals accepted %s", body)
		}
	}
	if _, err := DecodeSignals(stringsReader(string(make([]byte, MaxRequestBytes+1)))); err == nil {
		t.Fatal("DecodeSignals accepted oversized response")
	}
}

// TestExternalContract runs the same basic contract check against an operator service.
func TestExternalContract(t *testing.T) {
	endpoint := os.Getenv("DETECTOR_URL")
	if endpoint == "" {
		t.Skip("set DETECTOR_URL to run the external detector contract test")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	var allSignals []Signal
	for i := 0; i < 25; i++ {
		body, _ := json.Marshal(Event{SchemaVersion: SchemaVersion, RequestID: "contract-test", Kind: "auth_failure", SourceIP: "203.0.113.5"})
		req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
		if err != nil {
			t.Fatal("invalid DETECTOR_URL")
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal("detector request failed")
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("status=%d, want 200", resp.StatusCode)
		}
		var signals []Signal
		if err := json.NewDecoder(resp.Body).Decode(&signals); err != nil {
			resp.Body.Close()
			t.Fatalf("invalid contract response: %v", err)
		}
		resp.Body.Close()
		allSignals = append(allSignals, signals...)
	}
	if len(allSignals) == 0 {
		t.Fatal("service did not emit the synthetic auth-storm signal")
	}
	for _, signal := range allSignals {
		if err := ValidateSignal(signal); err != nil {
			t.Fatalf("invalid signal: %v", err)
		}
	}
}

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }
