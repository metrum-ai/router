// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package detector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type fixture struct{}

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
	client := &http.Client{Timeout: 3 * time.Second}
	badReq, err := http.NewRequest(http.MethodPost, endpoint, stringsReader(`{"schema_version":"metrum.detector.v1","kind":"benign","prompt":"private"}`))
	if err != nil {
		t.Fatal("invalid DETECTOR_URL")
	}
	badReq.Header.Set("Content-Type", "application/json")
	badResp, err := client.Do(badReq)
	if err != nil {
		t.Fatalf("detector bad-event request failed: %v", err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad-event status=%d, want 400", badResp.StatusCode)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, stringsReader(`{"schema_version":"metrum.detector.v1","request_id":"contract-test","kind":"benign"}`))
	if err != nil {
		t.Fatal("invalid DETECTOR_URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("detector request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	signals, err := DecodeSignals(resp.Body)
	if err != nil || len(signals) != 0 {
		t.Fatalf("no-match response=%+v err=%v", signals, err)
	}
}

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }
