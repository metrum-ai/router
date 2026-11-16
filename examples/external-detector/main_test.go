// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/metrum-ai/router/pkg/detector"
)

func TestExampleServiceContract(t *testing.T) {
	server := httptest.NewServer(newHandler())
	defer server.Close()
	post := func(body string) *http.Response {
		t.Helper()
		resp, err := http.Post(server.URL+"/v1/detect", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := post(`{"schema_version":"metrum.detector.v1","kind":"benign"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no-match status=%d", resp.StatusCode)
	}
	signals, err := detector.DecodeSignals(resp.Body)
	resp.Body.Close()
	if err != nil || len(signals) != 0 {
		t.Fatalf("no-match signals=%v err=%v", signals, err)
	}
	segmentMarker := "private tool-result marker"
	body, _ := json.Marshal(detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "benign", Segments: []detector.Segment{{Kind: "tool_result", Index: 0, Text: segmentMarker}}})
	resp = post(string(body))
	segmentResponse := new(bytes.Buffer)
	_, _ = segmentResponse.ReadFrom(resp.Body)
	resp.Body.Close()
	if strings.Contains(segmentResponse.String(), segmentMarker) {
		t.Fatal("detector echoed segment text in its result")
	}
	resp = post(`{"schema_version":"metrum.detector.v1","kind":"benign","prompt":"private"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad event status=%d", resp.StatusCode)
	}
	var last []detector.Signal
	for i := 0; i < authFailureLimit; i++ {
		body, _ := json.Marshal(detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "auth_failure", KeyID: strings.Repeat("a", 32), RequestID: fmt.Sprintf("request-%d", i)})
		resp = post(string(body))
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("trigger status=%d body=%s", resp.StatusCode, b.String())
		}
		last, err = detector.DecodeSignals(strings.NewReader(b.String()))
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(last) == 0 {
		t.Fatal("auth failure rule did not signal")
	}
	for _, signal := range last {
		if err := detector.ValidateSignal(signal); err != nil {
			t.Fatal(err)
		}
	}
}
