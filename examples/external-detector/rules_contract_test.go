// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/metrum-ai/router/pkg/detector"
)

// TestExternalRuleContract exercises all four shipped rules through the
// operator-facing HTTP endpoint and the contract's strict response decoder.
func TestExternalRuleContract(t *testing.T) {
	endpoint := os.Getenv("DETECTOR_URL")
	if endpoint == "" {
		t.Skip("set DETECTOR_URL to run the external example rule contract test")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	now := time.Now().UTC()
	keyID := fmt.Sprintf("%032x", now.UnixNano())
	ipValue := uint32(now.Unix()) ^ uint32(now.Nanosecond())
	sourceIP := fmt.Sprintf("%d.%d.%d.%d", byte(ipValue>>24), byte(ipValue>>16), byte(ipValue>>8), byte(ipValue))
	var allSignals []detector.Signal
	send := func(event detector.Event) []detector.Signal {
		t.Helper()
		body, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("build detector request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST detector event: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			t.Fatalf("detector status=%d body=%s", resp.StatusCode, raw)
		}
		signals, err := detector.DecodeSignals(resp.Body)
		if err != nil {
			t.Fatalf("decode detector signals: %v", err)
		}
		allSignals = append(allSignals, signals...)
		return signals
	}
	event := func(kind, id string, at time.Time) detector.Event {
		return detector.Event{
			SchemaVersion: detector.SchemaVersion,
			RequestID:     id,
			CallerID:      "rogue-agent-e2e",
			Kind:          kind,
			ObservedAt:    at.Format(time.RFC3339Nano),
			SourceIP:      sourceIP,
			KeyID:         keyID,
			Segments:      []detector.Segment{{Kind: "tool_result", Index: 0, Text: "secret prompt provider-token"}},
		}
	}

	benign := event("request", "rogue-e2e-benign", now)
	benign.Segments = []detector.Segment{{Kind: "tool_result", Index: 0, Text: "secret prompt provider-token"}}
	if got := send(benign); len(got) != 0 {
		t.Fatalf("benign event produced signals: %+v", got)
	}

	var observed []detector.Signal
	for i := 1; i <= authFailureLimit; i++ {
		observed = append(observed, send(event("auth_failure", fmt.Sprintf("rogue-e2e-auth-%02d", i), now))...)
	}
	assertExternalRule(t, observed, "auth_fail_storm", keyID)
	var keySignal, ipSignal bool
	for _, signal := range observed {
		if signal.DetectorID == "auth_fail_storm" {
			keySignal = keySignal || signal.Key == keyID
			ipSignal = ipSignal || signal.Key == ""
		}
	}
	if !keySignal || !ipSignal {
		t.Fatalf("auth storm must produce key and IP signals (IP key empty): %+v", observed)
	}

	observed = nil
	for i := 1; i <= tinyRequestLimit; i++ {
		req := event("request", fmt.Sprintf("rogue-e2e-tiny-%02d", i), now)
		req.TinyRequest = true
		observed = append(observed, send(req)...)
	}
	assertExternalRule(t, observed, "validation_sweep", keyID)

	observed = nil
	for i := 1; i <= deniedGroupLimit; i++ {
		denied := event("model_denied", fmt.Sprintf("rogue-e2e-denied-%02d", i), now)
		denied.DeniedGroup = fmt.Sprintf("denied-group-%02d", i)
		observed = append(observed, send(denied)...)
	}
	assertExternalRule(t, observed, "model_enumeration", keyID)

	observed = nil
	for i := 6; i >= 2; i-- {
		baseline := event("usage", fmt.Sprintf("rogue-e2e-spend-base-%02d", i), now.Add(-time.Duration(i)*time.Minute))
		baseline.Tokens = 100
		observed = append(observed, send(baseline)...)
	}
	spike := event("usage", "rogue-e2e-spend-spike", now)
	spike.Tokens = 600
	observed = append(observed, send(spike)...)
	assertExternalRule(t, observed, "spend_spike", keyID)

	encoded, err := json.Marshal(allSignals)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret prompt", "provider-token"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("signal response exposed event content %q: %s", forbidden, encoded)
		}
	}
}

func assertExternalRule(t *testing.T, signals []detector.Signal, want string, rawKeyID string) {
	t.Helper()
	for _, signal := range signals {
		if signal.DetectorID == want {
			if signal.Severity == "" || signal.Key != rawKeyID || len(signal.EvidenceRequestIDs) == 0 {
				t.Fatalf("incomplete or non-opaque %s signal: %+v", want, signal)
			}
			return
		}
	}
	t.Fatalf("missing %s signal in %+v", want, signals)
}
