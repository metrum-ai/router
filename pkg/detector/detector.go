// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

// Package detector defines the versioned contract for security detectors.
package detector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"time"
)

const SchemaVersion = "metrum.detector.v1"
const MaxRequestBytes = 64 << 10
const MaxSegments = 16
const MaxSegmentBytes = 8 << 10
const MaxSegmentBytesTotal = 48 << 10

// Event contains bounded metadata and, only when explicitly opted in, selected
// tool-result or document segments. It never contains prompts, caller messages,
// or token values.
type Event struct {
	SchemaVersion string    `json:"schema_version"`
	RequestID     string    `json:"request_id,omitempty"`
	CallerID      string    `json:"caller_id,omitempty"`
	Kind          string    `json:"kind"`
	ObservedAt    string    `json:"observed_at,omitempty"`
	SourceIP      string    `json:"source_ip,omitempty"`
	KeyID         string    `json:"key_id,omitempty"`
	TinyRequest   bool      `json:"tiny_request,omitempty"`
	DeniedGroup   string    `json:"denied_group,omitempty"`
	Tokens        int64     `json:"tokens,omitempty"`
	Segments      []Segment `json:"segments,omitempty"`
}

// Segment contains text that an operator explicitly authorizes for one detector.
type Segment struct {
	Kind  string `json:"kind"`
	Index int    `json:"index"`
	Text  string `json:"text"`
}

// Signal reports one detector result. Key is the router's opaque event.KeyID,
// never a credential; it is empty for signals scoped to a source address.
type Signal struct {
	SchemaVersion      string   `json:"schema_version"`
	DetectorID         string   `json:"detector_id"`
	Severity           string   `json:"severity"`
	Score              float64  `json:"score"`
	Key                string   `json:"key,omitempty"`
	EvidenceRequestIDs []string `json:"evidence_request_ids,omitempty"`
	SegmentKind        string   `json:"segment_kind,omitempty"`
	SegmentIndex       *int     `json:"segment_index,omitempty"`
}

// Detector evaluates one metadata event and returns zero or more signals.
type Detector interface {
	Detect(context.Context, Event) ([]Signal, error)
}

// Handler serves a detector through POST /v1/detect.
func Handler(d Detector) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/detect" {
			http.NotFound(w, r)
			return
		}
		if d == nil {
			http.Error(w, "detector unavailable", http.StatusServiceUnavailable)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var event Event
		if err := dec.Decode(&event); err != nil || ValidateEvent(event) != nil || !singleJSONValue(dec) {
			http.Error(w, "invalid detector event", http.StatusBadRequest)
			return
		}
		signals, err := d.Detect(r.Context(), event)
		if err != nil {
			http.Error(w, "detector failed", http.StatusBadGateway)
			return
		}
		for i := range signals {
			if signals[i].SchemaVersion == "" {
				signals[i].SchemaVersion = SchemaVersion
			}
			if err := ValidateSignal(signals[i]); err != nil {
				http.Error(w, "detector returned invalid signal", http.StatusBadGateway)
				return
			}
		}
		if signals == nil {
			signals = []Signal{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(signals)
	})
}

func singleJSONValue(dec *json.Decoder) bool {
	var extra any
	return errors.Is(dec.Decode(&extra), io.EOF)
}

// DecodeSignals strictly decodes a bounded response and validates every signal.
func DecodeSignals(r io.Reader) ([]Signal, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxRequestBytes+1))
	if err != nil || len(data) > MaxRequestBytes {
		return nil, errors.New("invalid detector response")
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var signals []Signal
	if err := dec.Decode(&signals); err != nil || !singleJSONValue(dec) {
		return nil, errors.New("invalid detector response")
	}
	for _, signal := range signals {
		if err := ValidateSignal(signal); err != nil {
			return nil, errors.New("invalid detector response")
		}
	}
	return signals, nil
}

func ValidateSignal(s Signal) error {
	if s.SchemaVersion != SchemaVersion || !safeLabel(s.DetectorID, 128) || !safeLabel(s.Severity, 16) || math.IsNaN(s.Score) || math.IsInf(s.Score, 0) || s.Score < 0 || s.Score > 1 {
		return errors.New("invalid detector signal")
	}
	if s.Key != "" && !safeLabel(s.Key, 128) {
		return errors.New("invalid detector signal")
	}
	if s.Key != "" && !opaqueKeyID(s.Key) {
		return errors.New("invalid detector signal")
	}
	if len(s.EvidenceRequestIDs) > 32 {
		return errors.New("invalid detector signal")
	}
	if s.SegmentKind != "" && s.SegmentKind != "tool_result" && s.SegmentKind != "document" {
		return errors.New("invalid detector signal")
	}
	if s.SegmentIndex != nil && (*s.SegmentIndex < 0 || s.SegmentKind == "") {
		return errors.New("invalid detector signal")
	}
	for _, id := range s.EvidenceRequestIDs {
		if !safeLabel(id, 128) {
			return errors.New("invalid detector signal")
		}
	}
	return nil
}

// ValidateEvent rejects unbounded or content-like fields before detector dispatch.
func ValidateEvent(e Event) error {
	if e.SchemaVersion != SchemaVersion || !safeLabel(e.Kind, 64) {
		return errors.New("invalid detector event")
	}
	for _, value := range []string{e.RequestID, e.CallerID, e.DeniedGroup} {
		if value != "" && !safeLabel(value, 128) {
			return errors.New("invalid detector event")
		}
	}
	if e.KeyID != "" && !opaqueKeyID(e.KeyID) {
		return errors.New("invalid detector event")
	}
	if e.Tokens < 0 || e.Tokens > 1_000_000_000 {
		return errors.New("invalid detector event")
	}
	if len(e.Segments) > MaxSegments {
		return errors.New("invalid detector event")
	}
	totalSegmentBytes := 0
	for _, segment := range e.Segments {
		if (segment.Kind != "tool_result" && segment.Kind != "document") || segment.Index < 0 || len(segment.Text) == 0 || len(segment.Text) > MaxSegmentBytes {
			return errors.New("invalid detector event")
		}
		totalSegmentBytes += len(segment.Text)
	}
	if totalSegmentBytes > MaxSegmentBytesTotal {
		return errors.New("invalid detector event")
	}
	if e.SourceIP != "" && net.ParseIP(e.SourceIP) == nil {
		return errors.New("invalid detector event")
	}
	if e.ObservedAt != "" {
		if len(e.ObservedAt) > 40 {
			return errors.New("invalid detector event")
		}
		if _, err := time.Parse(time.RFC3339Nano, e.ObservedAt); err != nil {
			return errors.New("invalid detector event")
		}
	}
	return nil
}

func opaqueKeyID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func safeLabel(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.:-", r)) {
			return false
		}
	}
	return true
}
