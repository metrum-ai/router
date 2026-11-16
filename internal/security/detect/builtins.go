// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package detect

import (
	"context"

	"github.com/metrum-ai/router/pkg/detector"
)

const (
	EventHoneytokenUsed      = "honeytoken.used"
	EventSpendBreakerTripped = "spend_breaker.tripped"
)

type HoneytokenDetector struct{}

func (HoneytokenDetector) Detect(_ context.Context, event detector.Event) ([]detector.Signal, error) {
	if event.Kind != EventHoneytokenUsed {
		return []detector.Signal{}, nil
	}
	return []detector.Signal{{SchemaVersion: detector.SchemaVersion, DetectorID: "honeytoken", Severity: "critical", Score: 1, Key: event.KeyID, EvidenceRequestIDs: evidence(event.RequestID)}}, nil
}

type SpendBreakerDetector struct{}

func (SpendBreakerDetector) Detect(_ context.Context, event detector.Event) ([]detector.Signal, error) {
	if event.Kind != EventSpendBreakerTripped {
		return []detector.Signal{}, nil
	}
	return []detector.Signal{{SchemaVersion: detector.SchemaVersion, DetectorID: "spend_breaker", Severity: "high", Score: 1, Key: event.KeyID, EvidenceRequestIDs: evidence(event.RequestID)}}, nil
}

func evidence(requestID string) []string {
	if requestID == "" {
		return nil
	}
	return []string{requestID}
}
